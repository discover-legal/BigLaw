// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
)

// The deviation vectors carried from v1 orchestrator/deviations_test.go (16 of 20), re-expressed
// against check_deviation's Go hook: the same instruction memo, draft trust and pour-over will.

const devMemoText = `Section 1. Residuary Estate.
The residuary estate shall be divided as follows: David shall receive Forty Percent (40%), Sophia shall receive Thirty-Five Percent (35%), and Tommy shall receive Twenty-Five Percent (25%).

Section 2. Piano Bequest.
The 2019 Steinway Model B piano, Serial #612847, shall pass to Tommy and shall be held by the trustee until Tommy reaches age 30.

Section 3. Disinheritance.
The pour-over will must include a clause expressly disinheriting any person not named in this memorandum.
`

const devTrustText = `Article I. Residuary.
The residuary trust estate shall be distributed forty percent (40%) to David, thirty percent (30%) to Sophia, and thirty percent (30%) to Tommy.

Article II. Tangible Property.
The Steinway piano shall pass to Tommy.
`

const devWillText = `Article 1. Pour-Over.
All the residue of my estate I give to the trustee of the Davenport Trust to be administered under its terms.

Article 2. Guardian.
I nominate Jennifer Chen as guardian of my minor children.
`

type devFix struct {
	*fixture
	memo, trust, will string
}

func newDev(t *testing.T) devFix {
	f := newFixture(t, devMemoText, devTrustText, devWillText)
	return devFix{f, f.docs[0].ID, f.docs[1].ID, f.docs[2].ID}
}

func (d devFix) verdict(part, v, doc, ctrl string, reviewed any, correction string) m {
	return m{"part": part, "verdict": v, "severity": "HIGH", "correction": correction, "document": doc,
		"controlling": m{"documentId": d.memo, "quote": ctrl}, "reviewed": reviewed}
}

// devReq is a requirement quoted from the memo (check_deviation refuses a paraphrase).
const devReq = "The residuary estate shall be divided as follows"

// judged is the paraphrase judge's ruling on an omission's provision in the reviewed document.
func judged(supported bool) model.Reply {
	return text(m{"supported": supported, "confidence": 0.9})
}

// check runs one check_deviation call with verdicts and returns the task's findings. The judge
// finds each omitted provision unsupported in the reviewed document (absent) unless judge says.
func (d devFix) check(t *testing.T, verdicts ...m) ([]FindingView, string) {
	return d.checkJudged(t, nil, verdicts...)
}

func (d devFix) checkJudged(t *testing.T, judge []model.Reply, verdicts ...m) ([]FindingView, string) {
	req := devReq // the memo line the verdicts quote (lab-r3 r3: a controlling quote must lie in the requirement's context)
	for _, v := range verdicts {
		if q, _ := v["controlling"].(m)["quote"].(string); v["verdict"] != "conform" && strings.Contains(devMemoText, q) {
			i := strings.Index(devMemoText, q)
			req = devMemoText[strings.LastIndexByte(devMemoText[:i], '\n')+1 : i+strings.IndexByte(devMemoText[i:], '\n')]
			break
		}
	}
	call := tc("check_deviation", m{"requirement": req, "requirementDocId": d.memo, "controllingDocIds": []string{d.memo}, "reviewedDocIds": []string{d.trust, d.will}, "issueId": "I1"})
	if judge == nil {
		judge = []model.Reply{judged(false), judged(false), judged(false), judged(false)}
	}
	v := d.run(d.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(call)},
		"check_deviation": {text(m{"verdicts": append([]m{}, verdicts...)})},
		"verify_quote":    judge,
	}), Config{})
	out := d.outputs(v.ID, "check_deviation")
	if len(out) != 1 || !strings.HasPrefix(out[0], "committed") {
		t.Fatalf("check_deviation: %q", out)
	}
	return v.Findings, out[0]
}

const (
	qSophiaMemo  = "Sophia shall receive Thirty-Five Percent (35%)"
	qSophiaTrust = "thirty percent (30%) to Sophia"
)

func TestDevConflictCarriesBothQuotesAndCitations(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, _ := d.check(t, d.verdict("Sophia's residuary share", "deviate", d.trust, qSophiaMemo, qSophiaTrust, "Change Sophia's share to Thirty-Five Percent (35%)."))
	if len(fs) != 1 || fs[0].Status != "verified" || len(fs[0].Citations) != 2 {
		t.Fatalf("%+v", fs)
	}
	f := fs[0]
	for _, s := range []string{qSophiaMemo, qSophiaTrust, "doc2.md", "doc1.md", "DEVIATION"} {
		if !strings.Contains(f.Claim, s) {
			t.Errorf("claim lacks %q: %s", s, f.Claim)
		}
	}
	if f.Citations[0].DocumentID != d.memo || f.Citations[1].DocumentID != d.trust || f.Citations[0].Method != "exact" || f.Citations[1].Method != "exact" {
		t.Fatalf("citations %+v", f.Citations)
	}
}

