// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxFileSize caps one stored file. A single upload or generated .docx
	// far above this is a mistake or an attack.
	MaxFileSize = 64 << 20
	// ChunkSize is the size of one file_chunks row. lib/pq sends and
	// receives bytea as hex text, so each chunk costs a few times its size
	// in transient memory; chunking keeps that bounded by ChunkSize instead
	// of by the file size.
	ChunkSize = 1 << 20
	// MaxNameLen caps a file name in bytes.
	MaxNameLen = 255
)

var (
	ErrFileTooLarge = fmt.Errorf("store: file exceeds %d bytes", MaxFileSize)
	ErrNotFound     = errors.New("store: not found")
	ErrBadName      = errors.New(`store: file name must be 1-255 bytes of valid UTF-8, not "." or "..", with no path separator, ` +
		"control or invisible character (bidi controls, zero-width spaces, fillers, variation selectors; one zero-width joiner " +
		"between two letters or emoji is allowed), line separator, run of two or more spaces, or leading/trailing space")
	ErrBadType = errors.New("store: content type must be a concrete media type such as text/plain (no wildcard), in printable ASCII, at most 255 bytes in canonical form")
	// ErrCorrupt means the stored bytes do not match the recorded size or
	// SHA-256. The checksum detects storage corruption, not tampering:
	// anyone who can write the rows can rewrite the hash too.
	ErrCorrupt = errors.New("store: file failed its integrity check")
)

// File is one stored blob and its metadata.
type File struct {
	ID          string
	Name        string
	ContentType string
	Size        int64
	SHA256      string // lowercase hex
	Data        []byte // nil from PutFile
	CreatedAt   time.Time
}

// PutFile streams r into a new file and returns its metadata. It holds one
// ChunkSize buffer at a time and stops with ErrFileTooLarge within one chunk
// of MaxFileSize. The file and its chunks are written in one transaction
// (bounded as MaxConcurrentUploads says; a call over the cap waits for a
// slot or ctx, but a call whose reader blocks returns only when the reader
// does). The content type is stored in canonical form.
func (s *Store) PutFile(ctx context.Context, name, contentType string, r io.Reader) (File, error) {
	if !ValidName(name) {
		return File{}, ErrBadName
	}
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.Contains(mt, "/") || strings.Contains(mt, "*") ||
		strings.ContainsFunc(contentType, func(r rune) bool { return r < 0x20 || r >= 0x7f }) {
		return File{}, ErrBadType
	}
	f := File{Name: name, ContentType: mime.FormatMediaType(mt, params)}
	if f.ContentType == "" || len(f.ContentType) > MaxNameLen { // the stored form: canonicalising adds "; " per parameter
		return File{}, ErrBadType
	}
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	case <-ctx.Done():
		return File{}, fmt.Errorf("store: put file: waiting for an upload slot: %w", ctx.Err())
	}
	err = s.InTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `INSERT INTO files (name, content_type, size, sha256) VALUES ($1, $2, 0, '')
			RETURNING id, created_at`, name, f.ContentType).Scan(&f.ID, &f.CreatedAt); err != nil {
			return err
		}
		ins, err := tx.PrepareContext(ctx, `INSERT INTO file_chunks (file_id, seq, data) VALUES ($1, $2, $3)`)
		if err != nil {
			return err
		}
		defer ins.Close()
		h, buf := sha256.New(), make([]byte, ChunkSize)
		for seq := 0; ; seq++ {
			n, rerr := io.ReadFull(r, buf)
			if f.Size += int64(n); f.Size > MaxFileSize {
				return ErrFileTooLarge
			}
			if n > 0 {
				h.Write(buf[:n])
				if _, err := ins.ExecContext(ctx, f.ID, seq, buf[:n]); err != nil {
					return err
				}
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			} else if rerr != nil {
				return fmt.Errorf("read: %w", rerr)
			}
		}
		f.SHA256 = hex.EncodeToString(h.Sum(nil))
		_, err = tx.ExecContext(ctx, `UPDATE files SET size = $2, sha256 = $3 WHERE id = $1`, f.ID, f.Size, f.SHA256)
		return err
	})
	if errors.Is(err, ErrFileTooLarge) {
		return File{}, ErrFileTooLarge
	}
	if err != nil {
		return File{}, fmt.Errorf("store: put file: %w", err)
	}
	return f, nil
}

// GetFile returns a file with its bytes (ReadFile into one buffer of the recorded size), so
// corruption is ErrCorrupt rather than a wrong document. At most MaxConcurrentReads reads (GetFile
// or ReadFile) run at once; a call over the cap waits for a slot or ctx.
func (s *Store) GetFile(ctx context.Context, id string) (File, error) {
	var buf bytes.Buffer
	f, err := s.ReadFile(ctx, id, func(f File) error { buf.Grow(int(f.Size)); return nil }, &buf)
	if err != nil {
		return File{}, err
	}
	if f.Data = buf.Bytes(); f.Data == nil {
		f.Data = []byte{} // an empty file
	}
	return f, nil
}

