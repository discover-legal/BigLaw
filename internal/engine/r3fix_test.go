// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"

	"fmt"
	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

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

// TestNearFalsePairForcesNothing (H2, end to end): a price and a deposit are not one figure stated
// differently, so neither finish nor the coverage pass tells the drafter the documents disagree.
func TestNearFalsePairForcesNothing(t *testing.T) {
	t.Parallel()
	spa := "1. Price\nBuyer shall pay the Purchase Price of $1,000,000 to Seller at Closing under this Purchase Agreement.\n"
	esc := "1. Deposit\nBuyer shall deposit $800,000 of the Purchase Price with the Escrow Agent under the Purchase Agreement.\n"
	f := newFixture(t, spa, esc)
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	q1 := "Buyer shall pay the Purchase Price of $1,000,000 to Seller at Closing under this Purchase Agreement"
	q2 := "Buyer shall deposit $800,000 of the Purchase Price with the Escrow Agent under the Purchase Agreement"
	three := func(r model.Reply) []model.Reply { return []model.Reply{r, r, r} }
	e, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What is the purchase price?", "Clause"))),
			calls(extract(d1, "price", "I1"), extract(d2, "deposit", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit")), calls(finishCall("submit")), calls(finishCall("submit")), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The purchase price is $1,000,000.", d1, q1, val("Buyer", "purchase price", "$1,000,000", "Money"))),
			facts(fd("Buyer deposits $800,000 in escrow.", d2, q2, val("Buyer", "escrow deposit", "$800,000", "Money")))},
		"draft_section": {section("The purchase price is $1,000,000 [F1].", "s"), section("The purchase price is $1,000,000 [F1].", "s")},
		"prosecute":     three(supported), "reproduce": three(noAnswers), "audit_gaps": three(noGaps),
	}), Config{})
	task := f.submit(0)
	f.step(context.Background())
	v, _ := e.Get(context.Background(), task.ID)
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	var placed string
	for _, c := range fake.Calls() {
		if c.Agent == "draft_section" {
			placed = fmt.Sprint(c.Messages[1].Content)
		}
	}
	if out := deref(v.Output); strings.Contains(fin, "differently") || strings.Contains(placed, "The documents disagree") || strings.Contains(out, "differently") {
		t.Errorf("a price and a deposit are treated as one figure stated differently:\nfinish: %.600s\ndrafter: %.500s\nmemo: %s", fin, placed, out)
	}
}

// TestDistinctPagesAreRead (M1): a page that differs from an earlier one only in its guarantor, or
// in a "not", is read; only an exact copy (case and white space aside) is skipped.
func TestDistinctPagesAreRead(t *testing.T) {
	body := func(who, verb string) string {
		var b strings.Builder
		fmt.Fprintf(&b, "Guaranty.\n%s hereby %s guarantee to the Lender the punctual payment of $2,500,000 due under the Credit Agreement dated March 1, 2024. ", who, verb)
		ws := strings.Fields("lender borrower guarantor obligations continuing absolute waiver notice demand protest subrogation setoff collateral release amendment extension indulgence bankruptcy insolvency reinstatement")
		for i := range 30 {
			fmt.Fprintf(&b, "The %s and the %s clause covers %s, %s and %s without any further %s. ", ws[i%len(ws)], ws[(i*3+1)%len(ws)], ws[(i*5+2)%len(ws)], ws[(i*7+3)%len(ws)], ws[(i*11+4)%len(ws)], ws[(i*13+5)%len(ws)])
		}
		return b.String()
	}
	a := Visit{DocumentID: "d", SectionID: "s1", text: body("Alpha Holdings LLC", "irrevocably and unconditionally")}
	for _, b := range []Visit{
		{DocumentID: "d", SectionID: "s2", text: body("Beta Partners LP", "irrevocably and unconditionally")},
		{DocumentID: "d", SectionID: "s3", text: body("Alpha Holdings LLC", "does not")},
		{DocumentID: "d", SectionID: "s4", text: body("Alpha Holdings LLC", "irrevocably and unconditionally")[:400]}, // a part of it: read
	} {
		if why, _ := skipWhy(b, []Visit{a}); why != "" {
			t.Errorf("%s skipped as %s: %.140q", b.SectionID, why, b.text)
		}
	}
	copied := Visit{DocumentID: "d", SectionID: "s5", text: strings.ToUpper(strings.ReplaceAll(a.text, ". ", ".\n  "))}
	if why, src := skipWhy(copied, []Visit{a}); why != "duplicate" || src != 0 {
		t.Errorf("an exact copy (case, white space): %q %d", why, src)
	}
}

