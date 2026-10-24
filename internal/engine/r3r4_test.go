// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/model"
)

// The lab-r3 round-3 verifier's repros (critique/biglaw-lab-r3-r3-verifier.md), kept as regression
// tests, and the round-3 design that answers them: a requirement is read in its section of the
// document's own section tree (no layout walk), trimmed around the requirement only, and a
// deviation is admitted only when an independent check of the whole section says the section imposes
// the requirement as the verdict states it. H1–H3 and M2 delivered a verified HIGH deviation whose
// quoted controlling text had lost its "NOT" on c3274c3; M1 cut an introduction's "Do NOT".

// v3Inverted returns the claims of verified findings citing both documents whose text lacks guard.
func v3Inverted(t *testing.T, memo, req, ctrl, guard string) []string {
	t.Helper()
	_, v := r2vDev(t, memo, req, ctrl)
	var bad []string
	for _, x := range v.Findings {
		ok := x.Status == "verified" && len(x.Citations) >= 2
		for _, c := range x.Citations {
			ok = ok && c.Method != "none"
		}
		if ok && !strings.Contains(x.Claim, guard) {
			bad = append(bad, x.Claim)
		}
	}
	return bad
}

// H1: a list whose items are separated by blank lines (unmarked: a PDF text layer, plain text or
// Markdown) governed only its first item; a later item was read under its heading alone.
func TestV3BlankSeparatedListLosesIntroduction(t *testing.T) {
	t.Parallel()
	memo := "B. Successor Trustees\n\nThe following persons shall NOT serve as successor trustee in any capacity:\n\nMartin Hale, the settlor's former business partner.\n\nSophia Davenport-Chen, the settlor's daughter, as a successor trustee.\n"
	req := "Sophia Davenport-Chen, the settlor's daughter, as a successor trustee."
	if bad := v3Inverted(t, memo, req, req, "shall NOT serve"); len(bad) > 0 {
		t.Errorf("a verified HIGH deviation quotes a list item without its negating introduction: %q", bad)
	}
}

// H2: the same shape with names (a title-cased line after a blank line read as a heading).
func TestV3BlankSeparatedNamesLoseIntroduction(t *testing.T) {
	t.Parallel()
	memo := "B. Successor Trustees\n\nThe following persons shall NOT serve as successor trustee:\n\nMartin Hale\n\nSophia Davenport-Chen\n"
	if bad := v3Inverted(t, memo, "Sophia Davenport-Chen", "Sophia Davenport-Chen", "shall NOT serve"); len(bad) > 0 {
		t.Errorf("a verified HIGH deviation quotes a name without its negating introduction: %q", bad)
	}
}

// H3: the verdict's controlling quote lies in the context but far from the requirement; the
// finding's context was trimmed around the verdict's quote, and the requirement's own "Do NOT" cut.
// Now the context is trimmed around the requirement only (and a quote outside it is dropped).
func TestV3VerdictFocusTrimsRequirementGuard(t *testing.T) {
	t.Parallel()
	for _, n := range []int{12, 80} { // inside the bound, and a section cut to it
		filler := strings.Repeat("The settlor has considered the family's circumstances, the tax position of each beneficiary and the views of the family council. ", n)
		memo := "Note\nDo NOT, under Art. 4 of the Trust, name Sophia Davenport-Chen as a successor trustee. " + filler + "Any successor trustee must accept the office in writing.\n"
		req := "name Sophia Davenport-Chen as a successor trustee."
		if bad := v3Inverted(t, memo, req, "Any successor trustee must accept the office in writing.", "Do NOT"); len(bad) > 0 {
			t.Errorf("%d: a verified HIGH deviation's controlling text drops the requirement's \"Do NOT\": %q", n, bad)
		}
	}
}

