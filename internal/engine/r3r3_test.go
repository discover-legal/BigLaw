// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// The lab-r3 round-2 verifier's repros (critique/biglaw-lab-r3-r2-verifier.md), kept as regression
// tests. r2 refused fragments by word lists (guards, qualifiers, governing introductions) and every
// list left a hole; r3 reads every verified requirement in its context instead, so each probe below
// now verifies and must carry, in what the subagent judges and in what the finding quotes, the words
// the fragment dropped. Each failed on 7a5d6be (a verified HIGH deviation quoted the bare fragment as
// what the memo "requires"; the probes had no context to read).

// TestR2VProbesReadInContext (H1, H2, M1, L2, L3, L7): each requirement drops or inverts what the
// document says; its context holds the dropped words.
func TestR2VProbesReadInContext(t *testing.T) {
	cases := []struct{ name, doc, req, want string }{
		{"abbrev Dr. hides guard", "4. Sale\nThe Trustee shall not, without the written consent of Dr. Chen, sell the family residence at 12 Elm Street.\n", "sell the family residence at 12 Elm Street.", "4. Sale\nThe Trustee shall not, without the written consent of Dr. Chen, ⟦sell"},
		{"abbrev Art. hides Do NOT", "Note\nDo NOT, under Art. 4 of the Trust, name Sophia Davenport-Chen as a successor trustee.\n", "name Sophia Davenport-Chen as a successor trustee.", "Note\nDo NOT, under Art. 4 of the Trust, ⟦name"},
		{"marked intro nested", "7. Restrictions\n(a) The Trustee shall not:\n(i) sell the family residence at 12 Elm Street;\n(ii) borrow against the trust assets;\n(b) The Trustee shall invest prudently.\n", "sell the family residence at 12 Elm Street", "7. Restrictions\n(a) The Trustee shall not:\n(i) ⟦sell"},
		{"marked intro numbered", "1. Distributions\n2. The following persons shall NOT serve as Trustee:\n(a) Sophia Davenport-Chen, the settlor's daughter.\n", "Sophia Davenport-Chen, the settlor's daughter.", "2. The following persons shall NOT serve as Trustee:\n(a) ⟦Sophia"},
		{"other than", "5. Sale\nOther than the family residence at 12 Elm Street, the Trustee shall sell all real property of the trust.\n", "the Trustee shall sell all real property of the trust.", "Other than the family residence at 12 Elm Street, ⟦the Trustee"},
		{"save for", "5. Gifts\nSave for Sophia, each grandchild of the settlor shall receive $10,000 outright.\n", "each grandchild of the settlor shall receive $10,000 outright.", "Save for Sophia, ⟦each"},
		{"apart from", "5. Gifts\nApart from Sophia, each grandchild of the settlor shall receive $10,000 outright.\n", "each grandchild of the settlor shall receive $10,000 outright.", "Apart from Sophia, ⟦each"},
		{"in the event", "5. Gifts\nIn the event that Sophia predeceases the settlor, Tommy shall receive her entire share outright.\n", "Tommy shall receive her entire share outright.", "In the event that Sophia predeceases the settlor, ⟦Tommy"},
		{"when", "5. Gifts\nWhen Sophia reaches age 40, the trustee shall distribute her share outright.\n", "the trustee shall distribute her share outright.", "When Sophia reaches age 40, ⟦the trustee"},
		{"semicolon proviso", "5. Sale\nThe Trustee may sell the family residence; provided that Sophia consents in writing.\n", "The Trustee may sell the family residence", "⟦The Trustee may sell the family residence⟧; provided that Sophia consents in writing."},
		{"colon proviso", "5. Sale\nThe Trustee may sell the family residence: provided, however, that Sophia consents in writing.\n", "The Trustee may sell the family residence", "⟦The Trustee may sell the family residence⟧: provided, however, that Sophia consents"},
		{"unmarked list past first line", "The Trustee shall not:\nborrow against the trust assets;\nsell the family residence at 12 Elm Street;\npledge the shares.\n", "sell the family residence at 12 Elm Street", "The Trustee shall not:\nborrow against the trust assets;\n⟦sell"},
		{"unmarked list, blank lines", "The Trustee shall not:\n\nborrow against the trust assets;\n\nsell the family residence at 12 Elm Street.\n", "sell the family residence at 12 Elm Street.", "The Trustee shall not:\n…\n⟦sell"},
		{"heading two lines up", "Prohibited Instructions\n\nThe Trustee shall sell the family residence at 12 Elm Street.\n", "The Trustee shall sell the family residence at 12 Elm Street.", "Prohibited Instructions\n\n⟦The Trustee"},
		{"zero-width in NOT", "Note\nDo N\u200bOT, in any capacity, name Sophia Davenport-Chen as a successor trustee.\n", "name Sophia Davenport-Chen as a successor trustee.", "Do N\u200bOT, in any capacity, ⟦name"},
		{"neither nor", "Note\nNeither David nor Sophia, nor any spouse of theirs, shall serve as Trustee.\n", "shall serve as Trustee.", "Neither David nor Sophia, nor any spouse of theirs, ⟦shall"},
		{"paren negation in head", "Note\nSophia (who shall never serve as a fiduciary), Tommy and David shall each receive one third of the residue.\n", "Tommy and David shall each receive one third of the residue.", "(who shall never serve as a fiduciary), ⟦Tommy"},
		{"but only if", "5. Sale\nThe Trustee may sell the family residence, but only if Sophia consents in writing.\n", "The Trustee may sell the family residence", "⟦The Trustee may sell the family residence⟧, but only if Sophia consents"},
		{"skip reversal", "5. Gifts\nSophia's share shall be distributed outright at age 25. The client has withdrawn that instruction. Tommy's share shall be held in trust until age 30.\n", "Sophia's share shall be distributed outright at age 25. … Tommy's share shall be held in trust until age 30.", "⟦Sophia's share shall be distributed outright at age 25.⟧ The client has withdrawn that instruction. ⟦Tommy's"},
		{"skip reversal never", "5. Gifts\nThe Trustee shall sell the residence. Peggy now says never do that. The proceeds shall be divided equally.\n", "The Trustee shall sell the residence. … The proceeds shall be divided equally.", "Peggy now says never do that."},
		{"sibling heading dodge", "A. General\nB. Trustees\nThe following shall NOT serve as Trustee:\nC. Sophia Davenport-Chen, the settlor's daughter.\n", "Sophia Davenport-Chen, the settlor's daughter.", "The following shall NOT serve as Trustee:\nC. ⟦Sophia"},
		{"smart quote join", "Note\nThe Trustee shall “never” sell the residence. The Trustee shall invest.\n", "sell the residence.", "The Trustee shall “never” ⟦sell"},
		{"negating heading, no colon", "Prohibited Instructions\nThe Trustee shall sell the family residence at 12 Elm Street.\n", "The Trustee shall sell the family residence at 12 Elm Street.", "Prohibited Instructions\n⟦The Trustee"},
		{"list opened without a colon", "The following persons shall NOT serve as Trustee\n(a) Sophia Davenport-Chen;\n(b) Martin Hale.\n", "Martin Hale.", "The following persons shall NOT serve as Trustee\n…\n(b) ⟦Martin"},
		{"short unmarked items", "4. Trustees\nThe following shall NOT serve as Trustee:\nSophia Davenport-Chen\nMartin Hale\n", "Martin Hale", "The following shall NOT serve as Trustee:\nSophia Davenport-Chen\n⟦Martin Hale⟧"},
	}
	for _, c := range cases {
		spans, bad, err := placeRequirement(context.Background(), c.doc, c.req)
		if err != nil || bad != "" {
			t.Errorf("%s: %q did not verify (bad %q, err %v)", c.name, c.req, bad, err)
			continue
		}
		r := reqText{Text: c.doc, Spans: spans, Ctx: contextOf(c.doc, spans)}
		if got := r.Context(); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q is read in %q, which lacks %q", c.name, c.req, got, c.want)
		}
	}
	// a requirement typed without the zero-width character verifies in the document that has it (L3)
	if _, bad, _ := placeRequirement(context.Background(), cases[14].doc, "Do NOT, in any capacity, name Sophia Davenport-Chen as a successor trustee."); bad != "" {
		t.Errorf("format characters are not folded: bad %q", bad)
	}
	// a sibling of the item under a nested introduction is not governed by it, and the list's
	// other items are passed over
	doc := cases[2].doc
	spans, _, _ := placeRequirement(context.Background(), doc, "The Trustee shall invest prudently.")
	if got := (reqText{Text: doc, Spans: spans, Ctx: contextOf(doc, spans)}).Context(); strings.Contains(got, "borrow") || !strings.HasPrefix(got, "7. Restrictions") {
		t.Errorf("(b) read as %q", got)
	}
}

