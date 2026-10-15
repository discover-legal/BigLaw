// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build unix

package backend

import (
	"errors"
	"fmt"
	"os"
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
