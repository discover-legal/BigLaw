// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// StatePath is one piece of on-disk state BigLaw owns: a file or directory
// whose default lives in the data dir (RUVECTOR_DATA_DIR, default ./data).
type StatePath struct {
	Env      string // override variable ("" never: derived paths name their parent's)
	Path     string // resolved location
	Explicit bool   // set by a non-legacy env value: used verbatim, never migrated
	Dir      bool   // a directory rather than a file

	// legacy lists earlier default locations (relative to the working
	// directory), most likely first; the first one present is migrated.
	legacy []string
	// companions are suffixes of sibling files that must move with Path
	// (SQLite's -wal / -shm / -journal next to the database).
	companions []string
}

// stateSet records every state path Load resolves, so migration and the
// startup layout check see exactly the same list.
type stateSet struct {
	dataDir string
	paths   []StatePath
}

// file resolves a state file. An env value is explicit — used verbatim and
// never migrated — unless it equals one of the legacy defaults (a stale copy
// of an old .env.example, e.g. TASKS_FILE=.tasks.json), which counts as
// unset so the file is carried into the data dir like everyone else's.
func (s *stateSet) file(env, name string, legacy ...string) string {
	return s.add(env, name, false, legacy)
}

// dir is file for a state directory.
func (s *stateSet) dir(env, name string, legacy ...string) string {
	return s.add(env, name, true, legacy)
}

func (s *stateSet) add(env, name string, dir bool, legacy []string) string {
	if v := os.Getenv(env); v != "" {
		if !matchesAny(v, legacy) {
			s.paths = append(s.paths, StatePath{Env: env, Path: v, Explicit: true, Dir: dir})
			return v
		}
		slog.Info("state path env equals its pre-data-dir default; treating it as unset",
			"env", env, "value", v)
	}
	p := filepath.Join(s.dataDir, name)
	s.paths = append(s.paths, StatePath{Env: env, Path: p, Dir: dir, legacy: withDotData(name, legacy)})
	return p
}

// derived registers a file whose location follows another state path's
// directory (it has no env var of its own); env names that parent.
func (s *stateSet) derived(env, path string, legacy ...string) {
	s.paths = append(s.paths, StatePath{Env: env, Path: path, legacy: withDotData(filepath.Base(path), legacy)})
}

// withCompanions attaches sibling-file suffixes to the most recent path for env.
func (s *stateSet) withCompanions(env string, suffixes ...string) {
	for i := len(s.paths) - 1; i >= 0; i-- {
		if s.paths[i].Env == env {
			s.paths[i].companions = suffixes
			return
		}
	}
}

func (s *stateSet) explicit(env string) bool {
	for _, p := range s.paths {
		if p.Env == env {
			return p.Explicit
		}
	}
	return false
}

// withDotData adds ./data/<name> — where the default data dir kept it —
// to the legacy locations, so state follows a RUVECTOR_DATA_DIR that moved.
func withDotData(name string, legacy []string) []string {
	dd := filepath.Join(dotData, name)
	for _, l := range legacy {
		if filepath.Clean(l) == dd {
			return legacy
		}
	}
	return append(append([]string(nil), legacy...), dd)
}

func matchesAny(v string, legacy []string) bool {
	for _, l := range legacy {
		if filepath.Clean(v) == filepath.Clean(l) {
			return true
		}
	}
	return false
}

// StatePaths lists every state file and directory the config resolved.
func (c *Config) StatePaths() []StatePath {
	return append([]StatePath(nil), c.state...)
}

// CheckStateLayout asserts that every state path not explicitly overridden
// lies inside the data dir — the directory the single-writer lock guards. A
// failure is a programming error (a new default that bypassed the data dir)
// and the process must not start: two owners with different data dirs would
// otherwise share that file unguarded.
func (c *Config) CheckStateLayout() error {
	root, err := filepath.Abs(c.VectorDB.DataDir)
	if err != nil {
		return err
	}
	var outside []string
	for _, p := range c.state {
		if p.Explicit {
			continue
		}
		abs, err := filepath.Abs(p.Path)
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(root, abs); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			outside = append(outside, fmt.Sprintf("%s=%s", p.Env, p.Path))
		}
	}
	if len(outside) > 0 {
		return fmt.Errorf("state paths outside the data dir %s (unguarded by its lock): %s",
			root, strings.Join(outside, ", "))
	}
	return nil
}