// TestDeviationNeedsBothSidesVerified (trap): the controlling quote absent from its document →
// the verdict is dropped, never a finding (also TestDevFabricatedInstructionQuoteDropped).
func TestDeviationNeedsBothSidesVerified(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, out := d.check(t, d.verdict("Sophia's share", "deviate", d.trust, "Sophia shall receive Fifty Percent (50%)", qSophiaTrust, "Fix it."),
		d.verdict("Sophia's share", "deviate", d.trust, qSophiaMemo, "thirty-one percent to Sophia", "Fix it."))
	if len(fs) != 0 || !strings.Contains(out, `"dropped":2`) {
		t.Fatalf("%+v %s", fs, out)
	}
}

func TestDevFabricatedInstructionQuoteDropped(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	if fs, _ := d.check(t, d.verdict("Sophia's share", "deviate", d.trust, "Sophia shall receive Fifty Percent (50%)", qSophiaTrust, "Fix it.")); len(fs) != 0 {
		t.Fatalf("%+v", fs)
	}
}

func TestDevFabricatedSummaryValueWithheld(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, _ := d.check(t, d.verdict("Sophia's share", "deviate", d.trust, qSophiaMemo, qSophiaTrust, "Change Sophia's share to 20%."))
	if len(fs) != 1 || strings.Contains(fs[0].Claim, "20%") || !strings.Contains(fs[0].Claim, "[model correction withheld") || !strings.Contains(fs[0].Claim, genericCorrection) ||
		!strings.Contains(fs[0].Claim, qSophiaMemo) {
		t.Fatalf("%+v", fs)
	}
}

// Changed: the ground is the verdict's own quotes, so a derived figure is a sum or difference of
// theirs (35 − 30 = 5), not of every retrieved passage.
func TestDevDerivedTotalNotFlagged(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, _ := d.check(t, d.verdict("Sophia's share", "deviate", d.trust, qSophiaMemo, qSophiaTrust, "Raise Sophia's share by 5 points, to 35%."))
	if len(fs) != 1 || !strings.Contains(fs[0].Claim, "by 5 points, to 35%") {
		t.Fatalf("%+v", fs)
	}
}

func TestDevUnverifiedValues(t *testing.T) {
	ground := "Forty Percent (40%) … Thirty-Five Percent (35%) … Twenty-Five Percent (25%) … Serial #612847 … $92,600"
	for _, c := range []struct {
		assertion string
		bad       int
	}{
		{"requires 35% not 25%", 0}, {"the shares total 100%", 0}, {"understated by 10%", 0}, {"the serial is 612847", 0},
		{"$92,600 in compensation", 0}, {"requires 30% for Sophia", 1}, {"a bequest of $150,000", 1}, {"serial number 999999", 1},
		{"no numeric assertion here", 0}, {"paragraph 3 of the memo lists", 0},
	} {
		if got := len(Untraced(c.assertion, ground, true)); got != c.bad {
			t.Errorf("Untraced(%q) = %d, want %d", c.assertion, got, c.bad)
		}
	}
}

func TestDevConformLeakGuardPreserved(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, _ := d.check(t, d.verdict("Sophia's share", "deviate", d.trust, qSophiaMemo, qSophiaTrust, "The document correctly states Sophia's share as required."),
		d.verdict("David's share", "conform", d.trust, "David shall receive Forty Percent (40%)", "forty percent (40%) to David", ""))
	if len(fs) != 0 {
		t.Fatalf("%+v", fs)
	}
}

func TestDevMultiPartPerPartVerdicts(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, _ := d.check(t, d.verdict("piano passes to Tommy", "conform", d.trust, "shall pass to Tommy", "The Steinway piano shall pass to Tommy", ""),
		d.verdict("serial number", "omitted", d.trust, "Serial #612847", nil, "Add the serial number."),
		d.verdict("held until age 30", "omitted", d.trust, "shall be held by the trustee until Tommy reaches age 30", nil, "Add the holding instruction."))
	if len(fs) != 2 {
		t.Fatalf("%+v", fs)
	}
	for _, f := range fs {
		if !strings.Contains(f.Claim, "only in part (conforming: piano passes to Tommy)") || !strings.HasPrefix(f.Claim, "OMISSION") || len(f.Citations) != 1 {
			t.Errorf("%s", f.Claim)
		}
	}
	if !strings.Contains(fs[0].Claim, "Serial #612847") || !strings.Contains(fs[1].Claim, "until Tommy reaches age 30") {
		t.Fatalf("%s | %s", fs[0].Claim, fs[1].Claim)
	}
}

func TestDevMultiPartUngroundedPartDropped(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, _ := d.check(t, d.verdict("serial number", "omitted", d.trust, "Serial #612847", nil, "Add it."),
		d.verdict("insurance rider", "omitted", d.trust, "a $50,000 insurance rider", nil, "Add it."))
	if len(fs) != 1 || strings.Contains(fs[0].Claim, "insurance rider") || !strings.Contains(fs[0].Claim, "Serial #612847") {
		t.Fatalf("%+v", fs)
	}
}

