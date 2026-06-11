// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

// Config holds defaults (spec §12). Defaults are starting points, not sacred.
type Config struct {
	// signature
	BeliefBins int

	// policy graph
	Lam         float64
	UCBc0       float64
	UCBHalfLife int
	UCBFloor    float64

	// reward
	WQ       float64
	WC       float64
	WRho     float64
	TokenCap int

	// variant pool [AF]: 3 skill protocols x 3 models = 9 solver cells
	SkillProtocols []string
	Models         []string
	AuxCells       []string
	OptionalAux    []string
	DefaultSolver  string
	DefaultModel   string

	// topology [NEW]
	TopoModes    []string
	TauBuckets   []float64
	RoundBuckets []int
	DytopoKIn    int
	DytopoTemp   float64
	DytopoMaxTok int
	Encoder      string
	NAgentsCode  int
	NAgentsMath  int

	// reward audit [AF]
	LiveJudge   string
	AuditJudges []string
	RubricAxes  []string
	AxisWeights []float64

	// quality strategy: "auto" | "ground_truth" | "judged"
	QualityStrategy string

	// governance
	MaxMacroSteps int
	MaxRetries    int
}

// DefaultConfig returns the spec §12 defaults.
func DefaultConfig() Config {
	return Config{
		BeliefBins:      4,
		Lam:             0.5,
		UCBc0:           1.4,
		UCBHalfLife:     50,
		UCBFloor:        0.5,
		WQ:              1.0,
		WC:              0.3,
		WRho:            0.15,
		TokenCap:        8000,
		SkillProtocols:  []string{"solver_concise", "solver_cot", "solver_evidence"},
		Models:          []string{"haiku", "fast", "mini"},
		AuxCells:        []string{"planner", "memory", "web_search_exa", "web_search_tavily", "verifier_a", "verifier_b", "evaluator"},
		OptionalAux:     []string{"planner", "memory", "web_search_exa", "web_search_tavily", "verifier_a", "verifier_b"},
		DefaultSolver:   "solver_cot",
		DefaultModel:    "haiku",
		TopoModes:       []string{"linear", "dytopo"},
		TauBuckets:      []float64{0.2, 0.3, 0.4, 0.5},
		RoundBuckets:    []int{3, 6, 10},
		DytopoKIn:       3,
		DytopoTemp:      0.3,
		DytopoMaxTok:    4000,
		Encoder:         "sentence-transformers/all-MiniLM-L6-v2",
		NAgentsCode:     4,
		NAgentsMath:     3,
		LiveJudge:       "claude-haiku-4-5",
		AuditJudges:     []string{"claude-haiku-4-5", "gpt-5-mini", "qwen-flash"},
		RubricAxes:      []string{"goal_achievement", "grounding", "coordination", "recovery"},
		QualityStrategy: "auto",
		MaxMacroSteps:   24,
		MaxRetries:      6,
	}
}

// DefaultModelFor returns the default model binding for an aux cell.
func (c Config) DefaultModelFor(cell string) string { return c.DefaultModel }

// VariantPool returns the (skill, model) invoke variants: 9 solvers + aux cells.
func (c Config) VariantPool() [][2]string {
	var out [][2]string
	for _, k := range c.SkillProtocols {
		for _, m := range c.Models {
			out = append(out, [2]string{k, m})
		}
	}
	for _, cell := range c.AuxCells {
		out = append(out, [2]string{cell, c.DefaultModelFor(cell)})
	}
	return out
}

// EffectiveAxisWeights returns per-axis weights composing the judge scalar.
func (c Config) EffectiveAxisWeights() map[string]float64 {
	out := map[string]float64{}
	if len(c.AxisWeights) == len(c.RubricAxes) {
		for i, a := range c.RubricAxes {
			out[a] = c.AxisWeights[i]
		}
		return out
	}
	w := 1.0 / float64(len(c.RubricAxes))
	for _, a := range c.RubricAxes {
		out[a] = w
	}
	return out
}

// IsOptionalAux reports whether a cell may be invoked at most once.
func (c Config) IsOptionalAux(skill string) bool {
	for _, a := range c.OptionalAux {
		if a == skill {
			return true
		}
	}
	return false
}
