// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package regulatory

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/providers"
)

type seqProvider struct {
	mu      sync.Mutex
	replies []interface{} // string | error
}

func (p *seqProvider) Chat(providers.ChatParams) (*providers.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.replies[0]
	p.replies = p.replies[1:]
	if err, ok := r.(error); ok {
		return nil, err
	}
	return &providers.ChatResponse{Content: []providers.ContentBlock{{Type: providers.BlockText, Text: r.(string)}}}, nil
}

// Results whose relevance check failed are reported as missed (error), never
// silently treated as "not relevant"; confirmed alerts are still returned.
func TestFilterRelevantSurfacesMissedChecks(t *testing.T) {
	results := []tavilyResult{{Title: "A", URL: "u1"}, {Title: "B", URL: "u2"}, {Title: "C", URL: "u3"}}
	m := New(&seqProvider{replies: []interface{}{
		`{"relevant":true,"reason":"r"}`, errors.New("HTTP 500"), "garbage",
	}}, "light")
	alerts, err := m.filterRelevant(results, "Employment", "UK", "M-1")
	if len(alerts) != 1 || alerts[0].URL != "u1" {
		t.Fatalf("confirmed alert lost: %+v", alerts)
	}
	if !errors.Is(err, providers.ErrModelUnavailable) || !strings.Contains(err.Error(), "2 of 3") {
		t.Fatalf("want a model failure naming 2 of 3 unchecked, got %v", err)
	}

	alerts, err = New(&seqProvider{replies: []interface{}{`{"relevant":false}`}}, "light").filterRelevant(results[:1], "Employment", "UK", "M-1")
	if err != nil || len(alerts) != 0 {
		t.Fatalf("clean not-relevant: got (%v, %v)", alerts, err)
	}
}
