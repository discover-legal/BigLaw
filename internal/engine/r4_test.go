// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"hash/fnv"
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