// TestDataTableIsNotBoilerplate (H3): a trade blotter (tab-separated, a share count last), a payment
// schedule with PDF spacing (a year last) and an amortization page with no words are read; a real
// table of contents (a Contents heading, or dot-leadered entries) is skipped; r3: rising numbers alone are read.
func TestDataTableIsNotBoilerplate(t *testing.T) {
	var tab, sp, nums, pipes, lead, rise strings.Builder
	tab.WriteString("Trade Date\tAccount\tSecurity\tSide\tShares\n")
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&tab, "2021-02-%02d\tOceanic Fund I LP\tXYZ Corp\tBuy\t%d\n", i, 100*i)
		fmt.Fprintf(&sp, "Payment %d due under the Note      %d\n", i, 2024+i/12)
		fmt.Fprintf(&nums, "%02d/01/2025    $12,500.00    $%d,000.00\n", i%12+1, 500-i)
		fmt.Fprintf(&pipes, "| Payment %d | %d |\n", i, 10+i)
		fmt.Fprintf(&lead, "Article %d Definitions and interpretation ........ %d\n", i, 3)
		fmt.Fprintf(&rise, "Article %d Definitions and interpretation\t%d\n", i, 2*i+1)
	}
	for name, text := range map[string]string{"trade blotter": tab.String(), "payment schedule (PDF spacing)": sp.String(), "amortization page with no words": nums.String(), "a |-table": pipes.String(), "rising numbers, no Contents heading (r3: M4)": rise.String(),
		"a short contents batched with a clause": "Contents\n" + lead.String()[:300] + "\n2. Price\nThe price for the goods is $100,000, payable within thirty days of delivery.\n"} {
		if why, _ := skipWhy(Visit{DocumentID: "d", SectionID: "s9", text: text}, nil); why != "" {
			t.Errorf("%s skipped as %s", name, why)
		}
	}
	for name, text := range map[string]string{"dot leaders": lead.String(), "Contents heading": "Contents\nDefinitions\t1\nThe Loan\t4\nRepayment\t4\n", "blank": " — \n · \n"} {
		if why, _ := skipWhy(Visit{DocumentID: "d", SectionID: "s9", text: text}, nil); why != "boilerplate" {
			t.Errorf("%s: %q, want boilerplate", name, why)
		}
	}
}

