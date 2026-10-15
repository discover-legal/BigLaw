// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build unix

package backend

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDataLockIsExclusiveAndNamesTheOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data") // created on demand
	me := LockInfo{PID: os.Getpid(), API: "http://127.0.0.1:3161"}

	first, err := AcquireDataLock(dir, me)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !first.Enforced() {
		t.Fatal("lock should be enforced on a local filesystem")
	}

	// flock is per open file description, so a second acquire from this same
	// process conflicts exactly as another process would.
	_, err = AcquireDataLock(dir, LockInfo{PID: 1, API: "http://127.0.0.1:9"})
	if !errors.Is(err, ErrDataDirLocked) {
		t.Fatalf("second acquire: want ErrDataDirLocked, got %v", err)
	}
	var le *LockedError
	if !errors.As(err, &le) || le.Holder != me {
		t.Fatalf("locked error should carry the owner's info %+v: %#v", me, err)
	}
	if got := ReadLockHolder(dir, 0); got != me {
		t.Fatalf("ReadLockHolder = %+v", got)
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil { // idempotent
		t.Fatal(err)
	}
	again, err := AcquireDataLock(dir, me)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	defer again.Release()
}

func TestLockReadsLegacyPIDOnlyFormat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, LockFileName), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadLockHolder(dir, 10*time.Millisecond); got.PID != 4242 || got.API != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestDataLockOtherErrorsAreNotLockContention(t *testing.T) {
	// A data dir path that cannot be created is a hard error, never
	// mistaken for "another owner is running" (which would make auto mode
	// silently become a client).
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireDataLock(filepath.Join(file, "data"), LockInfo{})
	if err == nil || errors.Is(err, ErrDataDirLocked) {
		t.Fatalf("want a non-lock error, got %v", err)
	}
}
