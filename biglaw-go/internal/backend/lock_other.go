// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build !unix

package backend

import (
	"log/slog"
	"os"
)

// tryLock on platforms without flock (BigLaw ships for Linux/macOS/ARM SBCs)
// always succeeds: the data dir is unguarded, so do not run two owners there.
func tryLock(*os.File) (bool, error) {
	slog.Warn("data-dir single-writer lock is not supported on this platform; " +
		"run at most one owning BigLaw process per data dir")
	return false, nil
}