// M2: an override in the next paragraph or item was not in the context.
func TestV3OverrideBelowNotInContext(t *testing.T) {
	t.Parallel()
	memo := "5. Powers\n(a) The Trustee may name Sophia Davenport-Chen as a successor trustee.\n(b) Paragraph (a) shall not apply, and Sophia Davenport-Chen shall not serve, while she resides outside the state.\n"
	req := "The Trustee may name Sophia Davenport-Chen as a successor trustee."
	if bad := v3Inverted(t, memo, req, req, "shall not apply"); len(bad) > 0 {
		t.Errorf("a verified deviation quotes a permission without the override in the next item: %q", bad)
	}
	memo = "5. Powers\nThe Trustee may sell the family residence at 12 Elm Street.\n\nNotwithstanding the foregoing, the Trustee shall not sell the family residence while Peggy lives.\n"
	req = "The Trustee may sell the family residence at 12 Elm Street."
	if bad := v3Inverted(t, memo, req, req, "Notwithstanding the foregoing"); len(bad) > 0 {
		t.Errorf("a verified deviation quotes a permission without the override in the next paragraph: %q", bad)
	}
}

// M1: an introduction over 300 bytes kept only its tail when the requirement's own item was long;
// its opening "Do NOT" was cut from what the subagent judges and from what the finding quotes.
func TestV3LongIntroductionLosesItsHead(t *testing.T) {
	intro := "Do NOT, " + strings.Repeat("whether acting alone or jointly with any co-trustee, agent, attorney-in-fact or other fiduciary, and whether before or after the settlor's death, ", 3) + "appoint any of the following persons as successor trustee:\n"
	item := "(a) Sophia Davenport-Chen, the settlor's daughter, " + strings.Repeat("together with any trust, entity, nominee or agent that she controls directly or indirectly, ", 12) + "as a successor trustee in any capacity;\n"
	doc := "4. Trustees\n" + intro + item + "(b) Martin Hale.\n"
	spans, bad, _ := placeRequirement(context.Background(), doc, "Sophia Davenport-Chen, the settlor's daughter,")
	if bad != "" {
		t.Fatal(bad)
	}
	if got := newReq(doc, spans).Context(); !strings.Contains(got, "Do NOT") {
		t.Errorf("read without its introduction's \"Do NOT\": %.200q", got)
	}
}

// TestContextIsTheTextAroundIt (lab-r3 r4, coordinator decision): the context is the document's text
// around the requirement, cut at sentence or line ends, whatever docs.Parse makes of its headings; a
// heading path above the window is a label only; a short document is read whole.
func TestContextIsTheTextAroundIt(t *testing.T) {
	for _, c := range []struct{ name, doc, req, want string }{
		{"short document read whole", "ARTICLE IV TRUSTEES\nSection 4.1 Appointment\nThe following persons shall NOT serve:\n(a) Sophia Davenport-Chen.\n(b) Martin Hale.\nSection 4.2 Removal\nThe Trust Protector may remove a corporate trustee.\n", "Martin Hale.",
			"ARTICLE IV TRUSTEES\nSection 4.1 Appointment\nThe following persons shall NOT serve:\n(a) Sophia Davenport-Chen.\n(b) ⟦Martin Hale.⟧\nSection 4.2 Removal\nThe Trust Protector may remove a corporate trustee."},
		{"full-width colon", "The following persons may never act as Trustee：\n(a) Martin Hale\n(b) Sophia Davenport-Chen\n", "Sophia Davenport-Chen",
			"The following persons may never act as Trustee：\n(a) Martin Hale\n(b) ⟦Sophia Davenport-Chen⟧"},
	} {
		spans, bad, err := placeRequirement(context.Background(), c.doc, c.req)
		if err != nil || bad != "" {
			t.Errorf("%s: bad %q, err %v", c.name, bad, err)
			continue
		}
		r := newReq(c.doc, spans)
		if got := r.Context(); got != c.want || r.Truncated {
			t.Errorf("%s: context %q (truncated %v), want %q", c.name, got, r.Truncated, c.want)
		}
	}
	pad := strings.Repeat("The Trustee shall keep accurate records of every transaction and report them annually. ", 50) // ~4.4 KB
	doc := "9. Records\n\n" + pad + "\n\nThe Trustee shall not sell the family residence at 12 Elm Street.\n\n" + pad + "\n"
	spans, _, _ := placeRequirement(context.Background(), doc, "The Trustee shall not sell the family residence at 12 Elm Street.")
	r := newReq(doc, spans)
	got := r.Context()
	if r.Truncated || len(got) > ctxMax || !strings.HasPrefix(got, "[headings above it, a label read from the document's numbering, not part of its text: 9. Records]\n") ||
		strings.Contains(got, "9. Records\n") || !strings.Contains(got, "⟦The Trustee shall not sell the family residence at 12 Elm Street.⟧") || !strings.HasSuffix(got, "annually. …") {
		t.Errorf("a paragraph of its own in a long section: truncated %v, %d bytes: %.300q … %.120q", r.Truncated, len(got), got, got[max(0, len(got)-120):])
	}
}

