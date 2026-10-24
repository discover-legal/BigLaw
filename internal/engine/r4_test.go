// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
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