// TestSkipOfUnreadPageIsUnread (M2): a page skipped as a copy of a page the sweep then failed to read
// is not covered either: its document is reported unread, to the lead and to join_discrepancies.
func TestSkipOfUnreadPageIsUnread(t *testing.T) {
	t.Parallel()
	shared := "The Seller represents that the warehouse lease expires on June 30, 2027, that no default has occurred under it, that the landlord has consented to the assignment, that the security deposit of $250,000 is held by the landlord, and that no amendment has been made to the lease since it was signed by both parties."
	f := newFixture(t, "1. Lease\n"+shared+"\n\n2. Other\nFurther text of the disclosure schedule follows here.\n", "1. Lease\n"+shared+"\n")
	fake := newFake(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Is anything unusual?", "Conduct"))),
		calls(tc("sweep", m{"over": "sections", "documentIds": nil}))}, "extract_facts": {{Text: "not json"}, {Text: "not json"}, {Text: "not json"}, {Text: "not json"}}}))
	f.e = New(f.st, f.d, fake, testRoles, nil, Config{BudgetCalls: 150, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	sw := strings.Join(f.outputs(task.ID, "sweep"), "")
	ids, _ := f.e.Unread(context.Background(), task.ID)
	if !strings.Contains(sw, `"incomplete":true`) || !slices.Contains(ids, f.docs[1].ID) {
		t.Errorf("doc2 was never read (its page a copy of an unread page) yet is not reported unread: %v\nsweep: %s", ids, sw)
	}
}

// TestSkippedPagesReachLimitations (M3): the pages a sweep skipped are disclosed as a Limitations
// line (a Go-written note: finish puts every one in the memo's Limitations), with their count and
// documents, not only in the lead's tool output.
func TestSkippedPagesReachLimitations(t *testing.T) {
	t.Parallel()
	var toc strings.Builder
	toc.WriteString("Contents\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&toc, "Terms of the agreement, part %d ........ %d\n", i, i+2)
	}
	price := "2. Price\nThe price for the goods is $100,000, payable within thirty days of delivery.\n" + filler + "\n"
	f := newFixture(t, toc.String()+"\n"+price, price)
	var ex []model.Reply
	for range 6 {
		ex = append(ex, facts())
	}
	v := f.run(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "What is the price?", "Clause"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"extract_facts": ex}), Config{})
	notes, _ := f.e.rows(context.Background(), v.ID, kNote)
	var lim string
	for _, n := range notes {
		if n.Key == "sweep-skipped" {
			_ = json.Unmarshal(n.Data, &lim)
		}
	}
	if !strings.Contains(lim, "did not read 2 pages it judged exact duplicates of pages it read or boilerplate") || !strings.Contains(lim, f.docs[0].Name+" (1 of 2)") || !strings.Contains(lim, f.docs[1].Name+" (1 of 1)") {
		t.Errorf("skipped pages not disclosed: %q", lim)
	}
}

// TestLedgerFindingsPage (bench4, SEC: ledger op findings detail full returned all 892 findings,
// 682 KB, in one result, which overflowed the lead's context): the list comes a page at a time
// (ledgerPage bytes), next names the offset of the following page, and the pages hold every
// finding once, by issue too.
func TestLedgerFindingsPage(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Terms\nThe Seller shall deliver the goods.\n")
	f.engine(map[string][]model.Reply{}, Config{})
	task := f.submit(0)
	quote := strings.Repeat("The Seller shall deliver the goods on time and in full under the Agreement. ", 8)
	for i := range 900 {
		x := Finding{Finding: analysis.Finding{FID: fmt.Sprintf("F%d", i+1), IssueID: fmt.Sprintf("I%d", i%3+1), Claim: "The Seller must deliver.", Verified: true}}
		x.Citations = []Citation{{DocumentID: f.docs[0].ID, Quote: quote, Method: "exact", Confidence: 1}}
		if err := f.e.write(context.Background(), task.ID, kFinding, x.FID, x, true); err != nil {
			t.Fatal(err)
		}
	}
	page := func(issue any, offset any) (fids []string, next *int, size int) {
		in, _ := json.Marshal(m{"op": "findings", "issues": nil, "issueId": issue, "fid": nil, "detail": "full", "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": offset})
		res, err := f.e.ledgerTool(builtinSpecs).Call(context.Background(), agent.Call{TaskID: task.ID, Input: in})
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Findings []struct{ FID string }
			Next     *int
		}
		_ = json.Unmarshal(res.Output, &out)
		for _, x := range out.Findings {
			fids = append(fids, x.FID)
		}
		return fids, out.Next, len(res.Output)
	}
	for _, issue := range []any{nil, "I2"} {
		seen, pages := map[string]int{}, 0
		var off any
		for {
			fids, next, size := page(issue, off)
			if pages++; size > agent.ShrunkResult-1000 || len(fids) == 0 || pages > 100 {
				t.Fatalf("issue %v page %d: %d bytes, %d findings", issue, pages, size, len(fids))
			}
			for _, x := range fids {
				seen[x]++
			}
			if next == nil {
				break
			}
			off = *next
		}
		if want := map[bool]int{true: 900, false: 300}[issue == nil]; len(seen) != want || pages < 2 {
			t.Errorf("issue %v: %d findings over %d pages, want %d over several", issue, len(seen), pages, want)
		}
		for x, n := range seen {
			if n != 1 {
				t.Errorf("%s listed %d times", x, n)
			}
		}
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