// r2vDev runs one check_deviation call with a scripted deviate verdict and returns the run.
func r2vDev(t *testing.T, memoText, req, ctrlQuote string) (*fixture, TaskView) {
	t.Helper()
	f := newFixture(t, memoText, "Article 4. Successor Trustees.\nIf Peggy ceases to serve, Dr. David Chen shall serve as successor Trustee, and then Broadleaf Fiduciary Services, Inc.\nThe Trustee shall retain the family residence at 12 Elm Street.\n")
	memo, trust := f.docs[0].ID, f.docs[1].ID
	dev := tc("check_deviation", m{"requirement": req, "requirementDocId": memo, "controllingDocIds": []string{memo}, "reviewedDocIds": []string{trust}, "issueId": "I1"})
	verdict := m{"part": "the instruction", "verdict": "deviate", "severity": "HIGH", "correction": "Name Sophia Davenport-Chen as a successor trustee.", "document": trust,
		"controlling": m{"documentId": memo, "quote": ctrlQuote}, "reviewed": "If Peggy ceases to serve, Dr. David Chen shall serve as successor Trustee, and then Broadleaf Fiduciary Services, Inc."}
	v := f.run(f.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(dev)},
		"check_deviation": {text(m{"verdicts": []m{verdict}})},
	}), Config{})
	return f, v
}

