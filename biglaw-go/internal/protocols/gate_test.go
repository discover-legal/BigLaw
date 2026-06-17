// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package protocols

import (
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func gateRunner(required, drop bool) *Runner {
	cfg := &config.Config{}
	cfg.Debate.CitationRequired = required
	cfg.Debate.CitationDropUnsupported = drop
	// provReg and costs are unused by ApplyCitationGate.
	return New(cfg, nil, nil)
}

// A citation whose quote appears verbatim in the source verifies and is not
// flagged.
func TestCitationGate_VerifiedPasses(t *testing.T) {
	r := gateRunner(true, false)
	src := map[string]string{"doc-1": "the employee shall not compete for two years thereafter"}
	in := []types.Finding{{
		ID:        "f1",
		Citations: []types.Citation{{Source: "doc-1", Quote: "shall not compete for two years"}},
	}}
	passed, rejected := r.ApplyCitationGate(in, src)
	if len(passed) != 1 || len(rejected) != 0 {
		t.Fatalf("passed=%d rejected=%d", len(passed), len(rejected))
	}
	if passed[0].EvidenceStatus != types.EvidenceGrounded {
		t.Errorf("verified finding should be grounded, got %q", passed[0].EvidenceStatus)
	}
	if !passed[0].Citations[0].MechanicallyVerified {
		t.Errorf("citation should be mechanically verified")
	}
}

// A finding with no citation is retained but flagged (not silently dropped).
func TestCitationGate_NoCitationFlagged(t *testing.T) {
	r := gateRunner(true, false)
	in := []types.Finding{{ID: "f1", Content: "uncited assertion"}}
	passed, rejected := r.ApplyCitationGate(in, map[string]string{})
	if len(passed) != 1 || len(rejected) != 0 {
		t.Fatalf("want retained+flagged, passed=%d rejected=%d", len(passed), len(rejected))
	}
	if passed[0].EvidenceStatus != types.EvidenceUnsupported || passed[0].EvidenceNote == "" {
		t.Errorf("expected unsupported + note, got %+v", passed[0])
	}
}

// A citation whose quote is NOT in the source is flagged as possible
// fabrication but still retained.
func TestCitationGate_UnverifiedFlagged(t *testing.T) {
	r := gateRunner(true, false)
	src := map[string]string{"doc-1": "entirely unrelated source text"}
	in := []types.Finding{{
		ID:        "f1",
		Citations: []types.Citation{{Source: "doc-1", Quote: "a quote that does not appear"}},
	}}
	passed, _ := r.ApplyCitationGate(in, src)
	if len(passed) != 1 || passed[0].EvidenceStatus != types.EvidenceUnverified {
		t.Fatalf("expected unverified finding, got %+v", passed)
	}
}

// With the legacy strict knob on, unsupported findings are dropped.
func TestCitationGate_DropUnsupportedKnob(t *testing.T) {
	r := gateRunner(true, true)
	in := []types.Finding{{ID: "f1", Content: "uncited"}}
	passed, rejected := r.ApplyCitationGate(in, map[string]string{})
	if len(passed) != 0 || len(rejected) != 1 {
		t.Fatalf("want dropped, passed=%d rejected=%d", len(passed), len(rejected))
	}
}

// The model cites a document by its title/filename while the source map is
// keyed by UUID — verification must still resolve and verify (the exact defect
// the end-to-end qwen run surfaced: 18/18 falsely flagged).
func TestCitationGate_ResolvesByTitle(t *testing.T) {
	r := gateRunner(true, false)
	src := map[string]string{
		"fdab4075-uuid":            "the staff alleges substantial unauthorized trading by the firm",
		"sec-referral-notice.docx": "the staff alleges substantial unauthorized trading by the firm",
		"sec-referral-notice":      "the staff alleges substantial unauthorized trading by the firm",
	}
	in := []types.Finding{{
		ID: "f1",
		// cites by filename, with a trailing extension variant
		Citations: []types.Citation{{Source: "sec-referral-notice.docx", Quote: "substantial unauthorized trading"}},
	}}
	passed, _ := r.ApplyCitationGate(in, src)
	if passed[0].EvidenceStatus != types.EvidenceGrounded {
		t.Fatalf("title-resolved verified quote should be grounded: %+v", passed[0])
	}
}

// Verbatim matching tolerates whitespace/line-wrap differences from extraction
// but not paraphrase.
func TestVerbatimContains(t *testing.T) {
	src := "The Commission\nalleges   that on March 3, 2024,  Hargrove purchased shares."
	if !verbatimContains(src, "alleges that on March 3, 2024, Hargrove purchased") {
		t.Errorf("whitespace-normalized verbatim match should succeed")
	}
	if verbatimContains(src, "Hargrove sold his shares in a panic") {
		t.Errorf("paraphrase must NOT match")
	}
}

func TestNormalizeSourceKey(t *testing.T) {
	cases := map[string]string{
		"SEC-Referral-Notice.docx":       "sec-referral-notice",
		"docs/sub/Compliance Manual.pdf": "compliance manual",
		"plain-id":                       "plain-id",
	}
	for in, want := range cases {
		if got := NormalizeSourceKey(in); got != want {
			t.Errorf("NormalizeSourceKey(%q)=%q want %q", in, got, want)
		}
	}
}

// When citations are not required, findings pass through untouched.
func TestCitationGate_NotRequired(t *testing.T) {
	r := gateRunner(false, false)
	in := []types.Finding{{ID: "f1", Content: "no cite"}}
	passed, rejected := r.ApplyCitationGate(in, map[string]string{})
	if len(passed) != 1 || len(rejected) != 0 || passed[0].EvidenceStatus != "" {
		t.Fatalf("not-required path altered findings: %+v", passed)
	}
}
