// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/model"
)

// trustMemoExcerpt is part of the trust task's client instruction memo as the LAB driver converts it
// (headings and paragraphs one per line).
const trustMemoExcerpt = `B. Successor Trustees
Upon Peggy's death, incapacity, resignation, or inability to serve, the following individuals and entities shall serve as successor Trustee, in the following order of priority:
First Successor Trustee: Dr. David Chen, Peggy's son, currently residing in Palo Alto, California.
Second Successor Trustee: Broadleaf Fiduciary Services, Inc., a California corporation, located at 200 California Street, Suite 1050, San Francisco, California 94111.
CRITICAL NOTE — Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity. Peggy and I discussed this matter at length during our November 2024 meeting. Given Sophia's Chapter 7 bankruptcy history (discharged in 2018) and her acknowledged pattern of past financial difficulties, Peggy is firm and clear in her decision that Sophia should not serve in any fiduciary capacity with respect to trust assets. This is a deliberate and considered decision by the client, and it should not be second-guessed or modified in drafting. Please ensure that the successor trustee line goes directly from David to Broadleaf Fiduciary Services, with no intermediate individual trustee and no provision naming Sophia as an alternate or backup to David. If David is unwilling or unable to serve, the trust goes directly to institutional management by Broadleaf.
B. Sophia's Share — Thirty-Five Percent (35%)
Thirty-five percent (35%) of the residuary estate shall NOT be distributed outright. Instead, Sophia's share shall be held in a continuing sub-trust titled the "Sophia Davenport-Chen Protected Trust," which shall be administered as a spendthrift trust with the following terms and conditions:
C. Tommy's Share — Twenty-Five Percent (25%)
Twenty-five percent (25%) of the residuary estate shall be distributed outright and free of trust to Thomas "Tommy" Davenport-Chen. Like David's share, Tommy's share is to be distributed without any continuing trust restrictions.
(a) To remove and replace any corporate trustee serving under the trust agreement. This power applies only to corporate or institutional trustees (such as Broadleaf Fiduciary Services, Inc.), and not to individual family member trustees.
(ii) Accelerate distributions to any beneficiary beyond what the trust terms otherwise permit — that is, the Trust Protector may not direct or authorize the Trustee to make distributions earlier than, or in greater amounts than, the trust's distribution provisions authorize.
These limitations are essential. Peggy wants Martin to serve as an administrative safety valve — someone who can address institutional trustee problems, tax law changes, and interpretive ambiguities — but she does not want the Trust Protector to have any power over who receives what, in what amounts, or on what timeline. The Trust Protector role is administrative and structural, not substantive. Please draft these limitations prominently, clearly, and in a manner that leaves no room for ambiguity or creative interpretation.`

// The requirements the lead sent check_deviation in the bench r3 trust run (steps.json), as it sent them.
var (
	reqHeadingJoined = "Successor Trustees: Upon Peggy's death, incapacity, resignation, or inability to serve, the following individuals and entities shall serve as successor Trustee, in the following order of priority: First Successor Trustee: Dr. David Chen, Peggy's son, currently residing in Palo Alto, California. Second Successor Trustee: Broadleaf Fiduciary Services, Inc., a California corporation, located at 200 California Street, Suite 1050, San Francisco, California 94111. CRITICAL NOTE — Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity." // step 30: a heading joined to its list
	reqEscaped       = "Tommy's Share — Twenty-Five Percent (25%): Twenty-five percent (25%) of the residuary estate shall be distributed outright and free of trust to Thomas \\\"Tommy\\\" Davenport-Chen. Like David's share, Tommy's share is to be distributed without any continuing trust restrictions."                                                                                                                                                                                                                                                                                   // step 32: heading, and \"Tommy\" escaped
	reqEscapedSophia = "Sophia's Share — Thirty-Five Percent (35%): Thirty-five percent (35%) of the residuary estate shall NOT be distributed outright. Instead, Sophia's share shall be held in a continuing sub-trust titled the \\\"Sophia Davenport-Chen Protected Trust,\\\" which shall be administered as a spendthrift trust with the following terms and conditions"                                                                                                                                                                                                                    // step 31
	reqTwoParagraphs = "The Trust Protector may not direct or authorize the Trustee to make distributions earlier than, or in greater amounts than, the trust's distribution provisions authorize. The Trust Protector role is administrative and structural, not substantive."                                                                                                                                                                                                                                                                                                                   // step 76: two sentences from two paragraphs
	reqParaphrase    = "The Trust Protector shall NOT have the power to remove or replace individual trustees. The Trust Protector shall have the power to remove any corporate Trustee then serving hereunder and to appoint a replacement corporate Trustee, but only a state or federally chartered trust company or bank trust department with assets under management of at least One Hundred Million Dollars ($100,000,000). The Trust Protector shall not have the power to remove or replace any individual Trustee."                                                                     // step 72: the memo says no such thing in these words
)

