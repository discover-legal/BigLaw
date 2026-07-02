// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package topoflow

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// ── datasets (spec §14) ─────────────────────────────────────────────────────

// MockSuite is a tiny mixed offline suite: verifiable math + judged
// incident/advisory across scenario classes C1–C8.
func MockSuite() []TaskContext {
	return []TaskContext{
		{TaskID: "math-1", Prompt: "Compute 2+2.", ScenarioClass: "C1", Domain: "math", GroundTruth: "4"},
		{TaskID: "math-2", Prompt: "Compute 6/3 + 2.", ScenarioClass: "C6", Domain: "math", GroundTruth: "4"},
		{TaskID: "inc-1", Prompt: "Triage a cascading service outage.", ScenarioClass: "C3", Domain: "incident"},
		{TaskID: "inc-2", Prompt: "Procedural restart runbook for node X.", ScenarioClass: "C1", Domain: "incident"},
		{TaskID: "adv-1", Prompt: "Advise on conflicting CVE remediations.", ScenarioClass: "C7", Domain: "advisory"},
		{TaskID: "adv-2", Prompt: "Cross-team security exception review.", ScenarioClass: "C8", Domain: "advisory"},
		{TaskID: "adv-3", Prompt: "Routine dependency bump advisory.", ScenarioClass: "C6", Domain: "advisory"},
		{TaskID: "inc-3", Prompt: "Ambiguous multi-cause latency incident.", ScenarioClass: "C3", Domain: "incident"},
	}
}

// HarnessResponder is a deterministic MockTransport responder for the suite.
func HarnessResponder(req LLMRequest) map[string]any {
	if req.Purpose == "judge" {
		base := 0.6
		switch req.Model {
		case "claude-haiku-4-5":
			base = 0.7
		case "gpt-5-mini":
			base = 0.6
		case "qwen-flash":
			base = 0.5
		}
		cands, _ := req.Meta["candidates"].([]map[string]any)
		arr := make([]any, 0, len(cands))
		for _, c := range cands {
			arr = append(arr, map[string]any{"id": c["id"],
				"goal_achievement": base, "grounding": base, "coordination": base, "recovery": base})
		}
		return map[string]any{"scores": arr}
	}
	role := strings.ToLower(req.Role)
	out := map[string]any{"_tokens": 40.0}
	if req.Purpose == "agent" {
		out["public"] = req.Role
		out["private"] = req.Role
		out["q_desc"] = "need inputs"
		out["k_desc"] = "provide expertise"
		if strings.Contains(role, "develop") || strings.Contains(role, "solver") {
			out["draft_answer"] = "4"
		}
		if strings.Contains(role, "test") || strings.Contains(role, "verif") {
			out["verification"] = "supported"
		}
		return out
	}
	switch {
	case strings.HasPrefix(role, "solver"):
		out["draft_answer"] = "4"
	case role == "evaluator":
		out["complete"] = true
	case strings.HasPrefix(role, "verifier"):
		out["verification"] = "supported"
	case role == "memory" || strings.HasPrefix(role, "web_search"):
		out["evidence"] = []any{"fact"}
	case role == "planner":
		out["goal"] = "g"
		out["subproblem"] = "s"
	}
	return out
}

// ── fixed-policy selectors (non-learning arms) ──────────────────────────────
func fixedLinearSelector() func(Signature, []Action) Action {
	i := 0
	return func(sig Signature, legal []Action) Action {
		i++
		if i == 1 {
			for _, a := range legal {
				if a.Kind == "topology" && a.TopoMode == "linear" {
					return a
				}
			}
		}
		return evalOrFirst(legal)
	}
}

func fixedDytopoSelector() func(Signature, []Action) Action {
	i := 0
	return func(sig Signature, legal []Action) Action {
		i++
		if i == 1 {
			for _, a := range legal {
				if a.Kind == "topology" && a.TopoMode == "dytopo" && a.TauBucket == 1 && a.RoundBucket == 0 {
					return a
				}
			}
		}
		return evalOrFirst(legal)
	}
}

func evalOrFirst(legal []Action) Action {
	for _, a := range legal {
		if a.Kind == "invoke" && a.Skill == "evaluator" {
			return a
		}
	}
	return legal[0]
}

