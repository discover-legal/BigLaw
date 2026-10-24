// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

// pricedEmbedder is a hash embedder that bills each call at usd (an embeddings endpoint's usage).
type pricedEmbedder struct{ usd float64 }

func (p pricedEmbedder) Embed(_ context.Context, r model.EmbedRequest) (*model.Embeddings, error) {
	e := &model.Embeddings{Model: "emb-m", Usage: model.Usage{InputTokens: 10, CostUSD: &p.usd}}
	for _, in := range r.Inputs {
		h := fnv.New32a()
		h.Write([]byte(in))
		s := h.Sum32()
		e.Vectors = append(e.Vectors, []float32{1, float32(s%97) + 1, float32(s%89) + 1})
	}
	return e, nil
}

// agentKill kills the process at the at-th call of one agent.
type agentKill struct {
	*model.Fake
	mu    sync.Mutex
	agent string
	n, at int
	kill  func()
}

func (k *agentKill) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	k.mu.Lock()
	k.n += btoi(req.Agent == k.agent)
	hit := req.Agent == k.agent && k.n == k.at && k.kill != nil
	k.mu.Unlock()
	if hit {
		k.kill()
		return nil, context.Canceled
	}
	return k.Fake.Chat(ctx, req)
}

// TestKillDuringCommitteeChargesMeteredSpend (bench5 trust: a container restart with the committee
// in flight): every model call inside the committee's step is journaled as it happens, the parallel
// critics' turns and their subagents' query embeddings alike, so a process that dies mid-committee is
// charged what those calls spent plus the worst case of the ones in flight, never the committee's
// whole grant, and never less than it spent (here a reproduce critic's retrieve embedded its query
// at $0.05 before the crash: without the embedding metered the lost charge left it out).
func TestKillDuringCommitteeChargesMeteredSpend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{t: t, st: st, d: docs.New(st)}
	f.d.UseEmbedder(pricedEmbedder{0.05}, "emb-m")
	for _, x := range []string{complaintText, docketText} {
		d, err := f.d.IngestText(ctx, "doc.md", x)
		if err != nil {
			t.Fatal(err)
		}
		f.docs = append(f.docs, d)
	}
	sc := f.happy()
	search := calls(tc("retrieve", m{"op": "search", "query": "damages invoices", "documentId": nil, "sectionId": nil, "k": nil, "offset": nil, "limit": nil}))
	sc["reproduce"] = []model.Reply{search, noAnswers, noAnswers}
	sc["prosecute"] = []model.Reply{supported, supported}
	sc["audit_gaps"] = []model.Reply{noGaps, noGaps}
	cost := 0.0001
	for k, v := range sc {
		for i := range v {
			v[i].Usage = model.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: &cost}
		}
		sc[k] = v
	}
	prices, err := model.LoadPrices(`{"lead-m": {"in": 0.01, "out": 0.05}, "spec-m": {"in": 0.01, "out": 0.05}, "crit-m": {"in": 0.01, "out": 0.05}}`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}
	k := &agentKill{Fake: model.NewFake(sc), agent: "reproduce", at: 2}
	f.e = New(f.st, f.d, k, testRoles, prices, cfg, nil)
	task := f.submit(0)
	k.kill = func() { // the process dies: another holds the lease, so nothing it says is recorded
		if _, err := f.st.DB().Exec(`UPDATE tasks SET lease_owner = 'dead' WHERE id = $1`, task.ID); err != nil {
			t.Error(err)
		}
		f.e.active(task.ID).cancel()
	}
	f.step(ctx)
	var held agent.Grant
	if err := f.st.DB().QueryRow(`SELECT grant_held FROM task_steps WHERE task_id = $1 AND tool = 'committee' AND status = 'started'`, task.ID).Scan(jsonScan{&held}); err != nil {
		t.Fatalf("the crashed committee's started step: %v (%s)", err, f.steps(task.ID))
	}
	k.kill = nil
	f.e = New(f.st, f.d, k, testRoles, prices, cfg, nil)
	f.stale(task.ID)
	f.step(ctx)
	var lost agent.Usage
	f.st.DB().QueryRow(`SELECT lost FROM tasks WHERE id = $1`, task.ID).Scan(jsonScan{&lost})
	a := Allocate(cfg.BudgetCalls, cfg.BudgetUSD)
	// spent before the crash: a few critic calls at $0.0001 and the $0.05 embedding; in flight: at most
	// three critic calls, each worst case under $0.001 at these prices.
	if lost != (agent.Usage{Calls: held.Calls, USD: held.USD}) || lost.USD < 0.05 || lost.USD > 0.05+0.005 || lost.Calls > 6 || lost.USD >= a.CycleUSD {
		t.Fatalf("lost %+v (journaled %+v; the cycle's grant is $%.2f)\n%s", lost, held, a.CycleUSD, f.steps(task.ID))
	}
	if v := f.get(task.ID); stop(v) != "awaiting_gate/finished" {
		t.Fatalf("%s\n%s", stop(v), f.steps(task.ID))
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
	fake := model.NewFake(nil) // an unfunded grant makes no call
	spans, _, _ := placeRequirement(context.Background(), incapMemo, incapReq)
	chk, why, err := (&Engine{}).checkSection(context.Background(), &agent.Runtime{Chat: fake, Roles: testRoles}, agent.NewBudget(0, 0, 0), newReq(incapMemo, spans))
	if err != nil || chk != nil || !strings.Contains(why, "could not fund") || len(fake.Calls()) != 0 {
		t.Errorf("no grant: check %+v, why %q, err %v, calls %d", chk, why, err, len(fake.Calls()))
	}
}

