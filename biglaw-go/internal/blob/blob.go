// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package blob is the binary-artifact storage seam for document attachments
// (retained original images/PDFs, rendered pages, figures to place into
// output). It mirrors the persistence seam in internal/store: a single
// interface with a local disk implementation today and an S3/Supabase-Storage
// implementation to slot in for cloud later.
//
// Access control is NOT enforced here — the blob layer is a dumb key→bytes
// store. Visibility is governed at the metadata layer: the API resolves an
// attachment through the RLS-scoped repository first and only then fetches its
// bytes, so an unauthorized caller never learns a key to fetch.
package blob

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/config"
)

// Store is key→bytes blob storage. Keys are opaque, "/"-delimited paths
// (e.g. "<docID>/<attachmentID>").
//
// Every bundled backend is open / vendor-neutral: local disk, WebDAV (RFC 4918),
// Supabase Storage's native REST API, and an OCI registry via ORAS. AWS S3 is
// deliberately not offered (no Amazon dependency ships).
type Store interface {
	Put(key string, data []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
	Backend() string
}

// Compile-time guarantee every bundled backend satisfies the seam.
var (
	_ Store = (*DiskStore)(nil)
	_ Store = (*WebDAVStore)(nil)
	_ Store = (*SupabaseStore)(nil)
	_ Store = (*OCIStore)(nil)
)

// Open builds the blob store selected by config.
func Open(cfg config.BlobConfig) (Store, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Backend)) {
	case "", "disk":
		return NewDiskStore(cfg.Dir)
	case "webdav":
		return NewWebDAVStore(cfg.WebDAVURL, cfg.WebDAVUser, cfg.WebDAVPass)
	case "supabase":
		return NewSupabaseStore(cfg.SupabaseURL, cfg.SupabaseBucket, cfg.SupabaseKey)
	case "oci":
		return NewOCIStore(cfg.OCIRef, cfg.OCIUser, cfg.OCIPass, cfg.OCIPlainHTTP)
	default:
		return nil, fmt.Errorf("blob: backend %q is not bundled (disk | webdav | supabase | oci)", cfg.Backend)
	}
}

// DiskStore stores blobs as files under a root directory.
type DiskStore struct {
	root string
}

// NewDiskStore roots a disk blob store at dir (created if absent).
func NewDiskStore(dir string) (*DiskStore, error) {
	if dir == "" {
		dir = filepath.Join("data", "attachments")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("blob: create root %s: %w", dir, err)
	}
	return &DiskStore{root: dir}, nil
}

func (d *DiskStore) Backend() string { return "disk" }

// safePath resolves key under the root, rejecting any path-traversal attempt.
func (d *DiskStore) safePath(key string) (string, error) {
	slash := strings.ReplaceAll(key, "\\", "/")
	for _, seg := range strings.Split(slash, "/") {
		if seg == ".." {
			return "", fmt.Errorf("blob: key contains a parent reference: %q", key)
		}
	}
	clean := filepath.Clean("/" + slash)
	p := filepath.Join(d.root, filepath.FromSlash(clean))
	absRoot, err := filepath.Abs(d.root)
	if err != nil {
		return "", err
	}
	absP, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if absP != absRoot && !strings.HasPrefix(absP, absRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("blob: key escapes root: %q", key)
	}
	return absP, nil
}

func (d *DiskStore) Put(key string, data []byte) error {
	p, err := d.safePath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

func (d *DiskStore) Get(key string) ([]byte, error) {
	p, err := d.safePath(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

func (d *DiskStore) Delete(key string) error {
	p, err := d.safePath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
