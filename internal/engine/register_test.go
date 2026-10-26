// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
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

// TestRestatedNeverMergesTwoRows: two rows of one table share most of their words and the same
// amount, and are two payments; only an exact duplicate merges.
func TestRestatedNeverMergesTwoRows(t *testing.T) {
	row := func(month string) Finding {
		q := "Monthly Dues — 2022\tMonthly membership dues paid to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini (" + month + " 2022)\t1\t$2,800.00"
		return Finding{Finding: analysis.Finding{Verified: true, Claim: "A dues payment of $2,800.00 was made in " + month + " 2022.", Quotes: []analysis.Quote{{Quote: q}},
			Values: []analysis.Value{{Value: "$2,800.00", Class: "Money"}}}}
	}
	if restated(row("May"), row("Jun")) {
		t.Error("two rows merged")
	}
	if a, b := row("May"), row("May"); !restated(a, b) {
		t.Error("a row restated is not merged")
	}
	p := func(s string) Finding {
		return Finding{Finding: analysis.Finding{Verified: true, Claim: s, Quotes: []analysis.Quote{{Quote: s}}, Values: []analysis.Value{{Value: "$438,000", Class: "Money"}}}}
	}
	if restated(p("The Division estimates that excess profits in the personal account totaled approximately $438,000 over the Review Period."),
		p("The Division estimates that excess profits in the personal account totaled approximately $438,000 over the Review Period (2021-2023).")) {
		t.Error("a near restatement is merged (lab-r4 verifier H2: only exact duplicates merge)")
	}
}

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

// The lab-r4 r2 verifier's repros (H1, M1–M6, L3, L4, L7), kept as regression tests: each failed at
// 4c54aeb. The figure parser's full specification is figures_test.go.

// mv is a typed value as a subagent states it.
func mv(v, c string) analysis.Value { return analysis.Value{Value: v, Class: c} }

// H1: a decimal comma is read as a thousands separator: "EUR 3,5 million" is 35,000,000.
func TestR4V2DecimalCommaTenfold(t *testing.T) {
	q := "The Purchaser shall pay EUR 3,5 million on Closing."
	for _, v := range []analysis.Value{mv("€35 million", "Money"), mv("$35 million", "Money"), mv("15%", "Percentage")} {
		qq := q
		if v.Class == "Percentage" {
			qq = "Interest accrues at 1,5 % per annum."
		}
		if valueInQuote(v, qq) {
			t.Errorf("%s verified from %q", v.Value, qq)
		}
	}
	if got := Untraced("The purchase price was €35 million.", q, false); len(got) == 0 {
		t.Errorf("G8 traces €35 million to %q", q)
	}
	if !valueInQuote(mv("€3.5 million", "Money"), q) {
		t.Errorf("the correct €3.5 million is rejected")
	}
}

// H1 end to end: the tenfold figure is a verified value of a verified finding.
func TestR4V2DecimalCommaReachesLedger(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Price\nThe Purchaser shall pay EUR 3,5 million on Closing.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The Purchaser pays €35 million on Closing.", d1, "The Purchaser shall pay EUR 3,5 million on Closing", val("Purchaser", "purchase price", "€35 million", "Money")))},
	}), Config{})
	for _, x := range ledgerFindings(t, f, v.ID) {
		for _, vv := range x.Values {
			if strings.Contains(vv.Value, "35") && x.Verified {
				t.Errorf("%s verified with value %+v from a quote stating EUR 3,5 million", x.FID, vv)
			}
		}
	}
	if out := deref(v.Output); strings.Contains(out, "35 million") || strings.Contains(out, "35,000,000") {
		t.Errorf("the deliverable states a tenfold sum:\n%s", out)
	}
	fs := ledgerFindings(t, f, v.ID) // M5: the dropped value is noted in its finding, never silent
	if len(fs) != 1 || len(fs[0].Values) != 0 || len(fs[0].ValueNotes) != 1 || !strings.Contains(fs[0].ValueNotes[0], "€35 million") {
		t.Errorf("%+v", fs)
	}
}