// TestContextTruncatedWhenTheSentenceStartsBefore: a requirement whose paragraph starts more than the
// window above it, or whose sentence does, is read in a bounded window marked truncated (no colon line
// is kept past the bound any more: the verdict is admitted only at low confidence).
func TestContextTruncatedWhenTheSentenceStartsBefore(t *testing.T) {
	long := strings.Repeat("The Trustee shall keep accurate records of every transaction and report them annually. ", 60) // ~5 KB
	far := "9. Records\nThe Trustee shall not:\n" + long + long + "sell the family residence at 12 Elm Street.\n" + long
	spans, _, _ := placeRequirement(context.Background(), far, "sell the family residence at 12 Elm Street.")
	if r := newReq(far, spans); !r.Truncated || len(r.Context()) > ctxMax || strings.Contains(r.Context(), "The Trustee shall not:") {
		t.Errorf("a paragraph starting 10 KB above: truncated %v, %d bytes", r.Truncated, len(r.Context()))
	}
	run := "Do NOT, " + strings.Repeat("whether acting alone or jointly with any co-trustee, agent or fiduciary, ", 60) + "name Sophia Davenport-Chen as a successor trustee.\n"
	spans, _, _ = placeRequirement(context.Background(), run, "name Sophia Davenport-Chen as a successor trustee.")
	if r := newReq(run, spans); !r.Truncated || len(r.Context()) > ctxMax || !strings.HasPrefix(r.Context(), "… ") {
		t.Errorf("a 4 KB sentence: truncated %v, %d bytes: %.120q", r.Truncated, len(r.Context()), r.Context())
	}
}

// r4Dev runs one check_deviation call with a scripted deviate verdict and section-check answers.
func r4Dev(t *testing.T, memoText, req, ctrlQuote string, check []model.Reply) (*fixture, TaskView) {
	t.Helper()
	f := newFixture(t, memoText, "Article 4. Successor Trustees.\nIf Peggy ceases to serve, Dr. David Chen shall serve as successor Trustee, and then Broadleaf Fiduciary Services, Inc.\nThe Trustee shall retain the family residence at 12 Elm Street.\n")
	memo, trust := f.docs[0].ID, f.docs[1].ID
	dev := tc("check_deviation", m{"requirement": req, "requirementDocId": memo, "controllingDocIds": []string{memo}, "reviewedDocIds": []string{trust}, "issueId": "I1"})
	verdict := m{"part": "the instruction", "verdict": "deviate", "severity": "HIGH", "correction": "Name Sophia Davenport-Chen as a successor trustee.", "document": trust,
		"controlling": m{"documentId": memo, "quote": ctrlQuote}, "reviewed": "If Peggy ceases to serve, Dr. David Chen shall serve as successor Trustee, and then Broadleaf Fiduciary Services, Inc."}
	v := f.run(f.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(dev)},
		"check_deviation": {text(m{"verdicts": []m{verdict}})},
		"section_check":   check,
	}), Config{})
	return f, v
}

func ledgerFindings(t *testing.T, f *fixture, id string) []Finding {
	t.Helper()
	fs, err := load[Finding](context.Background(), f.e, id, kFinding)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// sectionCheckRequest is what the section check was sent.
func sectionCheckRequest(f *fixture) (model.Request, bool) {
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "section_check" {
			return c, true
		}
	}
	return model.Request{}, false
}

