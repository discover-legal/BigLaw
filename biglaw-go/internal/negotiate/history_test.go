// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Judge-memory tests: history lookup is case-insensitive, the rendered block
// caps at the last historyMaxRounds rounds and historyTokenBudget tokens
// (newest rounds always survive), and Decide threads the block into the judge
// prompt, surfacing historyRounds and the judge's escalation on the decision.

package negotiate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

func TestHistoryForIsCaseInsensitive(t *testing.T) {
	h := History{}
	h.Add("Limitation of Liability", HistoryEntry{Round: 1, Disposition: "counter"})
	if got := h.For("limitation of liability"); len(got) != 1 {
		t.Errorf("case-insensitive lookup returned %d entries, want 1", len(got))
	}
	if got := h.For("  LIMITATION OF LIABILITY "); len(got) != 1 {
		t.Errorf("trimmed lookup returned %d entries, want 1", len(got))
	}
	if got := h.For("Governing law"); got != nil {
		t.Errorf("unknown clause returned %d entries, want nil", len(got))
	}
	var nilHist History
	if got := nilHist.For("anything"); got != nil {
		t.Error("nil History must return nil, not panic")
	}
}

// TestFormatHistoryCapsRounds: six rounds in, only the newest three render.
func TestFormatHistoryCapsRounds(t *testing.T) {
	var entries []HistoryEntry
	for r := 1; r <= 6; r++ {
		entries = append(entries, HistoryEntry{
			Round: r, TheirMove: fmt.Sprintf("move %d", r), Disposition: "reject",
		})
	}
	out := formatHistory(entries)
	for r := 1; r <= 3; r++ {
		if strings.Contains(out, fmt.Sprintf("[round %d]", r)) {
			t.Errorf("round %d rendered — only the last %d rounds should survive:\n%s", r, historyMaxRounds, out)
		}
	}
	for r := 4; r <= 6; r++ {
		if !strings.Contains(out, fmt.Sprintf("[round %d]", r)) {
			t.Errorf("round %d missing from the rendered history:\n%s", r, out)
		}
	}
	// Chronological order is preserved after the newest-first fill.
	if strings.Index(out, "[round 4]") > strings.Index(out, "[round 6]") {
		t.Errorf("history not in chronological order:\n%s", out)
	}
}

// TestFormatHistoryTokenBudget: bloated entries stay inside the token budget
// and the cut sacrifices the OLDEST entries, never the newest.
func TestFormatHistoryTokenBudget(t *testing.T) {
	bloat := strings.Repeat("indemnification liability carve-out escrow ", 200)
	var entries []HistoryEntry
	for r := 1; r <= 3; r++ {
		for i := 0; i < 4; i++ {
			entries = append(entries, HistoryEntry{
				Round: r, TheirMove: bloat, OurMove: bloat, Rationale: bloat,
				Disposition: "counter",
			})
		}
	}
	out := formatHistory(entries)
	if out == "" {
		t.Fatal("bloated history rendered empty")
	}
	// Small slack for the newline joins between per-line estimates.
	if got := strutil.EstimateTokens(out); got > historyTokenBudget+10 {
		t.Errorf("rendered history estimates %d tokens, budget is %d", got, historyTokenBudget)
	}
	if !strings.Contains(out, "[round 3]") {
		t.Errorf("newest round did not survive the budget cut:\n%s", strutil.Truncate(out, 400))
	}
	// No mid-word cuts: every truncated fragment still ends on a full word.
	for _, line := range strings.Split(out, "\n") {
		for _, frag := range strings.Split(line, " | ") {
			if strings.HasSuffix(frag, "carve-ou") || strings.HasSuffix(frag, "escro") {
				t.Errorf("field cut mid-word: %q", frag)
			}
		}
	}
}

// TestDecideWithHistory: the judge prompt for a clause with history carries
// the NEGOTIATION HISTORY block (with the prior counter verbatim) plus the
// escalation guidance, while a clause without history gets neither; a judge
// that reacts by flagging the standoff surfaces escalation + historyRounds on
// the decision.
func TestDecideWithHistory(t *testing.T) {
	var judgePrompts []string
	sp := &scriptedProvider{chat: func(p providers.ChatParams) (*providers.ChatResponse, error) {
		user := userContent(p)
		switch p.Model {
		case "cls-model":
			if strings.Contains(user, "thirty-six (36)") {
				return textResp(`{"clauseType":"Indemnification cap"}`), nil
			}
			return textResp(`{"clauseType":"Notice"}`), nil
		case "jdg-model":
			judgePrompts = append(judgePrompts, user)
			if strings.Contains(user, "NEGOTIATION HISTORY") {
				return textResp(`{"disposition":"review","rationale":"Our 12-month counter was already rejected; no unoffered fallback — standoff for the lawyer.","escalation":"twelve (12) month counter rejected in round 3; standoff flagged"}`), nil
			}
			return textResp(`{"disposition":"accept","rationale":"Market standard."}`), nil
		}
		return nil, fmt.Errorf("unexpected model %q", p.Model)
	}}

	hist := History{}
	hist.Add("Indemnification cap", HistoryEntry{
		Round:       2,
		TheirMove:   `replaced "twelve (12)" with "thirty-six (36)"`,
		OurMove:     `countered with "twelve (12)"`,
		Disposition: "counter",
		Rationale:   "Held the client's hard 12-month cap.",
	})

	revs := []ooxml.Revision{
		{Kind: ooxml.RevSubstitution, Author: "OC",
			DeletedText: "twelve (12)", InsertedText: "thirty-six (36)",
			ContextBefore: "liability cap of ", ContextAfter: " months of fees"},
		{Kind: ooxml.RevInsertion, Author: "OC",
			InsertedText: ", acting reasonably", ContextBefore: "notify", ContextAfter: " promptly"},
	}

	decisions := New(sp, "jdg-model", "cls-model").Decide(revs, testStore(t), Opts{
		ClientID: "C-100", History: hist,
	})
	if len(decisions) != 2 {
		t.Fatalf("got %d decisions, want 2", len(decisions))
	}

	d0 := decisions[0]
	if d0.Disposition != DispositionReview {
		t.Errorf("decision 0 = %s, want review (judge flagged the standoff)", d0.Disposition)
	}
	if d0.HistoryRounds != 1 {
		t.Errorf("decision 0 historyRounds = %d, want 1", d0.HistoryRounds)
	}
	if !strings.Contains(d0.Escalation, "standoff") {
		t.Errorf("decision 0 escalation = %q, want the judge's standoff note", d0.Escalation)
	}

	d1 := decisions[1]
	if d1.HistoryRounds != 0 || d1.Escalation != "" {
		t.Errorf("decision 1 = historyRounds %d / escalation %q, want no memory fields", d1.HistoryRounds, d1.Escalation)
	}

	if len(judgePrompts) != 2 {
		t.Fatalf("judge called %d times, want 2", len(judgePrompts))
	}
	if !strings.Contains(judgePrompts[0], "NEGOTIATION HISTORY") ||
		!strings.Contains(judgePrompts[0], `countered with "twelve (12)"`) ||
		!strings.Contains(judgePrompts[0], "HISTORY GUIDANCE") {
		t.Errorf("history-clause judge prompt missing the history block or guidance:\n%s", judgePrompts[0])
	}
	if strings.Contains(judgePrompts[1], "NEGOTIATION HISTORY") {
		t.Errorf("no-history clause got a history block:\n%s", judgePrompts[1])
	}
}
