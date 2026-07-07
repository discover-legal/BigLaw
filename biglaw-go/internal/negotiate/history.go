// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Judge memory — per-clause negotiation history threaded into the judgment
// step. When the document under negotiation belongs to a Redtime lineage, the
// caller assembles each clause type's prior moves and decisions (assembly
// lives with the version store — internal/redtime — keeping this package
// store-free) and passes them in Opts.History. The judge then sees what was
// already offered and rejected on the clause, with escalation guidance:
// never re-issue a rejected counter — move to the playbook fallback position
// or flag the standoff for the negotiating lawyer.

package negotiate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// HistoryEntry is one prior round's exchange on a clause: what opposing
// counsel did, how the firm responded, and why.
type HistoryEntry struct {
	// Round is the lineage round of the response version that carried the
	// decision — i.e. the round in which the firm answered the move.
	Round       int    `json:"round"`
	TheirMove   string `json:"theirMove,omitempty"`
	OurMove     string `json:"ourMove,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
}

// History maps a clause-type label (matched case-insensitively) to that
// clause's prior-round entries, oldest first. A nil History judges amnesiac —
// exactly the pre-memory behaviour.
type History map[string][]HistoryEntry

// Add appends one entry under the clause's normalised key.
func (h History) Add(clauseType string, e HistoryEntry) {
	key := historyKey(clauseType)
	if key == "" {
		return
	}
	h[key] = append(h[key], e)
}

// For returns the entries recorded for clauseType (case-insensitive); nil
// when the clause has no history.
func (h History) For(clauseType string) []HistoryEntry {
	if len(h) == 0 {
		return nil
	}
	return h[historyKey(clauseType)]
}

func historyKey(clauseType string) string {
	return strings.ToLower(strings.TrimSpace(clauseType))
}

// History caps: the judge sees at most the last historyMaxRounds rounds and
// at most historyTokenBudget (estimated) tokens of history — enough to
// recognise a standoff, never enough to crowd out the change under judgment.
const (
	historyMaxRounds   = 3
	historyTokenBudget = 600
	// Per-field ceilings inside one rendered entry. Assemblers (redtime)
	// already compact their summaries; these are the defensive bound for
	// History built by any other caller.
	historyMoveTokens      = 60
	historyRationaleTokens = 50
)

// historyGuidance is appended after the rendered history block. It licenses
// the optional "escalation" JSON field — the judge's own record of a
// fallback move or standoff — without changing the required contract.
const historyGuidance = `HISTORY GUIDANCE: Weigh the history above before deciding. Do not re-issue a counter that is substantively identical to one opposing counsel already rejected in a prior round. If the counter you would otherwise propose has already been rejected: (a) when the playbook provides a fallback position that has not yet been offered, counter with the fallback; (b) otherwise return disposition "review" with a rationale flagging the standoff for the negotiating lawyer. Whenever you move to the fallback or flag a standoff, also include an optional "escalation" field in your JSON object — one short sentence naming what was rejected and where you moved, e.g. {"disposition":"counter","rationale":"...","counterText":"...","escalation":"12-month cap rejected in round 3; moved to the fallback position"}.`

// distinctRounds counts the distinct negotiation rounds present in entries.
func distinctRounds(entries []HistoryEntry) int {
	seen := map[int]bool{}
	for _, e := range entries {
		seen[e.Round] = true
	}
	return len(seen)
}

// formatHistory renders the newest historyMaxRounds rounds of entries as
// prompt lines within historyTokenBudget tokens. The block fills newest-first
// so that when the budget bites, the most recent rounds survive; the kept
// lines are then restored to chronological order for the judge.
func formatHistory(entries []HistoryEntry) string {
	if len(entries) == 0 {
		return ""
	}

	// The cutoff round: keep only the top historyMaxRounds distinct rounds.
	var rounds []int
	seen := map[int]bool{}
	for _, e := range entries {
		if !seen[e.Round] {
			seen[e.Round] = true
			rounds = append(rounds, e.Round)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(rounds)))
	minRound := rounds[len(rounds)-1]
	if len(rounds) > historyMaxRounds {
		minRound = rounds[historyMaxRounds-1]
	}

	var kept []string
	total := 0
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Round < minRound {
			continue
		}
		line := formatHistoryEntry(e)
		n := strutil.EstimateTokens(line)
		if total+n > historyTokenBudget {
			break // only older entries remain — the newest are already kept
		}
		kept = append(kept, line)
		total += n
	}
	// Restore chronological (oldest-first) order.
	for l, r := 0, len(kept)-1; l < r; l, r = l+1, r-1 {
		kept[l], kept[r] = kept[r], kept[l]
	}
	return strings.Join(kept, "\n")
}

// formatHistoryEntry renders one entry as a single compact prompt line, each
// free-text field bounded in tokens and cut on a word boundary.
func formatHistoryEntry(e HistoryEntry) string {
	var parts []string
	if s := strings.TrimSpace(e.TheirMove); s != "" {
		parts = append(parts, "their move: "+strutil.TruncateToTokens(s, historyMoveTokens))
	}
	if s := strings.TrimSpace(e.OurMove); s != "" {
		parts = append(parts, "our response: "+strutil.TruncateToTokens(s, historyMoveTokens))
	}
	if s := strings.TrimSpace(e.Disposition); s != "" {
		parts = append(parts, "disposition: "+s)
	}
	if s := strings.TrimSpace(e.Rationale); s != "" {
		parts = append(parts, "rationale: "+strutil.TruncateToTokens(s, historyRationaleTokens))
	}
	return fmt.Sprintf("[round %d] %s", e.Round, strings.Join(parts, " | "))
}
