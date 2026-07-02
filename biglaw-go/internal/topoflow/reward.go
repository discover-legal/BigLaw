// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package topoflow

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// hybridReward implements eq (6): r = w_q*Q - w_c*(tokens/cap) - w_rho*retries.
// tokens/retries aggregate over the ENTIRE trajectory incl. all inner DyTopo
// rounds [NEW] — the guardrail that stops the policy treating DyTopo as free.
func hybridReward(quality float64, tokens, retries int, cfg Config) float64 {
	return cfg.WQ*quality - cfg.WC*(float64(tokens)/float64(cfg.TokenCap)) - cfg.WRho*float64(retries)
}

// QualitySignal is the pluggable Q (spec §7). Score returns (Q in [0,1], sub
// scores incl. "confidence").
type QualitySignal interface {
	Score(ctx TaskContext, traj *Trace, peers []*Trace) (float64, map[string]any)
}

// CodeRunner runs candidate code against ground-truth tests (live path; a fake is
// used in tests). Returns the fraction of tests passed.
type CodeRunner interface {
	Run(candidate string, gt map[string]any) float64
}

// GroundTruthQ scores verifiable domains: code via a CodeRunner, math via
// exact-match. Confidence is always 1.0.
type GroundTruthQ struct {
	Runner CodeRunner
}

var mathNum = regexp.MustCompile(`-?\d+(?:/\d+|\.\d+)?`)

func normMath(s string) string {
	if i := strings.Index(s, "####"); i >= 0 {
		s = s[i+4:]
	}
	s = strings.NewReplacer("$", "", ",", "", " ", "").Replace(strings.TrimSpace(s))
	s = strings.TrimRight(s, ".")
	if m := mathNum.FindString(s); m != "" {
		return m
	}
	return strings.ToLower(s)
}

// Score implements QualitySignal.
func (q GroundTruthQ) Score(ctx TaskContext, traj *Trace, peers []*Trace) (float64, map[string]any) {
	ans := traj.FinalAnswer
	if strings.ToLower(ctx.Domain) == "code" {
		if gt, ok := ctx.GroundTruth.(map[string]any); ok && q.Runner != nil {
			f := q.Runner.Run(ans, gt)
			return f, map[string]any{"confidence": 1.0, "kind": "ground_truth_code", "tests_fraction": f}
		}
		return 0, map[string]any{"confidence": 1.0, "kind": "ground_truth_code"}
	}
	want := fmt.Sprintf("%v", ctx.GroundTruth)
	ok := 0.0
	if normMath(ans) == normMath(want) {
		ok = 1.0
	}
	return ok, map[string]any{"confidence": 1.0, "kind": "ground_truth_match", "exact": ok}
}

// JudgedQ wraps a RelativeJudge; without one it falls back to a transparent
// heuristic so the loop is runnable before the judge is wired.
type JudgedQ struct {
	Judge *RelativeJudge
}

// Score implements QualitySignal.
func (q JudgedQ) Score(ctx TaskContext, traj *Trace, peers []*Trace) (float64, map[string]any) {
	if q.Judge != nil {
		return q.Judge.ScoreOne(ctx, traj, peers)
	}
	score := 0.0
	if traj.FinalAnswer != "" {
		score = 0.6
	}
	return score, map[string]any{"confidence": 0.4, "kind": "judged_heuristic"}
}

// MakeQuality selects the Q strategy (spec §7).
func MakeQuality(ctx TaskContext, cfg Config, judge *RelativeJudge, runner CodeRunner) QualitySignal {
	switch cfg.QualityStrategy {
	case "ground_truth":
		return GroundTruthQ{Runner: runner}
	case "judged":
		return JudgedQ{Judge: judge}
	}
	if ctx.GroundTruth != nil {
		return GroundTruthQ{Runner: runner}
	}
	return JudgedQ{Judge: judge}
}

// ── RelativeJudge ───────────────────────────────────────────────────────────

const rubric = "Rank the candidate trajectories RELATIVE to each other for the same task. " +
	"Score each on goal_achievement, grounding, coordination, recovery in [0,1]. " +
	`Return JSON {"scores":[{"id":int,<axis>:float,...},...]}.`

