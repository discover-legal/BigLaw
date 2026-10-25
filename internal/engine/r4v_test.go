// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
)

// The lab-r4 verifier's repros (H1, H2, M1–M3, L3, L7), kept as regression tests.

func fnd(issue, claim, quote string, vs ...analysis.Value) Finding {
	return Finding{Finding: analysis.Finding{IssueID: issue, Verified: true, Claim: claim, Quotes: []analysis.Quote{{Quote: quote}}, Values: vs}}
}

// TestClaimSumNeverReachesTheDeliverable (verifier H1): the unstated "$3 million" is not a value of
// the verified finding, so it is no salient figure the deliverable reports.
func TestClaimSumNeverReachesTheDeliverable(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Damages\nGreystone claims damages of $644,910 for unpaid invoices.\n\n2. Note\nSection 3. Payment. The Company shall pay the outstanding principal on demand.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices", val("Greystone", "damages", "$644,910", "Money")),
			fd("The Company owes $3 million under Section 3 of the Note.", d1, "Section 3. Payment. The Company shall pay the outstanding principal on demand."))},
	}), Config{})
	for _, x := range ledgerFindings(t, f, v.ID) {
		if strings.Contains(x.Claim, "$3 million") && len(x.Values) > 0 {
			t.Errorf("%s typed %+v", x.FID, x.Values)
		}
	}
	if out := deref(v.Output); strings.Contains(out, "$3 million") || strings.Contains(out, "$3,000,000") {
		t.Errorf("the deliverable states a sum no document states:\n%s", out)
	}
}

// TestRestatedNeverMergesANegatedTerm (verifier H2): an amendment that reverses a term in nearly the
// same words is a finding of its own, in its own issue or the same one.
func TestRestatedNeverMergesANegatedTerm(t *testing.T) {
	vs := []analysis.Value{{Entity: "Purchaser", Metric: "deposit", Value: "$50,000", Class: "Money"}, {Entity: "Purchaser", Metric: "outside date", Value: "June 30, 2026", Class: "Date"}}
	q := "The Deposit of $50,000 shall be %s to the Purchaser if Closing has not occurred on or before June 30, 2026, for any reason other than the Purchaser's default."
	orig := fnd("I1", "The Purchase Agreement makes the $50,000 deposit refundable to the Purchaser if Closing has not occurred by June 30, 2026.", fmt.Sprintf(q, "refundable"), vs...)
	for _, issue := range []string{"I2", "I1"} {
		amend := fnd(issue, "The First Amendment makes the deposit non-refundable.", fmt.Sprintf(q, "non-refundable"), vs...)
		if restated(orig, amend) {
			t.Errorf("%s: the amendment's reversal of the deposit term is folded into the original term", issue)
		}
	}
	if may := fnd("I1", "x", strings.Replace(fmt.Sprintf(q, "refundable"), "shall", "may", 1), vs...); restated(orig, may) {
		t.Error("a modal changed is the same polarity")
	}
}

// TestRestatedNeverMergesMirroredParties (verifier M1): each party's own cap is a finding of its own.
func TestRestatedNeverMergesMirroredParties(t *testing.T) {
	seller := fnd("I1", "Seller's aggregate liability is capped at $1,000,000.", "The aggregate liability of the Seller under this Agreement shall not exceed $1,000,000.",
		analysis.Value{Entity: "Seller", Metric: "liability cap", Value: "$1,000,000", Class: "Money"})
	buyer := fnd("I2", "Buyer's aggregate liability (our client's exposure) is capped at $1,000,000.", "The aggregate liability of the Buyer under this Agreement shall not exceed $1,000,000.",
		analysis.Value{Entity: "Buyer", Metric: "liability cap", Value: "$1,000,000", Class: "Money"})
	if restated(seller, buyer) {
		t.Errorf("the Buyer's cap (issue I2) is folded into the Seller's (issue I1)")
	}
	buyer.IssueID, buyer.Quotes = "I1", seller.Quotes // the same words, the other party typed
	if restated(seller, buyer) {
		t.Errorf("a value typed for another entity is folded in")
	}
	same := fnd("I2", seller.Claim, seller.Quotes[0].Quote, seller.Values...)
	if restated(seller, same) {
		t.Errorf("an exact duplicate in another issue is folded in")
	}
	if same.IssueID = "I1"; !restated(seller, same) {
		t.Errorf("an exact duplicate in the issue is not merged")
	}
}

