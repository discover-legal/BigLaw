// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/email"
)

// windowFake returns a distinct message per window so we can assert paging.
type windowFake struct {
	calls   int
	windows [][2]int // recorded (newerDays, olderDays) per call
}

func (w *windowFake) Window(_ string, _ int, newerDays, olderDays int) ([]email.Message, error) {
	w.windows = append(w.windows, [2]int{newerDays, olderDays})
	id := fmt.Sprintf("msg-%d", w.calls)
	w.calls++
	return []email.Message{{ID: id, MatterRef: "M-001", Subject: "[M-001] x", Provider: "graph"}}, nil
}

func newBackfill(t *testing.T, src WindowedSource, cfg BackfillConfig) (*Backfill, *RoutedStore) {
	t.Helper()
	if cfg.CursorFile == "" {
		cfg.CursorFile = filepath.Join(t.TempDir(), "cursor.json")
	}
	store := NewRoutedStore(filepath.Join(t.TempDir(), "routed.jsonl"))
	_ = store.Init()
	router := NewRouter(&fakeProvider{}, "m", 0.6)
	b := NewBackfill(cfg, src, router, store, func() []MatterOption { return roster })
	return b, store
}

func TestBackfillStepsThroughWindowsAndCompletes(t *testing.T) {
	src := &windowFake{}
	b, store := newBackfill(t, src, BackfillConfig{WindowDays: 21, StepDays: 7})

	// 21 days / 7-day steps = 3 steps.
	for i := 0; i < 3; i++ {
		done, routed, err := b.Step()
		if err != nil {
			t.Fatal(err)
		}
		if routed != 1 {
			t.Errorf("step %d routed %d, want 1", i, routed)
		}
		if i < 2 && done {
			t.Errorf("step %d should not be done yet", i)
		}
		if i == 2 && !done {
			t.Error("final step should report done")
		}
	}

	// Windows must page from recent to old without gaps: (7,0),(14,7),(21,14).
	want := [][2]int{{7, 0}, {14, 7}, {21, 14}}
	for i, w := range want {
		if src.windows[i] != w {
			t.Errorf("window %d = %v, want %v", i, src.windows[i], w)
		}
	}

	// Further steps are no-ops once done.
	done, _, _ := b.Step()
	if !done {
		t.Error("post-completion step should remain done")
	}
	if store.CountForMatter("M-001", time.Time{}) != 3 {
		t.Errorf("expected 3 routed records, got %d", store.CountForMatter("M-001", time.Time{}))
	}
}

func TestBackfillResumesFromCursor(t *testing.T) {
	cursor := filepath.Join(t.TempDir(), "cursor.json")
	src := &windowFake{}
	b1, _ := newBackfill(t, src, BackfillConfig{WindowDays: 21, StepDays: 7, CursorFile: cursor})

	if _, _, err := b1.Step(); err != nil { // covers first 7 days
		t.Fatal(err)
	}

	// A fresh backfill over the same cursor file resumes where the first left off.
	src2 := &windowFake{}
	b2, _ := newBackfill(t, src2, BackfillConfig{WindowDays: 21, StepDays: 7, CursorFile: cursor})
	if _, _, err := b2.Step(); err != nil {
		t.Fatal(err)
	}
	// The resumed step must start at the 7→14 window, not restart at 0→7.
	if src2.windows[0] != [2]int{14, 7} {
		t.Errorf("resume window = %v, want {14, 7}", src2.windows[0])
	}
}