func TestDevMultipleIndependentDeviationsOnOneRequirement(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, _ := d.check(t, d.verdict("Sophia's share", "deviate", d.trust, qSophiaMemo, qSophiaTrust, "Change Sophia's share to Thirty-Five Percent (35%)."),
		d.verdict("Tommy's share", "deviate", d.trust, "Tommy shall receive Twenty-Five Percent (25%)", "thirty percent (30%) to Tommy", "Change Tommy's share to Twenty-Five Percent (25%)."))
	if len(fs) != 2 || !strings.Contains(fs[0].Claim, "Sophia") || !strings.Contains(fs[1].Claim, "Tommy") {
		t.Fatalf("%+v", fs)
	}
}

func TestDevEmptyArrayYieldsNoFindings(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	if fs, out := d.check(t); len(fs) != 0 || !strings.Contains(out, `"dropped":0`) {
		t.Fatalf("%+v %s", fs, out)
	}
}

func TestDevOmissionInSecondDocument(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, _ := d.check(t, d.verdict("disinheritance clause", "omitted", d.will, "The pour-over will must include a clause expressly disinheriting any person not named", nil, "Add an express disinheritance clause."))
	if len(fs) != 1 || !strings.HasPrefix(fs[0].Claim, "OMISSION") || !strings.Contains(fs[0].Claim, "doc3.md") || len(fs[0].Citations) != 1 || fs[0].Citations[0].DocumentID != d.memo {
		t.Fatalf("%+v", fs)
	}
}

// TestDevRetrieveDraftContextScopedToDocument, changed: absence is confirmed in the named document
// only, so the trust's piano clause does not mask the will's gap (and vice versa the trust's own
// clause drops an omission named against the trust: TestOmissionRequiresVerifiedAbsence).
func TestDevRetrieveDraftContextScopedToDocument(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, _ := d.check(t, d.verdict("piano bequest", "omitted", d.will, "The 2019 Steinway Model B piano, Serial #612847, shall pass to Tommy", nil, "Add it."))
	if len(fs) != 1 || !strings.Contains(fs[0].Claim, "doc3.md") {
		t.Fatalf("%+v", fs)
	}
}

// TestOmissionRequiresVerifiedAbsence (trap): "omitted" while the provision is in the reviewed
// document (the judge finds it there) → dropped.
func TestOmissionRequiresVerifiedAbsence(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, out := d.checkJudged(t, []model.Reply{judged(true)}, d.verdict("piano bequest", "omitted", d.trust, "The 2019 Steinway Model B piano, Serial #612847, shall pass to Tommy", nil, "Add it."))
	if len(fs) != 0 || !strings.Contains(out, "present in doc2.md") {
		t.Fatalf("%+v %s", fs, out)
	}
}

// The mechanical numeric join (v1's deviationNumericJoin → join_discrepancies over the findings).
func devFindings(trustQuote string, verified bool, trustMetric string) []analysis.Finding {
	fs := []analysis.Finding{
		{FID: "F1", Verified: true, Quotes: []analysis.Quote{{DocumentID: "memo", Quote: qSophiaMemo}}, Values: []analysis.Value{{Entity: "Sophia", Metric: "residuary share", Value: "35%", Fig: read(analysis.KindPercent, "", "35", "Sophia")}}},
		{FID: "F2", Verified: verified, Quotes: []analysis.Quote{{DocumentID: "trust", Quote: trustQuote}}, Values: []analysis.Value{{Entity: "Sophia", Metric: trustMetric, Value: "30%", Fig: read(analysis.KindPercent, "", "30", "Sophia")}}},
	}
	for _, f := range fs { // what each measures: the reader's label, here the analyst's metric
		f.Values[0].Fig.Measures = f.Values[0].Metric
	}
	return fs
}

// The join proposes the two documents' shares to the judge (who decides: judge.go), one from each.
func TestDevNumericJoinBothQuotes(t *testing.T) {
	cs, _ := analysis.Candidates(devFindings(qSophiaTrust, true, "residuary share"), 100, nil)
	if len(cs) != 1 || cs[0].A.DocumentID == cs[0].B.DocumentID {
		t.Fatalf("%+v", cs)
	}
}

func TestDevNumericJoinEnumerationGuard(t *testing.T) {
	fs := devFindings(qSophiaTrust, true, "residuary share")
	fs[1].Values[0].Value, fs[1].Values[0].Fig = "35%", read(analysis.KindPercent, "", "35", "Sophia")
	fs[1].Values[0].Fig.Measures = "residuary share"
	if cs, _ := analysis.Candidates(fs, 100, nil); len(cs) != 0 {
		t.Fatalf("a matching value is conformance: %+v", cs)
	}
}

func TestDevNumericJoinSubstringLock(t *testing.T) {
	if cs, _ := analysis.Candidates(devFindings("a fabricated quote that appears nowhere", false, "residuary share"), 100, nil); len(cs) != 0 {
		t.Fatalf("a quarantined finding joined: %+v", cs)
	}
}

// Changed again (lab-r4 r7): whether two figures are one quantity is the figure judge's verdict, so
// values read as two measures are still put to it; with no verdict, nothing is asserted.
func TestDevNumericJoinNoJudgeConservative(t *testing.T) {
	cs, _, unjudged, _, err := analysis.Join(context.Background(), agent.Call{}, devFindings(qSophiaTrust, true, "sophia trust allocation"), nil)
	if err != nil || len(cs) != 0 || unjudged != 1 {
		t.Fatalf("%+v (%d not judged, %v)", cs, unjudged, err)
	}
}