// TestSectionCheckNegatedDropsVerdict: an inverted verdict (the memo forbids naming Sophia; the
// verdict asks the draft to name her) is dropped when the check answers that the section negates it.
func TestSectionCheckNegatedDropsVerdict(t *testing.T) {
	t.Parallel()
	memo := "B. Successor Trustees\n\nThe following persons shall NOT serve as successor trustee in any capacity:\n\nMartin Hale, the settlor's former business partner.\n\nSophia Davenport-Chen, the settlor's daughter, as a successor trustee.\n"
	req := "Sophia Davenport-Chen, the settlor's daughter, as a successor trustee."
	f, v := r4Dev(t, memo, req, req, []model.Reply{text(m{"answer": "no", "reason": "the introduction says these persons shall NOT serve"})})
	out := strings.Join(f.outputs(v.ID, "check_deviation"), "")
	if len(v.Findings) != 0 || !strings.Contains(out, "does not impose it as the verdict states it (negated or overridden): the introduction says these persons shall NOT serve") {
		t.Errorf("findings %+v; %s", v.Findings, out)
	}
	req2, ok := sectionCheckRequest(f)
	if !ok || req2.Role != model.Critic || !strings.Contains(req2.Messages[1].Content.(string), "<<<\nB. Successor Trustees\n\nThe following persons shall NOT serve") ||
		!strings.Contains(req2.Messages[1].Content.(string), "⟦Sophia Davenport-Chen, the settlor's daughter, as a successor trustee.⟧\n>>>") {
		t.Errorf("the check was not sent the fenced section on the critic role: %+v", req2)
	}
}

// TestSectionCheckYesAdmitsBenchRequirements: the bench r3 trust requirements (steps 30, 31, 32, 76)
// are admitted on a "yes", the answer and reason recorded on the finding and shown in its claim.
func TestSectionCheckYesAdmitsBenchRequirements(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, req, ctrl string }{
		{"step 30", reqHeadingJoined, "CRITICAL NOTE — Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity."},
		{"step 31", reqEscapedSophia, "Thirty-five percent (35%) of the residuary estate shall NOT be distributed outright."},
		{"step 32", reqEscaped, "Twenty-five percent (25%) of the residuary estate shall be distributed outright and free of trust"},
		{"step 76", reqTwoParagraphs, "The Trust Protector role is administrative and structural, not substantive."},
	} {
		f, v := r4Dev(t, trustMemoExcerpt, c.req, c.ctrl, []model.Reply{text(m{"answer": "yes", "reason": "the section states the requirement as quoted"})})
		fs := ledgerFindings(t, f, v.ID)
		if len(fs) != 1 || fs[0].Check == nil || fs[0].Check.Answer != "yes" || fs[0].Low ||
			!strings.Contains(fs[0].Claim, "An independent check read the surrounding text (") ||
			!strings.Contains(fs[0].Claim, "bytes) and agreed. The checker's reasoning (not a quotation): the section states the requirement as quoted") {
			t.Errorf("%s: findings %+v; %q", c.name, fs, f.outputs(v.ID, "check_deviation"))
		}
	}
}

// TestSectionCheckUnclearOrCutIsLow: an unclear answer, or a "yes" on a section cut to its bound,
// admits the verdict at low confidence, labelled for a lawyer's review (a finding gate at finish).
func TestSectionCheckUnclearOrCutIsLow(t *testing.T) {
	t.Parallel()
	memo := "Note\nDo NOT name Sophia Davenport-Chen as a successor trustee.\n"
	req := "Do NOT name Sophia Davenport-Chen as a successor trustee."
	f, v := r4Dev(t, memo, req, req, []model.Reply{text(m{"answer": "unclear", "reason": "the note may be overridden elsewhere"})})
	if fs := ledgerFindings(t, f, v.ID); len(fs) != 1 || !fs[0].Low || fs[0].Check.Answer != "unclear" || !strings.Contains(fs[0].Claim, "low confidence, for a lawyer's review") {
		t.Errorf("unclear: %+v", fs)
	}
	big := "Note\n" + strings.Repeat("The settlor has considered the family's circumstances and the views of the family council. ", 120) + "Do NOT name Sophia Davenport-Chen as a successor trustee.\n" // in a 11 KB paragraph
	f, v = r4Dev(t, big, req, req, []model.Reply{text(m{"answer": "yes", "reason": "it says so"})})
	if fs := ledgerFindings(t, f, v.ID); len(fs) != 1 || !fs[0].Low || !fs[0].Check.Truncated || !strings.Contains(fs[0].Claim, "that text begins after the requirement's sentence, paragraph or list does") {
		t.Errorf("cut section: %+v", fs)
	}
	if !gated(Finding{Low: true, Citations: []Citation{{Method: "exact"}}}, false) || gated(Finding{Citations: []Citation{{Method: "exact"}}}, false) {
		t.Error("a low-confidence finding is not gated")
	}
}

