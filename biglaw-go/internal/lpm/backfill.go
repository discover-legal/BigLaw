// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Historical email backfill — the "run it for ages on the box" piece. It pages
// backwards through the mail archive one date window at a time, routing each old
// message through the same router and into the same store as the live intake.
// It is:
//   - resumable: a small cursor file records how far back it has reached, so a
//     restart continues rather than starting over;
//   - rate-limited: a configurable pause between windows keeps load low on cheap
//     hardware so it can grind for days without disrupting the daily reports;
//   - idempotent: dedup by message ID means overlap with the live window is free.
package lpm

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/email"
)

// WindowedSource fetches messages received between newerDays and olderDays ago
// (olderDays == 0 means no upper bound).
type WindowedSource interface {
	Window(query string, maxResults, newerDays, olderDays int) ([]email.Message, error)
}

type defaultWindowedSource struct{}

func (defaultWindowedSource) Window(query string, maxResults, newerDays, olderDays int) ([]email.Message, error) {
	var out []email.Message
	if g, err := email.SearchGraphMailWindow(query, maxResults, newerDays, olderDays); err == nil {
		out = append(out, g...)
	}
	if gm, err := email.SearchGmailWindow(query, maxResults, newerDays, olderDays); err == nil {
		out = append(out, gm...)
	}
	return out, nil
}

// BackfillConfig configures a backfill run.
type BackfillConfig struct {
	WindowDays int    // total history to cover
	StepDays   int    // window size per step
	MaxPerStep int    // page size per step
	PauseMs    int    // pause between steps (rate limit)
	CursorFile string // resumable progress
	Query      string // optional search scope
}

// backfillCursor is the persisted, resumable progress marker.
type backfillCursor struct {
	CoveredDays int    `json:"coveredDays"` // days back already processed
	Done        bool   `json:"done"`
	Routed      int    `json:"routed"`
	Processed   int    `json:"processed"`
	UpdatedAt   string `json:"updatedAt"`
}

// Backfill grinds the historical mail archive.
type Backfill struct {
	src     WindowedSource
	router  *Router
	store   *RoutedStore
	matters MatterLister
	cfg     BackfillConfig
	pause   time.Duration
	mu      sync.Mutex
	stop    chan struct{}
	stopped bool
}

// NewBackfill builds a backfill driver. src may be nil to use the default
// windowed providers.
func NewBackfill(cfg BackfillConfig, src WindowedSource, router *Router, store *RoutedStore, matters MatterLister) *Backfill {
	if src == nil {
		src = defaultWindowedSource{}
	}
	if cfg.WindowDays <= 0 {
		cfg.WindowDays = 365
	}
	if cfg.StepDays <= 0 {
		cfg.StepDays = 7
	}
	if cfg.MaxPerStep <= 0 {
		cfg.MaxPerStep = 100
	}
	pause := time.Duration(cfg.PauseMs) * time.Millisecond
	if pause < 0 {
		pause = 0
	}
	return &Backfill{src: src, router: router, store: store, matters: matters, cfg: cfg, pause: pause, stop: make(chan struct{})}
}

// Start runs the backfill loop until complete or stopped.
func (b *Backfill) Start() {
	go func() {
		for {
			select {
			case <-b.stop:
				return
			default:
			}
			done, _, err := b.Step()
			if err != nil {
				slog.Warn("LPM backfill step failed", "error", err)
			}
			if done {
				slog.Info("LPM backfill complete")
				return
			}
			select {
			case <-b.stop:
				return
			case <-time.After(b.pause):
			}
		}
	}()
	slog.Info("LPM backfill started", "windowDays", b.cfg.WindowDays, "stepDays", b.cfg.StepDays)
}

// Stop halts the backfill loop (progress is preserved in the cursor file).
func (b *Backfill) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.stopped {
		close(b.stop)
		b.stopped = true
	}
}

// Step processes exactly one date window and advances the cursor. It returns
// done=true once the whole window has been covered. Exposed for testing.
func (b *Backfill) Step() (bool, int, error) {
	cur := b.loadCursor()
	if cur.Done || cur.CoveredDays >= b.cfg.WindowDays {
		cur.Done = true
		b.saveCursor(cur)
		return true, 0, nil
	}

	newerDays := cur.CoveredDays + b.cfg.StepDays
	if newerDays > b.cfg.WindowDays {
		newerDays = b.cfg.WindowDays
	}
	olderDays := cur.CoveredDays // 0 on the first step → no upper bound, capped by newerDays

	msgs, err := b.src.Window(b.cfg.Query, b.cfg.MaxPerStep, newerDays, olderDays)
	if err != nil {
		return false, 0, err
	}

	matters := b.matters()
	routed := 0
	for _, m := range msgs {
		if m.ID == "" || b.store.Seen(m.ID) {
			continue
		}
		res := b.router.Route(m, matters)
		rec := RoutedEmail{
			MessageID: m.ID, Subject: m.Subject, From: m.From, ReceivedAt: m.ReceivedAt,
			Provider: m.Provider, MatterNumber: res.MatterNumber, Confidence: res.Confidence, Method: res.Method,
		}
		if err := b.store.Append(rec); err != nil {
			continue
		}
		cur.Processed++
		if res.Method != RouteUnrouted {
			routed++
		}
	}

	cur.CoveredDays = newerDays
	cur.Routed += routed
	cur.Done = cur.CoveredDays >= b.cfg.WindowDays
	b.saveCursor(cur)
	return cur.Done, routed, nil
}

func (b *Backfill) loadCursor() backfillCursor {
	var c backfillCursor
	data, err := os.ReadFile(b.cfg.CursorFile)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, &c)
	return c
}

func (b *Backfill) saveCursor(c backfillCursor) {
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(b.cfg.CursorFile), 0o755); err != nil {
		return
	}
	tmp := b.cfg.CursorFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, b.cfg.CursorFile)
}
