// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Judge memory assembly — the cheap path from a lineage to per-clause
// negotiation history. Prior versions and the respond_to_redline decision
// cards attached to them are read straight off the VersionRepository — no
// model calls, unlike BuildTimeline, whose event bucketing classifies clauses
// with an extraction-tier model. History matches to a future change by the
// clauseType each stored decision already carries (negotiate's classifier
// labels the current change in the same playbook vocabulary), so decisions
// alone give everything the judge needs: prior moves, our responses, and
// their rationales, round by round.

package redtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/negotiate"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// Per-field token caps for history summaries — compact by construction, so
// negotiate's block-level budget rarely has to cut anything.
const (
	historyMoveTokens      = 40
	historyRationaleTokens = 60
)

// NegotiationHistory assembles the per-clause negotiation history of a
// lineage from its stored versions and their attached decisions. Versions
// without decisions (sent drafts, inbound markups, uploads) contribute
// nothing; a decisions payload that does not unmarshal is skipped rather than
// failing the assembly. The result may be empty but is never nil on success.
func NegotiationHistory(ctx context.Context, repo store.VersionRepository, lineageID string) (negotiate.History, error) {
	if repo == nil {
		return nil, ErrUnavailable
	}
	versions, err := repo.ListLineage(ctx, lineageID)
	if err != nil {
		return nil, err
	}
	h := negotiate.History{}
	for _, v := range versions {
		if len(v.Decisions) == 0 {
			continue
		}
		var decisions []negotiate.Decision
		if err := json.Unmarshal(v.Decisions, &decisions); err != nil {
			continue // legacy or foreign payload — never breaks judging
		}
		for _, d := range decisions {
			if strings.TrimSpace(d.ClauseType) == "" {
				continue
			}
			h.Add(d.ClauseType, negotiate.HistoryEntry{
				Round:       v.Round,
				TheirMove:   summariseTheirMove(d),
				OurMove:     summariseOurMove(d),
				Disposition: string(d.Disposition),
				Rationale:   strutil.TruncateToTokens(strings.TrimSpace(d.Rationale), historyRationaleTokens),
			})
		}
	}
	return h, nil
}

// summariseTheirMove compacts the opposing change a decision answered.
func summariseTheirMove(d negotiate.Decision) string {
	del := strutil.TruncateToTokens(strings.TrimSpace(d.DeletedText), historyMoveTokens)
	ins := strutil.TruncateToTokens(strings.TrimSpace(d.InsertedText), historyMoveTokens)
	switch {
	case del != "" && ins != "":
		return fmt.Sprintf("replaced %q with %q", del, ins)
	case ins != "":
		return fmt.Sprintf("inserted %q", ins)
	case del != "":
		return fmt.Sprintf("deleted %q", del)
	}
	return ""
}

// summariseOurMove compacts the firm's response so the judge can tell what
// has already been offered (and, when the same opposing language comes back
// in a later round, that the offer was rejected).
func summariseOurMove(d negotiate.Decision) string {
	switch d.Disposition {
	case negotiate.DispositionAccept:
		return "accepted their change"
	case negotiate.DispositionReject:
		if del := strutil.TruncateToTokens(strings.TrimSpace(d.DeletedText), historyMoveTokens); del != "" {
			return fmt.Sprintf("rejected — restored %q", del)
		}
		return "rejected — original language restored"
	case negotiate.DispositionCounter:
		if ct := strutil.TruncateToTokens(strings.TrimSpace(d.CounterText), historyMoveTokens); ct != "" {
			return fmt.Sprintf("countered with %q", ct)
		}
		return "countered"
	case negotiate.DispositionReview:
		return "flagged for lawyer review (unresolved)"
	}
	return string(d.Disposition)
}