// subagentRequest is what the check_deviation subagent was sent.
func subagentRequest(f *fixture) string {
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "check_deviation" {
			return c.Messages[1].Content.(string)
		}
	}
	return ""
}

// TestR2VFragmentsQuotedInContext (H1, H2, H3 end to end, L8): the subagent is sent the requirement
// in context, and a verified deviation never quotes the bare fragment as what the memo requires: its
// claim and its controlling citation quote the governing words, and its correction is labelled the
// analyst's, not the document's.
func TestR2VFragmentsQuotedInContext(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, memo, req, ctrl, want string }{
		{"H1 abbreviation", "Note\nDo NOT, under Art. 4 of the Trust, name Sophia Davenport-Chen as a successor trustee.\n",
			"name Sophia Davenport-Chen as a successor trustee.", "name Sophia Davenport-Chen as a successor trustee.", "Do NOT, under Art. 4 of the Trust, "},
		{"H2 marked introduction", "7. Restrictions\n(a) The following persons shall NOT serve as successor trustee:\n(i) Sophia Davenport-Chen, the settlor's daughter, as a successor trustee in any capacity;\n(ii) Martin Hale, the settlor's former business partner;\n(b) The Trustee shall invest prudently.\n",
			"Sophia Davenport-Chen, the settlor's daughter, as a successor trustee in any capacity", "Sophia Davenport-Chen, the settlor's daughter, as a successor trustee in any capacity", "(a) The following persons shall NOT serve as successor trustee:"},
		{"H3 the verdict's own fragment", trustMemoExcerpt, "CRITICAL NOTE — Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity.",
			"name Sophia Davenport-Chen as a successor trustee in any capacity", "Do NOT ⟦name Sophia Davenport-Chen as a successor trustee in any capacity⟧"},
	} {
		f, v := r2vDev(t, c.memo, c.req, c.ctrl)
		guard := strings.ReplaceAll(c.want, "⟦", "")
		guard = guard[:strings.IndexAny(guard+"⟧", "⟧")]
		if req := subagentRequest(f); !strings.Contains(req, "requirementContext") || !strings.Contains(strings.NewReplacer("⟦", "", "⟧", "").Replace(req), strings.ReplaceAll(guard, "\n", `\n`)) {
			t.Errorf("%s: the subagent was not sent the governing words %q: %.600q", c.name, guard, req)
		}
		if len(v.Findings) != 1 {
			t.Fatalf("%s: findings %+v; %q", c.name, v.Findings, f.outputs(v.ID, "check_deviation"))
		}
		x := v.Findings[0]
		if !strings.Contains(x.Claim, strings.ReplaceAll(c.want, "\n", " / ")) || !strings.Contains(x.Citations[0].Quote, guard) ||
			!strings.Contains(x.Claim, "The analyst's correction (not a quotation): Name Sophia") || strings.Contains(x.Claim, "which requires “"+c.ctrl) {
			t.Errorf("%s: the finding does not quote the governing words %q with the verdict's words marked:\n%s\n%+v", c.name, c.want, x.Claim, x.Citations)
		}
	}
}

// TestR2VVerdictQuoteOutsideContextRefused (H3): a verdict whose controlling quote is real text of the
// memo but outside the requirement's context (another paragraph) is dropped, not admitted as what
// the memo requires.
func TestR2VVerdictQuoteOutsideContextRefused(t *testing.T) {
	t.Parallel()
	f, v := r2vDev(t, trustMemoExcerpt, "CRITICAL NOTE — Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity.",
		"Peggy wants Martin to serve as an administrative safety valve")
	if out := strings.Join(f.outputs(v.ID, "check_deviation"), ""); len(v.Findings) != 0 || !strings.Contains(out, "its controlling quote is not in the requirement's passage") {
		t.Errorf("findings %+v; %s", v.Findings, out)
	}
}