// ── arms ────────────────────────────────────────────────────────────────────
type arm struct {
	name      string
	topoModes []string
	learning  bool
	selector  func() func(Signature, []Action) Action
	warmFrom  string
	skipOff   bool // SkipEnabled=false: the paper's no-skip ablation arm (§6.2)
}

func arms() []arm {
	return []arm{
		{"1_fixed_linear", []string{"linear"}, false, fixedLinearSelector, "", false},
		{"2_pure_dytopo", []string{"dytopo"}, false, fixedDytopoSelector, "", false},
		{"3_pure_agensflow", []string{"linear"}, true, nil, "", false},
		{"4_topoflow_linear", []string{"linear"}, true, nil, "", false},
		{"5_topoflow_dytopo", []string{"dytopo"}, true, nil, "", false},
		{"6_topoflow_free_cold", []string{"linear", "dytopo"}, true, nil, "", false},
		{"7_topoflow_free_warm", []string{"linear", "dytopo"}, true, nil, "6_topoflow_free_cold", false},
		// AgensFlow no-skip ablation: same as arm 3 (linear, learning, skip-on)
		// but with skip:X forced off, isolating topology compression (§6.2).
		{"8_no_skip_ablation", []string{"linear"}, true, nil, "", true},
	}
}

// TrajRecord is a per-task record in an arm result.
type TrajRecord struct {
	TaskID        string   `json:"taskId"`
	ScenarioClass string   `json:"scenarioClass"`
	Domain        string   `json:"domain"`
	Quality       float64  `json:"quality"`
	AuditQuality  float64  `json:"auditQuality"`
	Reward        float64  `json:"reward"`
	Tokens        int      `json:"tokens"`
	TopologyModes []string `json:"topologyModes"`
}

// ArmResult aggregates one arm's run.
type ArmResult struct {
	Arm              string       `json:"arm"`
	Trajectories     []TrajRecord `json:"trajectories"`
	MeanReward       float64      `json:"meanReward"`
	MeanQuality      float64      `json:"meanQuality"`
	AuditMeanQuality float64      `json:"auditMeanQuality"`
	MeanTokens       float64      `json:"meanTokens"`
	TokensPerEpoch   []int        `json:"tokensPerEpoch"`
	graph            *PolicyGraph
}

func topologyModes(traj *Trajectory) []string {
	var out []string
	for _, d := range traj.Trace.DecisionPath() {
		if d.Act.Kind == "topology" {
			out = append(out, d.Act.TopoMode)
		}
	}
	return out
}

func runArm(a arm, tasks []TaskContext, base Config, opts SuiteOptions, liveRJ *RelativeJudge, warm *PolicyGraph) ArmResult {
	cfg := base
	cfg.TopoModes = a.topoModes
	cfg.SkipEnabled = base.SkipEnabled && !a.skipOff
	var g *PolicyGraph
	if warm != nil {
		g = warm.Clone()
	} else {
		g = NewPolicyGraph(cfg)
	}
	emb := opts.Embedder
	if emb == nil {
		emb = NewMockEmbedder()
	}
	nEpochs := 1
	if a.learning {
		nEpochs = opts.Epochs
		if nEpochs <= 0 {
			nEpochs = 1
		}
	}
	var tokensPerEpoch []int
	var final []*Trajectory
	var finalTasks []TaskContext

	for e := 0; e < nEpochs; e++ {
		epochTokens := 0
		var snap []*Trajectory
		var snapTasks []TaskContext
		for _, task := range tasks {
			var q QualitySignal
			if task.GroundTruth != nil {
				q = GroundTruthQ{Runner: opts.CodeRunner}
			} else {
				q = JudgedQ{Judge: liveRJ}
			}
			var sel func(Signature, []Action) Action
			if a.selector != nil {
				sel = a.selector()
			}
			traj, err := RunTask(task, cfg, g, RunOptions{
				Transport: opts.Transport, Embedder: emb, Quality: q,
				SearchProvider: opts.SearchProvider, SelectFn: sel,
			})
			if err != nil || traj == nil {
				// Real transports can fail (rate limit, bad key); record a
				// zero-quality failed trajectory rather than panic.
				traj = &Trajectory{Trace: NewTrace(task.TaskID)}
			}
			epochTokens += traj.Trace.Tokens
			snap = append(snap, traj)
			snapTasks = append(snapTasks, task)
		}
		tokensPerEpoch = append(tokensPerEpoch, epochTokens)
		final, finalTasks = snap, snapTasks
	}

	res := ArmResult{Arm: a.name, TokensPerEpoch: tokensPerEpoch, graph: g}
	var sumR, sumQ, sumA, sumT float64
	for i, traj := range final {
		task := finalTasks[i]
		auditQ := traj.Quality
		if task.GroundTruth == nil {
			auditQ = AuditRescore(task, []*Trace{traj.Trace}, opts.Transport, cfg)[0].Q
		}
		res.Trajectories = append(res.Trajectories, TrajRecord{
			TaskID: task.TaskID, ScenarioClass: task.ScenarioClass, Domain: task.Domain,
			Quality: traj.Quality, AuditQuality: auditQ, Reward: traj.Reward,
			Tokens: traj.Trace.Tokens, TopologyModes: topologyModes(traj),
		})
		sumR += traj.Reward
		sumQ += traj.Quality
		sumA += auditQ
		sumT += float64(traj.Trace.Tokens)
	}
	n := float64(len(final))
	if n > 0 {
		res.MeanReward, res.MeanQuality, res.AuditMeanQuality, res.MeanTokens = sumR/n, sumQ/n, sumA/n, sumT/n
	}
	return res
}