// TestDevParaphrasedRequirementRefused (bench, both LAB tasks: the lead passed its own restatement
// of the requirement, and the subagent quoted that as the controlling source): a requirement that
// is not the controlling document's own words is refused before any model call, with the nearest
// exact passage (bounded, fenced) to copy; the same check with the memo's words runs, and its
// subagent gets them.
func TestDevParaphrasedRequirementRefused(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	para := tc("check_deviation", m{"requirement": "Residuary estate distribution shall be 40% to David, 35% to Sophia and 25% to Tommy.", "requirementDocId": d.memo,
		"controllingDocIds": []string{d.memo}, "reviewedDocIds": []string{d.trust}, "issueId": "I1"})
	quoted := tc("check_deviation", m{"requirement": "the residuary estate  shall be divided as follows: David shall receive Forty Percent (40%), Sophia shall receive Thirty-Five Percent (35%), and Tommy shall receive Twenty-Five Percent (25%).", "requirementDocId": d.memo,
		"controllingDocIds": []string{d.memo}, "reviewedDocIds": []string{d.trust}, "issueId": "I1"})
	v := d.run(d.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(para), calls(quoted)},
		"check_deviation": {text(m{"verdicts": []m{d.verdict("Sophia's residuary share", "deviate", d.trust, qSophiaMemo, qSophiaTrust, "Change Sophia's share to Thirty-Five Percent (35%).")}})},
	}), Config{})
	out := d.outputs(v.ID, "check_deviation")
	var reqs []string
	seen := "" // what the lead read of the refusal
	for _, c := range d.e.chat.(*model.Fake).Calls() {
		if c.Agent == "check_deviation" {
			reqs = append(reqs, c.Messages[1].Content.(string))
		} else if last := fmt.Sprint(c.Messages[len(c.Messages)-1].Content); c.Agent == "lead" && strings.HasPrefix(last, "check_deviation did not run or failed") {
			seen = last
		}
	}
	if len(out) != 2 || !strings.HasPrefix(out[0], "failed") || !strings.HasPrefix(out[1], "committed") || len(v.Findings) != 1 {
		t.Fatalf("check_deviation %q, findings %+v", out, v.Findings)
	}
	_, passage, _ := strings.Cut(seen, "<<<")
	passage, _, fenced := strings.Cut(passage, ">>>")
	if !strings.Contains(seen, "the requirement is not quoted from doc1.md") || !strings.Contains(seen, "the closest passage, in section s0 (Section 1)") ||
		!fenced || len(passage) > nearestMax || !strings.Contains(devMemoText, passage) || !strings.Contains(passage, "David shall receive") { // a bounded exact passage to copy (r3)
		t.Fatalf("the lead read %q", seen)
	}
	if len(reqs) != 1 || !strings.Contains(reqs[0], "The residuary estate shall be divided as follows: David") { // as the memo has it (tolerant match, case and spacing)
		t.Fatalf("subagent requests %q", reqs)
	}
}

// TestDevRealOmissionsKept (bench, trust task: eight omitted verdicts, among them the contingent
// guardian, were dropped as "present" because most of their words occur somewhere in the will):
// an omission stands when the provision does not verify in the reviewed document, the paraphrase
// judge ruling on its closest passage; the passage where it belongs is quoted with it. The judge
// finding it there still drops it.
func TestDevRealOmissionsKept(t *testing.T) {
	t.Parallel()
	memo := "Section 4. Guardians.\nIf Jennifer Chen is unable to serve, Eleanor Davenport shall serve as contingent guardian of my minor children.\n"
	will := "Article 1. Pour-Over.\nAll the residue of my estate I give to the trustee of the Davenport Trust.\n\nArticle 2. Guardian.\n" +
		"I nominate Jennifer Chen as guardian of my minor children. If Jennifer Chen is unable or unwilling to serve, the court shall appoint a guardian.\n"
	ctrl := "If Jennifer Chen is unable to serve, Eleanor Davenport shall serve as contingent guardian of my minor children."
	for _, c := range []struct {
		supported bool
		want      int
	}{{false, 1}, {true, 0}} {
		f := newFixture(t, memo, will)
		mID, wID := f.docs[0].ID, f.docs[1].ID
		if absent(ctrl, will) {
			t.Fatal("the vector no longer reproduces the word-count drop")
		}
		call := tc("check_deviation", m{"requirement": ctrl, "requirementDocId": mID, "controllingDocIds": []string{mID}, "reviewedDocIds": []string{wID}, "issueId": "I1"})
		v := f.run(f.with(map[string][]model.Reply{
			"lead": {calls(plan(issue("I1", "Does the will follow the memo?", "Requirement"))), calls(call)},
			"check_deviation": {text(m{"verdicts": []m{{"part": "contingent guardian Eleanor Davenport", "verdict": "omitted", "severity": "HIGH", "correction": "Name Eleanor Davenport as contingent guardian.",
				"document": wID, "controlling": m{"documentId": mID, "quote": ctrl}, "reviewed": "I nominate Jennifer Chen as guardian of my minor children."}}})},
			"verify_quote": {judged(c.supported)},
		}), Config{})
		if len(v.Findings) != c.want {
			t.Fatalf("judge supported=%v: findings %+v; %q", c.supported, v.Findings, f.outputs(v.ID, "check_deviation"))
		}
		if c.want == 1 && (!strings.HasPrefix(v.Findings[0].Claim, "OMISSION") || len(v.Findings[0].Citations) != 2 ||
			!strings.Contains(v.Findings[0].Claim, "the closest provision reads “I nominate Jennifer Chen")) {
			t.Fatalf("%+v", v.Findings[0])
		}
	}
}

