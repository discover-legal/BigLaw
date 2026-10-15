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

// StateMove reports one legacy state path carried into the data dir.
type StateMove struct {
	Env    string // the env var whose default moved
	From   string // legacy location
	To     string // new default location (data dir)
	Copied bool   // rename crossed devices and fell back to copy+remove
}

// MigrateLegacyStateFiles carries state left at a pre-data-dir default —
// ./.tasks.json, ./audit.jsonl, or ./data/<x> when RUVECTOR_DATA_DIR points
// elsewhere — to its default inside the data dir. Idempotent and
// conservative:
//
//   - explicit overrides are never touched (an env value equal to an old
//     default is not explicit; see stateSet.file);
//   - a path moves only when a legacy copy exists and the data-dir one does
//     not; if both exist the data-dir copy wins and the legacy one is left
//     in place with a warning;
//   - a symlink moves as a symlink (relative targets are re-pointed so they
//     still resolve); companions (SQLite -wal/-shm) move with their file;
//   - rename first; across devices a file falls back to copy + fsync +
//     atomic rename + remove.
//
// Anything that cannot be migrated is returned as an error and the caller
// must refuse to start: coming up on an empty data dir while the user's
// state sits elsewhere would look like data loss. Run it only in the process
// holding the data-dir lock, before any store opens its file.
func MigrateLegacyStateFiles(c *Config) ([]StateMove, error) {
	var moves []StateMove
	var errs []error
	for _, p := range c.state {
		if p.Explicit {
			continue
		}
		mv, err := migratePath(p)
		moves = append(moves, mv...)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Env, err))
		}
	}
	for _, mv := range moves {
		slog.Info("migrated legacy state into the data dir",
			"env", mv.Env, "from", mv.From, "to", mv.To, "copied", mv.Copied)
	}
	return moves, errors.Join(errs...)
}

func migratePath(p StatePath) ([]StateMove, error) {
	var src string
	for _, l := range p.legacy {
		if samePath(l, p.Path) {
			continue // the old default is the new one (data dir is ./data or .)
		}
		if _, err := os.Lstat(l); err == nil {
			if src == "" {
				src = l
				continue
			}
			slog.Warn("another legacy copy left in place; only the first is migrated",
				"env", p.Env, "migrated", src, "left", l)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	if src == "" {
		return nil, nil
	}
	if _, err := os.Lstat(p.Path); err == nil {
		slog.Warn("legacy state left in place: the data dir already has its own copy (it wins); "+
			"merge or delete the legacy one by hand", "env", p.Env, "legacy", src, "current", p.Path)
		return nil, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	// Companions travel with their file; a stray one at the destination
	// would be replayed against the wrong database.
	for _, suf := range p.companions {
		if _, err := os.Lstat(p.Path + suf); err == nil {
			if _, serr := os.Lstat(src + suf); serr == nil {
				return nil, fmt.Errorf("%s exists without %s; refusing to pair it with the migrated %s",
					p.Path+suf, p.Path, src)
			}
		}
	}

	mv, err := movePath(p.Env, src, p.Path, p.Dir)
	if err != nil {
		return nil, err
	}
	moves := []StateMove{mv}
	for _, suf := range p.companions {
		if _, err := os.Lstat(src + suf); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		cmv, err := movePath(p.Env, src+suf, p.Path+suf, false)
		if err != nil {
			return moves, err
		}
		moves = append(moves, cmv)
	}
	return moves, nil
}

// movePath moves src to dst (dst known absent).
func movePath(env, src, dst string, wantDir bool) (StateMove, error) {
	mv := StateMove{Env: env, From: src, To: dst}
	info, err := os.Lstat(src)
	if err != nil {
		return mv, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return mv, err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return mv, moveSymlink(src, dst)
	case wantDir && !info.IsDir(), !wantDir && !info.Mode().IsRegular():
		want, got := "a regular file", "something else"
		if wantDir {
			want = "a directory"
		}
		if info.IsDir() {
			got = "a directory"
		} else if info.Mode().IsRegular() {
			got = "a regular file"
		}
		return mv, fmt.Errorf("%s is %s, not %s; move it to %s by hand or set %s", src, got, want, dst, env)
	}
	err = os.Rename(src, dst)
	if err == nil {
		return mv, nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return mv, err
	}
	if info.IsDir() {
		return mv, fmt.Errorf("%s is on another filesystem than %s; move the directory by hand or set %s", src, dst, env)
	}
	if err := copyThenRemove(src, dst, info.Mode().Perm()); err != nil {
		return mv, err
	}
	mv.Copied = true
	return mv, nil
}

// moveSymlink moves a symlink as a symlink. A relative target is resolved
// against the link's old directory so the moved link still points at the
// same file.
func moveSymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if filepath.IsAbs(target) {
		if err := os.Rename(src, dst); err == nil || !errors.Is(err, syscall.EXDEV) {
			return err
		}
	} else if target, err = filepath.Abs(filepath.Join(filepath.Dir(src), target)); err != nil {
		return err
	}
	if err := os.Symlink(target, dst); err != nil {
		return err
	}
	return os.Remove(src)
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
