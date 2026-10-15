// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package services

import (
	"errors"
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

const goodProfile = `{"formality":"formal","sentenceStyle":"mixed","vocabulary":"balanced","rhetoricalStyle":"assertive","signaturePatterns":["In short,"],"injectionSnippet":"Write tersely."}`

func TestToneModelFailuresReturnErrorNotDefaultProfile(t *testing.T) {
	upstream := errors.New("ollama: HTTP 503: unavailable")
	cases := []struct {
		name    string
		replies []interface{}
	}{
		{"chunk call fails", []interface{}{upstream}},
		{"chunk empty", []interface{}{"  "}},
		{"profile call fails", []interface{}{"note", upstream}},
		{"profile unparseable", []interface{}{"note", "not json"}},
		{"profile without snippet", []interface{}{"note", `{"formality":"formal"}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewToneAnalyzer(&scripted{replies: tc.replies}, "light").Analyze([]string{"one post"}, "Jane", "text", "p1")
			if p != nil {
				t.Fatalf("failed analysis returned a profile: %+v", p)
			}
			if !errors.Is(err, providers.ErrModelUnavailable) {
				t.Fatalf("want ErrModelUnavailable, got %v", err)
			}
		})
	}
}

func TestToneParallelChunkFailureFailsWhole(t *testing.T) {
	// 9 posts → 2 chunks; second chunk fails → whole analysis fails.
	posts := make([]string, 9)
	for i := range posts {
		posts[i] = "post"
	}
	p, err := NewToneAnalyzer(&scripted{replies: []interface{}{"note", errors.New("boom")}}, "light").Analyze(posts, "Jane", "text", "p1")
	if p != nil || !errors.Is(err, providers.ErrModelUnavailable) {
		t.Fatalf("want (nil, ErrModelUnavailable), got (%+v, %v)", p, err)
	}
}

func TestToneHappyPath(t *testing.T) {
	p, err := NewToneAnalyzer(&scripted{replies: []interface{}{"note", goodProfile}}, "light").Analyze([]string{"one post"}, "Jane", "text", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Formality != "formal" || p.InjectionSnippet != "Write tersely." {
		t.Fatalf("unexpected profile %+v", p)
	}
}
