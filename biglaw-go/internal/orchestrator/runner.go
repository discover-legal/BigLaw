// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/learning"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Internal task runner ─────────────────────────────────────────────────────

func (o *Orchestrator) runTask(task *types.Task) {
	o.update(task, func(t *types.Task) { t.Status = "running" })
	emitProgress(task.ID, "started", map[string]interface{}{"taskId": task.ID, "workflowType": task.WorkflowType})
	audit.Default.Write(audit.WriteRequest{Event: "task.started", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"workflowType": task.WorkflowType}})

	phases := phaseSequences[task.WorkflowType]
	// Reconciliation is opt-in (RECONCILIATION_ENABLED=true). Its detection still has
	// poor precision (false-positive controversies) and the extra round bloats findings,
	// so by default the pipeline skips it; the code + graph types remain for when it
	// earns its place (and for the TypeDB contradiction graph).
	if os.Getenv("RECONCILIATION_ENABLED") != "true" {
		filtered := make([]types.TaskPhase, 0, len(phases))
		for _, p := range phases {
			if p != types.PhaseReconciliation {
				filtered = append(filtered, p)
			}
		}
		phases = filtered
	}
	var runErr error

	// At-start intent steering: hunt the matter's specific facts (amounts, rates,
	// account numbers, counts, %) into the finding pool up front via entity-aware
	// queries, so the rounds' conceptual queries don't leave them undiscovered and
	// synthesis is aware of them. Bounded + deduped (no flood). Best-effort — returns
	// nil when the matter has no retrievable exhibits.
	stier := types.TierTool
	sweepModel := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &stier, TaskType: routing.TaskExtraction})
	if prov, perr := o.provReg.Get(sweepModel); perr == nil {
		bare := routing.ResolveModelID(sweepModel)
		// Evidence graph FIRST: one grounded extraction (entities + relations + the matter's
		// distinct allegations) that everything downstream reads — so recruitment and the
		// coverage spine derive the SAME allegation set from the graph, instead of separate
		// LLM enumerations that vary run-to-run (the 12↔16-section swing that dominated
		// scores). buildEvidenceGraph populates task.Allegations via ensureAllegations.
		o.buildEvidenceGraph(task, prov, bare)
		// Classify the matter (practice area / sector / work type) from its DOCUMENTS,
		// so recruitment seats the right specialists. The task description is too thin
		// (the practice area lives in the exhibits, not "review and summarize"), so we
		// classify from sampled passages and populate NosLegal — which recruitment then
		// uses as a signal alongside the (kept-specific) round goal.
		if tags := o.classifyMatter(task, prov, bare); tags.AreaOfLaw != nil || tags.Sector != nil {
			o.update(task, func(t *types.Task) { t.NosLegal = &tags })
			slog.Info("matter classified", "task", task.ID, "area", strDeref(tags.AreaOfLaw), "sector", strDeref(tags.Sector))
			// On-demand specialist synthesis: generate fine-grained sub-specialty agents
			// for this area (cached in the agentdb), so the matter is staffed by tailored
			// specialists rather than only the generic registry.
			o.ensureSpecialists(strDeref(tags.AreaOfLaw), strDeref(tags.Sector), strDeref(tags.WorkType), prov, bare, task)
		}
		if sweep := o.specificsSweep(task, prov, bare); len(sweep) > 0 {
			o.update(task, func(t *types.Task) { t.Findings = append(t.Findings, sweep...) })
			slog.Info("specifics sweep seeded findings", "task", task.ID, "n", len(sweep))
		}
	}

	// Re-entrant machinery baseline (reentry.go): snapshot what the exhaustive round-0
	// passes produced, so each round boundary can compute its DELTA and re-fire the
	// targeted machinery on only what the round actually discovered.
	o.initReentryState(task)

	for _, phase := range phases {
		// CurrentRound is written only by this goroutine (under the lock,
		// for readers' sake), so reading it here is safe.
		if task.CurrentRound >= task.MaxRounds {
			slog.Warn("task hit maxRounds cap", "taskId", task.ID, "maxRounds", task.MaxRounds)
			break
		}
		o.update(task, func(t *types.Task) {
			t.CurrentPhase = phase
			t.UpdatedAt = time.Now()
		})
		emitProgress(task.ID, "phase", map[string]interface{}{"phase": phase})

		// Count the pool before the round so the re-entry hook below sees exactly
		// the round's own contribution (its previous outputs are already counted).
		o.mu.RLock()
		preRoundFindings := len(task.Findings)
		o.mu.RUnlock()

		if err := o.runPhase(task, phase); err != nil {
			runErr = err
			break
		}

		// Round-boundary re-entry (reentry.go): absorb the round's findings into the
		// evidence graph, compute the delta, and re-fire the targeted machinery on it
		// BEFORE the next round's Need/Offer descriptors are generated — so the next
		// round's agents (and synthesis) see what the machinery made of this round's
		// discoveries. No-op when the round added nothing or REENTRANT_MACHINERY=false.
		o.machineryReentry(task, preRoundFindings)

		// Wait for any pending gates. PendingGates is rewritten by the
		// gate handlers, so read it under the lock.
		hasPending := false
		o.mu.RLock()
		for _, g := range task.PendingGates {
			if g.Status == "pending" {
				hasPending = true
				break
			}
		}
		o.mu.RUnlock()
		if hasPending {
			o.update(task, func(t *types.Task) { t.Status = "awaiting_gate" })
			o.waitForGates(task)
			o.update(task, func(t *types.Task) { t.Status = "running" })
		}
	}

	if runErr != nil {
		o.update(task, func(t *types.Task) {
			t.Status = "failed"
			t.Error = runErr.Error()
		})
		if task.ActiveTimeEntryID != "" {
			o.time.Close(task.ActiveTimeEntryID)
			o.update(task, func(t *types.Task) { t.ActiveTimeEntryID = "" })
		}
		emitProgress(task.ID, "failed", map[string]interface{}{"error": runErr.Error()})
		audit.Default.Write(audit.WriteRequest{Event: "task.failed", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"error": runErr.Error()}})
		o.persistTasks()
		return
	}

	// Final synthesis. Reads Findings/PendingGates, which gate handlers
	// rewrite concurrently — work from a locked snapshot.
	output, err := o.synthesise(o.snapshot(task))
	if err != nil {
		output = fmt.Sprintf("Synthesis error: %v", err)
	}
	o.update(task, func(t *types.Task) { t.Output = output })

	// Tabulation for tabulate workflow.
	if task.WorkflowType == types.WorkflowTabulate {
		if table, err := o.tabulate(o.snapshot(task)); err == nil && table != nil {
			o.update(task, func(t *types.Task) { t.Table = table })
		}
	}

	o.update(task, func(t *types.Task) {
		t.Status = "complete"
		now := time.Now()
		t.CompletedAt = &now
		t.UpdatedAt = now
	})

	if task.ActiveTimeEntryID != "" {
		o.time.Close(task.ActiveTimeEntryID)
		o.update(task, func(t *types.Task) { t.ActiveTimeEntryID = "" })
	}

	final := o.snapshot(task)
	o.recordAgentOutcomes(final)

	emitProgress(task.ID, "complete", map[string]interface{}{"findings": len(final.Findings), "output": strutil.Truncate(final.Output, 200)})
	audit.Default.Write(audit.WriteRequest{Event: "task.complete", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"findings": len(final.Findings)}})
	o.persistTasks()
}

