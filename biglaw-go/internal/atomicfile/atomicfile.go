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
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteFile atomically replaces path with data, created with perm (store
// files holding client data should pass 0o600). See Write.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return Write(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// Write atomically replaces path with whatever fill streams into w — for
// snapshots too large to marshal into one buffer first. The file ends up
// with exactly perm, also when it replaces an older, looser file. The
// parent directory is created (0o700) if missing.
//
// A symlinked path is followed: the link's final target is replaced and the
// link itself is kept, as a plain os.WriteFile would (a store path symlinked
// onto another volume keeps pointing there).
func Write(path string, perm os.FileMode, fill func(w io.Writer) error) (err error) {
	if path, err = resolve(path); err != nil {
		return err
	}
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
	bw := bufio.NewWriter(f)
	if err = fill(bw); err != nil {
		return err
	}
	if err = bw.Flush(); err != nil {
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

// resolve follows path through any chain of symlinks to the file that
// should be replaced. A dangling link resolves to the (not yet existing)
// target it names, so the first write creates the file there.
func resolve(path string) (string, error) {
	for hops := 0; ; hops++ {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return path, nil // a regular file, or nothing there yet
		}
		if hops == 40 {
			return "", fmt.Errorf("atomicfile: too many levels of symbolic links: %s", path)
		}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
}
