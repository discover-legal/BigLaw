// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
//     still resolve);
//   - a file with companions (SQLite's -wal / -shm / -journal) moves with
//     them as one unit: companions first, the main file last, under a
//     journal written beside the destination before the first move and
//     removed after the last. An interrupted unit is resumed (or refused)
//     on the next start, never half-applied; orphaned or stray companions
//     refuse the start rather than open a database without its WAL;
//   - rename first; across devices a file falls back to copy + fsync +
//     atomic rename + remove.
//
// Anything that cannot be migrated is returned as an error (one
// *MigrationError per path, saying exactly what moved and what did not) and
// the caller must refuse to start: coming up on an empty data dir while the
// user's state sits elsewhere would look like data loss. Run it only in the
// process holding the data-dir lock, before any store opens its file.
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
			errs = append(errs, err)
		}
	}
	for _, mv := range moves {
		slog.Info("migrated legacy state into the data dir",
			"env", mv.Env, "from", mv.From, "to", mv.To, "copied", mv.Copied)
	}
	return moves, errors.Join(errs...)
}

// JournalSuffix names the journal a multi-file migration unit writes beside
// its destination (e.g. biglaw.db.migration-journal) while it runs.
const JournalSuffix = ".migration-journal"

// unitMove is one file of a migration unit, as recorded in its journal.
type unitMove struct {
	From string `json:"from"`
	To   string `json:"to"`
	Dir  bool   `json:"dir,omitempty"`
}

type unitJournal struct {
	Env   string     `json:"env"`
	Moves []unitMove `json:"moves"` // in move order: companions, then the main file
}

// MigrationError reports a path whose migration failed or was refused,
// listing exactly what moved and what did not.
type MigrationError struct {
	Env     string
	Moved   []StateMove // moved (or found already moved, when resuming)
	Pending []unitMove  // still at the legacy location (or otherwise not moved)
	Journal string      // journal left in place; the next start resumes it
	Err     error
}

func (e *MigrationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %v", e.Env, e.Err)
	for _, m := range e.Moved {
		fmt.Fprintf(&b, "\n    moved:     %s -> %s", m.From, m.To)
	}
	for _, m := range e.Pending {
		fmt.Fprintf(&b, "\n    not moved: %s (destination %s)", m.From, m.To)
	}
	if e.Journal != "" {
		fmt.Fprintf(&b, "\n    journal:   %s records this move; fix the cause and start again to resume it", e.Journal)
	}
	return b.String()
}

func (e *MigrationError) Unwrap() error { return e.Err }

func migratePath(p StatePath) ([]StateMove, error) {
	fail := func(err error, pending ...unitMove) error {
		return &MigrationError{Env: p.Env, Pending: pending, Err: err}
	}
	jpath := p.Path + JournalSuffix
	switch j, err := readJournal(jpath); {
	case err == nil:
		return resumeUnit(p.Env, jpath, j)
	case errors.Is(err, errTornJournal):
		// Moves start only after the journal is fsynced, so a torn one means
		// nothing moved: drop it and plan afresh (with every check below).
		slog.Warn("discarding an incomplete migration journal (nothing had moved)", "env", p.Env, "journal", jpath)
		if err := os.Remove(jpath); err != nil {
			return nil, fail(err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fail(fmt.Errorf("unreadable migration journal %s: %w", jpath, err))
	}

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
			return nil, fail(err)
		} else if orphans := present(l, p.companions); len(orphans) > 0 {
			// A -wal without its database: an interrupted older migration,
			// or a database deleted by hand. Starting would open the data-dir
			// database without the WAL holding its latest commits.
			return nil, fail(fmt.Errorf("%s without %s; a WAL or journal can hold commits the database "+
				"is missing — move it back beside its database (or delete it if that database is gone for good)",
				exist(orphans), l))
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
		return nil, fail(err)
	}

	// The unit: companions first, the main file last — so the database never
	// sits at its destination without the WAL it needs.
	// Absolute paths: the journal must mean the same files to a later start
	// from another working directory.
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return nil, fail(err)
	}
	absDst, err := filepath.Abs(p.Path)
	if err != nil {
		return nil, fail(err)
	}
	var plan []unitMove
	for _, suf := range p.companions {
		if _, err := os.Lstat(absSrc + suf); err == nil {
			plan = append(plan, unitMove{From: absSrc + suf, To: absDst + suf})
		}
	}
	plan = append(plan, unitMove{From: absSrc, To: absDst, Dir: p.Dir})

	// A companion already at the destination would be replayed against the
	// migrated database (or be overwritten by its own).
	if stray := present(p.Path, p.companions); len(stray) > 0 {
		return nil, fail(fmt.Errorf("%s without %s; refusing to pair it with the migrated %s — "+
			"remove it if it is not that database's", exist(stray), p.Path, src), plan...)
	}

	if len(plan) == 1 { // a single rename (or copy+atomic rename) needs no journal
		return runUnit(p.Env, "", plan)
	}
	if err := writeJournal(jpath, unitJournal{Env: p.Env, Moves: plan}); err != nil {
		return nil, fail(fmt.Errorf("write migration journal: %w", err), plan...)
	}
	return runUnit(p.Env, jpath, plan)
}

