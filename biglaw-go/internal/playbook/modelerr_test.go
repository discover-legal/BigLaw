// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package playbook

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

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

func noSearch(string, int) []types.SearchResult { return nil }

// A failed extraction must not persist a playbook whose positions read
// "Extraction failed" — later redlines would apply it as the firm position.
func TestBuildModelFailurePersistsNothing(t *testing.T) {
	for name, reply := range map[string]interface{}{
		"call fails":          errors.New("ollama: HTTP 500"),
		"unparseable":         "no json here",
		"empty entries":       `{"entries":[]}`,
		"missing a type":      `{"entries":[{"clauseType":"Governing law","standardPosition":"England"}]}`,
		"empty position":      `{"entries":[{"clauseType":"Governing law","standardPosition":"England"},{"clauseType":"Limitation of liability","standardPosition":"  "}]}`,
		"only invented types": `{"entries":[{"clauseType":"Force majeure","standardPosition":"x"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			st := New(filepath.Join(t.TempDir(), "pb.json"))
			if err := st.Init(); err != nil {
				t.Fatal(err)
			}
			pb, err := NewBuilder(&scripted{replies: []interface{}{reply}}, "light").Build(st, noSearch, BuildOpts{
				Scope: types.PlaybookScopeFirm, PracticeArea: "Corporate & M&A", Name: "x", ClauseTypes: []string{"Governing law", "Limitation of liability"},
			})
			if pb != nil || !errors.Is(err, providers.ErrModelUnavailable) {
				t.Fatalf("want (nil, ErrModelUnavailable), got (%+v, %v)", pb, err)
			}
			if n := len(st.List("", "", "")); n != 0 {
				t.Fatalf("store has %d playbooks after a failed build, want 0", n)
			}
		})
	}
}

func TestBuildCompleteEntriesSaved(t *testing.T) {
	st := New(filepath.Join(t.TempDir(), "pb.json"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	reply := `{"entries":[{"clauseType":"governing_law","standardPosition":"England"},{"clauseType":"Limitation of Liability","standardPosition":"Cap at fees"},{"clauseType":"Invented","standardPosition":"x"}]}`
	pb, err := NewBuilder(&scripted{replies: []interface{}{reply}}, "light").Build(st, noSearch, BuildOpts{
		Scope: types.PlaybookScopeFirm, PracticeArea: "Corporate & M&A", Name: "x", ClauseTypes: []string{"Governing law", "Limitation of liability"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pb.Entries) != 2 || pb.Entries[0].ClauseType != "Governing law" || pb.Entries[1].StandardPosition != "Cap at fees" {
		t.Fatalf("unexpected entries %+v", pb.Entries)
	}
	if len(st.List("", "", "")) != 1 {
		t.Fatal("complete playbook was not saved")
	}
}

// Models echo the prompt's numbering or add "Clause" to the name; when the
// count matches, an otherwise-unmatched entry is taken by position.
func TestBuildAcceptsEchoedClauseNames(t *testing.T) {
	for name, reply := range map[string]string{
		"numbered":   `{"entries":[{"clauseType":"1. Governing law","standardPosition":"England"},{"clauseType":"2) Limitation of Liability","standardPosition":"Cap at fees"}]}`,
		"suffixed":   `{"entries":[{"clauseType":"Governing Law Clause","standardPosition":"England"},{"clauseType":"Limitation of liability provision","standardPosition":"Cap at fees"}]}`,
		"positional": `{"entries":[{"clauseType":"Choice of law","standardPosition":"England"},{"clauseType":"Liability cap","standardPosition":"Cap at fees"}]}`,
		"mixed":      `{"entries":[{"clauseType":"Liability cap","standardPosition":"Cap at fees"},{"clauseType":"Governing law","standardPosition":"England"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			st := New(filepath.Join(t.TempDir(), "pb.json"))
			if err := st.Init(); err != nil {
				t.Fatal(err)
			}
			pb, err := NewBuilder(&scripted{replies: []interface{}{reply}}, "light").Build(st, noSearch, BuildOpts{
				Scope: types.PlaybookScopeFirm, PracticeArea: "Corporate & M&A", Name: "x", ClauseTypes: []string{"Governing law", "Limitation of liability"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if pb.Entries[0].StandardPosition != "England" || pb.Entries[1].StandardPosition != "Cap at fees" {
				t.Fatalf("mismatched entries %+v", pb.Entries)
			}
		})
	}
}
