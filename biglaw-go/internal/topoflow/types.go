// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package topoflow is a two-level coordination substrate for LLM multi-agent
// systems: a slow cross-trajectory UCB1 contextual bandit (AgensFlow) selects
// skills, model bindings, skips, and which topology generator to run; a fast
// within-trajectory generator (DyTopo semantic graph induction, or
// linear-with-skip) produces the actual coordination structure.
//
// There is no neural training: everything "learned" is tabular bandit
// statistics; the semantic encoder is frozen.
//
// Implemented as a single Go package (the spec's Python subdir layout would
// create import cycles in Go, since the shared types are used everywhere).
package topoflow

// Regime labels [AF].
type Regime string

const (
	RegimeStraightforward Regime = "straightforward"
	RegimeEvidenceHeavy   Regime = "evidence_heavy"
	RegimeAmbiguous       Regime = "ambiguous"
	RegimeContradictory   Regime = "contradictory"
	RegimeHighRisk        Regime = "high_risk"
	RegimeExploratory     Regime = "exploratory"
)

// HandoffFields is the fixed 7-bit handoff mask order [AF].
var HandoffFields = [7]string{
	"goal", "subproblem", "evidence", "critique",
	"verification", "draft_answer", "merged_answer",
}

// Signature [AF] eq (1) — comparable, so it is usable directly as a map key.
// The handoff mask is a fixed-size array (comparable); topology knobs are
// deliberately NOT in the signature (they live in the action space).
type Signature struct {
	Regime         Regime
	Mask           [7]int
	CorrectnessB   int
	UncertaintyB   int
	ContradictionB int
	EvidenceB      int
	// NOTE: handoff_quality belief is tracked at runtime but EXCLUDED here [AF].
}

// BeliefVector is the continuous, pre-bucketing belief state [AF].
type BeliefVector struct {
	Correctness    float64
	Uncertainty    float64
	Contradiction  float64
	Evidence       float64
	HandoffQuality float64 // inspected, not folded
}

// NewBeliefVector returns the initial belief state (max uncertainty).
func NewBeliefVector() BeliefVector { return BeliefVector{Uncertainty: 1.0} }

// HandoffState is the structured, typed handoff [AF].
type HandoffState struct {
	Fields map[string]any
}

// NewHandoffState returns an empty handoff.
func NewHandoffState() *HandoffState { return &HandoffState{Fields: map[string]any{}} }

// Mask returns the 7-bit presence mask in HandoffFields order.
func (h *HandoffState) Mask() [7]int {
	var m [7]int
	for i, f := range HandoffFields {
		if v, ok := h.Fields[f]; ok && v != nil {
			m[i] = 1
		}
	}
	return m
}

// Get returns a field value or nil.
func (h *HandoffState) Get(key string) any { return h.Fields[key] }

// Set stores a field value.
func (h *HandoffState) Set(key string, value any) { h.Fields[key] = value }

// Clone returns a deep-enough copy (the field map is copied).
func (h *HandoffState) Clone() *HandoffState {
	c := NewHandoffState()
	for k, v := range h.Fields {
		c.Fields[k] = v
	}
	return c
}

// TaskContext is x_t.
type TaskContext struct {
	TaskID        string
	Prompt        string
	ScenarioClass string // e.g. "C3"; for eval grouping only
	Domain        string // "code" | "math" | "incident" | "advisory"
	GroundTruth   any    // tests / answer key, if verifiable
}

// Unset is the sentinel for optional integer action fields (0 is a valid bucket).
const Unset = -1

// Action [AF] + [NEW]. All fields are scalar, so Action is comparable and is used
// directly as part of a policy-graph map key.
type Action struct {
	Kind        string // "invoke" | "skip" | "topology" | "terminate"
	Skill       string // invoke: skill protocol k
	Model       string // invoke: model binding m
	Target      string // skip:X -> X
	TopoMode    string // topology: "linear" | "dytopo"   [NEW]
	TauBucket   int    // topology: index into TauBuckets, else Unset
	KIn         int    // topology: max in-degree, else Unset
	RoundBucket int    // topology: index into RoundBuckets, else Unset
}

// InvokeAction builds an invoke(skill, model) action.
func InvokeAction(skill, model string) Action {
	return Action{Kind: "invoke", Skill: skill, Model: model, TauBucket: Unset, KIn: Unset, RoundBucket: Unset}
}

// SkipAction builds a skip:X action.
func SkipAction(target string) Action {
	return Action{Kind: "skip", Target: target, TauBucket: Unset, KIn: Unset, RoundBucket: Unset}
}

// LinearTopoAction builds a topology(linear) action.
func LinearTopoAction() Action {
	return Action{Kind: "topology", TopoMode: "linear", TauBucket: Unset, KIn: Unset, RoundBucket: Unset}
}

// DytopoAction builds a topology(dytopo, tau, k_in, round) action.
func DytopoAction(tau, kIn, round int) Action {
	return Action{Kind: "topology", TopoMode: "dytopo", TauBucket: tau, KIn: kIn, RoundBucket: round}
}

// CellOutput is the result of a single invoke cell (Layer 2 typed I/O).
type CellOutput struct {
	Skill        string
	Model        string
	Tokens       int
	Retries      int
	Failed       bool
	Complete     bool // evaluator sets this to trigger termination
	HandoffDelta map[string]any
	Meta         map[string]any
}
