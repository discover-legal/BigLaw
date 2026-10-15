// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// legacyStateFile is one state file whose default moved from the working
// directory into the data dir. Legacy is the old default (relative to the
// process CWD); Path is the new default. Env names the override that, when
// set, takes the file out of migration altogether.
type legacyStateFile struct {
	Env    string
	Legacy string
	Path   string
}

// StateMove reports one legacy state file carried into the data dir.
type StateMove struct {
	Env    string // the env var whose default moved
	From   string // legacy location (working directory)
	To     string // new default location (data dir)
	Copied bool   // true when rename crossed devices and fell back to copy+remove
}

// MigrateLegacyStateFiles moves state files left at their pre-data-dir
// defaults (./.tasks.json, ./.profiles.json, ./audit.jsonl, …) to their new
// defaults under the data dir. It is idempotent and conservative:
//
//   - only files whose env var is unset are considered (explicit overrides
//     always win and are never touched);
//   - a file is moved only when the legacy path exists and the new one does
//     not — if both exist the new one is kept, the legacy one is left in
//     place, and a warning names both;
//   - rename is tried first; across devices it falls back to copy + fsync +
//     atomic rename into place + remove.
//
// It must run before any store opens its file, and only in the process that
// owns the data dir. Each move is logged; failures are joined and returned
// so the caller can refuse to start rather than come up with empty state.
func MigrateLegacyStateFiles(c *Config) ([]StateMove, error) {
	var moves []StateMove
	var errs []error
	for _, f := range c.legacyState {
		mv, err := migrateOne(f)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.Env, err))
			continue
		}
		if mv != nil {
			slog.Info("migrated legacy state file into the data dir",
				"env", mv.Env, "from", mv.From, "to", mv.To, "copied", mv.Copied)
			moves = append(moves, *mv)
		}
	}
	return moves, errors.Join(errs...)
}

func migrateOne(f legacyStateFile) (*StateMove, error) {
	if samePath(f.Legacy, f.Path) {
		return nil, nil // data dir is the working directory: nothing moved
	}
	src, err := os.Lstat(f.Legacy)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !src.Mode().IsRegular() {
		slog.Warn("legacy state path is not a regular file; leaving it alone",
			"env", f.Env, "path", f.Legacy)
		return nil, nil
	}
	if _, err := os.Lstat(f.Path); err == nil {
		slog.Warn("legacy state file left in place: the data dir already has one (it wins); "+
			"merge or delete the legacy file by hand",
			"env", f.Env, "legacy", f.Legacy, "current", f.Path)
		return nil, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return nil, err
	}
	mv := &StateMove{Env: f.Env, From: f.Legacy, To: f.Path}
	err = os.Rename(f.Legacy, f.Path)
	if err == nil {
		return mv, nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return nil, err
	}
	if err := copyThenRemove(f.Legacy, f.Path, src.Mode().Perm()); err != nil {
		return nil, err
	}
	mv.Copied = true
	return mv, nil
}

// copyThenRemove copies src to a temp file beside dst, fsyncs it, renames it
// into place (atomic on dst's filesystem) and only then removes src — so a
// crash at any point leaves at least one complete copy.
func copyThenRemove(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".migrating-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if _, err := io.Copy(tmp, in); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Remove(src)
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && aa == bb
}
