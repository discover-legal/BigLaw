// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Email intake — polls the configured mailbox(es) on an interval, routes each new
// message to a matter, and records the decision. The intake mode (shared_inbox |
// polling | both) only shapes the query; setting it to "polling" drops the
// shared-inbox dependency with no code change. Dedup is by message ID via the
// RoutedStore, so re-polling is safe and idempotent.
package lpm

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/email"
)

// EmailSource fetches recent messages. The default implementation merges the
// Graph and Gmail search clients; tests substitute a fake.
type EmailSource interface {
	Recent(query string, maxResults, daysBack int) ([]email.Message, error)
}

// defaultEmailSource queries both providers and merges the results. Unconfigured
// providers return nothing rather than erroring, so this is always safe to call.
type defaultEmailSource struct{}

func (defaultEmailSource) Recent(query string, maxResults, daysBack int) ([]email.Message, error) {
	var out []email.Message
	if g, err := email.SearchGraphMail(query, maxResults, daysBack); err == nil {
		out = append(out, g...)
	}
	if gm, err := email.SearchGmail(query, maxResults, daysBack); err == nil {
		out = append(out, gm...)
	}
	return out, nil
}

// MatterLister supplies the current roster of routable matters.
type MatterLister func() []MatterOption

// Intake drives the poll → route → record loop.
type Intake struct {
	src      EmailSource
	router   *Router
	store    *RoutedStore
	matters  MatterLister
	interval time.Duration
	query    string
	daysBack int
	maxBatch int
	stop     chan struct{}
	stopOnce sync.Once
}

// IntakeConfig configures an Intake.
type IntakeConfig struct {
	IntakeMode  string // shared_inbox | polling | both
	SharedInbox string
	IntervalMin int
	Query       string // optional override; defaults to a broad recent-mail query
	DaysBack    int
	MaxBatch    int
}

// NewIntake builds an intake driver. src may be nil to use the default providers.
func NewIntake(cfg IntakeConfig, src EmailSource, router *Router, store *RoutedStore, matters MatterLister) *Intake {
	if src == nil {
		src = defaultEmailSource{}
	}
	interval := time.Duration(cfg.IntervalMin) * time.Minute
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	daysBack := cfg.DaysBack
	if daysBack <= 0 {
		daysBack = 2
	}
	maxBatch := cfg.MaxBatch
	if maxBatch <= 0 {
		maxBatch = 50
	}
	return &Intake{
		src:      src,
		router:   router,
		store:    store,
		matters:  matters,
		interval: interval,
		query:    buildIntakeQuery(cfg),
		daysBack: daysBack,
		maxBatch: maxBatch,
		stop:     make(chan struct{}),
	}
}

// buildIntakeQuery scopes the search by intake mode. For shared-inbox modes the
// query targets mail addressed to the shared inbox; pure polling uses a broad
// recent-mail query.
func buildIntakeQuery(cfg IntakeConfig) string {
	if cfg.Query != "" {
		return cfg.Query
	}
	if (cfg.IntakeMode == "shared_inbox" || cfg.IntakeMode == "both") && cfg.SharedInbox != "" {
		return "to:" + cfg.SharedInbox
	}
	return "" // broad: recent mail in the configured mailbox
}

// Start runs the polling loop until Stop is called.
func (i *Intake) Start() {
	go func() {
		t := time.NewTicker(i.interval)
		defer t.Stop()
		i.runOnce()
		for {
			select {
			case <-i.stop:
				return
			case <-t.C:
				i.runOnce()
			}
		}
	}()
	slog.Info("LPM email intake started", "interval", i.interval, "query", redactQuery(i.query))
}

// Stop halts the polling loop (idempotent).
func (i *Intake) Stop() { i.stopOnce.Do(func() { close(i.stop) }) }

func (i *Intake) runOnce() {
	routed, err := i.PollOnce()
	if err != nil {
		slog.Warn("LPM intake poll failed", "error", err)
		return
	}
	if routed > 0 {
		slog.Info("LPM intake routed messages", "count", routed)
	}
}

// PollOnce fetches a batch, routes new messages, and records the decisions.
// Returns the number of messages confidently routed to a matter.
func (i *Intake) PollOnce() (int, error) {
	msgs, err := i.src.Recent(i.query, i.maxBatch, i.daysBack)
	if err != nil {
		return 0, err
	}
	matters := i.matters()
	routed := 0
	for _, m := range msgs {
		if m.ID == "" || i.store.Seen(m.ID) {
			continue
		}
		res := i.router.Route(m, matters)
		rec := RoutedEmail{
			MessageID:    m.ID,
			Subject:      m.Subject,
			From:         m.From,
			ReceivedAt:   m.ReceivedAt,
			Provider:     m.Provider,
			MatterNumber: res.MatterNumber,
			Confidence:   res.Confidence,
			Method:       res.Method,
		}
		if err := i.store.Append(rec); err != nil {
			slog.Warn("LPM intake store append failed", "error", err)
			continue
		}
		if res.Method != RouteUnrouted {
			routed++
		}
	}
	return routed, nil
}

// redactQuery avoids logging a full shared-inbox address.
func redactQuery(q string) string {
	if strings.HasPrefix(q, "to:") {
		return "to:<redacted>"
	}
	if q == "" {
		return "(recent mail)"
	}
	return q
}
