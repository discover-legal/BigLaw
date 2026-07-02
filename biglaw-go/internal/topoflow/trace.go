// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package topoflow

// decision is one macro decision-path entry (for backup).
type decision struct {
	Sig Signature
	Act Action
}

// Trace is the two-level, JSON-serializable record (spec §11).
type Trace struct {
	TaskID      string           `json:"taskId"`
	Events      []map[string]any `json:"events"`
	Tokens      int              `json:"tokens"`
	Retries     int              `json:"retries"`
	AnyFailure  bool             `json:"anyFailure"`
	FinalAnswer string           `json:"finalAnswer"`
	decisions   []decision
}

// NewTrace creates an empty trace.
func NewTrace(taskID string) *Trace { return &Trace{TaskID: taskID} }

func (t *Trace) add(ev map[string]any) { t.Events = append(t.Events, ev) }
func (t *Trace) recordDecision(s Signature, a Action) {
	t.decisions = append(t.decisions, decision{s, a})
}

// DecisionPath returns the macro decisions in order (for backup).
func (t *Trace) DecisionPath() []decision { return t.decisions }

func invokeEvent(a Action, out CellOutput) map[string]any {
	keys := make([]string, 0, len(out.HandoffDelta))
	for k := range out.HandoffDelta {
		keys = append(keys, k)
	}
	return map[string]any{
		"type": "invoke", "skill": a.Skill, "model": a.Model,
		"tokens": out.Tokens, "complete": out.Complete, "failed": out.Failed,
		"deltaFields": keys,
	}
}

func skipEvent(a Action) map[string]any {
	return map[string]any{"type": "skip", "target": a.Target}
}

func topologyEvent(a Action, res TopologyResult) map[string]any {
	return map[string]any{
		"type": "topology", "mode": a.TopoMode,
		"tauBucket": a.TauBucket, "kIn": a.KIn, "roundBucket": a.RoundBucket,
		"rounds_run": res.RoundsRun, "total_tokens": res.TotalTokens,
		"retries": res.Retries, "failed": res.Failed, "subtrace": res.Subtrace,
	}
}

// RunReport is the per-task artifact (spec §11), emitted even on governance abort.
type RunReport struct {
	TaskID       string           `json:"taskId"`
	Reward       float64          `json:"reward"`
	Quality      float64          `json:"quality"`
	SubScores    map[string]any   `json:"subScores"`
	Tokens       int              `json:"tokens"`
	Retries      int              `json:"retries"`
	DecisionPath []map[string]any `json:"decisionPath"`
	Aborted      bool             `json:"aborted"`
	AbortReason  string           `json:"abortReason,omitempty"`
	Trace        *Trace           `json:"trace"`
}

func buildRunReport(t *Trace, reward, quality float64, sub map[string]any, aborted bool, reason string) *RunReport {
	dp := make([]map[string]any, 0, len(t.decisions))
	for _, d := range t.decisions {
		dp = append(dp, map[string]any{"signature": sigLabel(d.Sig), "action": d.Act})
	}
	return &RunReport{
		TaskID: t.TaskID, Reward: reward, Quality: quality, SubScores: sub,
		Tokens: t.Tokens, Retries: t.Retries, DecisionPath: dp,
		Aborted: aborted, AbortReason: reason, Trace: t,
	}
}