// TestRequirementPartsVerify (bench r3, trust: 7 of 17 check_deviation calls were refused on the
// exact-quote check, so the lead dropped the residuary issue): JSON escape artefacts are undone, a
// requirement joined from several passages verifies part by part (each part the document's own
// words, joined by an ellipsis), and a real paraphrase stays refused, naming its part.
func TestRequirementPartsVerify(t *testing.T) {
	for _, c := range []struct {
		name, req string
		parts     int
	}{{"heading joined", reqHeadingJoined, 2}, {"escaped", reqEscaped, 2}, {"escaped sophia", reqEscapedSophia, 2}, {"two paragraphs", reqTwoParagraphs, 2}} {
		parts, bad, _ := requirementParts(context.Background(), trustMemoExcerpt, c.req)
		if bad != "" || len(parts) != c.parts {
			t.Errorf("%s: parts %q, bad %q", c.name, parts, bad)
		}
		for _, p := range parts {
			if !strings.Contains(trustMemoExcerpt, p) {
				t.Errorf("%s: part %q is not the memo's own words", c.name, p)
			}
		}
	}
	if parts, bad, _ := requirementParts(context.Background(), trustMemoExcerpt, reqParaphrase); bad == "" || !strings.Contains(bad, "shall NOT have the power to remove or replace individual trustees") {
		t.Errorf("paraphrase: parts %q, bad %q", parts, bad)
	}
	for _, p := range []string{"Sophia: 35%", "Tommy: outright and free of trust", "Trust Protector. Accelerate. Corporate trustee. Remove.", " … … ", ". . .", "Successor Trustees: … … …"} {
		if _, bad, _ := requirementParts(context.Background(), trustMemoExcerpt, p); bad == "" { // words of the memo, in pieces: still a paraphrase
			t.Errorf("%q verified as the memo's words", p)
		}
	}
	if got := unescape(`Thomas \"Tommy\" \u2014 a\nb`); got != "Thomas \"Tommy\" — a\nb" {
		t.Errorf("unescape %q", got)
	}
}

