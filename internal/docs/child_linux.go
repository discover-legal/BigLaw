// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import "syscall"

// childAttr kills the reader child when the thread that started it dies (the parent's death).
func childAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} }

// limitMemory caps this process's data segment: the Go heap and stacks are private writable
// mappings, so an allocation past n fails and the runtime exits with "out of memory".
func limitMemory(n uint64) {
	_ = syscall.Setrlimit(syscall.RLIMIT_DATA, &syscall.Rlimit{Cur: n, Max: n})
}
