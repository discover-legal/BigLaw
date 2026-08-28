// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func quarOrch(policy string) *Orchestrator {
	return &Orchestrator{cfg: &config.Config{Writer: config.WriterConfig{UnverifiedPolicy: policy}}}
}

func TestQuarantineExcludesUnverified(t *testing.T) {
	o := quarOrch("exclude")
	in := append(mkFindings(10, 6), types.Finding{EvidenceStatus: ""}) // machinery finding survives
	kept, n := o.quarantineUnverified(&types.Task{ID: "t"}, in)
	if len(kept) != 11 || n != 6 {
		t.Fatalf("want 11 kept / 6 quarantined, got %d/%d", len(kept), n)
	}
}

func TestQuarantineFloorBacksOffToCaveat(t *testing.T) {
	o := quarOrch("exclude")
	// Only 3 grounded — excluding 20 unverified would gut the record.
	kept, n := o.quarantineUnverified(&types.Task{ID: "t"}, mkFindings(3, 20))
	if len(kept) != 23 || n != 0 {
		t.Fatalf("floor fallback should keep everything, got %d/%d", len(kept), n)
	}
}

func TestQuarantineCaveatPolicyKeepsAll(t *testing.T) {
	o := quarOrch("caveat")
	kept, n := o.quarantineUnverified(&types.Task{ID: "t"}, mkFindings(10, 40))
	if len(kept) != 50 || n != 0 {
		t.Fatalf("caveat policy keeps everything, got %d/%d", len(kept), n)
	}
}

func TestAppendEvidenceNote(t *testing.T) {
	if got := appendEvidenceNote("BODY", 0); got != "BODY" {
		t.Fatalf("no note when nothing quarantined, got %q", got)
	}
	got := appendEvidenceNote("BODY", 7)
	if !strings.Contains(got, "## Evidence Note") || !strings.Contains(got, "7 finding(s)") {
		t.Fatalf("note must disclose the count, got %q", got)
	}
}