// TestSectionCheckChargedWithinGrant: the check is one model call charged to check_deviation's grant
// (its step's usage holds it), and with nothing left in the grant the verdict is dropped with a note,
// no call made.
func TestSectionCheckChargedWithinGrant(t *testing.T) {
	t.Parallel()
	cost, sub := 0.0123, 0.01
	memo := "Note\nDo NOT name Sophia Davenport-Chen as a successor trustee.\n"
	req := "Do NOT name Sophia Davenport-Chen as a successor trustee."
	chk := text(m{"answer": "yes", "reason": "it says so"})
	chk.Usage = model.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: &cost}
	f := newFixture(t, memo, "Article 4. Successor Trustees.\nIf Peggy ceases to serve, Dr. David Chen shall serve as successor Trustee, and then Broadleaf Fiduciary Services, Inc.\n")
	memoID, trust := f.docs[0].ID, f.docs[1].ID
	dev := tc("check_deviation", m{"requirement": req, "requirementDocId": memoID, "controllingDocIds": []string{memoID}, "reviewedDocIds": []string{trust}, "issueId": "I1"})
	verdict := text(m{"verdicts": []m{{"part": "the instruction", "verdict": "deviate", "severity": "HIGH", "correction": "Omit Sophia.", "document": trust,
		"controlling": m{"documentId": memoID, "quote": req}, "reviewed": "If Peggy ceases to serve, Dr. David Chen shall serve as successor Trustee, and then Broadleaf Fiduciary Services, Inc."}}})
	verdict.Usage = model.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: &sub}
	v := f.run(f.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(dev)},
		"check_deviation": {verdict},
		"section_check":   {chk},
	}), Config{})
	ss, _ := f.e.Steps(context.Background(), v.ID, 0)
	found := false
	for _, s := range ss {
		if s.Tool == "check_deviation" && s.Kind == "tool_call" && s.Status == "committed" {
			found = true
			if s.usage.Calls != 2 || math.Abs(s.usage.USD-(cost+sub)) > 1e-9 {
				t.Errorf("check_deviation's step is charged %+v, want 2 calls and $%.4f (the subagent and the section check)", s.usage, cost+sub)
			}
		}
	}
	if !found || len(v.Findings) != 1 {
		t.Fatalf("steps %s; findings %+v", f.steps(v.ID), v.Findings)
	}
	// no grant left: dropped with a note, no call
	fake := model.NewFake(nil)
	rt := &agent.Runtime{Chat: fake, Roles: testRoles}
	spans, _, _ := placeRequirement(context.Background(), memo, req)
	chk2, drop, err := (&Engine{}).checkSection(context.Background(), rt, agent.NewBudget(0, 0, 0), newReq(memo, spans), verdictOf(req))
	if err != nil || chk2 != nil || !strings.Contains(drop, "no model call or budget was left") || len(fake.Calls()) != 0 {
		t.Errorf("no grant: check %+v, drop %q, err %v, calls %d", chk2, drop, err, len(fake.Calls()))
	}
}

func verdictOf(quote string) verdict {
	v := verdict{Part: "the instruction", Verdict: "deviate", Severity: "HIGH", Correction: "Omit Sophia."}
	v.Controlling.Quote = quote
	return v
}

// TestContextRendersBlankLinesOnce (lab-r3 r3 verifier L3): a blank line inside a section renders as
// one " / " in a finding's claim, and the requirement is always marked (the marks are the
// requirement's own spans, never a verdict quote's that may be stitched across pieces).
func TestContextRendersBlankLinesOnce(t *testing.T) {
	doc := "B. Successor Trustees\n\nThe following persons shall NOT serve:\n\n\nMartin Hale\r\n\r\nSophia Davenport-Chen\n"
	spans, _, _ := placeRequirement(context.Background(), doc, "Sophia Davenport-Chen")
	r := newReq(doc, spans)
	if got := renderContext(doc, r.Ctx, r.marks(), " / "); got != "B. Successor Trustees / The following persons shall NOT serve: / Martin Hale / ⟦Sophia Davenport-Chen⟧" {
		t.Errorf("%q", got)
	}
}