// bench5 SEC: the referral's compensation paragraph and the bank-records exhibit's last rows, verbatim.
const (
	compRef  = "The Division alleges that Bellini received undisclosed compensation from Ostrowski in connection with the directed brokerage arrangement.\nThe total undisclosed compensation received by Bellini, in the form of payments made by Ostrowski to Bayshore Palms Golf & Country Club for Bellini's benefit, was approximately $92,600 ($45,000 initiation fee + $47,600 in monthly dues = $92,600). The Division alleges that these payments constituted compensation to Bellini for directing client trades to Lakeshore Trading and were not disclosed to WCA, its compliance personnel, or any affected client.\n"
	compRows = "Total Monthly Dues\tSum of all monthly membership dues payments (2022 + 2023 + 2024)\t05/03/2022 – 01/02/2024\t21\t$2,800.00\t$58,800.00\nGRAND TOTAL\tTotal of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini\t05/03/2022 – 01/02/2024\t22\t\t$103,800.00\n"
)

// TestBench5CompensationConflictReachesFinish (bench5 SEC C-035/C-036: $92,600 vs $103,800 never
// paired): F388 as the run extracted it, verbatim (its row quoted with " | " for the exhibit's tabs, its
// $103,800.00 stated in its claim and quote but typed nowhere), now verifies, keeps the sum as an
// untyped value, and finish's own join over all verified findings asks the memo to state both figures.
func TestBench5CompensationConflictReachesFinish(t *testing.T) {
	t.Parallel()
	f := newFixture(t, compRef, compRows)
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	f752 := fd("Bellini received undisclosed compensation from Ostrowski totaling approximately $92,600 in connection with a directed brokerage arrangement with Lakeshore Trading.", d1,
		"The total undisclosed compensation received by Bellini, in the form of payments made by Ostrowski to Bayshore Palms Golf & Country Club for Bellini's benefit, was approximately $92,600 ($45,000 initiation fee + $47,600 in monthly dues = $92,600).",
		val("Bellini", "total undisclosed compensation", "$92,600", "Money"), val("Ostrowski", "initiation fee payment", "$45,000", "Money"), val("Ostrowski", "total monthly dues for period", "$47,600", "Money"))
	f388 := fd("Kevin Ostrowski's personal checking account at Coastal Commerce Bank was used to make payments totaling $103,800.00 to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini between May 3, 2022 and January 2, 2024.", d2,
		"Total of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini | 05/03/2022 – 01/02/2024 | 22 | | $103,800.00",
		val("Marcus T. Bellini", "CRD Number", "5098321", "Identifier"), val("payments from Ostrowski", "count", "22", "Count"), val("monthly dues payments", "count", "21", "Count"))
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What compensation did Bellini receive?", "RedFlag"))), calls(extract(d1, "compensation", "I1")), calls(extract(d2, "payments", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("check"))},
		"extract_facts": {facts(f752), facts(f388)},
		"draft_section": {section("Bellini received about $92,600 [F1].", "s")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 2 || !fs[1].Verified || !slices.ContainsFunc(fs[1].Values, func(x analysis.Value) bool { return x.Value == "$103,800.00" && x.Metric == "" }) {
		t.Fatalf("F2 %+v", fs)
	}
	fin := strings.Join(f.outputs(v.ID, "finish"), "")
	if !strings.Contains(fin, "[F1] $92,600 vs [F2] $103,800.00") || !strings.Contains(fin, "the memo does not state both") {
		t.Fatalf("finish %s\n%s", fin, f.steps(v.ID))
	}
}

// TestJoinAliasesReachFinish: aliases the lead gives join_discrepancies are kept for the run, so
// finish's own join of the memo's conflicts unifies the same parties (it ran with none before).
func TestJoinAliasesReachFinish(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe Adviser paid commissions of $291,400 to Lakeshore Trading LLC.\n", "Schedule\nOstrowski received commissions of $310,000 in the period.\n")
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	join := tc("join_discrepancies", m{"fids": nil, "aliases": []m{{"name": "Lakeshore Trading LLC", "aliases": []string{"Ostrowski"}}}})
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What commissions were paid?", "RedFlag"))), calls(extract(d1, "commissions", "I1")), calls(extract(d2, "commissions", "I1")),
			calls(join), calls(draft("I1", "F1")), calls(committee()), calls(finishCall("check"))},
		"extract_facts": {facts(fd("The Adviser paid commissions of $291,400 to Lakeshore Trading LLC.", d1, "The Adviser paid commissions of $291,400 to Lakeshore Trading LLC",
			val("Lakeshore Trading LLC", "commissions", "$291,400", "Money"))),
			facts(fd("Ostrowski received commissions of $310,000.", d2, "Ostrowski received commissions of $310,000 in the period", val("Ostrowski", "commissions", "$310,000", "Money")))},
		"draft_section": {section("The Adviser paid $291,400 in commissions [F1].", "s")},
	}), Config{})
	if j := strings.Join(f.outputs(v.ID, "join_discrepancies"), ""); !strings.Contains(j, "$310,000") {
		t.Fatalf("join %s", j)
	}
	if fin := strings.Join(f.outputs(v.ID, "finish"), ""); !strings.Contains(fin, "commissions differently ([F2] $310,000 vs [F1] $291,400)") && !strings.Contains(fin, "commissions differently ([F1] $291,400 vs [F2] $310,000)") {
		t.Fatalf("finish %s\n%s", fin, f.steps(v.ID))
	}
}