// RelativeJudge ranks peer trajectories side-by-side; cross-judge averaging
// surfaces disagreement std as confidence (spec §7) [AF].
type RelativeJudge struct {
	tx      Transport
	models  []string
	cfg     Config
	axes    []string
	weights map[string]float64
}

// NewRelativeJudge builds a judge over the given judge models.
func NewRelativeJudge(tx Transport, models []string, cfg Config) *RelativeJudge {
	return &RelativeJudge{tx: tx, models: models, cfg: cfg, axes: cfg.RubricAxes, weights: cfg.EffectiveAxisWeights()}
}

func (j *RelativeJudge) indexScores(raw any, n int) []map[string]float64 {
	byID := map[int]map[string]float64{}
	if list, ok := raw.([]any); ok {
		for _, row := range list {
			r, ok := row.(map[string]any)
			if !ok {
				continue
			}
			idF, ok := asFloat(r["id"])
			if !ok {
				continue
			}
			m := map[string]float64{}
			for _, ax := range j.axes {
				v, _ := asFloat(r[ax])
				m[ax] = clamp01(v)
			}
			byID[int(idF)] = m
		}
	}
	out := make([]map[string]float64, n)
	for i := 0; i < n; i++ {
		if m, ok := byID[i]; ok {
			out[i] = m
		} else {
			d := map[string]float64{}
			for _, ax := range j.axes {
				d[ax] = 0.5
			}
			out[i] = d
		}
	}
	return out
}

type judgeScore struct {
	Q   float64
	Sub map[string]any
}

// ScoreGroup ranks a group of peer trajectories.
func (j *RelativeJudge) ScoreGroup(ctx TaskContext, group []*Trace) []judgeScore {
	cands := make([]map[string]any, len(group))
	for i, t := range group {
		ans := t.FinalAnswer
		if len(ans) > 2000 {
			ans = strutil.Truncate(ans, 2000)
		}
		cands[i] = map[string]any{"id": i, "answer": ans}
	}
	perJudge := make([][]map[string]float64, 0, len(j.models))
	for _, jm := range j.models {
		fields, _, err := j.tx.Complete(LLMRequest{
			Model: jm, System: rubric,
			User:   fmt.Sprintf("Task: %s\nCandidates: %v", ctx.Prompt, cands),
			Schema: []string{"scores"}, Purpose: "judge",
			Meta: map[string]any{"candidates": cands},
		})
		if err != nil {
			fields = map[string]any{}
		}
		perJudge = append(perJudge, j.indexScores(fields["scores"], len(group)))
	}

	results := make([]judgeScore, len(group))
	for i := range group {
		axisMeans := map[string]float64{}
		for _, ax := range j.axes {
			var sum float64
			for jj := range perJudge {
				sum += perJudge[jj][i][ax]
			}
			axisMeans[ax] = sum / float64(len(perJudge))
		}
		scalars := make([]float64, len(perJudge))
		for jj := range perJudge {
			var s float64
			for _, ax := range j.axes {
				s += j.weights[ax] * perJudge[jj][i][ax]
			}
			scalars[jj] = s
		}
		var q float64
		for _, ax := range j.axes {
			q += j.weights[ax] * axisMeans[ax]
		}
		std := pstdev(scalars)
		results[i] = judgeScore{
			Q: q,
			Sub: map[string]any{
				"confidence": clamp01(1.0 - std), "axes": axisMeans,
				"per_judge_scalars": scalars, "disagreement_std": std,
				"n_judges": len(perJudge), "kind": "relative_judge",
			},
		}
	}
	return results
}

// ScoreOne scores a single trajectory within its peer group.
func (j *RelativeJudge) ScoreOne(ctx TaskContext, traj *Trace, peers []*Trace) (float64, map[string]any) {
	group := append([]*Trace{traj}, peers...)
	r := j.ScoreGroup(ctx, group)
	return r[0].Q, r[0].Sub
}

// AuditRescore is the post-hoc cross-family three-judge audit (metric H5).
func AuditRescore(ctx TaskContext, group []*Trace, tx Transport, cfg Config) []judgeScore {
	return NewRelativeJudge(tx, cfg.AuditJudges, cfg).ScoreGroup(ctx, group)
}

func pstdev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	var v float64
	for _, x := range xs {
		v += (x - mean) * (x - mean)
	}
	v /= float64(len(xs))
	return math.Sqrt(v)
}
