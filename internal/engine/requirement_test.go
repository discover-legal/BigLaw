// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"strings"
	"testing"

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
		parts, bad := requirementParts(trustMemoExcerpt, c.req)
		if bad != "" || len(parts) != c.parts {
			t.Errorf("%s: parts %q, bad %q", c.name, parts, bad)
		}
		for _, p := range parts {
			if !strings.Contains(trustMemoExcerpt, p) {
				t.Errorf("%s: part %q is not the memo's own words", c.name, p)
			}
		}
	}
	if parts, bad := requirementParts(trustMemoExcerpt, reqParaphrase); bad == "" || !strings.Contains(bad, "shall NOT have the power to remove or replace individual trustees") {
		t.Errorf("paraphrase: parts %q, bad %q", parts, bad)
	}
	for _, p := range []string{"Sophia: 35%", "Tommy: outright and free of trust", "Trust Protector. Accelerate. Corporate trustee. Remove.", " … … ", ". . .", "Successor Trustees: … … …"} {
		if _, bad := requirementParts(trustMemoExcerpt, p); bad == "" { // words of the memo, in pieces: still a paraphrase
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
	if len(v.Findings) != 1 || len(v.Findings[0].Citations) != 3 {
		t.Fatalf("findings %+v", v.Findings)
	}
	for _, c := range v.Findings[0].Citations {
		if c.Method == "none" {
			t.Fatalf("citation %+v did not verify", c)
		}
	}
}
