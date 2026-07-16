// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/google/uuid"
)

func (o *Orchestrator) detectControversies(task *types.Task, prov providers.Provider, model string) []types.Controversy {
	var lb strings.Builder
	n := 0
	for _, f := range task.Findings {
		line := strings.Join(strings.Fields(f.Content), " ")
		if line == "" {
			continue
		}
		src := ""
		if len(f.Citations) > 0 {
			src = f.Citations[0].Source
		}
		fmt.Fprintf(&lb, "- [%s] %s\n", src, strutil.Truncate(line, 220))
		if n++; n >= 140 {
			break
		}
	}
	if n == 0 {
		return nil
	}
	prompt := fmt.Sprintf("Below are facts extracted from a legal matter's documents, each tagged with its [source]. Identify CONTROVERSIES — subjects where two or more sources assert DIFFERENT or INCONSISTENT values (a numeric discrepancy, a date conflict, a count mismatch, a contradictory statement). Report ONLY genuine conflicts, not restatements of the same value. Respond with ONLY a JSON array (max 6 items):\n[{\"subject\":\"<the disputed subject>\",\"kind\":\"monetary|temporal|count|categorical\",\"claims\":[{\"value\":\"<asserted value>\",\"source\":\"<source>\"},{\"value\":\"<conflicting value>\",\"source\":\"<source>\"}],\"significance\":\"<why the discrepancy matters>\"}]\n\nFACTS:\n%s",
		strutil.TruncateToTokens(lb.String(), 3000))
	resp, err := prov.Chat(providers.ChatParams{
		Model: model, MaxTokens: 1200,
		System:   "You are a meticulous reconciliation analyst. You surface only genuine cross-source conflicts. Output only the JSON array.",
		Messages: []providers.Message{{Role: "user", Content: prompt}}, CacheSystem: true, Temperature: o.cfg.LLMTemperature,
	})
	if err != nil {
		return nil
	}
	o.recordCost(resp, model, cost.ContextTask, task.ID)
	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	s, e := strings.Index(text, "["), strings.LastIndex(text, "]")
	if s < 0 || e <= s {
		return nil
	}
	var raw []types.Controversy
	if json.Unmarshal([]byte(text[s:e+1]), &raw) != nil {
		return nil
	}
	var out []types.Controversy
	for _, c := range raw {
		if strings.TrimSpace(c.Subject) == "" || len(c.Claims) < 2 {
			continue // a controversy needs a subject and ≥2 conflicting claims
		}
		for i := range c.Claims {
			c.Claims[i].Subject = c.Subject
			if c.Claims[i].Kind == "" {
				c.Claims[i].Kind = c.Kind
			}
		}
		out = append(out, c)
	}
	return out
}

// reconciliationGoal detects the matter's controversies, stores them (graph seed), and
// turns each into an objective for this round — so DyTopo recruits a specialist per
// controversy to write a grounded, debated finding on it.
func (o *Orchestrator) reconciliationGoal(task *types.Task) (types.RoundGoal, error) {
	base := types.RoundGoal{ID: uuid.New().String(), Round: task.CurrentRound, Phase: types.PhaseReconciliation}
	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &tier, TaskType: routing.TaskSynthesis})
	prov, err := o.provReg.Get(model)
	if err != nil {
		base.Description = "Reconcile the matter: confirm that key figures, dates, and claims are consistent across all source documents; flag any discrepancy."
		return base, nil
	}
	cons := o.detectControversies(task, prov, routing.ResolveModelID(model))
	o.update(task, func(t *types.Task) { t.Controversies = cons })
	if len(cons) == 0 {
		base.Description = "No cross-document controversies were detected; confirm the consistency of key figures, dates, and claims across sources and note any that warrant a closer look."
		return base, nil
	}
	slog.Info("reconciliation: controversies detected", "task", task.ID, "n", len(cons))
	var b strings.Builder
	b.WriteString("Resolve these cross-document CONTROVERSIES. For EACH: determine which source governs and why, assess the significance, and state the strategic/defence implication — writing a grounded finding that cites BOTH conflicting sources verbatim.\n")
	for i, c := range cons {
		var vs []string
		for _, cl := range c.Claims {
			vs = append(vs, fmt.Sprintf("%q (%s)", cl.Value, cl.Source))
		}
		fmt.Fprintf(&b, "%d. %s: %s", i+1, c.Subject, strings.Join(vs, " vs "))
		if c.Significance != "" {
			b.WriteString(" — " + c.Significance)
		}
		b.WriteString("\n")
	}
	base.Description = b.String()
	base.ExpectedOutputs = []string{"A grounded finding per controversy, citing both sources", "Which value governs and why", "The strategic or defence implication"}
	return base, nil
}

