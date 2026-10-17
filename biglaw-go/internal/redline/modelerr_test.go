// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package redline

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// scripted replays one reply per Chat call: an error, or a text block.
type scripted struct {
	mu      sync.Mutex
	replies []interface{} // string | error
	calls   int
}

func (s *scripted) Chat(providers.ChatParams) (*providers.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls >= len(s.replies) {
		s.calls++
		return nil, errors.New("scripted: no more replies")
	}
	r := s.replies[s.calls]
	s.calls++
	if err, ok := r.(error); ok {
		return nil, err
	}
	return &providers.ChatResponse{Content: []providers.ContentBlock{{Type: providers.BlockText, Text: r.(string)}}}, nil
}

func newStore(t *testing.T, withPlaybook bool) *playbook.Store {
	t.Helper()
	st := playbook.New(filepath.Join(t.TempDir(), "playbooks.json"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if withPlaybook {
		st.Upsert(types.Playbook{
			ID: "pb1", Name: "Firm", Scope: types.PlaybookScopeFirm,
			Entries: []types.PlaybookEntry{
				{ClauseType: "Governing law", StandardPosition: "England and Wales"},
				{ClauseType: "Limitation of liability", StandardPosition: "Cap at fees paid"},
			},
		})
	}
	return st
}

const doc = "1. Governing law. This agreement is governed by the laws of Delaware."

const clausesJSON = `{"clauses":[{"clauseType":"Governing law","text":"governed by the laws of Delaware"}]}`
const verdictsJSON = `{"verdicts":[{"clauseIndex":1,"action":"redline","severity":"high","proposedText":"England and Wales","rationale":"r","isRedLine":false}]}`

func TestRedlineModelFailuresReturnErrorNotReport(t *testing.T) {
	upstream := errors.New("ollama: HTTP 500: overloaded")
	cases := []struct {
		name       string
		replies    []interface{}
		withPB     bool
		noProvider bool
	}{
		{name: "extraction call fails", replies: []interface{}{upstream}},
		{name: "extraction provider unconfigured", replies: []interface{}{providers.ModelFailure("x", errors.Join(providers.ErrNoProvider))}, noProvider: true},
		{name: "extraction unparseable", replies: []interface{}{"I cannot help with that."}},
		{name: "clause analysis call fails", replies: []interface{}{clausesJSON, upstream}},
		{name: "clause analysis unparseable", replies: []interface{}{clausesJSON, "garbage"}},
		{name: "missing-clause call fails", replies: []interface{}{clausesJSON, verdictsJSON, upstream}, withPB: true},
		{name: "missing-clause unparseable", replies: []interface{}{clausesJSON, verdictsJSON, "no idea"}, withPB: true},
		{name: "summary call fails", replies: []interface{}{clausesJSON, verdictsJSON, upstream}},
		{name: "summary empty", replies: []interface{}{clausesJSON, verdictsJSON, "   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := New(&scripted{replies: tc.replies}, "mid", "light")
			rep, err := eng.Redline(doc, newStore(t, tc.withPB), RedlineOpts{})
			if err == nil {
				t.Fatalf("want error, got report %+v", rep)
			}
			if rep != nil {
				t.Fatalf("a failed run must not return a report: %+v", rep)
			}
			want := providers.ErrModelUnavailable
			if tc.noProvider {
				want = providers.ErrNoProvider
			}
			if !errors.Is(err, want) {
				t.Fatalf("err = %v, want errors.Is %v", err, want)
			}
		})
	}
}

func TestRedlineHappyPathStillWorks(t *testing.T) {
	missing := `{"missing":[{"clauseType":"Limitation of liability","suggestedText":"Cap.","rationale":"exposure","severity":"high"}]}`
	eng := New(&scripted{replies: []interface{}{clausesJSON, verdictsJSON, missing, "Summary."}}, "mid", "light")
	rep, err := eng.Redline(doc, newStore(t, true), RedlineOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalClauses != 1 || rep.RedlineCount != 1 || rep.MissingCount != 1 || rep.ExecutiveSummary != "Summary." {
		t.Fatalf("unexpected report: %+v", rep)
	}
}

// A valid reply with zero clauses is "nothing was reviewed" (ErrNoContent →
// 422), never a zero-issue report.
func TestRedlineZeroClausesIsNoContent(t *testing.T) {
	rep, err := New(&scripted{replies: []interface{}{`{"clauses":[]}`}}, "mid", "light").Redline(doc, newStore(t, false), RedlineOpts{})
	if rep != nil || !errors.Is(err, providers.ErrNoContent) {
		t.Fatalf("want (nil, ErrNoContent), got (%+v, %v)", rep, err)
	}
}

// A valid verdict reply that skips a clause escalates just that clause (a
// disclosed non-verdict); the clauses it did judge keep their verdicts.
func TestRedlineSkippedClauseEscalates(t *testing.T) {
	two := `{"clauses":[{"clauseType":"Governing law","text":"Delaware"},{"clauseType":"Payment","text":"90 days"}]}`
	onlyFirst := `{"verdicts":[{"clauseIndex":1,"action":"accept","severity":"low","rationale":"ok","isRedLine":false}]}`
	rep, err := New(&scripted{replies: []interface{}{two, onlyFirst, "Summary."}}, "mid", "light").Redline(doc, newStore(t, false), RedlineOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Issues) != 2 || rep.Issues[0].Action != ActionAccept || rep.Issues[1].Action != ActionEscalate {
		t.Fatalf("want [accept, escalate], got %+v", rep.Issues)
	}
}

// Cancelling the caller's context aborts the run with context.Canceled.
func TestRedlineHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var seen context.Context
	p := providerFunc(func(cp providers.ChatParams) (*providers.ChatResponse, error) {
		seen = cp.Ctx
		return nil, cp.Ctx.Err()
	})
	_, err := New(p, "mid", "light").Redline(doc, newStore(t, false), RedlineOpts{Ctx: ctx})
	if seen != ctx || !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx not propagated or cancel lost: seen=%v err=%v", seen, err)
	}
}

type providerFunc func(providers.ChatParams) (*providers.ChatResponse, error)

func (f providerFunc) Chat(p providers.ChatParams) (*providers.ChatResponse, error) { return f(p) }