// TestBenchRequirementsReadInContext: the bench r3 trust requirements (steps 30, 31, 32, 76) still
// verify, each in its context, and the finding on step 32 quotes that context (TestDevMultiPartRequirementRuns).
func TestBenchRequirementsReadInContext(t *testing.T) {
	for _, c := range []struct{ name, req, want string }{
		{"step 30", reqHeadingJoined, "B. ⟦Successor Trustees⟧\n⟦Upon Peggy's death"},
		{"step 31", reqEscapedSophia, "B. ⟦Sophia's Share — Thirty-Five Percent (35%)⟧\n⟦Thirty-five percent (35%) of the residuary estate shall NOT"},
		{"step 32", reqEscaped, "C. ⟦Tommy's Share — Twenty-Five Percent (25%)⟧\n⟦Twenty-five percent"},
		{"step 76", reqTwoParagraphs, "authorize.⟧\nThese limitations are essential. Peggy wants Martin"},
	} {
		spans, bad, err := placeRequirement(context.Background(), trustMemoExcerpt, c.req)
		if err != nil || bad != "" || len(spans) != 2 {
			t.Errorf("%s: spans %v, bad %q, err %v", c.name, spans, bad, err)
			continue
		}
		r := reqText{Text: trustMemoExcerpt, Spans: spans, Ctx: contextOf(trustMemoExcerpt, spans)}
		if got := r.Context(); !strings.Contains(got, c.want) || len(got) > ctxMax+200 {
			t.Errorf("%s: context (%d bytes) %q lacks %q", c.name, len(got), got, c.want)
		}
	}
}

// TestContextIsBounded: a requirement in a long paragraph under a long introduction is quoted with
// about ctxMax bytes, the requirement whole and marked, the introduction's end (its colon) kept.
func TestContextIsBounded(t *testing.T) {
	long := strings.Repeat("The Trustee shall keep accurate records of every transaction and report them annually. ", 30)
	doc := "9. Records\n" + long + "Subject to the foregoing, the Trustee shall not:\n" + long + "sell the family residence at 12 Elm Street. " + long + "\n"
	spans, bad, _ := placeRequirement(context.Background(), doc, "sell the family residence at 12 Elm Street.")
	if bad != "" {
		t.Fatalf("bad %q", bad)
	}
	r := reqText{Text: doc, Spans: spans, Ctx: contextOf(doc, spans)}
	got := r.Context()
	if len(got) > ctxMax+100 || !strings.Contains(got, "⟦sell the family residence at 12 Elm Street.⟧") || !strings.Contains(got, "the Trustee shall not:") || !strings.HasPrefix(got, "9. Records") {
		t.Errorf("%d bytes: %q", len(got), got)
	}
}

// TestR2VRealTablesNotBoilerplate (M4): a two-column allocation (one tab), a dot-leadered damages
// schedule and a chronology of years are read; a table with rising numbers and no Contents heading
// is read too. Only a page with a Contents heading, or dot-leadered heading-like entries whose page
// numbers rise, is a table of contents.
func TestR2VRealTablesNotBoilerplate(t *testing.T) {
	var two, rent, years, rise strings.Builder
	two.WriteString("Allocation of Shares\n")
	for i, who := range []string{"Oceanic Fund I LP", "Bellini Family Trust", "Delgado Holdings", "Whitmore Capital", "Ostrowski IRA", "Meridian Nominee"} {
		fmt.Fprintf(&two, "%s\t%d\n", who, 500*(i+1))
	}
	rent.WriteString("Schedule A — Damages Claimed\n")
	for _, l := range []string{"Unpaid rent ............ 9500", "Late fees .............. 450", "Repair costs ........... 2300", "Attorney fees .......... 7800", "Interest ............... 1200"} {
		rent.WriteString(l + "\n")
	}
	years.WriteString("Chronology\n")
	for i, ev := range []string{"Account opened at Meridian", "First omnibus trade", "Bellini joins club", "OQR review begins", "Shared drive deleted"} {
		fmt.Fprintf(&years, "%s   %d\n", ev, 2019+i)
	}
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&rise, "Holder %d\t%d\n", i, 100*i)
	}
	for name, text := range map[string]string{"two-column allocation (tab)": two.String(), "damages schedule with leaders": rent.String(), "chronology (PDF spacing, years)": years.String(), "tab table, rising numbers": rise.String()} {
		if why, _ := skipWhy(Visit{DocumentID: "d", SectionID: "s9", text: text}, nil); why != "" {
			t.Errorf("%s skipped as %s:\n%s", name, why, text)
		}
	}
}
