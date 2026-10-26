// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"math"
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
	if !strings.Contains(reqs[0], `\nC. ⟦Tommy's Share — Twenty-Five Percent (25%)⟧\n⟦Twenty-five percent (25%)`) {
		t.Fatalf("the subagent did not get the requirement in context: %q", reqs[0])
	}
	if len(v.Findings) != 1 || len(v.Findings[0].Citations) != 2 || !strings.Contains(v.Findings[0].Citations[0].Quote, "C. Tommy's Share — Twenty-Five Percent (25%)\nTwenty-five percent") ||
		!strings.Contains(v.Findings[0].Claim, "C. ⟦Tommy's Share — Twenty-Five Percent (25%)⟧ / ⟦Twenty-five percent (25%)") {
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
		!strings.Contains(newReq(contract, spans).Context(), "unless the Buyer elects otherwise in writing") {
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
		r := newReq(c.doc, spans)
		if got := r.Context(); !strings.Contains(got, c.want) {
			t.Errorf("%q: context %q lacks %q", c.req, got, c.want)
		}
	}
}

// requirementParts verifies a requirement in a document's text and returns the document's words for
// it, part by part (placeRequirement).
func requirementParts(ctx context.Context, text, req string) (parts []string, bad string, err error) {
	spans, bad, err := placeRequirement(ctx, text, req)
	for _, s := range spans {
		parts = append(parts, text[s[0]:s[1]])
	}
	return parts, bad, err
}

// newReq is the requirement at spans of text, read in its section.
func newReq(text string, spans [][2]int) reqText {
	r := reqText{Text: text, Spans: spans}
	r.Ctx, r.Path, r.Truncated = contextOf(text, spans)
	return r
}

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
		{"unmarked list, blank lines", "The Trustee shall not:\n\nborrow against the trust assets;\n\nsell the family residence at 12 Elm Street.\n", "sell the family residence at 12 Elm Street.", "The Trustee shall not:\n\nborrow against the trust assets;\n\n⟦sell"},
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
		{"list opened without a colon", "The following persons shall NOT serve as Trustee\n(a) Sophia Davenport-Chen;\n(b) Martin Hale.\n", "Martin Hale.", "The following persons shall NOT serve as Trustee\n(a) Sophia Davenport-Chen;\n(b) ⟦Martin"},
		{"short unmarked items", "4. Trustees\nThe following shall NOT serve as Trustee:\nSophia Davenport-Chen\nMartin Hale\n", "Martin Hale", "The following shall NOT serve as Trustee:\nSophia Davenport-Chen\n⟦Martin Hale⟧"},
	}
	for _, c := range cases {
		spans, bad, err := placeRequirement(context.Background(), c.doc, c.req)
		if err != nil || bad != "" {
			t.Errorf("%s: %q did not verify (bad %q, err %v)", c.name, c.req, bad, err)
			continue
		}
		r := newReq(c.doc, spans)
		if got := r.Context(); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q is read in %q, which lacks %q", c.name, c.req, got, c.want)
		}
	}
	// a requirement typed without the zero-width character verifies in the document that has it (L3)
	if _, bad, _ := placeRequirement(context.Background(), cases[14].doc, "Do NOT, in any capacity, name Sophia Davenport-Chen as a successor trustee."); bad != "" {
		t.Errorf("format characters are not folded: bad %q", bad)
	}
	// a sibling of the item under a nested introduction is read with the text around it (whether
	// "(a) The Trustee shall not:" governs "(b)" is the section check's reading)
	doc := cases[2].doc
	spans, _, _ := placeRequirement(context.Background(), doc, "The Trustee shall invest prudently.")
	if got := newReq(doc, spans).Context(); got != "7. Restrictions\n(a) The Trustee shall not:\n(i) sell the family residence at 12 Elm Street;\n(ii) borrow against the trust assets;\n(b) ⟦The Trustee shall invest prudently.⟧" {
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
			"name Sophia Davenport-Chen as a successor trustee in any capacity", "⟦CRITICAL NOTE — Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity.⟧"},
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
	memo := trustMemoExcerpt + "\n\n" + strings.Repeat("The settlor has considered the family's circumstances and the views of the family council. ", 50) + "\n\nPeggy wants Martin to serve as an administrative safety valve far from here.\n"
	f, v := r2vDev(t, memo, "CRITICAL NOTE — Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity.",
		"Peggy wants Martin to serve as an administrative safety valve far from here.")
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
		r := newReq(trustMemoExcerpt, spans)
		if got := r.Context(); !strings.Contains(got, c.want) || len(got) > ctxMax+200 {
			t.Errorf("%s: context (%d bytes) %q lacks %q", c.name, len(got), got, c.want)
		}
	}
}

// TestContextIsBounded: a requirement in a long paragraph under a long introduction is quoted with
// at most ctxMax bytes, the requirement whole and marked, the introduction within the window kept.
func TestContextIsBounded(t *testing.T) {
	long := strings.Repeat("The Trustee shall keep accurate records of every transaction and report them annually. ", 30)
	doc := "9. Records\n" + long + "Subject to the foregoing, the Trustee shall not:\n" + long + "sell the family residence at 12 Elm Street. " + long + "\n"
	spans, bad, _ := placeRequirement(context.Background(), doc, "sell the family residence at 12 Elm Street.")
	if bad != "" {
		t.Fatalf("bad %q", bad)
	}
	r := newReq(doc, spans)
	got := r.Context()
	if len(got) > ctxMax || !strings.Contains(got, "⟦sell the family residence at 12 Elm Street.⟧") || !strings.Contains(got, "the Trustee shall not:") || !strings.HasPrefix(got, "[headings above it") {
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

// TestSectionCheckNegatedDropsVerdict: a requirement the memo negates (it forbids naming Sophia) fails
// the call before its subagent runs, when the check answers that the text does not impose it as quoted.
func TestSectionCheckNegatedDropsVerdict(t *testing.T) {
	t.Parallel()
	memo := "B. Successor Trustees\n\nThe following persons shall NOT serve as successor trustee in any capacity:\n\nMartin Hale, the settlor's former business partner.\n\nSophia Davenport-Chen, the settlor's daughter, as a successor trustee.\n"
	req := "Sophia Davenport-Chen, the settlor's daughter, as a successor trustee."
	f, v := r4Dev(t, memo, req, req, []model.Reply{text(m{"answer": "no", "reason": "the introduction says these persons shall NOT serve"})})
	out := strings.Join(f.outputs(v.ID, "check_deviation"), "")
	sub := 0
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		sub += btoi(c.Agent == "check_deviation")
	}
	if len(v.Findings) != 0 || !strings.HasPrefix(out, "failed") || sub != 0 ||
		!strings.Contains(out, "does not impose the requirement as quoted (negated or overridden): the introduction says") {
		t.Errorf("findings %+v; %d subagent calls; %s", v.Findings, sub, out)
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
			!strings.Contains(fs[0].Claim, "bytes) and found that it imposes the requirement as quoted. The checker's reasoning (not a quotation): the section states the requirement as quoted") {
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
// (its step's usage holds it); TestSectionCheckNotHadAdmitsLowGated covers an unfunded one.
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
