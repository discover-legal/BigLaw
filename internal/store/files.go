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
	"time"
)

// MaxFileSize caps one stored file. Postgres bytea allows 1 GB, but a single
// upload or generated .docx far above this is a mistake or an attack.
const MaxFileSize = 64 << 20

var (
	ErrFileTooLarge = fmt.Errorf("store: file exceeds %d bytes", MaxFileSize)
	ErrNotFound     = errors.New("store: not found")
)

// File is one stored blob and its metadata.
type File struct {
	ID          string
	Name        string
	ContentType string
	Size        int64
	SHA256      string // hex
	Data        []byte // nil from FileInfo
	CreatedAt   time.Time
}

// PutFile stores data and returns the new file's metadata (Data is not
// echoed back).
func (s *Store) PutFile(ctx context.Context, name, contentType string, data []byte) (File, error) {
	if len(data) > MaxFileSize {
		return File{}, ErrFileTooLarge
	}
	if data == nil {
		data = []byte{} // bytea NOT NULL: an empty file is still a file
	}
	sum := sha256.Sum256(data)
	f := File{Name: name, ContentType: contentType, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO files (name, content_type, size, sha256, data) VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, created_at`,
		f.Name, f.ContentType, f.Size, f.SHA256, data).Scan(&f.ID, &f.CreatedAt)
	if err != nil {
		return File{}, fmt.Errorf("store: put file: %w", err)
	}
	return f, nil
}

// GetFile returns a file with its bytes. Unknown or malformed ids give
// ErrNotFound. The stored checksum is re-verified, so silent corruption is
// an error rather than a wrong document.
func (s *Store) GetFile(ctx context.Context, id string) (File, error) {
	f, err := s.scanFile(ctx, `SELECT id, name, content_type, size, sha256, created_at, data FROM files WHERE id = $1`, id, true)
	if err != nil {
		return File{}, err
	}
	sum := sha256.Sum256(f.Data)
	if hex.EncodeToString(sum[:]) != f.SHA256 {
		return File{}, fmt.Errorf("store: file %s failed its checksum", f.ID)
	}
	return f, nil
}

// FileInfo returns a file's metadata without its bytes.
func (s *Store) FileInfo(ctx context.Context, id string) (File, error) {
	return s.scanFile(ctx, `SELECT id, name, content_type, size, sha256, created_at FROM files WHERE id = $1`, id, false)
}

// DeleteFile removes a file. Deleting an unknown id gives ErrNotFound.
func (s *Store) DeleteFile(ctx context.Context, id string) error {
	if !validUUID(id) {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete file: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) scanFile(ctx context.Context, q, id string, withData bool) (File, error) {
	if !validUUID(id) {
		return File{}, ErrNotFound
	}
	var f File
	dest := []any{&f.ID, &f.Name, &f.ContentType, &f.Size, &f.SHA256, &f.CreatedAt}
	if withData {
		dest = append(dest, &f.Data)
	}
	err := s.db.QueryRowContext(ctx, q, id).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, fmt.Errorf("store: get file: %w", err)
	}
	return f, nil
}

// validUUID accepts the canonical 8-4-4-4-12 hex form, so a malformed id is
// "not found" instead of a Postgres cast error, and lookups use the index.
func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'):
			return false
		}
	}
	return true
}