// TestDevMultiPartRequirementRuns: check_deviation over the bench's escaped, heading-joined
// requirement runs (it was refused), its subagent gets the memo's words part by part, and a
// deviation that restates the requirement stands on the verified parts; the paraphrase is refused
// with a bounded exact passage to copy, before any model call.
func TestDevMultiPartRequirementRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, trustMemoExcerpt, "Article 8. Residuary.\nThe residue shall be distributed thirty percent (30%) to Thomas Davenport-Chen outright.\n")
	memo, trust := f.docs[0].ID, f.docs[1].ID
	dev := func(req string) model.ToolCall {
		return tc("check_deviation", m{"requirement": req, "requirementDocId": memo, "controllingDocIds": []string{memo}, "reviewedDocIds": []string{trust}, "issueId": "I1"})
	}
	verdict := m{"part": "Tommy's share", "verdict": "deviate", "severity": "HIGH", "correction": "Distribute Twenty-five percent (25%) to Tommy.", "document": trust,
		"controlling": m{"documentId": memo, "quote": unescape(reqEscaped)}, "reviewed": "The residue shall be distributed thirty percent (30%) to Thomas Davenport-Chen outright."}
	v := f.run(f.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(dev(reqParaphrase)), calls(dev(reqEscaped))},
		"check_deviation": {text(m{"verdicts": []m{verdict}})},
	}), Config{})
	out := f.outputs(v.ID, "check_deviation")
	var reqs []string
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "check_deviation" {
			reqs = append(reqs, c.Messages[1].Content.(string))
		}
	}
	if len(out) != 2 || !strings.HasPrefix(out[0], "failed") || !strings.HasPrefix(out[1], "committed") || len(reqs) != 1 ||
		!strings.Contains(reqs[0], "Tommy's Share — Twenty-Five Percent (25%) … Twenty-five percent (25%) of the residuary estate") {
		t.Fatalf("check_deviation %q\nsubagent got %q", out, reqs)
	}
	if !strings.Contains(reqs[0], `"requirementContext":"C. ⟦Tommy's Share — Twenty-Five Percent (25%)⟧\n⟦Twenty-five percent (25%)`) {
		t.Fatalf("the subagent did not get the requirement in context: %q", reqs[0])
	}
	if len(v.Findings) != 1 || len(v.Findings[0].Citations) != 2 || !strings.HasPrefix(v.Findings[0].Citations[0].Quote, "C. Tommy's Share — Twenty-Five Percent (25%)\nTwenty-five percent") ||
		!strings.Contains(v.Findings[0].Claim, "“C. ⟦Tommy's Share — Twenty-Five Percent (25%) / Twenty-five percent (25%)") {
		t.Fatalf("findings %+v", v.Findings)
	}
	for _, c := range v.Findings[0].Citations {
		if c.Method == "none" {
			t.Fatalf("citation %+v did not verify", c)
		}
	}
}