// L3: a conflict's sides are addressed only by figures of their kind: a memo's "3,000,000 shares"
// never states a usd:3000000 side.
func TestR4V2AddressedReadsKind(t *testing.T) {
	c := analysis.Conflict{Kind: "numeric", Values: []analysis.ConflictVal{{Value: "$3,000,000", Canon: "usd:3000000"}, {Value: "$2,500,000", Canon: "usd:2500000"}}}
	if addressed(c, oracleSections("The Company issued 3,000,000 shares and paid $2,500,000."), nil) {
		t.Error("a share count addresses a dollar side")
	}
	if !addressed(c, oracleSections("The records differ: $3 million against $2,500,000."), nil) {
		t.Error("both sides stated, not addressed")
	}
	if addressed(c, oracleSections("The records differ: €3 million against $2,500,000."), nil) {
		t.Error("a euro sum addresses a dollar side")
	}
}

// L4: identical lines of two documents (invoices 1042 and 1043) are two findings, never merged.
func TestR4V2RestatedComparesDocuments(t *testing.T) {
	v := analysis.Value{Entity: "Firm", Metric: "retainer", Value: "$5,000", Class: "Money"}
	x := fnd("I1", "Invoice 1042 bills the monthly retainer of $5,000.", "Monthly retainer: $5,000", v)
	y := fnd("I1", "Invoice 1043 bills the monthly retainer of $5,000.", "Monthly retainer: $5,000", v)
	x.Quotes[0].DocumentID, y.Quotes[0].DocumentID = "inv1042", "inv1043"
	if restated(x, y) {
		t.Error("identical lines of two documents merged")
	}
	y.Quotes[0].DocumentID = "inv1042"
	if !restated(x, y) {
		t.Error("an exact duplicate in one document is not merged")
	}
}

// L7: a long requirement's passage query keeps its names, defined terms and numbers first.
func TestR4V2PassageQueryKeepsNames(t *testing.T) {
	r := "Notwithstanding anything to the contrary in the Engagement Letter, the Consultant, Dr. Lee, shall provide comprehensive quarterly compliance certifications, independently audited reconciliation statements, contemporaneous documentation of every subcontracting arrangement, written acknowledgements from representatives, confidentiality undertakings, indemnification confirmations and insurance certificates evidencing professional liability coverage within thirty days of the Fee being paid to Dr. Wu."
	q := " " + passageQuery(r) + " "
	for _, w := range []string{"lee", "wu", "thirty", "fee", "consultant", "engagement"} {
		if !strings.Contains(q, " "+w+" ") {
			t.Errorf("%q is not in the passage query %q", w, q)
		}
	}
	if n := len(strings.Fields(q)); n > passageTerms {
		t.Errorf("%d words", n)
	}
}

// lab-r4 r3 verifier repros (critique/biglaw-lab-r4-r3-verifier.md): each failed at 4b5f776 and passes since lab-r4 r4.

// H1: two adjacent figures whose currency sits between them ("1,000 USD 2,000 USD") overlap, and
// ranges() slices line[a.end:b.start] with a.end > b.start: a panic.
func TestR4V3AdjacentCurrencyPanics(t *testing.T) {
	for _, s := range []string{"Fees 1,000 USD 2,000 USD", "Rent 5 $ 10", "Q1 1.000 EUR 2.000 EUR", "100 € 200 €", "fee 50 GBP 75 GBP total"} {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("Literal(%q) panicked: %v", s, p)
				}
			}()
			analysis.Literal(s)
		}()
	}
}

