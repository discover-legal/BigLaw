// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LockFileName is the single-writer lock inside the data dir.
const LockFileName = "biglaw.lock"

// ErrDataDirLocked reports that another live process owns the data dir.
// Test with errors.Is; the concrete error is a *LockedError naming the holder.
var ErrDataDirLocked = errors.New("data dir is owned by another BigLaw process")

// errLockUnsupported: the filesystem does not do advisory locks.
var errLockUnsupported = errors.New("advisory locks not supported")

// LockInfo is what an owner records in the lock file: who it is and where
// its REST API answers, so an auto-mode process attaches to the actual owner
// rather than to whatever BIG_MICHAEL_API happens to say.
type LockInfo struct {
	PID int    `json:"pid"`
	API string `json:"api,omitempty"`
}

// LockedError is returned by AcquireDataLock when the lock is held.
type LockedError struct {
	Path   string   // lock file
	Holder LockInfo // zero fields when the holder has not written them yet
	// ByRecord: the lock itself could not be taken (unsupported filesystem
	// or BIGLAW_LOCK=off), but the lock file names a live process.
	ByRecord bool
}

func (e *LockedError) Error() string {
	holder := "another process"
	if e.Holder.PID > 0 {
		holder = "pid " + strconv.Itoa(e.Holder.PID)
	}
	if e.Holder.API != "" {
		holder += ", API " + e.Holder.API
	}
	msg := fmt.Sprintf("%v (%s holds %s)", ErrDataDirLocked, holder, e.Path)
	if e.ByRecord {
		msg += "; this filesystem cannot lock, so that is going by the lock file's record of a " +
			"live process — if it is not a BigLaw owner of this data dir, delete the lock file"
	}
	return msg
}

func (e *LockedError) Unwrap() error { return ErrDataDirLocked }

// DataLock is the held single-writer lock on a data dir. Exactly one process
// may own a data dir's database, state files and in-process vector stores;
// every owner mode (backend, standalone, auto-as-owner) takes this lock
// before opening any of them. It is an OS advisory lock on an open file
// descriptor, released automatically when the process dies — a crashed owner
// never leaves a stale lock behind.
type DataLock struct {
	f        *os.File
	path     string
	enforced bool
}

// Path is the lock file's path.
func (l *DataLock) Path() string { return l.path }

// Enforced is false when the filesystem cannot lock (the process runs
// unguarded, with a warning logged).
func (l *DataLock) Enforced() bool { return l != nil && l.enforced }

// Release drops the lock. Safe to call on a nil lock and more than once.
// An unenforced owner also clears its record, which is all that announced
// it to the next process.
func (l *DataLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	if !l.enforced {
		_ = l.f.Truncate(0)
	}
	err := l.f.Close() // closing the descriptor releases the advisory lock
	l.f = nil
	return err
}

// LockMode says what AcquireDataLock may fall back to.
type LockMode int

const (
	// LockRequired: only a filesystem without advisory locks
	// (ENOLCK/ENOTSUP) may run unguarded; any other locking error is fatal.
	LockRequired LockMode = iota
	// LockBestEffort (BIGLAW_LOCK=off): any locking error runs unguarded.
	// A lock that is actually held still refuses — its holder is alive.
	LockBestEffort
)

// tryLockFn is tryLock; tests swap it to simulate an unlockable filesystem.
var tryLockFn = tryLock

// AcquireDataLock takes the exclusive single-writer lock on dataDir without
// blocking, creating the directory if needed, and records info (PID + REST
// URL) in it. When another live process holds it the error wraps
// ErrDataDirLocked (a *LockedError carrying the holder's LockInfo).
//
// When the lock cannot be taken at all (ENOLCK/ENOTSUP, e.g. some network
// mounts; or any error under LockBestEffort) the process runs unguarded,
// with a warning — but only after reading the lock file: a record naming a
// live process other than this one refuses (ErrDataDirLocked, ByRecord) and
// is left intact, so auto-mode clients keep finding the real owner.
func AcquireDataLock(dataDir string, info LockInfo, mode LockMode) (*DataLock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	path := filepath.Join(dataDir, LockFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	held, err := tryLockFn(f)
	switch {
	case held:
		holder := readInfo(f)
		f.Close()
		return nil, &LockedError{Path: path, Holder: holder}
	case err != nil && (errors.Is(err, errLockUnsupported) || mode == LockBestEffort):
		if holder := readInfo(f); holder.PID > 0 && holder.PID != info.PID && pidAlive(holder.PID) {
			f.Close()
			return nil, &LockedError{Path: path, Holder: holder, ByRecord: true}
		}
		slog.Warn("data-dir lock unavailable; running UNGUARDED — "+
			"make sure no other BigLaw process owns this data dir", "lock", path, "error", err)
		writeInfo(f, info)
		return &DataLock{f: f, path: path}, nil
	case err != nil:
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	writeInfo(f, info)
	return &DataLock{f: f, path: path, enforced: true}, nil
}

// ReadLockHolder returns what the current owner recorded in dataDir's lock
// file, polling briefly while a just-started owner has not written it yet.
func ReadLockHolder(dataDir string, wait time.Duration) LockInfo {
	deadline := time.Now().Add(wait)
	for {
		f, err := os.Open(filepath.Join(dataDir, LockFileName))
		var info LockInfo
		if err == nil {
			info = readInfo(f)
			f.Close()
		}
		if info.API != "" || !time.Now().Before(deadline) {
			return info
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func writeInfo(f *os.File, info LockInfo) {
	raw, _ := json.Marshal(info)
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt(append(raw, '\n'), 0)
	}
}

func readInfo(f *os.File) LockInfo {
	buf := make([]byte, 4096)
	n, _ := f.ReadAt(buf, 0)
	text := strings.TrimSpace(string(buf[:n]))
	var info LockInfo
	if json.Unmarshal([]byte(text), &info) == nil {
		return info
	}
	info.PID, _ = strconv.Atoi(text) // pre-JSON lock files held a bare PID
	return info
}
