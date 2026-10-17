// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package bots

import (
	"errors"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

type knowledgeFn func(string, map[string]interface{}) ([]types.SearchResult, error)

func (f knowledgeFn) Search(q string, o map[string]interface{}) ([]types.SearchResult, error) {
	return f(q, o)
}

type searchFacade struct {
	*fakeFacade
	ks KnowledgeSearcher
}

func (f searchFacade) Knowledge() KnowledgeSearcher { return f.ks }

// A failed search must say it failed — "No results" would read as the
// knowledge store holding nothing on the topic.
func TestDispatchSearchFailureIsNotNoResults(t *testing.T) {
	fail := searchFacade{&fakeFacade{}, knowledgeFn(func(string, map[string]interface{}) ([]types.SearchResult, error) {
		return nil, errors.New("embedder down at http://10.0.0.9")
	})}
	r := Dispatch(BotMessage{Text: "@BigMichael search indemnity cap"}, fail)
	if !strings.Contains(r.Immediate, "Search failed") || strings.Contains(r.Immediate, "No results") || strings.Contains(r.Immediate, "10.0.0.9") {
		t.Fatalf("unexpected reply: %q", r.Immediate)
	}
	empty := searchFacade{&fakeFacade{}, knowledgeFn(func(string, map[string]interface{}) ([]types.SearchResult, error) { return nil, nil })}
	if r := Dispatch(BotMessage{Text: "@BigMichael search indemnity cap"}, empty); !strings.Contains(r.Immediate, "No results") {
		t.Fatalf("genuine empty search: %q", r.Immediate)
	}
}
