// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package briefing

import (
	"errors"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

type failing struct{}

func (failing) Chat(providers.ChatParams) (*providers.ChatResponse, error) {
	return nil, errors.New("ollama: HTTP 500 at http://10.9.9.9")
}

// A failed synthesis yields a briefing clearly marked degraded (statistics
// only) — never passed off as the partner briefing — without internal detail.
func TestBriefingSynthesisFailureIsMarkedDegraded(t *testing.T) {
	b, err := New(failing{}, "mid").Generate(&types.Client{ID: "c1", Name: "Acme", ClientNumber: "C-1"}, nil, nil, GenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Degraded != DegradedStatsOnly {
		t.Fatalf("Degraded = %q", b.Degraded)
	}
	if !strings.HasPrefix(b.ExecutiveSummary, "DEGRADED") || !strings.Contains(b.Document, "NOT a partner briefing") {
		t.Fatalf("not clearly marked degraded:\n%s\n%s", b.ExecutiveSummary, b.Document)
	}
	if strings.Contains(b.ExecutiveSummary+b.Document, "10.9.9.9") {
		t.Fatal("internal error detail leaked into the briefing")
	}
}
