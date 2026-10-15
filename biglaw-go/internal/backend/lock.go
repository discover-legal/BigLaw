// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LockFileName is the single-writer lock inside the data dir.
const LockFileName = "biglaw.lock"

// ErrDataDirLocked reports that another live process owns the data dir.
// Test with errors.Is; the concrete error is a *LockedError naming the holder.
var ErrDataDirLocked = errors.New("data dir is owned by another BigLaw process")

// LockedError is returned by AcquireDataLock when the lock is held.
type LockedError struct {
	Path      string // lock file
	HolderPID int    // 0 when the holder's PID could not be read
}

func (e *LockedError) Error() string {
	holder := "another process"
	if e.HolderPID > 0 {
		holder = "pid " + strconv.Itoa(e.HolderPID)
	}
	return fmt.Sprintf("%v (%s holds %s)", ErrDataDirLocked, holder, e.Path)
}

func (e *LockedError) Unwrap() error { return ErrDataDirLocked }

// DataLock is the held single-writer lock on a data dir. Exactly one process
// may own a data dir's SQLite file, JSON state files and in-process vector
// stores; every owner mode (backend, standalone, auto-as-owner) takes this
// lock before opening any of them. The lock is an OS advisory lock on an open
// file descriptor, so it is released automatically when the process dies —
// a crashed owner never leaves a stale lock behind.
type DataLock struct {
	f    *os.File
	path string
}

// Path is the lock file's path.
func (l *DataLock) Path() string { return l.path }

// Release drops the lock. Safe to call on a nil lock and more than once.
func (l *DataLock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close() // closing the descriptor releases the advisory lock
	l.f = nil
	return err
}

// AcquireDataLock takes the exclusive single-writer lock on dataDir without
// blocking, creating the directory if needed. When another live process holds
// it the error wraps ErrDataDirLocked (a *LockedError naming the holder's
// PID); any other error means the lock could not be attempted at all.
func AcquireDataLock(dataDir string) (*DataLock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	path := filepath.Join(dataDir, LockFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	held, err := tryLock(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if held {
		pid := readPID(f)
		f.Close()
		return nil, &LockedError{Path: path, HolderPID: pid}
	}
	// Record our PID for the diagnostics of whoever loses the race next.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &DataLock{f: f, path: path}, nil
}

func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	return pid
}
