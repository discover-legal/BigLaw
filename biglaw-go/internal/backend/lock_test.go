// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build unix

package backend

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDataLockIsExclusiveAndNamesTheOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data") // created on demand
	me := LockInfo{PID: os.Getpid(), API: "http://127.0.0.1:3161"}

	first, err := AcquireDataLock(dir, me, LockRequired)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !first.Enforced() {
		t.Fatal("lock should be enforced on a local filesystem")
	}

	// flock is per open file description, so a second acquire from this same
	// process conflicts exactly as another process would.
	_, err = AcquireDataLock(dir, LockInfo{PID: 1, API: "http://127.0.0.1:9"}, LockRequired)
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
	again, err := AcquireDataLock(dir, me, LockRequired)
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
	_, err := AcquireDataLock(filepath.Join(file, "data"), LockInfo{}, LockRequired)
	if err == nil || errors.Is(err, ErrDataDirLocked) {
		t.Fatalf("want a non-lock error, got %v", err)
	}
}

// BIGLAW_LOCK=off still tries the lock: one that is held means a live
// owner, and the second process refuses rather than share the data dir.
func TestBestEffortLockStillRefusesAHeldLock(t *testing.T) {
	dir := t.TempDir()
	owner := LockInfo{PID: os.Getpid(), API: "http://127.0.0.1:3163"}
	first, err := AcquireDataLock(dir, owner, LockRequired)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	_, err = AcquireDataLock(dir, LockInfo{PID: os.Getpid() + 1}, LockBestEffort)
	var le *LockedError
	if !errors.As(err, &le) || le.Holder != owner {
		t.Fatalf("want the held lock to refuse under BIGLAW_LOCK=off, got %v", err)
	}
}

func unlockable(t *testing.T, err error) {
	t.Helper()
	tryLockFn = func(*os.File) (bool, error) { return false, err }
	t.Cleanup(func() { tryLockFn = tryLock })
}

// Without working locks (ENOLCK), the lock file's record is all that
// announces an owner: a live one refuses the newcomer and is not
// overwritten; a dead one is taken over.
func TestUnguardedLockHonoursALiveRecord(t *testing.T) {
	unlockable(t, fmt.Errorf("%w: %v", errLockUnsupported, syscall.ENOLCK))
	dir := t.TempDir()

	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Process.Kill(); _ = live.Wait() }()
	record := fmt.Sprintf(`{"pid":%d,"api":"http://127.0.0.1:3185"}`+"\n", live.Process.Pid)
	lockPath := filepath.Join(dir, LockFileName)
	if err := os.WriteFile(lockPath, []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	me := LockInfo{PID: os.Getpid(), API: "http://127.0.0.1:3186"}
	_, err := AcquireDataLock(dir, me, LockRequired)
	var le *LockedError
	if !errors.As(err, &le) || !le.ByRecord || le.Holder.PID != live.Process.Pid {
		t.Fatalf("want a by-record refusal naming the live owner, got %v", err)
	}
	if b, _ := os.ReadFile(lockPath); string(b) != record {
		t.Fatalf("the live owner's record was overwritten: %q", b)
	}

	// The owner exits: its record is stale and the data dir is free.
	_ = live.Process.Kill()
	_ = live.Wait()
	l, err := AcquireDataLock(dir, me, LockRequired)
	if err != nil || l.Enforced() {
		t.Fatalf("want an unguarded lock over a dead record, got %v / %+v", err, l)
	}
	if got := ReadLockHolder(dir, 0); got != me {
		t.Fatalf("record = %+v", got)
	}
	// Our own earlier record (a restart reusing the PID) is not a conflict.
	again, err := AcquireDataLock(dir, me, LockRequired)
	if err != nil {
		t.Fatal(err)
	}
	again.Release()
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if got := ReadLockHolder(dir, 0); got.PID != 0 {
		t.Fatalf("an unguarded owner clears its record on release: %+v", got)
	}
}

// Under LockRequired an unexpected locking error is fatal; under
// BIGLAW_LOCK=off it runs unguarded.
func TestBestEffortToleratesLockErrors(t *testing.T) {
	unlockable(t, syscall.EIO)
	dir := t.TempDir()
	if _, err := AcquireDataLock(dir, LockInfo{PID: os.Getpid()}, LockRequired); err == nil || errors.Is(err, ErrDataDirLocked) {
		t.Fatalf("want a hard error, got %v", err)
	}
	l, err := AcquireDataLock(dir, LockInfo{PID: os.Getpid()}, LockBestEffort)
	if err != nil || l.Enforced() {
		t.Fatalf("want an unguarded lock, got %v", err)
	}
	l.Release()
}

// A crashed owner its parent has not reaped yet is dead, not an owner.
func TestPIDAliveTreatsZombiesAsDead(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no procfs")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if !pidAlive(pid) {
		t.Fatal("a running process must be alive")
	}
	_ = cmd.Process.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(pid) { // not reaped: a zombie until Wait
		if time.Now().After(deadline) {
			t.Fatal("a killed, unreaped process still counts as alive")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = cmd.Wait()
}
