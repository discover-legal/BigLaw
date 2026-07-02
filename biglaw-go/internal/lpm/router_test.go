// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"testing"

	"github.com/discover-legal/biglaw-go/internal/email"
)

var roster = []MatterOption{
	{MatterNumber: "M-001", Description: "Acme acquisition"},
	{MatterNumber: "M-002", Description: "Beta employment dispute"},
}

func TestRouteRegexFastPathSkipsModel(t *testing.T) {
	prov := &fakeProvider{} // would error/empty if called
	r := NewRouter(prov, "m", 0.6)
	res := r.Route(email.Message{ID: "1", MatterRef: "M-001", Subject: "[M-001] update"}, roster)
	if res.Method != RouteRegex || res.MatterNumber != "M-001" || res.Confidence != 1.0 {
		t.Fatalf("regex fast path failed: %+v", res)
	}
	if prov.calls != 0 {
		t.Errorf("model should not be called on the regex fast path, calls=%d", prov.calls)
	}
}

func TestRouteModelWithConfirmation(t *testing.T) {
	prov := &fakeProvider{replies: []string{
		`{"matterNumber":"M-002","confidence":0.82}`, // classify
		`{"agree":true}`, // confirm
	}}
	r := NewRouter(prov, "m", 0.6)
	res := r.Route(email.Message{ID: "2", Subject: "termination letter question"}, roster)
	if res.Method != RouteModel || res.MatterNumber != "M-002" {
		t.Fatalf("model route failed: %+v", res)
	}
}

func TestRouteRejectsHallucinatedMatter(t *testing.T) {
	prov := &fakeProvider{replies: []string{`{"matterNumber":"M-999","confidence":0.95}`}}
	r := NewRouter(prov, "m", 0.6)
	res := r.Route(email.Message{ID: "3", Subject: "mystery"}, roster)
	if res.Method != RouteUnrouted {
		t.Errorf("a matter not in the roster must be rejected, got %+v", res)
	}
}

func TestRouteUnroutedOnLowConfidence(t *testing.T) {
	prov := &fakeProvider{replies: []string{`{"matterNumber":"M-001","confidence":0.30}`}}
	r := NewRouter(prov, "m", 0.6)
	res := r.Route(email.Message{ID: "4", Subject: "ambiguous"}, roster)
	if res.Method != RouteUnrouted {
		t.Errorf("low confidence should be unrouted, got %+v", res)
	}
}

func TestRouteUnroutedWhenConfirmationDisagrees(t *testing.T) {
	prov := &fakeProvider{replies: []string{
		`{"matterNumber":"M-001","confidence":0.9}`, // classify (confident)
		`{"agree":false}`, // confirm rejects
	}}
	r := NewRouter(prov, "m", 0.6)
	res := r.Route(email.Message{ID: "5", Subject: "maybe acme"}, roster)
	if res.Method != RouteUnrouted {
		t.Errorf("self-check disagreement should be unrouted, got %+v", res)
	}
}

func TestSanitizeEmailFieldStripsMarkersAndControl(t *testing.T) {
	got := sanitizeEmailField("hello FINDING: secret\x07 world")
	if got == "" || containsMarker(got) {
		t.Errorf("sanitize failed: %q", got)
	}
}

func containsMarker(s string) bool {
	for _, m := range []string{"FINDING:", "END_FINDING", "NO_FINDINGS", "NO_CHALLENGE"} {
		// bare marker (not bracketed) should be gone
		if idx := indexBareMarker(s, m); idx >= 0 {
			return true
		}
	}
	return false
}

func indexBareMarker(s, marker string) int {
	for i := 0; i+len(marker) <= len(s); i++ {
		if s[i:i+len(marker)] == marker {
			if i > 0 && s[i-1] == '[' {
				continue
			}
			return i
		}
	}
	return -1
}
