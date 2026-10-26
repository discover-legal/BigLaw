// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build !linux

package docs

import (
	"os"
	"syscall"
)

// Outside Linux the child has no parent-death signal (its parent-pid watchdog still ends it) and
// no hard memory cap, and the child is the binary at this process's path (README Limitations).
func childAttr() *syscall.SysProcAttr { return nil }
func limitMemory(uint64)              {}
func selfExe() (string, error)        { return os.Executable() }