// ReadFile writes a file to w one chunk at a time (one query per chunk, so it holds two chunks,
// not the file) and returns its metadata; head, if not nil, gets the metadata before the first
// byte. Unknown or malformed ids give ErrNotFound. The recorded size is checked against
// MaxFileSize before any content is read, and the size and SHA-256 are verified before the last
// chunk is written: on ErrCorrupt w has received at most all but the last chunk, never the whole
// file, so a client given the recorded size as Content-Length sees a short body (r2 N3: rows
// changed between a caller's verifying read and its streaming read). A read slot is held
// throughout.
func (s *Store) ReadFile(ctx context.Context, id string, head func(File) error, w io.Writer) (File, error) {
	if !ValidID(id) {
		return File{}, ErrNotFound
	}
	select {
	case s.reads <- struct{}{}:
		defer func() { <-s.reads }()
	case <-ctx.Done():
		return File{}, fmt.Errorf("store: get file: waiting for a read slot: %w", ctx.Err())
	}
	var f File
	var chunks int
	err := s.db.QueryRowContext(ctx, `SELECT id, name, content_type, size, sha256, created_at,
		(SELECT count(*) FROM file_chunks WHERE file_id = f.id) FROM files f WHERE id = $1`, id).
		Scan(&f.ID, &f.Name, &f.ContentType, &f.Size, &f.SHA256, &f.CreatedAt, &chunks)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	} else if err != nil {
		return File{}, fmt.Errorf("store: get file: %w", err)
	} else if f.Size < 0 || f.Size > MaxFileSize {
		return File{}, fmt.Errorf("%w: file %s records size %d", ErrCorrupt, f.ID, f.Size)
	}
	if head != nil {
		if err := head(f); err != nil {
			return File{}, err
		}
	}
	h, n, seq, held := sha256.New(), int64(0), 0, []byte(nil)
	for ; ; seq++ {
		var chunk []byte
		err := s.db.QueryRowContext(ctx, `SELECT data FROM file_chunks WHERE file_id = $1 AND seq = $2`, id, seq).Scan(&chunk)
		if errors.Is(err, sql.ErrNoRows) {
			break
		} else if err != nil {
			return File{}, fmt.Errorf("store: get file: %w", err)
		}
		if n += int64(len(chunk)); n > f.Size {
			return File{}, fmt.Errorf("%w: file %s holds more than its recorded %d bytes", ErrCorrupt, f.ID, f.Size)
		}
		h.Write(chunk)
		if _, err := w.Write(held); err != nil {
			return File{}, fmt.Errorf("store: get file: write: %w", err)
		}
		held = chunk
	}
	if n != f.Size || seq != chunks || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), f.SHA256) {
		return File{}, fmt.Errorf("%w: file %s", ErrCorrupt, f.ID)
	}
	if _, err := w.Write(held); err != nil {
		return File{}, fmt.Errorf("store: get file: write: %w", err)
	}
	return f, nil
}

// ValidName is the file-name policy (README § Files): no path separator, control, line separator
// or invisible character (Cf such as bidi controls and zero-width spaces, Unicode
// Default_Ignorable_Code_Point such as Hangul fillers and variation selectors, the braille blank),
// no run of white space and no white space at either end. ZWNJ/ZWJ stay allowed once between
// two letters or emoji (Persian, Indic scripts and emoji sequences need them), and one
// presentation selector (U+FE0E/U+FE0F) right after an emoji symbol.
func ValidName(name string) bool {
	if name == "" || len(name) > MaxNameLen || name == "." || name == ".." || !utf8.ValidString(name) ||
		strings.TrimFunc(name, unicode.IsSpace) != name { // "report.pdf " hides an extension or a trailing character
		return false
	}
	rs := []rune(name)
	joins := func(i int) bool {
		return i >= 0 && i < len(rs) && unicode.In(rs[i], unicode.L, unicode.M, unicode.So, unicode.Sk)
	}
	for i, r := range rs {
		switch {
		case (r == '\ufe0e' || r == '\ufe0f') && i > 0 && unicode.Is(unicode.So, rs[i-1]): // an emoji's presentation: "❤️"
		case r == '\u200c' || r == '\u200d':
			if !joins(i-1) || !joins(i+1) {
				return false
			}
		case r == '/' || r == '\\' || unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector),
			r == '\u2800', unicode.IsSpace(r) && unicode.IsSpace(rs[i-1]): // i > 0: no leading space
			return false
		}
	}
	return true
}

// ValidID reports whether id is a canonical uuid.
func ValidID(id string) bool { return uuidRe.MatchString(id) }

// uuidRe is the canonical 8-4-4-4-12 form: a malformed id is "not found"
// instead of a Postgres cast error, and lookups use the index.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
