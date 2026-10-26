// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// The lab-r3 round-4 verifier's repros (critique/biglaw-lab-r3-r4-verifier.md), kept as regression
// tests. On 89229c9 a requirement was read in a section of docs.Parse's tree: a wrapped line read as a
// heading split its sentence (H2), a chapeau or a sibling's override outside the leaf section was
// never seen (M1), and the colon line above was never cut, so the bound did not hold (M2). Now it is
// read in a bounded window of the text around it.

// v4Confident returns the claims of findings admitted at full confidence (verified, not Low, every
// citation verified) whose claim lacks guard. The section check is scripted "yes": a correct reading
// of the text it is shown.
func v4Confident(t *testing.T, memo, req, guard string) []string {
	t.Helper()
	yes := []model.Reply{text(m{"answer": "yes", "reason": "the text says to name her as a successor trustee"})}
	f, v := r4Dev(t, memo, req, req, yes)
	var bad []string
	for _, x := range ledgerFindings(t, f, v.ID) {
		ok := !x.Low && len(x.Citations) >= 2
		for _, c := range x.Citations {
			ok = ok && c.Method != "none"
		}
		if ok && !strings.Contains(x.Claim, guard) {
			bad = append(bad, x.Claim)
		}
	}
	if r, ok := sectionCheckRequest(f); ok && !strings.Contains(r.Messages[1].Content.(string), guard) {
		t.Errorf("the section check was shown no %q: %.400q", guard, r.Messages[1].Content.(string))
	}
	return bad
}

// H2: a PDF-wrapped line that starts with a cross-reference ("Article 4", "Section 9") or a number and
// a capitalised word ("12 Elm Street") split the requirement's sentence after its own "Do NOT".
func TestV4WrappedLineSplitsTheSentence(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, memo, req, guard string }{
		{"Article", "Note\nCRITICAL NOTE. Do NOT, under\nArticle 4 of the Trust, name Sophia Davenport-Chen as a successor trustee.\n", "name Sophia Davenport-Chen as a successor trustee.", "Do NOT"},
		{"Section", "4. Successor Trustees\nThe Trustee shall NOT, except as permitted by\nSection 9, name Sophia Davenport-Chen as a successor trustee.\n", "name Sophia Davenport-Chen as a successor trustee.", "shall NOT"},
		{"street number", "Note\nThe Trustee shall NOT name Sophia Davenport-Chen, of\n12 Elm Street, Springfield, as a successor trustee.\n", "Springfield, as a successor trustee.", "shall NOT"},
	} {
		if bad := v4Confident(t, c.memo, c.req, c.guard); len(bad) > 0 {
			t.Errorf("%s: a confirmed (not low-confidence) HIGH deviation quotes the requirement without its %q: %q", c.name, c.guard, bad)
		}
	}
}

// M1: a chapeau without a colon above decimal items, and an override in a sibling decimal subclause.
func TestV4DecimalSiblingsAndChapeau(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, memo, req, guard string }{
		{"em-dash chapeau", "4. Restrictions\nThe Trustee shall not—\n4.1 Sell the family residence at 12 Elm Street.\n4.2 Name Sophia Davenport-Chen as a successor trustee.\n", "Name Sophia Davenport-Chen as a successor trustee.", "shall not—"},
		{"sibling override", "5. Powers\n5.1 The Trustee may name Sophia Davenport-Chen as a successor trustee.\n5.2 Clause 5.1 shall not apply while she resides outside the state.\n", "The Trustee may name Sophia Davenport-Chen as a successor trustee.", "shall not apply"},
	} {
		if bad := v4Confident(t, c.memo, c.req, c.guard); len(bad) > 0 {
			t.Errorf("%s: a confirmed HIGH deviation lacks %q: %q", c.name, c.guard, bad)
		}
	}
}

// M2: a colon line at the top of a flat 180 KB memo sent the whole memo to the subagent and the section
// check, and the finding's claim carried it.
func TestV4ContextBoundHolds(t *testing.T) {
	t.Parallel()
	filler := strings.Repeat("The settlor has considered the family's circumstances and the views of the family council. ", 2000) // ~180 KB
	memo := "Background:\n" + filler + "\nDo NOT name Sophia Davenport-Chen as a successor trustee.\n"
	req := "Do NOT name Sophia Davenport-Chen as a successor trustee."
	f, v := r4Dev(t, memo, req, req, []model.Reply{text(m{"answer": "yes", "reason": "it says so"})})
	sub := len(subagentRequest(f))
	chk, _ := sectionCheckRequest(f)
	n := 0
	if len(chk.Messages) > 1 {
		n = len(chk.Messages[1].Content.(string))
	}
	var claim int
	fs := ledgerFindings(t, f, v.ID)
	for _, x := range fs {
		claim = max(claim, len(x.Claim))
	}
	if sub > 4*ctxMax || n > 4*ctxMax || claim > 4*ctxMax || n == 0 || len(fs) != 1 {
		t.Errorf("the %d-byte context bound does not hold: subagent request %d bytes, section check %d, claim %d; %d findings", ctxMax, sub, n, claim, len(fs))
	}
}

// L2: the section check's answer is read from one strict JSON object only.
func TestCheckAnswerStrict(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{`{"answer":"yes","reason":"r"}`, "yes", true},
		{"  ```json\n{\"answer\":\"YES\",\"reason\":\"r\"}\n```", "yes", true},
		{`{"answer":"Yes","reason":"r"}`, "yes", true},
		{`{"answer":"No"}`, "no", true},
		{`{"answer":"Yes, but only in part","reason":"r"}`, "unclear", true},
		{`{"answer":"No.","reason":"r"}`, "no", true},
		{`{"answer":"NO!"}`, "no", true},
		{`{"answer":"Yes."}`, "yes", true},
		{"```JSON\n{\"answer\":\"no\"}\n```", "no", true},
		{"```\n{\"answer\":\"unclear\"}\n```", "unclear", true},
		{`{"answer":"no","reason":"r","answer":"yes"}`, "", false},
		{`{"Answer":"yes"}`, "", false},
		{`{"reason":"r"}`, "", false},
		{`{"answer":""}`, "", false},
		{`{"answer":true}`, "", false},
		{`Sure: {"answer":"yes"}`, "", false},
		{`{"answer":"yes"} and more`, "", false},
		{`{"answer":"yes"}{"answer":"no"}`, "", false},
	} {
		if got, _, ok := checkAnswer(c.in); got != c.want || ok != c.ok {
			t.Errorf("%q: %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