// TestSweepAdmitsOnlyThePlansFindingsOnce (bench5 SEC: 896 findings from 142 pages, 177 from one
// brochure section of generic process text, figures restated page after page): a visit is sent the
// plan's issues; a finding that names none is not admitted, one call admits at most maxFactsPerCall,
// and a later page's restatement of an admitted figure (same values, its quote inside the first's) is
// merged into it, across issues.
func TestSweepAdmitsOnlyThePlansFindingsOnce(t *testing.T) {
	t.Parallel()
	doc := "1. Fees\nThe fee of $5,000 is due on March 1, 2026.\n" + filler + "\n\n2. Summary\nAs stated above, the fee of $5,000 is due on March 1, 2026.\n" + filler + "\n"
	f := newFixture(t, doc)
	d1 := f.docs[0].ID
	first := []m{on("I1", fd("The fee of $5,000 is due on March 1, 2026.", d1, "The fee of $5,000 is due on March 1, 2026", val("Buyer", "fee", "$5,000", "Money"), val("Buyer", "fee due", "March 1, 2026", "Date"))),
		fd("Further delivery terms apply.", d1, "Further delivery terms apply.")} // names no issue: boilerplate
	for i := range 13 {
		first = append(first, on("I2", fd(fmt.Sprintf("Delivery term %d applies.", i), d1, "Further delivery terms apply.")))
	}
	again := facts(on("I2", fd("The summary restates that the $5,000 fee falls due on March 1, 2026.", d1, "the fee of $5,000 is due on March 1, 2026",
		val("Buyer", "fee amount", "$5,000", "Money"), val("Buyer", "due date", "March 1, 2026", "Date"))))
	fake := model.NewFake(f.with(map[string][]model.Reply{
		"lead":  {calls(plan(issue("I1", "When is the fee due?", "Clause"), issue("I2", "What delivery terms apply?", "Clause"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"page1": {facts(first...)}, "page2": {again},
	}))
	fake.Route = func(r model.Request) string {
		if r.Agent != "extract_facts" {
			return r.Agent
		}
		return map[bool]string{true: "page2", false: "page1"}[strings.Contains(sweepReq(r).SectionText, "As stated above")]
	}
	f.e = New(f.st, f.d, fake, testRoles, nil, Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	fs := ledgerFindings(t, f, task.ID)
	if len(fs) != 12 || fs[0].IssueID != "I1" || fs[1].IssueID != "I2" || len(fs[0].Citations) != 2 || slices.ContainsFunc(fs, func(x Finding) bool { return x.Claim == "Further delivery terms apply." }) {
		t.Fatalf("%d findings: %+v", len(fs), fs)
	}
	for _, c := range fake.Calls() {
		if c.Agent == "extract_facts" && !strings.Contains(fmt.Sprint(c.Messages[1].Content), `"issues":[{"id":"I1","question":"When is the fee due?"},{"id":"I2","question":"What delivery terms apply?"}]`) {
			t.Errorf("a visit was not sent the plan's issues: %s", c.Messages[1].Content)
		}
	}
}

// TestCoverageLinesSummarised (bench5 SEC: 101 "Coverage:" lines in Limitations, one per party and
// figure): the parties the memo never names are one line and the missing figures another, each
// listing at most maxListed and counting the rest.
func TestCoverageLinesSummarised(t *testing.T) {
	var ps []analysis.PartyGap
	var hs []analysis.Handle
	for i := range 30 {
		ps = append(ps, analysis.PartyGap{Name: fmt.Sprintf("Party %d", i), FIDs: []string{fmt.Sprintf("F%d", i), "F99", "F100", "F101"}})
		hs = append(hs, analysis.Handle{FID: fmt.Sprintf("F%d", i), Label: "fee", Value: fmt.Sprintf("$%d,000", i+1)})
	}
	p, h := partyLine(ps), figureLine(hs)
	if !strings.HasPrefix(p, "Coverage: 30 parties named in verified findings never appear in the memo: Party 0 (F0, F99, F100); ") || !strings.HasSuffix(p, " and 18 more.") ||
		!strings.HasPrefix(h, "Coverage: 30 salient figures never appear in the memo: $1,000 (fee, F0); ") || !strings.HasSuffix(h, " and 18 more.") ||
		partyLine(nil) != "" || figureLine(nil) != "" {
		t.Fatalf("%q\n%q", p, h)
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

// TestPlacementsStrongestFirst: the coverage pass places an issue with a figure conflict, then the
// issues by their strongest left-out finding, not in plan order, so a pass whose share runs out has
// placed the most salient evidence.
func TestPlacementsStrongestFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe Firm failed to disclose the fee. The fee was $291,400 in 2023.\n", "Schedule\nThe fee was $310,000 in 2023.\n")
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Was the fee disclosed?", "RedFlag"), issue("I2", "What was the fee?", "RedFlag"))),
			calls(extract(d1, "disclose", "I1")), calls(extract(d1, "fee", "I2")), calls(extract(d2, "fee", "I2"))},
		"extract_facts": {facts(fd("The Firm failed to disclose the fee.", d1, "The Firm failed to disclose the fee")),
			facts(fd("The fee was $291,400.", d1, "The fee was $291,400 in 2023", val("Firm", "fee", "$291,400", "Money"))),
			facts(fd("The fee was $310,000.", d2, "The fee was $310,000 in 2023", val("Firm", "fee", "$310,000", "Money")))},
	}), Config{})
	order, place, err := f.e.placements(context.Background(), v.ID)
	if err != nil || strings.Join(order, ",") != "I2,I1" || len(place["I2"].Conflicts) != 1 || !slices.Contains(place["I1"].FIDs, "F1") {
		t.Fatalf("order %v, %+v, %v", order, place, err)
	}
}

// TestTablePageKeepsARowEach: on a table page each row is a fact (a trade, a payment), so the cap is
// one finding per row when the page has more rows than maxFactsPerCall.
func TestTablePageKeepsARowEach(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	doc.WriteString("Trades\nTrade\tDate\tShares\tPrice\n")
	for i := range 20 {
		fmt.Fprintf(&doc, "T-%d\t2023-01-%02d\t%d00\t%d.50\n", i, i+1, i+1, 40+i)
	}
	f := newFixture(t, doc.String())
	d1 := f.docs[0].ID
	var rows []m
	for i := range 20 {
		rows = append(rows, on("I1", fd(fmt.Sprintf("Trade T-%d was allocated.", i), d1, fmt.Sprintf("T-%d\t2023-01-%02d\t%d00\t%d.50", i, i+1, i+1, 40+i))))
	}
	v := f.run(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "Which trades were allocated?", "RedFlag"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"extract_facts": {facts(rows...)},
	}), Config{})
	if fs := ledgerFindings(t, f, v.ID); len(fs) != 20 {
		t.Fatalf("%d findings of a 20-row page", len(fs))
	}
}

// TestRestatedNeverMergesTwoRows: two rows of one table share most of their words and the same
// amount, and are two payments; only a quote inside another's is a restatement of it.
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
	if !restated(p("The Division estimates that excess profits in the personal account totaled approximately $438,000 over the Review Period."),
		p("The Division estimates that excess profits in the personal account totaled approximately $438,000 over the Review Period (2021-2023).")) {
		t.Error("a prose restatement is not merged")
	}
}
