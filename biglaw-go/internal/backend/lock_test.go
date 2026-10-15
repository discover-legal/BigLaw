// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build unix

package backend

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDataLockIsExclusiveAndReleasable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data") // created on demand

	first, err := AcquireDataLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// flock is per open file description, so a second acquire from this same
	// process conflicts exactly as another process would.
	_, err = AcquireDataLock(dir)
	if !errors.Is(err, ErrDataDirLocked) {
		t.Fatalf("second acquire: want ErrDataDirLocked, got %v", err)
	}
	var le *LockedError
	if !errors.As(err, &le) || le.HolderPID != os.Getpid() {
		t.Fatalf("locked error should name holder pid %d: %#v", os.Getpid(), err)
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil { // idempotent
		t.Fatal(err)
	}
	again, err := AcquireDataLock(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	defer again.Release()
}

func TestDataLockOtherErrorsAreNotLockContention(t *testing.T) {
	// A data dir path that cannot be created is a hard error, never
	// mistaken for "another owner is running" (which would make auto mode
	// silently become a client).
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireDataLock(filepath.Join(file, "data"))
	if err == nil || errors.Is(err, ErrDataDirLocked) {
		t.Fatalf("want a non-lock error, got %v", err)
	}
}