// TestDevRestatedControllingQuoteRescued (bench, trust task: "its controlling quote is not in the
// controlling document" dropped two real deviations whose subagent restated the requirement): a
// verdict whose controlling quote restates the request's verified requirement (its words, no
// figure of its own) stands on the requirement's quote; one quoting a figure the requirement does
// not state is still dropped.
func TestDevRestatedControllingQuoteRescued(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	req := "The residuary estate shall be divided as follows: David shall receive Forty Percent (40%), Sophia shall receive Thirty-Five Percent (35%), and Tommy shall receive Twenty-Five Percent (25%)." // a whole sentence (lab-r3 r2: a clause that drops "shall be divided as follows" is refused)
	call := tc("check_deviation", m{"requirement": req, "requirementDocId": d.memo, "controllingDocIds": []string{d.memo}, "reviewedDocIds": []string{d.trust}, "issueId": "I1"})
	v := d.run(d.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(call)},
		"check_deviation": {text(m{"verdicts": []m{
			d.verdict("Sophia's share", "deviate", d.trust, "Sophia receives Thirty-Five Percent (35%) of the residuary estate", qSophiaTrust, "Change it to Thirty-Five Percent (35%)."),
			d.verdict("Tommy's share", "deviate", d.trust, "Tommy receives Twenty Percent (20%) of the residuary estate", "thirty percent (30%) to Tommy", "Change it."),
		}})},
	}), Config{})
	out := strings.Join(d.outputs(v.ID, "check_deviation"), "")
	if len(v.Findings) != 1 || !strings.Contains(v.Findings[0].Claim, "⟦"+req+"⟧") || v.Findings[0].Citations[0].Method != "exact" ||
		!strings.Contains(out, "restated its requirement instead of quoting it") || !strings.Contains(out, `"dropped":1`) {
		t.Fatalf("findings %+v\n%s", v.Findings, out)
	}
}

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

// The lab-r3 round-5 verifier's repros (critique/biglaw-lab-r3-r5-verifier.md), kept as regression
// tests; each failed on 84adfa0.

// docxOf builds a .docx (zip + word/document.xml) holding the paragraphs.
func docxOf(paras ...string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paras {
		sb.WriteString("<w:p><w:r><w:t>" + p + "</w:t></w:r></w:p>")
	}
	sb.WriteString(`</w:body></w:document>`)
	w, _ := zw.Create("word/document.xml")
	_, _ = w.Write([]byte(sb.String()))
	_ = zw.Close()
	return buf.Bytes()
}

// M2: the .docx reader ends a paragraph with one line break, so a requirement that is a short
// paragraph past the document's first ~3 KB was always truncated (Low, gated, and a false claim).
func TestV5DocxParagraphNotTruncated(t *testing.T) {
	paras := []string{"Memorandum"}
	for range 70 {
		paras = append(paras, "The Trustee shall keep the accounts of the trust and report them annually to each beneficiary.")
	}
	req := "The Trustee may name Sophia Davenport-Chen as a successor trustee."
	paras = append(paras, req, "The Trustee shall retain the residence.")
	x, err := docs.Extract(context.Background(), docxOf(paras...))
	if err != nil || !strings.Contains(x.Text, "beneficiary.\n"+req+"\n") {
		t.Fatalf("extract: %v %.200q", err, x.Text)
	}
	spans, bad, _ := placeRequirement(context.Background(), x.Text, req)
	if bad != "" {
		t.Fatalf("bad %q", bad)
	}
	if r := newReq(x.Text, spans); r.Truncated || len(r.Context()) > ctxMax {
		t.Errorf("a whole .docx paragraph inside the window is truncated (%d bytes)", len(r.Context()))
	}
	// a paragraph that does start above the window still is
	long := strings.Repeat("the Trustee shall keep the accounts of the trust and report them annually to each beneficiary, ", 60)
	x, _ = docs.Extract(context.Background(), docxOf("Memorandum", "Subject to the protector's consent, "+long+req))
	spans, _, _ = placeRequirement(context.Background(), x.Text, req)
	if r := newReq(x.Text, spans); !r.Truncated {
		t.Error("a .docx paragraph starting 6 KB above the requirement is not truncated")
	}
}

