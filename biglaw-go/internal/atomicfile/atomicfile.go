// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package atomicfile writes state files crash-safely: a unique temp file in
// the target directory is written, fsynced and renamed over the target, and
// the directory is fsynced so the rename itself is durable. A store that
// returns success after WriteFile has its data on disk — not in a page cache
// that a crash or power loss discards — and a crash mid-write leaves the
// previous version intact rather than a truncated file.
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile atomically replaces path with data, created with perm (store
// files holding client data should pass 0o600). The parent directory is
// created (0o700) if missing.
func WriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Unique temp name: concurrent writers never share (and truncate) one
	// another's temp file.
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(perm); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	// Persist the directory entry (best effort: not every filesystem
	// supports fsync on a directory).
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
