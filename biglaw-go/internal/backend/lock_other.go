// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build !unix

package backend

import "os"

// tryLock on platforms without flock (BigLaw ships for Linux/macOS/ARM SBCs):
// the data dir runs unguarded, with a warning from AcquireDataLock.
func tryLock(*os.File) (bool, error) {
	return false, errLockUnsupported
}