// present returns base+suffix for each suffix whose file exists.
func present(base string, suffixes []string) []string {
	var out []string
	for _, suf := range suffixes {
		if _, err := os.Lstat(base + suf); err == nil {
			out = append(out, base+suf)
		}
	}
	return out
}

// exist renders "a exists" / "a, b exist".
func exist(paths []string) string {
	if len(paths) == 1 {
		return paths[0] + " exists"
	}
	return strings.Join(paths, ", ") + " exist"
}

// runUnit moves plan in order, then (with a journal) makes the moves durable
// and removes the journal. On failure the journal stays, so the next start
// resumes rather than seeing a database parted from its companions.
func runUnit(env, jpath string, plan []unitMove, already ...StateMove) ([]StateMove, error) {
	done := already
	for i, m := range plan {
		mv, err := movePath(env, m.From, m.To, m.Dir)
		if err != nil {
			return done, &MigrationError{Env: env, Moved: done, Pending: plan[i:], Journal: jpath, Err: err}
		}
		done = append(done, mv)
	}
	if jpath != "" {
		for _, m := range plan {
			syncDir(filepath.Dir(m.From))
		}
		syncDir(filepath.Dir(jpath))
		if err := os.Remove(jpath); err != nil {
			return done, &MigrationError{Env: env, Moved: done, Journal: jpath,
				Err: fmt.Errorf("every file moved, but the journal could not be removed: %w", err)}
		}
		syncDir(filepath.Dir(jpath))
	}
	return done, nil
}

// resumeUnit finishes a unit an earlier start was interrupted in. Each
// recorded file must be at exactly one end (or at both as identical copies,
// which a crash inside a cross-device copy leaves); anything else is refused.
func resumeUnit(env, jpath string, j unitJournal) ([]StateMove, error) {
	slog.Warn("resuming an interrupted state migration", "env", env, "journal", jpath)
	var done []StateMove
	var rest []unitMove
	for i, m := range j.Moves {
		_, serr := os.Lstat(m.From)
		_, derr := os.Lstat(m.To)
		switch {
		case serr != nil && derr == nil:
			done = append(done, StateMove{Env: env, From: m.From, To: m.To})
		case serr == nil && derr != nil:
			rest = append(rest, m)
		case serr == nil && sameContent(m.From, m.To):
			if err := os.Remove(m.From); err != nil {
				return done, &MigrationError{Env: env, Moved: done, Pending: j.Moves[i:], Journal: jpath, Err: err}
			}
			done = append(done, StateMove{Env: env, From: m.From, To: m.To, Copied: true})
		default:
			what := "neither exists"
			if serr == nil {
				what = "both exist and differ"
			}
			return done, &MigrationError{Env: env, Moved: done, Pending: append(rest, j.Moves[i:]...), Journal: jpath,
				Err: fmt.Errorf("interrupted migration cannot resume: for %s -> %s %s; reconcile by hand, "+
					"then delete the journal", m.From, m.To, what)}
		}
	}
	return runUnit(env, jpath, rest, done...)
}

// errTornJournal: the journal exists but does not parse — a crash while it
// was being written, before any move.
var errTornJournal = errors.New("incomplete migration journal")

func readJournal(path string) (unitJournal, error) {
	var j unitJournal
	raw, err := os.ReadFile(path)
	if err != nil {
		return j, err
	}
	if json.Unmarshal(raw, &j) != nil || len(j.Moves) == 0 {
		return j, errTornJournal
	}
	return j, nil
}

// writeJournal durably records the unit before anything moves.
func writeJournal(path string, j unitJournal) error {
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir makes renames in dir durable. Best effort: some filesystems
// reject fsync on a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// sameContent reports whether a and b are regular files with equal bytes.
func sameContent(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ia, err1 := fa.Stat()
	ib, err2 := fb.Stat()
	if err1 != nil || err2 != nil || !ia.Mode().IsRegular() || !ib.Mode().IsRegular() || ia.Size() != ib.Size() {
		return false
	}
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea != nil || eb != nil {
			return (ea == io.EOF || ea == io.ErrUnexpectedEOF) && (eb == io.EOF || eb == io.ErrUnexpectedEOF)
		}
	}
}

// rename is os.Rename; tests swap it to inject a failure mid-unit.
var rename = os.Rename

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
	err = rename(src, dst)
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
