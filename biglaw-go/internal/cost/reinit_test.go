// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package cost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Init → Close → Init (a demo re-run, tests at -count>1) and Init → Init
// keep exactly one writer: no "close of closed channel", no lost or doubled
// ledger lines, and re-Init reloads rather than appending to memory.
func TestReInitKeepsOneWriter(t *testing.T) {
	dir := t.TempDir()
	s := &Store{writeCh: make(chan CostEntry, 16)}
	a, b := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	if err := s.Init(a); err != nil {
		t.Fatal(err)
	}
	s.Record(RecordRequest{Model: "claude-haiku-4-5", Context: ContextTask})
	s.Close()
	if err := s.Init(a); err != nil { // after Close
		t.Fatal(err)
	}
	if err := s.Init(b); err != nil { // while running
		t.Fatal(err)
	}
	s.Record(RecordRequest{Model: "claude-haiku-4-5", Context: ContextTask})
	s.Close()
	s.Close()
	for path, want := range map[string]int{a: 1, b: 1} {
		raw, _ := os.ReadFile(path)
		if n := strings.Count(string(raw), "\n"); n != want {
			t.Errorf("%s has %d lines, want %d", filepath.Base(path), n, want)
		}
	}
	if got := len(s.ForTask("")); got != 1 {
		t.Errorf("in-memory entries after re-Init = %d, want 1 (ledger b)", got)
	}
}
