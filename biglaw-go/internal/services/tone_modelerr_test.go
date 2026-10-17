// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
			p, err := NewToneAnalyzer(&scripted{replies: tc.replies}, "light").Analyze(context.Background(), []string{"one post"}, "Jane", "text", "p1")
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
	p, err := NewToneAnalyzer(&scripted{replies: []interface{}{"note", errors.New("boom")}}, "light").Analyze(context.Background(), posts, "Jane", "text", "p1")
	if p != nil || !errors.Is(err, providers.ErrModelUnavailable) {
		t.Fatalf("want (nil, ErrModelUnavailable), got (%+v, %v)", p, err)
	}
}

func TestToneHappyPath(t *testing.T) {
	p, err := NewToneAnalyzer(&scripted{replies: []interface{}{"note", goodProfile}}, "light").Analyze(context.Background(), []string{"one post"}, "Jane", "text", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Formality != "formal" || p.InjectionSnippet != "Write tersely." {
		t.Fatalf("unexpected profile %+v", p)
	}
}

// The first failing chunk cancels the others' in-flight calls (errgroup):
// a blocked sibling call returns as soon as the group context is cancelled.
func TestToneFirstFailureCancelsSiblings(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	p := providerFn(func(cp providers.ChatParams) (*providers.ChatResponse, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			return nil, errors.New("boom")
		}
		select { // a slow sibling: only cancellation releases it
		case <-cp.Ctx.Done():
			return nil, cp.Ctx.Err()
		case <-time.After(10 * time.Second):
			return nil, errors.New("sibling was not cancelled")
		}
	})
	posts := make([]string, 17) // 3 chunks
	for i := range posts {
		posts[i] = "post"
	}
	start := time.Now()
	prof, err := NewToneAnalyzer(p, "light").Analyze(context.Background(), posts, "Jane", "text", "p1")
	if prof != nil || err == nil {
		t.Fatalf("want failure, got (%+v, %v)", prof, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("siblings were not cancelled (took %s)", time.Since(start))
	}
}

type providerFn func(providers.ChatParams) (*providers.ChatResponse, error)

func (f providerFn) Chat(p providers.ChatParams) (*providers.ChatResponse, error) { return f(p) }
