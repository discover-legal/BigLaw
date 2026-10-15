// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package headnotes

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

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

const opinion = "The court held that a contract requires consideration. Appeal dismissed."
const oneHeadnote = `[{"number":1,"proposition":"A contract requires consideration.","sourceText":"a contract requires consideration","holdingType":"ratio","confidence":0.9}]`
const meta = `{"caseName":"A v B","keyHolding":"Consideration is required."}`

func TestHeadnotesModelFailuresReturnError(t *testing.T) {
	upstream := errors.New("ollama: do request: connection refused")
	cases := []struct {
		name    string
		replies []interface{}
	}{
		{"extraction call fails", []interface{}{upstream}},
		{"extraction unparseable", []interface{}{"Sorry, I can't."}},
		{"key-holding call fails", []interface{}{oneHeadnote, upstream}},
		{"key-holding unparseable", []interface{}{oneHeadnote, "no json"}},
		{"key-holding empty", []interface{}{oneHeadnote, `{"caseName":"A v B","keyHolding":""}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := New(&scripted{replies: tc.replies}, "mid", "light").Generate(opinion, GenerateOpts{})
			if rep != nil || !errors.Is(err, providers.ErrModelUnavailable) {
				t.Fatalf("want (nil, ErrModelUnavailable), got (%+v, %v)", rep, err)
			}
		})
	}
}

func TestHeadnotesNoProviderStays503Class(t *testing.T) {
	cause := errors.Join(providers.ErrNoProvider, errors.New("no key"))
	_, err := New(&scripted{replies: []interface{}{cause}}, "mid", "light").Generate(opinion, GenerateOpts{})
	if !errors.Is(err, providers.ErrNoProvider) {
		t.Fatalf("want ErrNoProvider, got %v", err)
	}
}

func TestHeadnotesHappyPath(t *testing.T) {
	rep, err := New(&scripted{replies: []interface{}{oneHeadnote, meta}}, "mid", "light").Generate(opinion, GenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TotalHeadnotes != 1 || rep.KeyHolding != "Consideration is required." {
		t.Fatalf("unexpected report %+v", rep)
	}
}

// A valid reply with zero propositions is ErrNoContent (→ 422), never a
// "0 headnotes" report.
func TestHeadnotesZeroIsNoContent(t *testing.T) {
	rep, err := New(&scripted{replies: []interface{}{"[]"}}, "mid", "light").Generate(opinion, GenerateOpts{})
	if rep != nil || !errors.Is(err, providers.ErrNoContent) {
		t.Fatalf("want (nil, ErrNoContent), got (%+v, %v)", rep, err)
	}
}

// List fields serialise as [] (not null) — null crashed the Headnotes tab.
func TestHeadnotesListFieldsNeverNull(t *testing.T) {
	noLists := `[{"number":1,"proposition":"P","sourceText":"s","holdingType":"ratio"}]`
	rep, err := New(&scripted{replies: []interface{}{noLists, `{"keyHolding":"K"}`}}, "mid", "light").Generate(opinion, GenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), "null") {
		t.Fatalf("report JSON contains null: %s", b)
	}
}