// M2 end to end: the same shape as text with one line break per paragraph is admitted at full
// confidence on a "yes".
func TestV5DocxRequirementAlwaysLow(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for b.Len() < 6000 {
		b.WriteString("The Trustee shall keep the accounts of the trust and report them annually to each beneficiary.\n")
	}
	req := "The Trustee may name Sophia Davenport-Chen as a successor trustee."
	memo := "Memorandum\n" + b.String() + req + "\nThe Trustee shall retain the residence.\n"
	f, v := r4Dev(t, memo, req, req, []model.Reply{text(m{"answer": "yes", "reason": "the paragraph says she may be named"})})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || fs[0].Low {
		t.Errorf("a requirement that is a whole paragraph, its start inside the window, is not admitted at full confidence: %+v", fs)
	}
}

// L1: a "shall not:" chapeau > 3 KB above blank-line-separated items: the window started mid-list,
// untruncated, and a "yes" admitted a confirmed HIGH deviation without the chapeau.
func TestV5LongListChapeauConfirmed(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("7. Restrictions\n\nThe Trustee shall not:\n\n")
	for i := range 30 {
		b.WriteString("(" + string(rune('a'+i%26)) + ") Sell, lease, mortgage or otherwise encumber any parcel of the family land held in the trust estate without the protector's written consent.\n\n")
	}
	req := "Name Sophia Davenport-Chen as a successor trustee."
	yes := []model.Reply{text(m{"answer": "yes", "reason": "the text says to name her"})}
	f, v := r4Dev(t, b.String()+"(u) "+req+"\n", req, req, yes)
	for _, x := range ledgerFindings(t, f, v.ID) {
		if !x.Low && !strings.Contains(x.Claim, "shall not:") {
			t.Errorf("a confirmed HIGH deviation lacks the list's \"shall not:\": %.300q", x.Claim)
		}
	}
}

// L3: "No." is no: the verdict is dropped, not admitted as unclear.
func TestV5CheckNoWithPeriodDrops(t *testing.T) {
	t.Parallel()
	memo := "Note\n\nDo NOT name Sophia Davenport-Chen as a successor trustee.\n"
	req := "Do NOT name Sophia Davenport-Chen as a successor trustee."
	f, v := r4Dev(t, memo, req, req, []model.Reply{text(m{"answer": "No.", "reason": "the text forbids naming her"})})
	if fs := ledgerFindings(t, f, v.ID); len(fs) != 0 {
		t.Errorf("a check that answered \"No.\" admitted the verdict: %+v", fs)
	}
}

// L2: unclear on a truncated window is worded as unclear, not "agreed, but …".
func TestV5UnclearTruncatedSaysUnclear(t *testing.T) {
	t.Parallel()
	req := "Do NOT name Sophia Davenport-Chen as a successor trustee."
	big := "Note\n" + strings.Repeat("The settlor has considered the family's circumstances and the views of the family council. ", 120) + req + "\n" // in a 11 KB paragraph
	f, v := r4Dev(t, big, req, req, []model.Reply{text(m{"answer": "unclear", "reason": "cannot tell"})})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || !fs[0].Low || strings.Contains(fs[0].Claim, "agreed") || !strings.Contains(fs[0].Claim, "could not tell") {
		t.Errorf("an unclear check on a truncated window: %+v", fs)
	}
}

// The lab-r3 verifier's repros (H1–H3, M1–M3), kept as regression tests: each failed on 1814a6a.

// TestStitchedRequirementMakesNoDeviation (H1, end to end): the stitched requirement that names
// Sophia as successor trustee is refused before the subagent runs, with the whole nearest passage to
// copy, so no verified deviation tells the lawyer the opposite of the memo.
func TestStitchedRequirementMakesNoDeviation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, trustMemoExcerpt, "Article 4. Successor Trustees.\nIf Peggy ceases to serve, Dr. David Chen shall serve as successor Trustee, and then Broadleaf Fiduciary Services, Inc.\n")
	memo, trust := f.docs[0].ID, f.docs[1].ID
	stitched := "the following individuals and entities shall serve as successor Trustee, in the following order of priority: Sophia Davenport-Chen as a successor trustee in any capacity."
	dev := tc("check_deviation", m{"requirement": stitched, "requirementDocId": memo, "controllingDocIds": []string{memo}, "reviewedDocIds": []string{trust}, "issueId": "I1"})
	verdict := m{"part": "successor trustees", "verdict": "deviate", "severity": "HIGH", "correction": "Name Sophia Davenport-Chen as a successor trustee.", "document": trust,
		"controlling": m{"documentId": memo, "quote": stitched}, "reviewed": "If Peggy ceases to serve, Dr. David Chen shall serve as successor Trustee, and then Broadleaf Fiduciary Services, Inc."}
	v := f.run(f.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(dev)},
		"check_deviation": {text(m{"verdicts": []m{verdict}})},
	}), Config{})
	out := f.outputs(v.ID, "check_deviation")
	for _, x := range v.Findings {
		if x.Status == "verified" && len(x.Citations) >= 2 {
			t.Errorf("a verified deviation rests on a stitched requirement: %+v", x)
		}
	}
	var seen string // what the lead read back: the refusal
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "lead" {
			seen = fmt.Sprint(c.Messages)
		}
	}
	if len(out) != 1 || !strings.HasPrefix(out[0], "failed") || !strings.Contains(seen, "<<<CRITICAL NOTE — Do NOT name Sophia Davenport-Chen as a successor trustee in any capacity.") {
		t.Errorf("check_deviation %q: want a refusal quoting the negated sentence; the lead read %.3000s", out, seen)
	}
}