// H1 end to end: one verified quote from a fee table fails the whole task.
func TestR4V3AdjacentCurrencyFailsTheTask(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nAnnual fees 1,000 USD 2,000 USD per seat.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The annual fee is 1,000 USD.", d1, "Annual fees 1,000 USD 2,000 USD per seat", val("Customer", "annual fee", "1,000 USD", "Money")))},
	}), Config{})
	if v.Status == "failed" || v.Error != nil {
		t.Errorf("the task %s: %v", stop(v), deref(v.Error))
	}
}

// H3: the parser admits a decimal-comma value ("$3,5 million", "1,5%") as 3.5 million / 1.5%, but
// the analysis package reads the same verified value with its own parser (analysis.Number: commas
// stripped) as 35 million / 15%: Calc's worksheets (which G8 treats as traced) and the join's canon
// are tenfold.
func TestR4V3DecimalCommaValueReadTenfoldDownstream(t *testing.T) {
	cases := []struct {
		v analysis.Value
		q string
	}{
		{mv("$3,5 million", "Money"), "The Purchaser shall pay USD 3,5 million on Closing."},
		{analysis.Value{Value: "3,5 million", Class: "Money", Unit: "EUR"}, "The Purchaser shall pay EUR 3,5 million on Closing."},
		{mv("1,5%", "Percentage"), "Interest accrues at 1,5 % per annum."},
	}
	for _, c := range cases {
		if !valueInQuote(c.v, c.q) {
			continue // not admitted: nothing downstream reads it
		}
		_, canon, _, _ := analysis.Canon(c.v)
		if strings.HasSuffix(canon, ":35000000") || canon == "pct:15" {
			t.Errorf("%q (admitted from %q as 3.5 million / 1.5%%) has canon %s", c.v.Value, c.q, canon)
		}
	}
	fs := []analysis.Finding{
		{FID: "F1", Verified: true, Values: []analysis.Value{mv("1,5%", "Percentage")}},
		{FID: "F2", Verified: true, Values: []analysis.Value{mv("$1,000,000", "Money")}},
	}
	r1, r2 := "F1.v1", "F2.v1"
	w, err := analysis.Calc(fs, "product", []analysis.Operand{{Ref: &r2}, {Ref: &r1}}, nil)
	if err == nil && w.Result != "15000" {
		t.Errorf("1,5%% of $1,000,000 computes to %s %s (worksheet %s), not 15000", w.Result, w.Unit, w.WID)
	}
}

// H3 end to end: the admitted value reaches the ledger and the lead's calculator reads it tenfold
// (product of the verified rate and a literal principal, as the lint's own hint suggests).
func TestR4V3DecimalCommaReachesCalc(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Interest\nInterest accrues at 1,5 % per annum on the Loan.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("Interest accrues at 1,5% per annum on the Loan.", d1, "Interest accrues at 1,5 % per annum on the Loan",
			val("Borrower", "interest rate", "1,5%", "Percentage")))},
	}), Config{})
	an, err := f.e.Findings(context.Background(), v.ID)
	if err != nil || len(an) != 1 || len(an[0].Values) != 1 {
		t.Fatalf("%v %+v", err, an)
	}
	r1, lit, usd := "F1.v1", "1000000", "USD"
	w, err := analysis.Calc(an, "product", []analysis.Operand{{Literal: &lit, Unit: &usd}, {Ref: &r1}}, nil)
	if err != nil || w.Result != "15000" {
		t.Errorf("annual interest on $1,000,000 at the verified 1,5 %%: %s %s (%v), want 15000", w.Result, w.Unit, err)
	}
}

