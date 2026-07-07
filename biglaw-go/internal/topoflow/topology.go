// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package topoflow

import "fmt"

// TopologyResult is the single merged delta a composite cell returns (spec §8.1).
type TopologyResult struct {
	MergedHandoff  *HandoffState
	PublicMessages []string
	RoundsRun      int
	TotalTokens    int
	Retries        int
	Failed         bool
	Subtrace       map[string]any
}

// CompositeTopologyCell owns an internal multi-round loop and returns ONE merged
// handoff to the macro router. To the policy graph the whole cell looks like a
// single action with one reward (spec §8.1) — the central integration contract.
type CompositeTopologyCell interface {
	Run(ctx TaskContext, handoff *HandoffState, agents []Role, action Action,
		tx Transport, emb Embedder, cfg Config, budgetRemaining int) (TopologyResult, error)
}

func handoffSummary(h *HandoffState) string {
	var present []string
	for _, f := range HandoffFields {
		if h.Get(f) != nil {
			present = append(present, f)
		}
	}
	if len(present) == 0 {
		return "Known so far: (nothing yet)"
	}
	s := "Known so far: "
	for i, p := range present {
		if i > 0 {
			s += ", "
		}
		s += p
	}
	return s
}

var handoffThreadKeys = []string{
	"goal", "subproblem", "evidence", "critique", "verification", "draft_answer", "merged_answer",
}

// LinearWithSkipGenerator (spec §8.2) [AF]: a single-round sequential chain over
// the role set, threading the handoff; rounds_run = 1.
type LinearWithSkipGenerator struct{}

// Run implements CompositeTopologyCell.
func (LinearWithSkipGenerator) Run(ctx TaskContext, handoff *HandoffState, agents []Role, action Action,
	tx Transport, emb Embedder, cfg Config, budgetRemaining int) (TopologyResult, error) {
	merged := handoff.Clone()
	total, retries := 0, 0
	failed := false
	var publics []string
	var steps []map[string]any
	for _, role := range agents {
		fields, tokens, err := tx.Complete(LLMRequest{
			Model:   cfg.DefaultModel,
			System:  role.SkillCard,
			User:    "Task: " + ctx.Prompt + "\n" + handoffSummary(merged),
			Schema:  role.RequiredFields,
			Purpose: "agent",
			Role:    role.Name,
			Meta:    map[string]any{"round_goal": "single linear pass"},
		})
		if err != nil {
			return TopologyResult{}, err
		}
		total += tokens
		if asBool(fields["_failed"]) {
			failed = true
			retries++
		}
		if p := asString(fields["public"]); p != "" {
			publics = append(publics, p)
		}
		for _, k := range handoffThreadKeys {
			if v, ok := fields[k]; ok && v != nil {
				merged.Set(k, v)
			}
		}
		steps = append(steps, map[string]any{"role": role.Name, "tokens": tokens})
	}
	if merged.Get("draft_answer") != nil && merged.Get("merged_answer") == nil {
		merged.Set("merged_answer", merged.Get("draft_answer"))
	}
	return TopologyResult{
		MergedHandoff:  merged,
		PublicMessages: publics,
		RoundsRun:      1,
		TotalTokens:    total,
		Retries:        retries,
		Failed:         failed,
		Subtrace:       map[string]any{"mode": "linear", "steps": steps},
	}, nil
}

var dytopoHandoffKeys = []string{"draft_answer", "merged_answer", "verification", "evidence", "critique"}

// DyTopoGenerator (spec §8.3) [DT]: per-round semantic graph induction.
type DyTopoGenerator struct{}

