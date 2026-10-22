// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
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

// check runs one check_deviation call with verdicts and returns the task's findings.
func (d devFix) check(t *testing.T, verdicts ...m) ([]FindingView, string) {
	call := tc("check_deviation", m{"requirement": "Residuary split and bequests", "controllingDocIds": []string{d.memo}, "reviewedDocIds": []string{d.trust, d.will}, "issueId": "I1"})
	v := d.run(d.with(map[string][]model.Reply{
		"lead":            {calls(plan(issue("I1", "Does the draft follow the memo?", "Requirement"))), calls(call)},
		"check_deviation": {text(m{"verdicts": append([]m{}, verdicts...)})},
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
// document → dropped.
func TestOmissionRequiresVerifiedAbsence(t *testing.T) {
	t.Parallel()
	d := newDev(t)
	fs, out := d.check(t, d.verdict("piano bequest", "omitted", d.trust, "The 2019 Steinway Model B piano, Serial #612847, shall pass to Tommy", nil, "Add it."))
	if len(fs) != 0 || !strings.Contains(out, "present in doc2.md") {
		t.Fatalf("%+v %s", fs, out)
	}
}

// The mechanical numeric join (v1's deviationNumericJoin → join_discrepancies over the findings).
func devFindings(trustQuote string, verified bool, trustMetric string) []analysis.Finding {
	return []analysis.Finding{
		{FID: "F1", Verified: true, Quotes: []analysis.Quote{{DocumentID: "memo", Quote: qSophiaMemo}}, Values: []analysis.Value{{Entity: "Sophia", Metric: "residuary share", Value: "35%"}}},
		{FID: "F2", Verified: verified, Quotes: []analysis.Quote{{DocumentID: "trust", Quote: trustQuote}}, Values: []analysis.Value{{Entity: "Sophia", Metric: trustMetric, Value: "30%"}}},
	}
}

func TestDevNumericJoinBothQuotes(t *testing.T) {
	cs := analysis.Join(devFindings(qSophiaTrust, true, "residuary share"), nil)
	if len(cs) != 1 || len(cs[0].Values) != 2 || cs[0].Values[0].DocumentID == cs[0].Values[1].DocumentID {
		t.Fatalf("%+v", cs)
	}
}

func TestDevNumericJoinEnumerationGuard(t *testing.T) {
	fs := devFindings(qSophiaTrust, true, "residuary share")
	fs[1].Values[0].Value = "35%"
	if cs := analysis.Join(fs, nil); len(cs) != 0 {
		t.Fatalf("a matching value is conformance: %+v", cs)
	}
}

func TestDevNumericJoinSubstringLock(t *testing.T) {
	if cs := analysis.Join(devFindings("a fabricated quote that appears nowhere", false, "residuary share"), nil); len(cs) != 0 {
		t.Fatalf("a quarantined finding joined: %+v", cs)
	}
}

// Changed: v2 has no judge here and no referent floor for numbers; metric identity is the label,
// so differently labelled values never join.
func TestDevNumericJoinNoJudgeConservative(t *testing.T) {
	if cs := analysis.Join(devFindings(qSophiaTrust, true, "sophia trust allocation"), nil); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}
