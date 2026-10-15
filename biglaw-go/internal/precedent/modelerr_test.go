// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package precedent

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
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

func store(t *testing.T) *playbook.Store {
	t.Helper()
	st := playbook.New(filepath.Join(t.TempDir(), "pb.json"))
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestPrecedentModelFailuresReturnErrorNotPlaceholders(t *testing.T) {
	upstream := errors.New("ollama: HTTP 502: bad gateway")
	cases := []struct {
		name    string
		replies []interface{}
	}{
		{"structure call fails", []interface{}{upstream}},
		{"draft call fails", []interface{}{`["Parties","Confidentiality"]`, upstream}},
		{"draft unparseable", []interface{}{`["Parties"]`, "I am unable to draft this."}},
		{"draft empty", []interface{}{`["Parties"]`, `{"clauses":[],"document":"","draftingNotes":[]}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := New(&scripted{replies: tc.replies}, "heavy", "light").Generate("nda", nil, store(t), GenerateOpts{})
			if doc != nil {
				t.Fatalf("failed run returned a document (placeholders?): %+v", doc)
			}
			if !errors.Is(err, providers.ErrModelUnavailable) {
				t.Fatalf("want ErrModelUnavailable, got %v", err)
			}
		})
	}
}

func TestPrecedentHappyPathAndAssembly(t *testing.T) {
	draft := `{"clauses":[{"heading":"Parties","draftText":"Between A and B.","source":"generated"}],"document":"","draftingNotes":["check names"]}`
	doc, err := New(&scripted{replies: []interface{}{`["Parties"]`, draft}}, "heavy", "light").Generate("nda", nil, store(t), GenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Clauses) != 1 || !strings.Contains(doc.DocumentText, "Between A and B.") {
		t.Fatalf("unexpected doc %+v", doc)
	}
	if strings.Contains(doc.DocumentText, "[INSERT standard position]") {
		t.Fatal("placeholder leaked into a real draft")
	}
}