// TestDeviationGetsReviewedPassages (bench4, trust: all seven check_deviation calls used their five
// turns retrieving the reviewed text and checking quotes Go verifies anyway; two ended at the turn
// cap with no answer): the subagent is handed the reviewed documents' passages most like the
// requirement and offered only retrieve, so it can answer at once, in one model call.
func TestDeviationGetsReviewedPassages(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	call := tc("check_deviation", m{"requirement": devReq, "requirementDocId": d.memo, "controllingDocIds": []string{d.memo}, "reviewedDocIds": []string{d.trust}, "issueId": "I1"})
	v := d.run(d.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(call)},
		"check_deviation": {text(m{"verdicts": []m{d.verdict("Sophia's residuary share", "deviate", d.trust, qSophiaMemo, qSophiaTrust, "Change Sophia's share to Thirty-Five Percent (35%).")}})},
	}), Config{})
	var reqs []model.Request
	for _, c := range d.e.chat.(*model.Fake).Calls() {
		if c.Agent == "check_deviation" {
			reqs = append(reqs, c)
		}
	}
	if len(reqs) != 1 || len(v.Findings) != 1 {
		t.Fatalf("%d subagent calls, %d findings", len(reqs), len(v.Findings))
	}
	var in struct {
		ReviewedPassages []struct{ DocumentID, Text string }
	}
	jsonDecode(reqs[0].Messages[1].Content.(string), &in)
	var tools []string
	for _, x := range reqs[0].Tools {
		tools = append(tools, x.Name)
	}
	if len(in.ReviewedPassages) == 0 || in.ReviewedPassages[0].DocumentID != d.trust || !strings.Contains(in.ReviewedPassages[0].Text, qSophiaTrust) || !slices.Equal(tools, []string{"retrieve"}) {
		t.Errorf("passages %+v; tools %v", in.ReviewedPassages, tools)
	}
}

// The bench5 trust incapacity standard, verbatim: the instruction memo's requirement and the text
// around it, and the draft trust's section 5.1.
const (
	incapMemo = `V. Incapacity / Disability Provisions
A. Standard for Determining Incapacity
This is a provision that Peggy feels strongly about, and she was very specific during our meetings regarding the standard she wants applied. Peggy shall be deemed incapacitated or unable to manage her financial affairs if, and only if, two (2) licensed physicians independently examine her and provide written certifications that she is unable to manage her financial affairs. Both physicians must be licensed to practice medicine in the state where Peggy then resides.
I want to emphasize this point: two licensed physicians. Peggy was adamant during our October and November meetings that she does not want a single doctor's opinion — or the opinion of any non-physician clinical professional — to be sufficient to trigger loss of control over her trust assets. She views this as a critical safeguard against premature or erroneous determinations of incapacity. The trust should clearly state that two licensed physicians (i.e., individuals holding an M.D. or D.O. degree and licensed to practice medicine) must independently examine and certify incapacity. Please draft this provision with precision and do not deviate from the two-physician standard.
B. Effect of Incapacity
Upon a proper determination of incapacity in accordance with the two-physician standard described above, Dr. David Chen shall assume the role of acting Trustee and shall manage the trust and its assets on Peggy's behalf.
`
	incapTrust = `ARTICLE V — DETERMINATION OF INCAPACITY
Section 5.1 — Standard for Incapacity
The Settlor shall be deemed to be incapacitated for purposes of this Trust Agreement if, in the written opinion of one (1) licensed physician and one (1) licensed clinical psychologist, each of whom has personally examined the Settlor within the preceding thirty (30) days, the Settlor is unable to effectively manage her financial affairs by reason of mental illness.
`
	incapReq     = "Peggy shall be deemed incapacitated or unable to manage her financial affairs if, and only if, two (2) licensed physicians independently examine her and provide written certifications that she is unable to manage her financial affairs. Both physicians must be licensed to practice medicine in the state where Peggy then resides."
	incapReview  = "The Settlor shall be deemed to be incapacitated for purposes of this Trust Agreement if, in the written opinion of one (1) licensed physician and one (1) licensed clinical psychologist"
	incapCorrect = "Require two licensed physicians to examine the Settlor and certify incapacity in writing, replacing the physician-and-psychologist standard."
)