// Run implements CompositeTopologyCell (DyTopo Algorithm 1).
func (DyTopoGenerator) Run(ctx TaskContext, handoff *HandoffState, agents []Role, action Action,
	tx Transport, emb Embedder, cfg Config, budgetRemaining int) (TopologyResult, error) {
	if emb == nil {
		emb = NewMockEmbedder()
	}
	tau := cfg.TauBuckets[action.TauBucket]
	kIn := action.KIn
	if kIn <= 0 {
		kIn = cfg.DytopoKIn
	}
	maxRounds := cfg.RoundBuckets[action.RoundBucket]
	n := len(agents)

	// H_i: per-agent memory, seeded from the incoming handoff.
	seed := handoffSummary(handoff)
	memories := make([][]string, n)
	for i := range memories {
		memories[i] = []string{seed}
	}

	total, retries := 0, 0
	failed, complete := false, false
	var rounds []map[string]any
	var publicsAll []string
	lastFields := make([]map[string]any, n)

	for t := 0; t < maxRounds; t++ {
		roundGoal := fmt.Sprintf("Round %d: advance the task", t)
		outs := make([]map[string]any, n)
		// Phase 1 — single-pass agent inference [DT eq 1,2]
		for i, role := range agents {
			Si := role.SkillCard + "\n" + roundGoal + "\nMemory:\n"
			for _, line := range memories[i] {
				Si += line + "\n"
			}
			fields, tokens, err := tx.Complete(LLMRequest{
				Model: cfg.DefaultModel, System: Si, User: "Task: " + ctx.Prompt,
				Schema: role.RequiredFields, Purpose: "agent", Role: role.Name,
				Meta: map[string]any{"round": t},
			})
			if err != nil {
				return TopologyResult{}, err
			}
			total += tokens
			if asBool(fields["_failed"]) {
				failed = true
				retries++
			}
			if asBool(fields["complete"]) {
				complete = true
			}
			outs[i] = fields
		}
		lastFields = outs

		// Phase 2 — topology induction [DT eq 4,5,6,7]
		qDesc := make([]string, n)
		kDesc := make([]string, n)
		for i := range outs {
			qDesc[i] = asString(outs[i]["q_desc"])
			kDesc[i] = asString(outs[i]["k_desc"])
		}
		R := relevanceMatrix(emb.Embed(qDesc), emb.Embed(kDesc))
		edges := buildEdges(R, tau, kIn)

		// Phase 3 — deterministic ordering [DT eq 8-11]
		sigma := topoOrCycleBreak(n, edges)

		// Phase 4 — sync barrier + memory update [DT eq 3]: only AFTER all routing.
		newMem := make([][]string, n)
		for i := 0; i < n; i++ {
			newMem[i] = append([]string(nil), memories[i]...)
			if p := asString(outs[i]["public"]); p != "" {
				newMem[i] = append(newMem[i], "[public "+agents[i].Name+"] "+p)
			}
			for _, j := range orderIncoming(i, edges) {
				if priv := asString(outs[j]["private"]); priv != "" {
					newMem[i] = append(newMem[i], "[from "+agents[j].Name+"] "+priv)
				}
			}
		}
		memories = newMem

		memSnapshot := map[string]any{}
		for i := 0; i < n; i++ {
			memSnapshot[agents[i].Name] = append([]string(nil), memories[i]...)
		}
		for i := range outs {
			if p := asString(outs[i]["public"]); p != "" {
				publicsAll = append(publicsAll, p)
			}
		}

		descriptors := map[string]any{}
		for i := 0; i < n; i++ {
			descriptors[agents[i].Name] = map[string]any{"q": qDesc[i], "k": kDesc[i]}
		}
		var edgeRecs []map[string]any
		for _, e := range edges {
			edgeRecs = append(edgeRecs, map[string]any{
				"src": agents[e.Src].Name, "dst": agents[e.Dst].Name, "score": e.Score,
			})
		}
		order := make([]string, n)
		for k, idx := range sigma {
			order[k] = agents[idx].Name
		}
		rounds = append(rounds, map[string]any{
			"t": t, "round_goal": roundGoal, "descriptors": descriptors,
			"edges": edgeRecs, "order": order, "memory_after": memSnapshot,
		})

		// Phase 5 — termination [NEW]
		if complete {
			break
		}
		if budgetRemaining >= 0 && total >= budgetRemaining {
			break
		}
	}

	// fold final memories + outputs into a single merged handoff
	merged := handoff.Clone()
	for _, o := range lastFields {
		for _, k := range dytopoHandoffKeys {
			if v, ok := o[k]; ok && v != nil {
				merged.Set(k, v)
			}
		}
	}
	if merged.Get("draft_answer") != nil && merged.Get("merged_answer") == nil {
		merged.Set("merged_answer", merged.Get("draft_answer"))
	}
	if complete {
		merged.Set("complete", true)
	}

	return TopologyResult{
		MergedHandoff:  merged,
		PublicMessages: publicsAll,
		RoundsRun:      len(rounds),
		TotalTokens:    total,
		Retries:        retries,
		Failed:         failed,
		Subtrace:       map[string]any{"mode": "dytopo", "tau": tau, "k_in": kIn, "rounds": rounds},
	}, nil
}
