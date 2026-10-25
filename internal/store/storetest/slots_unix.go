// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build unix

package storetest

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// takeSlot waits for an exclusive flock on one of n files in a shared temp
// directory. The kernel drops the lock when the process dies, so a crashed
// test never keeps its slot.
func takeSlot(name string, n int) (func(), error) {
	dir := filepath.Join(os.TempDir(), name)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	files := make([]*os.File, n)
	for i := range files {
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("slot-%d", i)), os.O_CREATE|os.O_RDWR, 0o666)
		if err != nil {
			for _, g := range files[:i] {
				g.Close()
			}
			return nil, err
		}
		files[i] = f
	}
	for {
		for i, f := range files {
			if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
				for j, g := range files {
					if j != i {
						g.Close()
					}
				}
				return func() { f.Close() }, nil // closing the file drops its lock
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}
