// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/providers"
)

// A failed task's error field never carries provider/embedding internals
// (endpoint URLs, resolver addresses, upstream bodies); lawyer-facing
// failures such as grounding collapse are shown verbatim.
func TestTaskFailureMessage(t *testing.T) {
	leak := errors.New("openai embed: Post https://api.openai.com/v1/embeddings: dial tcp: lookup api.openai.com on 8.8.8.8:53")
	for name, tc := range map[string]struct {
		err      error
		want     string
		mustNot  []string
		wantsRef bool
	}{
		"transport": {fmt.Errorf("run round: %w", leak), "internal step failed", []string{"8.8.8.8", "https://", "openai"}, true},
		"model":     {fmt.Errorf("synthesis: %w", providers.ModelFailure("x", errors.New("HTTP 500 at http://10.0.0.1"))), "the AI model call failed", []string{"10.0.0.1"}, true},
		"user":      {fmt.Errorf("run round: %w", &UserFacingError{Msg: "grounding collapse: 80% unverified"}), "grounding collapse: 80% unverified", nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := taskFailureMessage("t1", tc.err)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want it to contain %q", got, tc.want)
			}
			for _, s := range tc.mustNot {
				if strings.Contains(got, s) {
					t.Fatalf("leaked %q: %q", s, got)
				}
			}
			if strings.Contains(got, "(ref ") != tc.wantsRef {
				t.Fatalf("ref presence wrong: %q", got)
			}
		})
	}
}