// TestSweepCapNeverDropsProseFacts (verifier M2): a prose page's facts are all admitted (a page is
// sweepTokens long; no per-call cap drops what it states).
func TestSweepCapNeverDropsProseFacts(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	doc.WriteString("1. Payments\n")
	var fs []m
	for i := range 15 {
		s := fmt.Sprintf("On March %d, 2024, Ostrowski paid $%d,250 to the Club for Bellini.", i+1, 11+i)
		doc.WriteString(s + " ")
		fs = append(fs, on("I1", fd(fmt.Sprintf("Ostrowski paid $%d,250 for Bellini on March %d, 2024.", 11+i, i+1), "", s,
			val("Ostrowski", "payment", fmt.Sprintf("$%d,250", 11+i), "Money"), val("Ostrowski", "payment date", fmt.Sprintf("March %d, 2024", i+1), "Date"))))
	}
	doc.WriteString("\n")
	f := newFixture(t, doc.String())
	for _, x := range fs {
		x["quotes"].([]m)[0]["documentId"] = f.docs[0].ID
	}
	v := f.run(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "What did Ostrowski pay for Bellini?", "RedFlag"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"extract_facts": {facts(fs...)},
	}), Config{})
	if got := ledgerFindings(t, f, v.ID); len(got) != 15 {
		t.Errorf("%d of 15 payments admitted from one prose page", len(got))
	}
}

func countAgent(f *model.Fake, a string) (n int) {
	for _, c := range f.Calls() {
		if c.Agent == a {
			n++
		}
	}
	return n
}

// issueAddedPage is a page with a fact on the plan's I1 and a fact the lead's later I2 needs.
func issueAddedPage(t *testing.T) (*fixture, model.Reply) {
	f := newFixture(t, "1. Fees\nThe fee of $5,000 is due on March 1, 2026. The Adviser received $48,000 from Lakeshore Trading in 2023 and did not disclose it.\n")
	d1 := f.docs[0].ID
	return f, facts(on("I1", fd("The fee of $5,000 is due on March 1, 2026.", d1, "The fee of $5,000 is due on March 1, 2026", val("Buyer", "fee", "$5,000", "Money"))),
		fd("The Adviser received $48,000 from Lakeshore Trading in 2023 and did not disclose it.", d1, "The Adviser received $48,000 from Lakeshore Trading in 2023 and did not disclose it",
			val("Adviser", "payment received", "$48,000", "Money"))) // bears on no listed issue (yet): issueId null
}

// TestIssueAddedAfterSweepGetsItsFacts (verifier M3): a page is read for the plan's issues as they
// are; once the lead adds I2, the next sweep reads the page again for I2 only (sent only I2, admitting
// only I2's findings), and the $48,000 reaches the ledger once.
func TestIssueAddedAfterSweepGetsItsFacts(t *testing.T) {
	t.Parallel()
	f, page := issueAddedPage(t)
	d1 := f.docs[0].ID
	again := facts(on("I1", fd("The fee of $5,000 is due on March 1, 2026.", d1, "The fee of $5,000 is due on March 1, 2026", val("Buyer", "fee", "$5,000", "Money"))),
		on("I2", fd("The Adviser received $48,000 from Lakeshore Trading in 2023 and did not disclose it.", d1, "The Adviser received $48,000 from Lakeshore Trading in 2023 and did not disclose it",
			val("Adviser", "payment received", "$48,000", "Money"))))
	sweep := calls(tc("sweep", m{"over": "sections", "documentIds": nil}))
	fake := newFake(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the fee due?", "Clause"))), sweep,
			calls(plan(issue("I2", "Did the Adviser receive undisclosed compensation?", "RedFlag"))), sweep, sweep},
		"extract_facts": {page, again},
	}))
	f.e = New(f.st, f.d, fake, testRoles, nil, Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	got := ledgerFindings(t, f, task.ID)
	n := 0
	for _, x := range got {
		n += btoi(strings.Contains(x.Claim, "$48,000") && x.IssueID == "I2")
	}
	if n != 1 || len(got) != 2 || countAgent(fake, "extract_facts") != 2 {
		t.Errorf("after I2 joins the plan: %d findings (%d on the $48,000); extract_facts calls %d (the third sweep reads nothing again)", len(got), n, countAgent(fake, "extract_facts"))
	}
	calls := 0
	for _, c := range fake.Calls() {
		if c.Agent == "extract_facts" {
			if calls++; calls == 2 && (strings.Contains(fmt.Sprint(c.Messages[1].Content), `"id":"I1"`) || !strings.Contains(fmt.Sprint(c.Messages[1].Content), `"id":"I2"`)) {
				t.Errorf("the page read again is not sent only the new issue: %s", c.Messages[1].Content)
			}
		}
	}
	if out := deref(f.get(task.ID).Output); strings.Contains(out, "joined the plan") {
		t.Errorf("the page was read again, but Limitations says it was not:\n%s", out)
	}
}