func (o *Orchestrator) generateRoundGoal(task *types.Task, phase types.TaskPhase) (types.RoundGoal, error) {
	// The reconciliation phase has a bespoke goal: the cross-document controversies the
	// reconciliation analyst surfaces become this round's objectives, so DyTopo recruits
	// a specialist per controversy to write a grounded finding on each (full debate/
	// verify, like any round). Controversy-driven recruitment.
	if phase == types.PhaseReconciliation {
		return o.reconciliationGoal(task)
	}

	safeDesc := adapters.SanitizePromptContent(task.Description)
	priorPhases := make([]string, 0, len(task.Rounds))
	for _, r := range task.Rounds {
		priorPhases = append(priorPhases, string(r.Goal.Phase))
	}
	prompt := fmt.Sprintf(`TASK: %s

WORKFLOW: %s
CURRENT PHASE: %s
PRIOR PHASES COMPLETED: %s
FINDINGS SO FAR: %d

Generate a specific, actionable round goal for the %s phase.
Format:
DESCRIPTION: <one paragraph describing what agents should do this round>
EXPECTED_OUTPUT_1: <first expected output>
EXPECTED_OUTPUT_2: <second expected output>
EXPECTED_OUTPUT_3: <third expected output>`,
		safeDesc, task.WorkflowType, phase,
		strings.Join(priorPhases, ", "), len(task.Findings), phase)

	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{
		Tier:     &tier,
		TaskType: routing.TaskSynthesis,
	})
	prov, err := o.provReg.Get(model)
	if err != nil {
		return types.RoundGoal{}, err
	}
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(model),
		MaxTokens:   600,
		System:      o.rootAgentDef.SystemPrompt,
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
	})
	if err != nil {
		// Fall back to a basic goal.
		return types.RoundGoal{
			ID:          uuid.New().String(),
			Round:       task.CurrentRound,
			Phase:       phase,
			Description: fmt.Sprintf("Execute the %s phase for: %s", phase, strutil.Truncate(safeDesc, 200)),
		}, nil
	}
	o.recordCost(resp, routing.ResolveModelID(model), cost.ContextRoundGoal, task.ID)

	text := ""
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
			break
		}
	}

	description := fmt.Sprintf("Execute the %s phase", phase)
	if m := regexpFindSubmatch(`(?i)DESCRIPTION:\s*([\s\S]+?)(?:EXPECTED_OUTPUT|$)`, text); len(m) > 1 {
		description = strings.TrimSpace(m[1])
	}
	expectedOutputs := []string{}
	for _, m := range regexpFindAllSubmatch(`(?i)EXPECTED_OUTPUT_\d+:\s*(.+)`, text, 5) {
		if len(m) > 1 {
			expectedOutputs = append(expectedOutputs, strings.TrimSpace(m[1]))
		}
	}

	return types.RoundGoal{
		ID:              uuid.New().String(),
		Round:           task.CurrentRound,
		Phase:           phase,
		Description:     description,
		ExpectedOutputs: expectedOutputs,
	}, nil
}