// incapacity runs bench5 trust step 67's call (the requirement verbatim, a deviate verdict on the
// two-physician standard) with the section check's scripted replies.
func incapacity(t *testing.T, check []model.Reply) (*fixture, TaskView) {
	t.Helper()
	f := newFixture(t, incapMemo, incapTrust)
	memo, trust := f.docs[0].ID, f.docs[1].ID
	dev := tc("check_deviation", m{"requirement": incapReq, "requirementDocId": memo, "controllingDocIds": []string{memo}, "reviewedDocIds": []string{trust}, "issueId": "I1"})
	verdict := m{"part": "Number and type of professionals required to certify incapacity", "verdict": "deviate", "severity": "HIGH", "correction": incapCorrect, "document": trust,
		"controlling": m{"documentId": memo, "quote": incapReq}, "reviewed": incapReview}
	v := f.run(f.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft trust follow the instruction memo?", "Requirement"))), calls(dev)},
		"check_deviation": {text(m{"verdicts": []m{verdict}})},
		"section_check":   check,
	}), Config{})
	return f, v
}

// TestSectionCheckAsksAboutTheRequirement (bench5 trust step 67: the check was handed the analyst's
// verdict and correction, judged the correction "removes the state-licensing requirement" and
// dropped the real incapacity-standard deviation): the check is asked only whether the memo imposes
// the requirement as quoted, from the text around it; it is never shown the verdict, its correction
// or the reviewed document, and the deviation is admitted on its "yes".
func TestSectionCheckAsksAboutTheRequirement(t *testing.T) {
	t.Parallel()
	f, v := incapacity(t, []model.Reply{text(m{"answer": "yes", "reason": "the memo requires two licensed physicians, if and only if"})})
	req, ok := sectionCheckRequest(f)
	body := ""
	if ok {
		body = req.Messages[1].Content.(string)
	}
	if !ok || !strings.Contains(body, "⟦"+incapReq+"⟧") || strings.Contains(body, incapCorrect) || strings.Contains(body, "Number and type of professionals") ||
		strings.Contains(body, "clinical psychologist, each") || strings.Contains(body, "Verdict") {
		t.Errorf("the check was not asked about the requirement alone: %q", body)
	}
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || fs[0].Low || fs[0].Check == nil || fs[0].Check.Answer != "yes" || !strings.Contains(fs[0].Claim, "found that it imposes the requirement as quoted") {
		t.Fatalf("findings %+v; %q", fs, f.outputs(v.ID, "check_deviation"))
	}
}

// TestSectionCheckNotHadAdmitsLowGated (bench5 trust step 25: three deviations dropped because "no
// model call or budget was left in this call's grant" for their checks): the check runs first, out of
// the call's grant, once per requirement; when it cannot be had (unfunded, failed, malformed) the
// deviation is admitted at low confidence and gated for a lawyer, never dropped.
func TestSectionCheckNotHadAdmitsLowGated(t *testing.T) {
	t.Parallel()
	for name, rep := range map[string]model.Reply{"failed": {Err: &model.Error{Kind: model.ErrUnavailable}}, "malformed": {Text: "I think yes."}} {
		f, v := incapacity(t, []model.Reply{rep})
		fs := ledgerFindings(t, f, v.ID)
		if len(fs) != 1 || !fs[0].Low || fs[0].Check != nil || !gated(fs[0], false) || !strings.Contains(fs[0].Claim, "did not run (") {
			t.Errorf("%s: findings %+v; %q", name, fs, f.outputs(v.ID, "check_deviation"))
		}
	}
	fake := newFake(nil) // an unfunded grant makes no call
	spans, _, _ := placeRequirement(context.Background(), incapMemo, incapReq)
	chk, why, err := (&Engine{}).checkSection(context.Background(), &agent.Runtime{Chat: fake, Roles: testRoles}, agent.NewBudget(0, 0, 0), newReq(incapMemo, spans))
	if err != nil || chk != nil || !strings.Contains(why, "could not fund") || len(fake.Calls()) != 0 {
		t.Errorf("no grant: check %+v, why %q, err %v, calls %d", chk, why, err, len(fake.Calls()))
	}
}

// TestSectionCheckLeavesTheSubagentItsTurns: the requirement's check is one call of the grant beside
// the subagent's own turns, so a subagent that needs all five (four searches, then its answer) still
// answers.
func TestSectionCheckLeavesTheSubagentItsTurns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, incapMemo, incapTrust)
	memo, trust := f.docs[0].ID, f.docs[1].ID
	dev := tc("check_deviation", m{"requirement": incapReq, "requirementDocId": memo, "controllingDocIds": []string{memo}, "reviewedDocIds": []string{trust}, "issueId": "I1"})
	verdict := m{"part": "the incapacity standard", "verdict": "deviate", "severity": "HIGH", "correction": incapCorrect, "document": trust,
		"controlling": m{"documentId": memo, "quote": incapReq}, "reviewed": incapReview}
	search := calls(tc("retrieve", m{"op": "search", "query": "incapacity", "documentId": nil, "sectionId": nil, "k": nil, "offset": nil, "limit": nil}))
	v := f.run(f.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft trust follow the instruction memo?", "Requirement"))), calls(dev)},
		"check_deviation": {search, search, search, search, text(m{"verdicts": []m{verdict}})},
		"section_check":   checks("yes", 1),
	}), Config{})
	if fs := ledgerFindings(t, f, v.ID); len(fs) != 1 {
		t.Fatalf("findings %+v; %q", fs, f.outputs(v.ID, "check_deviation"))
	}
}
