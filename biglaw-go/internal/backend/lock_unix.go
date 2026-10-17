// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build unix

package backend

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// tryLock takes a non-blocking exclusive flock. held reports that another
// open file description already holds it (EWOULDBLOCK) — an explicit errno
// check, not error-string matching. ENOLCK / ENOTSUP map to
// errLockUnsupported.
func tryLock(f *os.File) (held bool, err error) {
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return true, nil
	case errors.Is(err, syscall.ENOLCK), errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP):
		return false, fmt.Errorf("%w: %v", errLockUnsupported, err)
	}
	return false, err
}

// pidAlive reports whether pid names a running process (EPERM: it exists
// but belongs to someone else — still alive). A zombie — dead, not yet
// reaped, which kill(0) still finds — is not alive (Linux /proc check).
func pidAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true // no procfs (macOS): kill(0) is all there is
	}
	// "pid (comm) S ...": the state follows the last ')' (comm may hold one).
	if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z'
	}
	return true
}
