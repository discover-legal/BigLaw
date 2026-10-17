// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
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
	ErrBadName      = errors.New(`store: file name must be 1-255 bytes of valid UTF-8, not "." or "..", with no path separator, control or bidi-control character`)
	ErrBadType      = errors.New("store: content type must be a valid media type such as text/plain")
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
// of MaxFileSize. The file and all its chunks are written in one transaction.
func (s *Store) PutFile(ctx context.Context, name, contentType string, r io.Reader) (File, error) {
	if !validName(name) {
		return File{}, ErrBadName
	}
	if mt, _, err := mime.ParseMediaType(contentType); err != nil || !strings.Contains(mt, "/") || len(contentType) > MaxNameLen {
		return File{}, ErrBadType
	}
	f := File{Name: name, ContentType: contentType}
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `INSERT INTO files (name, content_type, size, sha256) VALUES ($1, $2, 0, '')
			RETURNING id, created_at`, name, contentType).Scan(&f.ID, &f.CreatedAt); err != nil {
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

// GetFile returns a file with its bytes. Unknown or malformed ids give
// ErrNotFound. Metadata and chunks come from one statement (one snapshot).
// The recorded size is checked against MaxFileSize before any content is
// read, the buffer is allocated once at that size, and the size and SHA-256
// are re-verified, so corruption is ErrCorrupt rather than a wrong document.
func (s *Store) GetFile(ctx context.Context, id string) (File, error) {
	if !uuidRe.MatchString(id) {
		return File{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.name, f.content_type, f.size, f.sha256, f.created_at, c.data
		FROM files f LEFT JOIN file_chunks c ON c.file_id = f.id WHERE f.id = $1 ORDER BY c.seq`, id)
	if err != nil {
		return File{}, fmt.Errorf("store: get file: %w", err)
	}
	defer rows.Close()
	var f File
	for first := true; rows.Next(); first = false {
		var chunk sql.RawBytes // valid until the next Next; copied below
		if first {
			err = rows.Scan(&f.ID, &f.Name, &f.ContentType, &f.Size, &f.SHA256, &f.CreatedAt, &chunk)
			if err == nil && (f.Size < 0 || f.Size > MaxFileSize) {
				return File{}, fmt.Errorf("%w: file %s records size %d", ErrCorrupt, f.ID, f.Size)
			}
			f.Data = make([]byte, 0, f.Size)
		} else {
			var skip any
			err = rows.Scan(&skip, &skip, &skip, &skip, &skip, &skip, &chunk)
		}
		if err != nil {
			return File{}, fmt.Errorf("store: get file: %w", err)
		}
		if int64(len(f.Data)+len(chunk)) > f.Size {
			return File{}, fmt.Errorf("%w: file %s holds more than its recorded %d bytes", ErrCorrupt, f.ID, f.Size)
		}
		f.Data = append(f.Data, chunk...)
	}
	if err := rows.Err(); err != nil {
		return File{}, fmt.Errorf("store: get file: %w", err)
	}
	if f.ID == "" {
		return File{}, ErrNotFound
	}
	sum := sha256.Sum256(f.Data)
	if int64(len(f.Data)) != f.Size || !strings.EqualFold(hex.EncodeToString(sum[:]), f.SHA256) {
		return File{}, fmt.Errorf("%w: file %s", ErrCorrupt, f.ID)
	}
	return f, nil
}

func validName(name string) bool {
	if name == "" || len(name) > MaxNameLen || name == "." || name == ".." || !utf8.ValidString(name) {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\' || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) // bidi: "a\u202Efdp.exe" displays as "aexe.pdf"
	})
}

// uuidRe is the canonical 8-4-4-4-12 form: a malformed id is "not found"
// instead of a Postgres cast error, and lookups use the index.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
