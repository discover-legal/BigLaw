// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package storetest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestSlotsBoundHolders: with n slots, an (n+1)th taker waits until a holder gives one back
// (PR 55 CI: 7566ba9's test stores exhausted Postgres's 100 connections).
func TestSlotsBoundHolders(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("no flock: slots do not bound")
	}
	name := fmt.Sprintf("biglaw-storetest-slots-test-%d", os.Getpid())
	t.Cleanup(func() { os.RemoveAll(filepath.Join(os.TempDir(), name)) })
	var releases []func()
	for range 2 {
		r, err := takeSlot(name, 2)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	got := make(chan func(), 1)
	go func() {
		r, err := takeSlot(name, 2)
		if err != nil {
			t.Error(err)
		}
		got <- r
	}()
	select {
	case <-got:
		t.Fatal("a third taker got one of 2 held slots")
	case <-time.After(200 * time.Millisecond):
	}
	releases[0]()
	select {
	case r := <-got:
		r()
	case <-time.After(5 * time.Second):
		t.Fatal("a slot given back was never taken")
	}
	releases[1]()
}