// recordAgentOutcomes feeds completed-task results back into the registry
// success scores and the Q-learning table, grouped per (agent, phase).
// Challenged-but-unresolved findings earn 30% of their stated confidence.
func (o *Orchestrator) recordAgentOutcomes(task *types.Task) {
	if len(task.Findings) == 0 {
		return
	}
	phaseByFinding := map[string]types.TaskPhase{}
	for _, r := range task.Rounds {
		for _, f := range r.Findings {
			phaseByFinding[f.ID] = r.Goal.Phase
		}
	}

	type bucket struct {
		phase  types.TaskPhase
		scores []float64
	}
	buckets := map[string]*bucket{}
	for _, f := range task.Findings {
		phase, ok := phaseByFinding[f.ID]
		if !ok {
			phase = task.CurrentPhase
		}
		key := f.AgentID + "::" + string(phase)
		b := buckets[key]
		if b == nil {
			b = &bucket{phase: phase}
			buckets[key] = b
		}
		effective := f.Confidence
		if f.Challenged && !f.Resolved {
			effective *= 0.3
		}
		b.scores = append(b.scores, effective)
	}

	phases := phaseSequences[task.WorkflowType]
	for key, b := range buckets {
		agentID := strings.SplitN(key, "::", 2)[0]
		sum := 0.0
		for _, s := range b.scores {
			sum += s
		}
		avg := sum / float64(len(b.scores))

		nextPhase := b.phase
		for i, p := range phases {
			if p == b.phase && i+1 < len(phases) {
				nextPhase = phases[i+1]
				break
			}
		}

		o.registry.RecordOutcome([]string{agentID}, avg)
		if err := o.learning.RecordEpisode(learning.EpisodeOpts{
			Phase:        b.phase,
			NextPhase:    nextPhase,
			Jurisdiction: task.Jurisdiction,
			WorkflowType: task.WorkflowType,
			AgentID:      agentID,
			Reward:       avg,
			Done:         true,
		}); err != nil {
			slog.Warn("learning: record episode failed", "agent", agentID, "err", err)
		}
	}
}