// SuiteReport is the harness output.
type SuiteReport struct {
	Arms    map[string]ArmResult `json:"arms"`
	Metrics map[string]any       `json:"metrics"`
	NTasks  int                  `json:"nTasks"`
	Epochs  int                  `json:"epochs"`
}

// SuiteOptions wires the (real, by default) components into the harness. Leave a
// field nil to fall back to an offline test double: Transport -> MockTransport,
// Embedder -> MockEmbedder, CodeRunner -> nil (code Q returns 0), Tasks ->
// MockSuite.
type SuiteOptions struct {
	Transport      Transport
	Embedder       Embedder
	CodeRunner     CodeRunner
	SearchProvider SearchProvider
	Tasks          []TaskContext
	Epochs         int
	OutPath        string
}

// RunSuite runs all 8 arms and computes metrics H1–H6 (spec §14).
func RunSuite(cfg Config, opts SuiteOptions) (*SuiteReport, error) {
	if opts.Transport == nil {
		opts.Transport = &MockTransport{Responder: HarnessResponder}
	}
	tasks := opts.Tasks
	if tasks == nil {
		tasks = MockSuite()
	}
	if opts.Epochs <= 0 {
		opts.Epochs = 1
	}
	liveRJ := NewRelativeJudge(opts.Transport, []string{cfg.LiveJudge}, cfg)

	results := map[string]ArmResult{}
	for _, a := range arms() {
		var warm *PolicyGraph
		if a.warmFrom != "" {
			if prev, ok := results[a.warmFrom]; ok {
				warm = prev.graph
			}
		}
		results[a.name] = runArm(a, tasks, cfg, opts, liveRJ, warm)
	}

	report := &SuiteReport{
		Arms:    results,
		Metrics: computeMetrics(results),
		NTasks:  len(tasks),
		Epochs:  opts.Epochs,
	}
	if opts.OutPath != "" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(opts.OutPath, data, 0o644); err != nil {
			return nil, err
		}
	}
	return report, nil
}

// ── metrics H1–H6 ───────────────────────────────────────────────────────────
func computeMetrics(results map[string]ArmResult) map[string]any {
	free := results["6_topoflow_free_cold"]
	h1 := h1Selection(free)
	return map[string]any{
		"H1_selection":        h1,
		"H1_separates":        h1Separates(h1),
		"H2_frontier":         h2Frontier(results),
		"H3_learned_vs_swept": h3(results),
		"H4_cold_start":       h4(results),
		"H5_reward_fragility": h5(results),
		"H6_skip_ablation":    h6(results),
	}
}