// TestIssueAddedAfterSweepNeverReadAgainIsDisclosed (verifier M3): when no sweep reads the page again
// for the issue the lead added, Limitations says so.
func TestIssueAddedAfterSweepNeverReadAgainIsDisclosed(t *testing.T) {
	t.Parallel()
	f, page := issueAddedPage(t)
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the fee due?", "Clause"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil})),
			calls(plan(issue("I2", "Did the Adviser receive undisclosed compensation?", "RedFlag"))), calls(draft("I1", "F1")), calls(draft("I2", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {page},
		"draft_section": {section("The fee of {Zephyr} is due on March 1, 2026 [F1].", "Fee {Zephyr} [F1]."), section("No undisclosed payment is in the findings [F1].", "None [F1].")},
	}), Config{})
	if out := deref(v.Output); !strings.Contains(out, "Coverage: 1 page the evidence sweep read before issue I2 joined the plan was not read again for it, so facts there that bear only on I2 may be missing.") {
		t.Errorf("the page never read for I2 is not disclosed:\n%s", out)
	}
}

// TestReviewedPassagesEmbeddingMetered (verifier L3; lab-r4 r2): check_deviation's reviewed-passage
// search embeds its query through the meter, so its cost is charged to the step, and a requirement of
// more distinct words than a search takes (bench5 trust's two-physician standard) still finds the
// reviewed passages: its query is its most distinctive words.
func TestReviewedPassagesEmbeddingMetered(t *testing.T) {
	t.Parallel()
	f := newFixture(t, incapMemo, incapTrust)
	f.d.UseEmbedder(pricedEmbedder{0.05}, "emb-m")
	if _, _, err := f.d.Backfill(context.Background()); err != nil {
		t.Fatal(err)
	}
	memo, trust := f.docs[0].ID, f.docs[1].ID
	c := agent.Call{TaskID: "t", DocumentIDs: f.ids(), Input: []byte(fmt.Sprintf(`{"reviewedDocIds":[%q]}`, trust))}
	f.e = New(f.st, f.d, newFake(nil), testRoles, nil, Config{BudgetCalls: 150, BudgetUSD: 5}, nil)
	ps, u := f.e.reviewedPassages(context.Background(), c, incapReq) // bench5's full requirement: over docs.MaxTerms distinct words
	if len(ps) == 0 || u.USD < 0.05 || memo == "" {
		t.Errorf("passages %d, usage %+v", len(ps), u)
	}
}

// TestLimitationsGrammar (verifier L7): one figure "never appears", one finding "states", and a
// figure with no label is listed with its fid alone.
func TestLimitationsGrammar(t *testing.T) {
	if h := figureLine([]analysis.Handle{{FID: "F2", Value: "$3 million"}}); h != "Coverage: 1 salient figure never appears in the memo: $3 million (F2)." {
		t.Errorf("%q", h)
	}
	if p := partyLine([]analysis.PartyGap{{Name: "Acme", FIDs: []string{"F1"}}}); p != "Coverage: 1 party named in verified findings never appears in the memo: Acme (F1)." {
		t.Errorf("%q", p)
	}
}

// TestPassageQueryFitsTheSearch (lab-r4 r2): the query is at most passageTerms distinct content words
// (under docs.MaxTerms), in the requirement's order, its longest words kept.
func TestPassageQueryFitsTheSearch(t *testing.T) {
	q := passageQuery(incapReq + " " + incapMemo)
	ws := strings.Fields(q)
	if len(ws) == 0 || len(ws) > passageTerms || len(q) > docs.MaxQuery || !strings.Contains(q, "physicians") || !strings.Contains(q, "certifications") || strings.Contains(q, " the ") {
		t.Errorf("%d words: %q", len(ws), q)
	}
	if q := passageQuery("Both physicians must be licensed."); q != "physicians licensed" {
		t.Errorf("%q", q)
	}
}

// TestTypedSumNeverReachesTheDeliverable (lab-r4 r2): a typed "$3 million" over a quote that states
// only "Section 3" is not admitted as a value: the finding is admitted without it, and the
// deliverable never states it.
func TestTypedSumNeverReachesTheDeliverable(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Damages\nGreystone claims damages of $644,910 for unpaid invoices.\n\n2. Note\nSection 3. Payment. The Company shall pay the outstanding principal on demand.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices", val("Greystone", "damages", "$644,910", "Money")),
			fd("The Company owes the principal under Section 3 of the Note.", d1, "Section 3. Payment. The Company shall pay the outstanding principal on demand.", val("Company", "principal", "$3 million", "Money")))},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 2 || !fs[1].Verified || len(fs[1].Values) != 0 || len(fs[0].Values) != 1 {
		t.Fatalf("%+v", fs)
	}
	if out := deref(v.Output); strings.Contains(out, "$3 million") || strings.Contains(out, "$3,000,000") {
		t.Errorf("the deliverable states a sum no document states:\n%s", out)
	}
}
