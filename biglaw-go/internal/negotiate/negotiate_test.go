// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Engine tests: a scripted in-process provider (no network, no real models)
// drives classification and judgment, verifying dispositions, playbook-tier
// resolution through the cascade, and that a failed model call downgrades the
// change to "review" instead of aborting the run.

package negotiate

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// scriptedProvider satisfies providers.Provider with a test-supplied handler.
type scriptedProvider struct {
	chat func(providers.ChatParams) (*providers.ChatResponse, error)
}

func (s *scriptedProvider) Chat(p providers.ChatParams) (*providers.ChatResponse, error) {
	return s.chat(p)
}

func textResp(body string) *providers.ChatResponse {
	return &providers.ChatResponse{
		StopReason: providers.StopEndTurn,
		Content:    []providers.ContentBlock{{Type: providers.BlockText, Text: body}},
		Usage:      providers.Usage{InputTokens: 100, OutputTokens: 20},
	}
}

// testStore builds a two-tier cascade: a firm playbook and a client playbook
// that both carry "Indemnification cap", so resolution with a ClientID must
// surface the client tier.
func testStore(t *testing.T) *playbook.Store {
	t.Helper()
	store := playbook.New(filepath.Join(t.TempDir(), "playbooks.json"))
	store.Upsert(types.Playbook{
		ID: "pb-firm", Scope: types.PlaybookScopeFirm, Name: "Firm defaults",
		Entries: []types.PlaybookEntry{{
			ClauseType:       "Indemnification cap",
			StandardPosition: "Cap indemnities at 12 months of fees.",
			FallbackPosition: "18 months with partner sign-off.",
			RedLines:         []string{"Never exceed 24 months"},
		}},
	})
	store.Upsert(types.Playbook{
		ID: "pb-client", Scope: types.PlaybookScopeClient, OwnerID: "C-100", Name: "Client requirements",
		Entries: []types.PlaybookEntry{{
			ClauseType:       "Indemnification cap",
			StandardPosition: "Client requires a hard 12-month cap.",
		}},
	})
	return store
}

func userContent(p providers.ChatParams) string {
	if len(p.Messages) == 0 {
		return ""
	}
	s, _ := p.Messages[0].Content.(string)
	return s
}

func TestDecideDispositionsAndCascade(t *testing.T) {
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
			switch {
			case strings.Contains(user, "thirty-six (36)"):
				return textResp(`{"disposition":"counter","rationale":"36 months crosses the red line; countered at 24 months per playbook.","counterText":"twenty-four (24)"}`), nil
			case strings.Contains(user, "JUDGE-FAILS"):
				return nil, fmt.Errorf("model backend unavailable")
			case strings.Contains(user, "EMPTY-COUNTER"):
				return textResp(`{"disposition":"counter","rationale":"Needs different language.","counterText":""}`), nil
			default:
				return textResp(`{"disposition":"accept","rationale":"Market-standard notice qualifier; no playbook position — judged on reasonableness."}`), nil
			}
		}
		return nil, fmt.Errorf("unexpected model %q", p.Model)
	}}

	revs := []ooxml.Revision{
		{Kind: ooxml.RevSubstitution, Author: "Opposing Counsel",
			DeletedText: "twelve (12)", InsertedText: "thirty-six (36)",
			ContextBefore: "liability cap of ", ContextAfter: " months of fees"},
		{Kind: ooxml.RevInsertion, Author: "Opposing Counsel",
			InsertedText: ", acting reasonably", ContextBefore: "notify the other party", ContextAfter: " within ten days"},
		{Kind: ooxml.RevInsertion, Author: "Opposing Counsel",
			InsertedText: "EMPTY-COUNTER", ContextBefore: "and ", ContextAfter: " thereafter"},
	}

	eng := New(sp, "jdg-model", "cls-model")
	decisions, err := eng.Decide(revs, testStore(t), Opts{ClientID: "C-100", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 3 {
		t.Fatalf("got %d decisions, want 3", len(decisions))
	}

	d0 := decisions[0]
	if d0.Disposition != DispositionCounter || d0.CounterText != "twenty-four (24)" {
		t.Errorf("decision 0 = %s / counter %q, want counter / twenty-four (24)", d0.Disposition, d0.CounterText)
	}
	if d0.ClauseType != "Indemnification cap" {
		t.Errorf("decision 0 clauseType = %q", d0.ClauseType)
	}
	if d0.PlaybookTier != string(types.PlaybookScopeClient) {
		t.Errorf("decision 0 playbookTier = %q, want client (cascade must prefer the client tier)", d0.PlaybookTier)
	}
	if d0.Author != "Opposing Counsel" || d0.Kind != "substitution" {
		t.Errorf("decision 0 identity fields = %q / %q", d0.Author, d0.Kind)
	}
	if d0.Rationale == "" {
		t.Error("decision 0 has no rationale card")
	}

	d1 := decisions[1]
	if d1.Disposition != DispositionAccept {
		t.Errorf("decision 1 = %s, want accept", d1.Disposition)
	}
	if d1.PlaybookTier != "" {
		t.Errorf("decision 1 playbookTier = %q, want empty (no position for Notice)", d1.PlaybookTier)
	}

	d2 := decisions[2]
	if d2.Disposition != DispositionReview {
		t.Errorf("decision 2 = %s, want review for counter without replacement text", d2.Disposition)
	}

	// The judge for the indemnity change must have seen the CLIENT-tier
	// position, proving Opts threaded through the cascade.
	sawClientPosition := false
	for _, p := range judgePrompts {
		if strings.Contains(p, "Client requires a hard 12-month cap.") {
			sawClientPosition = true
		}
	}
	if !sawClientPosition {
		t.Error("judge prompt never carried the client-tier playbook position")
	}
	// And the no-position change must have been flagged as such.
	sawNoPosition := false
	for _, p := range judgePrompts {
		if strings.Contains(p, ", acting reasonably") && strings.Contains(p, "none resolved") {
			sawNoPosition = true
		}
	}
	if !sawNoPosition {
		t.Error("judge prompt for the unmapped clause did not state that no playbook position resolved")
	}
}

// TestDecideModelFailureAborts: any failed classification or judgment aborts
// the run with a model-failure error and no decisions — a half-judged
// response must never be written as a negotiating position.
func TestDecideModelFailureAborts(t *testing.T) {
	revs := []ooxml.Revision{
		{Kind: ooxml.RevInsertion, Author: "OC", InsertedText: "text one"},
		{Kind: ooxml.RevInsertion, Author: "OC", InsertedText: "text two"},
	}
	for name, failModel := range map[string]string{"classification": "cls-model", "judgment": "jdg-model"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			sp := &scriptedProvider{chat: func(p providers.ChatParams) (*providers.ChatResponse, error) {
				calls++
				if p.Model == failModel && calls > 2 { // first change succeeds, second fails
					return nil, fmt.Errorf("model backend down")
				}
				if p.Model == "cls-model" {
					return textResp(`{"clauseType":"Notice"}`), nil
				}
				return textResp(`{"disposition":"accept","rationale":"fine"}`), nil
			}}
			decisions, err := New(sp, "jdg-model", "cls-model").Decide(revs, testStore(t), Opts{})
			if decisions != nil || !errors.Is(err, providers.ErrModelUnavailable) {
				t.Fatalf("want (nil, ErrModelUnavailable), got (%+v, %v)", decisions, err)
			}
		})
	}
}