// TestStitchedRequirementRefused (lab-r3 verifier H1): a requirement stitched from real fragments is
// refused when its parts are not one passage in the document's order: a later part that starts
// inside a sentence the gap leaves out (dropping "Do NOT", "shall NOT", "except" or a condition), a
// gap over a heading or a list's introduction, or parts out of order. (r3: a fragment of one passage
// verifies and is read in its context: TestOnePartFragmentReadInContext.)
// On 1814a6a the first three verified and the third became a verified HIGH deviation telling the
// lawyer to name Sophia as successor trustee, the opposite of the memo.
func TestStitchedRequirementRefused(t *testing.T) {
	const contract = "7. Assignment\nThe Seller shall not, except with the Buyer's prior written consent, assign this Agreement to any person. The Buyer may assign this Agreement to an affiliate on notice.\n8. Fees\nAll fees are payable in cash, unless the Buyer elects otherwise in writing, within thirty days of invoice. Late fees accrue at one percent per month.\n9. Licence\nLicensee shall not sublicense the Software. Licensee shall pay the licence fees quarterly in advance.\n"
	for _, c := range []struct{ doc, req string }{
		// memo: "Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity."
		{trustMemoExcerpt, "the following individuals and entities shall serve as successor Trustee … Sophia Davenport-Chen as a successor trustee in any capacity."},
		// memo: Sophia's 35% "shall NOT be distributed outright"
		{trustMemoExcerpt, "Thirty-five percent (35%) of the residuary estate shall … be distributed outright and free of trust to Thomas"},
		// the colon join of two fragments that names Sophia as successor trustee
		{trustMemoExcerpt, "the following individuals and entities shall serve as successor Trustee, in the following order of priority: Sophia Davenport-Chen as a successor trustee in any capacity."},
		// the whole intro sentence, then the tail of the CRITICAL NOTE: still a tail, and lines apart
		{trustMemoExcerpt, "Upon Peggy's death, incapacity, resignation, or inability to serve, the following individuals and entities shall serve as successor Trustee, in the following order of priority: … Sophia Davenport-Chen as a successor trustee in any capacity."},
		// out of the document's order
		{trustMemoExcerpt, "Like David's share, Tommy's share is to be distributed without any continuing trust restrictions. … Twenty-five percent (25%) of the residuary estate shall be distributed outright and free of trust to Thomas \"Tommy\" Davenport-Chen."},
		// two sections apart (a heading and a list's introduction between them)
		{trustMemoExcerpt, "Thirty-five percent (35%) of the residuary estate shall NOT be distributed outright. … Like David's share, Tommy's share is to be distributed without any continuing trust restrictions."},
		// clause tails that drop a negation, an exception, a condition
		{contract, "7. Assignment … assign this Agreement to any person."},
		{contract, "The Buyer may assign this Agreement to an affiliate on notice. … assign this Agreement to any person."},
		{contract, "8. Fees … within thirty days of invoice."},
		{contract, "9. Licence … sublicense the Software. Licensee shall pay the licence fees quarterly in advance."},
		// a short part that is not a line of its own
		{contract, "Licensee shall … sublicense the Software."},
	} {
		parts, bad, err := requirementParts(context.Background(), c.doc, c.req)
		if err != nil || bad == "" {
			t.Errorf("stitched requirement verified as the document's words: %q (parts %q, err %v)", c.req, parts, err)
		}
	}
	if spans, bad, _ := placeRequirement(context.Background(), contract, "8. Fees … All fees are payable in cash"); bad != "" ||
		!strings.Contains(renderContext(contract, contextOf(contract, spans), nil, "\n"), "unless the Buyer elects otherwise in writing") {
		t.Errorf("a part that ends before its qualifier: bad %q, or its context drops the qualifier", bad)
	}
	for _, req := range []string{ // whole sentences and lines of one passage still verify
		"7. Assignment … The Seller shall not, except with the Buyer's prior written consent, assign this Agreement to any person.",
		"Licensee shall not sublicense the Software. … Licensee shall pay the licence fees quarterly in advance.",
		"All fees are payable in cash, unless the Buyer elects otherwise in writing, within thirty days of invoice. … Late fees accrue at one percent per month.",
	} {
		if parts, bad, err := requirementParts(context.Background(), contract, req); err != nil || bad != "" || len(parts) != 2 {
			t.Errorf("%q: parts %q, bad %q, err %v", req, parts, bad, err)
		}
	}
}

// TestEllipsisJoinVerifies (lab-r3 verifier H4): the " … " join the refusal and check_deviation.md
// invite verifies for two whole sentences of one passage, written ". … ", " … " or " ... " (it was
// always refused: the cut kept the ellipsis on the part).
func TestEllipsisJoinVerifies(t *testing.T) {
	req := "Twenty-five percent (25%) of the residuary estate shall be distributed outright and free of trust to Thomas \"Tommy\" Davenport-Chen. … Like David's share, Tommy's share is to be distributed without any continuing trust restrictions."
	for _, r := range []string{req, strings.ReplaceAll(req, ". … ", " … "), strings.ReplaceAll(req, ". … ", ". ... "), strings.ReplaceAll(req, ". … ", " ... ")} {
		if parts, bad, err := requirementParts(context.Background(), trustMemoExcerpt, r); err != nil || bad != "" || len(parts) != 2 {
			t.Errorf("an ellipsis join of two exact sentences: %q: parts %q, bad %q, err %v", r, parts, bad, err)
		}
	}
}

