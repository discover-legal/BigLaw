// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Adjudication: debate losers and verification failures never draft ────────

func TestQuarantineExcludesAdjudicatedLosers(t *testing.T) {
	o := quarOrch("exclude")
	in := mkFindings(10, 0)
	in = append(in,
		types.Finding{EvidenceStatus: types.EvidenceGrounded, DebateVerdict: "OVERTURNED"},
		types.Finding{EvidenceStatus: types.EvidenceGrounded, DebateVerdict: "UPHELD"},
		types.Finding{EvidenceStatus: types.EvidenceGrounded, VerificationResult: &types.VerificationResult{Passed: false}},
		types.Finding{EvidenceStatus: types.EvidenceGrounded, VerificationResult: &types.VerificationResult{Passed: true}},
	)
	kept, n := o.quarantineUnverified(&types.Task{ID: "t"}, in)
	if len(kept) != 12 || n != 2 {
		t.Fatalf("want 12 kept (10 + UPHELD + verification-passed) and 2 quarantined, got %d/%d", len(kept), n)
	}
	for _, f := range kept {
		if f.DebateVerdict == "OVERTURNED" {
			t.Fatal("an OVERTURNED finding reached the writer")
		}
		if f.VerificationResult != nil && !f.VerificationResult.Passed {
			t.Fatal("a verification-failed finding reached the writer")
		}
	}
	// Losers are excluded even under the caveat policy — caveat only governs
	// unverified-evidence findings, not adjudicated ones.
	o = quarOrch("caveat")
	kept, _ = o.quarantineUnverified(&types.Task{ID: "t"}, in)
	if len(kept) != 12 {
		t.Fatalf("caveat policy must still exclude losers, kept %d", len(kept))
	}
}

// ─── External authorities: out-of-record citations are marked and listed ──────

const okafCorpus = `KEY DOCUMENT EXTRACTS — Okafor v. Meridian CloudWorks (EMP-2026-041)
§7(d) "Any breach of Section 7 shall result in forfeiture..." 28 U.S.C. § 2462 appears here.`

func TestFlagExternalAuthorities(t *testing.T) {
	body := "The clause fails under *Morgan v. Groupon, Inc.*, 27 N.Y.3d 423 (2016) and NYLL §193. " +
		"See §7(d) of the Agreement and Okafor v. Meridian CloudWorks. Also 28 U.S.C. § 2462 controls. " +
		"Again, Morgan v. Groupon applies."
	out := flagExternalAuthorities(body, okafCorpus)

	if !strings.Contains(out, "Morgan v. Groupon, Inc.*, 27 N.Y.3d 423 (2016)"+authorityMarker) {
		t.Fatalf("fabricated case must carry the inline marker:\n%s", out)
	}
	if !strings.Contains(out, "NYLL §193"+authorityMarker) {
		t.Fatal("out-of-record statute cite must carry the marker")
	}
	if strings.Contains(out, "Okafor v. Meridian CloudWorks"+authorityMarker) {
		t.Fatal("the matter's own caption must NOT be flagged")
	}
	if strings.Contains(out, "28 U.S.C. § 2462"+authorityMarker) {
		t.Fatal("a statute present in the record must NOT be flagged")
	}
	if strings.Contains(out, "§7(d)"+authorityMarker) {
		t.Fatal("contract-internal section refs must NOT be flagged")
	}
	if !strings.Contains(out, "## Authorities Requiring Verification") {
		t.Fatal("appendix missing")
	}
	if got := strings.Count(out, authorityMarker); got != 2 {
		t.Fatalf("want exactly 2 inline markers (first occurrence per unique authority), got %d", got)
	}
	// Clean body: untouched, no appendix.
	clean := "The agreement's §7(d) and the Okafor v. Meridian CloudWorks record support the claim."
	if got := flagExternalAuthorities(clean, okafCorpus); got != clean {
		t.Fatalf("clean body must pass unmodified, got:\n%s", got)
	}
}

// ─── Client guard: the represented party never appears as a respondent ───────

func TestClientPartyName(t *testing.T) {
	cases := map[string]string{
		"Act for Adaeze Okafor against Meridian CloudWorks Inc.": "Adaeze Okafor",
		"Please act for Danielle Tremblay in her separation":     "Danielle Tremblay",
		"our client Jane Roe seeks":                              "Jane Roe",
		"Client: Marc Fontaine — review the file":                "Marc Fontaine",
		"Summarize the credit agreement":                         "",
	}
	for desc, want := range cases {
		// EXACT equality — a lenient prefix assertion previously hid a
		// (?i)-scoping bug that captured "Adaeze Okafor against Meridian"
		// and silently disarmed the guard on a live run.
		if got := clientPartyName(desc); got != want {
			t.Errorf("clientPartyName(%q) = %q, want %q", desc, got, want)
		}
	}
	client := "Adaeze Okafor"
	for _, entity := range []string{"AdaezeOkafor", "Ms. Adaeze Okafor", "A. Okafor", "adaeze okafor"} {
		if !matchesClientParty(entity, client) {
			t.Errorf("guard must match entity %q to client %q", entity, client)
		}
	}
	for _, entity := range []string{"Marc Tremblay", "Meridian CloudWorks", "R. Calloway"} {
		if matchesClientParty(entity, client) {
			t.Errorf("guard must NOT match %q", entity)
		}
	}
}

func quarOrchCfg() *config.Config { return &config.Config{} }

// ─── Securities templates never fire on non-securities matters ────────────────

func TestDefenseTemplatesGatedBySecuritiesContent(t *testing.T) {
	employment := defenseContext{
		Auth:    "NYLL §193; FLSA §207; Acceptable Use Policy",
		DocText: "Ms. Okafor was terminated on 9 June 2026. Unpaid commissions of $48,300 under the 2026 Commission Plan.",
	}
	if got := analyseDefense(employment); len(got) != 0 {
		t.Fatalf("employment matter must produce no SEC defense issues, got %d: %q", len(got), got[0].Text)
	}
	securities := defenseContext{
		Auth:    "Advisers Act Section 206(1); Section 206(2); Rule 204A-1",
		DocText: "The SEC referral charges violations of Section 206(1) and 206(2) during the Review Period.",
	}
	if got := analyseDefense(securities); len(got) == 0 {
		t.Fatal("securities record must still produce defense issues")
	}
}