// h6 reproduces the paper's no-skip ablation comparison (§6.2): skip-on
// learning (arm 3) vs the same arm with skip:X forced off (arm 8). The paper's
// claim is that skip:X isolates topology compression, so the skip-on arm should
// spend no more tokens at matched-or-better quality. skip_compresses reports
// whether skip-on reached its operating point at <= the no-skip token cost.
func h6(results map[string]ArmResult) map[string]any {
	on, okOn := results["3_pure_agensflow"]
	off, okOff := results["8_no_skip_ablation"]
	if !okOn || !okOff {
		return map[string]any{}
	}
	return map[string]any{
		"skip_on_quality":  on.MeanQuality,
		"skip_off_quality": off.MeanQuality,
		"skip_on_tokens":   on.MeanTokens,
		"skip_off_tokens":  off.MeanTokens,
		"quality_delta":    on.MeanQuality - off.MeanQuality,
		"token_delta":      on.MeanTokens - off.MeanTokens,
		"skip_compresses":  on.MeanTokens <= off.MeanTokens+1e-9,
	}
}

func h1Selection(a ArmResult) map[string]map[string]any {
	counts := map[string]map[string]int{}
	for _, tr := range a.Trajectories {
		sc := tr.ScenarioClass
		if sc == "" {
			sc = "?"
		}
		if counts[sc] == nil {
			counts[sc] = map[string]int{}
		}
		for _, m := range tr.TopologyModes {
			counts[sc][m]++
		}
	}
	out := map[string]map[string]any{}
	for sc, modes := range counts {
		total := 0
		for _, c := range modes {
			total += c
		}
		row := map[string]any{"_n": total}
		for m, c := range modes {
			if total > 0 {
				row[m] = float64(c) / float64(total)
			}
		}
		out[sc] = row
	}
	return out
}

func h1Separates(table map[string]map[string]any) bool {
	lean := func(sc, mode string) bool {
		if row, ok := table[sc]; ok {
			if v, ok := row[mode].(float64); ok {
				return v >= 0.5
			}
		}
		return false
	}
	coord := lean("C3", "dytopo") || lean("C7", "dytopo") || lean("C8", "dytopo")
	proc := lean("C1", "linear") || lean("C6", "linear")
	return coord && proc
}

func h2Frontier(results map[string]ArmResult) map[string]any {
	out := map[string]any{}
	for arm, res := range results {
		per := map[string][]TrajRecord{}
		for _, tr := range res.Trajectories {
			sc := tr.ScenarioClass
			per[sc] = append(per[sc], tr)
		}
		armOut := map[string]any{}
		for sc, recs := range per {
			var q, tok float64
			for _, r := range recs {
				q += r.Quality
				tok += float64(r.Tokens)
			}
			armOut[sc] = map[string]any{"quality": q / float64(len(recs)), "tokens": tok / float64(len(recs))}
		}
		out[arm] = armOut
	}
	return out
}

func h3(results map[string]ArmResult) map[string]any {
	a5, ok5 := results["5_topoflow_dytopo"]
	a2, ok2 := results["2_pure_dytopo"]
	if !ok5 || !ok2 {
		return map[string]any{}
	}
	return map[string]any{
		"arm5_quality":     a5.MeanQuality,
		"arm2_quality":     a2.MeanQuality,
		"arm5_tokens":      a5.MeanTokens,
		"arm2_tokens":      a2.MeanTokens,
		"learned_ge_swept": a5.MeanQuality+1e-9 >= a2.MeanQuality,
	}
}

func h4(results map[string]ArmResult) map[string]any {
	out := map[string]any{}
	if a6, ok := results["6_topoflow_free_cold"]; ok {
		out["arm6_tokens_per_epoch"] = a6.TokensPerEpoch
	}
	if a3, ok := results["3_pure_agensflow"]; ok {
		out["arm3_tokens_per_epoch"] = a3.TokensPerEpoch
	}
	return out
}

func h5(results map[string]ArmResult) map[string]any {
	out := map[string]any{}
	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		res := results[n]
		out[n] = map[string]any{
			"live":      res.MeanQuality,
			"audit":     res.AuditMeanQuality,
			"delta":     res.AuditMeanQuality - res.MeanQuality,
			"sign_flip": (res.MeanQuality-0.5)*(res.AuditMeanQuality-0.5) < 0,
		}
	}
	return out
}