// M7: dropped values are not visible where the commit says: Cover.Unadmitted has no reader (not the
// cover tool's output, not finish, not the views), the sweep discards admit's notes, and the
// committee re-adds one LOW "admission" line per note every cycle, ahead of the reproduce and
// audit lines, so enough notes push a HIGH audit gap off the committee's page.
func TestR4V3AdmissionNotesFloodCommittee(t *testing.T) {
	var found []Critic
	for i := range 80 {
		found = append(found, Critic{ID: fmt.Sprintf("C%d", i+1), Critic: "admission", Severity: "LOW", FID: fmt.Sprintf("F%d", i+1),
			Text: fmt.Sprintf("value %q (purchase price) not admitted: the quote's \"1200000\" reads two ways (grouping or a decimal comma, or two currencies)", "$1,200,000")})
	}
	found = append(found, Critic{ID: "C81", Critic: "audit_gaps", Severity: "HIGH", Text: "Limitation period not addressed"})
	page := criticPage(1, found)
	list := page["findings"].([]map[string]any)
	if !slices.ContainsFunc(list, func(x map[string]any) bool { return x["severity"] == "HIGH" }) {
		t.Errorf("the HIGH audit gap is off the committee's page (%d of %d shown, truncated %v) behind LOW admission lines", len(list), len(found), page["truncated"])
	}
}

// FuzzFigures (the r3 verifier's, kept; it found H1 within seconds): no figure check panics on any
// text, the figure reader's stand-in included, and a text traces itself (G8 never flags a verbatim copy).
func FuzzFigures(f *testing.F) {
	for _, s := range []string{"Fees 1,000 USD 2,000 USD", "Rent 5 $ 10", "Q1 1.000 EUR 2.000 EUR", "100 € 200 €", "fee 50 GBP 75 GBP total",
		"Interest accrues at .5% per month", "a break fee of $.5 million", "The fee is CAD$1,200,000.", "Net loss: $(3,000)", "Change\t(12%)",
		"The fee is EUR 3,5 million and the cap is EUR 1,200,000.", "₹1,00,000 and INR 12,50,000", "Rent is $2000 per month.", "Shares outstanding: 3,000,000",
		"CHF 1'200'000", "$3 000 000", "three million dollars", "Revenue (in thousands): $4,000", "5 Mrd. EUR", "1.5.2024", "-$3,000", "between $3 and $5 million"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = Untraced(s, s, true)
		v := analysis.Value{Value: s, Class: "Money"}
		_, _, _, _ = valueStated(v, 0, oracleReadings(s, []string{s}, []analysis.Value{v}), s)
		_ = claimSumsIn(s, s, nil)
		_ = passageQuery(s)
		if u := Untraced(s, s, false); len(u) > 0 {
			t.Fatalf("%q does not trace itself: %v", s, u)
		}
	})
}

// L10: clause-reference digits take no slot of a requirement's passage query, and a capitalised word
// after a sentence's full stop is no proper noun because the word before the stop is short.
func TestR4V3PassageQueryReferencesAndAbbreviations(t *testing.T) {
	q := passageQuery("Subject to Sections 7.3(a), 7.3(b), 8.1 and 12.4 of this Agreement, the Purchaser shall indemnify, defend and hold harmless the Seller, its Affiliates and their respective Representatives from and against any and all Losses arising out of or relating to any breach of the representations and warranties in Article 4, provided that the aggregate liability shall not exceed the Cap as defined in Schedule 2.")
	for _, w := range strings.Fields(q) {
		if strings.ContainsAny(w, "0123456789") {
			t.Errorf("a clause number %q took a slot: %q", w, q)
		}
	}
	for _, w := range []string{"breach", "defend", "relating"} {
		if !slices.Contains(strings.Fields(q), w) {
			t.Errorf("%q dropped: %q", w, q)
		}
	}
	if !abbrev("Dr") || !abbrev("T") || abbrev("Lee") || abbrev("Wu") {
		t.Errorf("abbrev: Dr %v T %v Lee %v Wu %v", abbrev("Dr"), abbrev("T"), abbrev("Lee"), abbrev("Wu"))
	}
	if q := passageQuery("Repairs are due to Lee. Payment shall follow within thirty days of any water leakage or mould found by the tenant."); !strings.Contains(q, "lee") {
		t.Errorf("a name before a full stop lost: %q", q)
	}
}