// TestRequirementPartsBounded (lab-r3 verifier L1): a 600 KB controlling document and a requirement
// of 41 sentences that each verify alone: the joins are capped (refused as a whole, at once), a
// capped requirement over that document is placed by one index, and a cancelled ctx stops it.
func TestRequirementPartsBounded(t *testing.T) {
	var doc strings.Builder
	var sent []string
	for i := 0; doc.Len() < 600_000; i++ {
		s := fmt.Sprintf("Clause %d: the borrower shall maintain covenant number %d at all times.", i, i)
		doc.WriteString(s + "\n\n")
		if i%97 == 0 && len(sent) < 41 {
			sent = append(sent, s)
		}
	}
	text := doc.String()
	t0 := time.Now()
	if _, bad, _ := requirementParts(context.Background(), text, strings.Join(sent, " ")); bad == "" {
		t.Error("41 sentences from all over the document verified as one requirement")
	}
	near := strings.Join([]string{"Clause 500: the borrower shall maintain covenant number 500 at all times.", "Clause 501: the borrower shall maintain covenant number 501 at all times."}, " … ")
	if parts, bad, err := requirementParts(context.Background(), text, near); bad != "" || err != nil || len(parts) != 2 {
		t.Errorf("two adjacent clauses: parts %q, bad %q, err %v", parts, bad, err)
	}
	if d := time.Since(t0); d > 5*time.Second {
		t.Errorf("requirementParts took %v over 600 KB", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := requirementParts(ctx, text, near); err == nil {
		t.Error("a cancelled ctx did not stop requirementParts")
	}
}

// trusteeList is a list whose introduction carries the negation its items do not.
const trusteeList = "4. Trustees\nThe following persons shall NOT serve as Trustee in any capacity:\n(a) Sophia Davenport-Chen, the settlor's daughter.\n(b) Martin Hale, the settlor's former business partner.\nThe Trustee shall not:\nsell the family home without the consent of all beneficiaries.\n5. Distributions\nThe Trustee shall distribute the income quarterly.\n"

// TestOnePartFragmentReadInContext (lab-r3 r3, replacing r2's whole-part refusals): a fragment that
// drops its sentence's negation, a list item without its negating introduction, and a line under
// "The Trustee shall not:" verify, and each is read in a context that holds what governs it; a whole
// sentence's context is its paragraph and heading.
func TestOnePartFragmentReadInContext(t *testing.T) {
	for _, c := range []struct{ doc, req, want string }{
		{trustMemoExcerpt, "name Sophia Davenport-Chen as a successor trustee in any capacity.", "CRITICAL NOTE — Do NOT ⟦name Sophia"},
		{trustMemoExcerpt, "Sophia should not serve in any fiduciary capacity with respect to trust assets", "Peggy is firm and clear in her decision that ⟦Sophia should not"},
		{trusteeList, "(b) Martin Hale, the settlor's former business partner.", "The following persons shall NOT serve as Trustee in any capacity:"},
		{trusteeList, "Martin Hale, the settlor's former business partner.", "4. Trustees\nThe following persons shall NOT serve as Trustee in any capacity:"},
		{trusteeList, "Sophia Davenport-Chen, the settlor's daughter.", "The following persons shall NOT serve as Trustee in any capacity:\n(a) ⟦Sophia"},
		{trusteeList, "sell the family home without the consent of all beneficiaries.", "The Trustee shall not:\n⟦sell the family home"},
		{trustMemoExcerpt, "CRITICAL NOTE — Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity.", "B. Successor Trustees\nUpon Peggy's death"},
		{trusteeList, "The Trustee shall distribute the income quarterly.", "5. Distributions\n⟦The Trustee shall distribute"},
	} {
		spans, bad, err := placeRequirement(context.Background(), c.doc, c.req)
		if err != nil || bad != "" {
			t.Errorf("%q: bad %q, err %v", c.req, bad, err)
			continue
		}
		r := reqText{Text: c.doc, Spans: spans, Ctx: contextOf(c.doc, spans)}
		if got := r.Context(); !strings.Contains(got, c.want) {
			t.Errorf("%q: context %q lacks %q", c.req, got, c.want)
		}
	}
}
