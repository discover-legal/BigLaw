// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package topoflow

import (
	"math"
	"strings"
)

// bucket implements β(x) = floor(clip(x,0,1) * bins), clamped to [0, bins-1].
func bucket(x float64, bins int) int {
	if x < 0 {
		x = 0
	} else if x > 1 {
		x = 1
	}
	b := int(math.Floor(x * float64(bins)))
	if b >= bins {
		b = bins - 1
	}
	if b < 0 {
		b = 0
	}
	return b
}

// detectRegime is a deterministic, rule-based regime detector over typed
// features [AF]: contradiction risk, ambiguity/uncertainty, evidence
// availability, verification need, risk class.
func detectRegime(ctx TaskContext, h *HandoffState, b BeliefVector) Regime {
	hasProgress := false
	for _, v := range h.Mask() {
		if v == 1 {
			hasProgress = true
			break
		}
	}
	domain := strings.ToLower(ctx.Domain)
	risk := strings.ToUpper(ctx.ScenarioClass)

	switch {
	case b.Contradiction >= 0.5:
		return RegimeContradictory
	case risk == "C7" || risk == "C8" || (domain == "advisory" && risk == "C5"):
		return RegimeHighRisk
	case !hasProgress && b.Uncertainty >= 0.9:
		return RegimeExploratory
	case b.Uncertainty >= 0.66 && b.Evidence < 0.34:
		return RegimeAmbiguous
	case domain == "incident" || domain == "advisory" || b.Evidence >= 0.5:
		return RegimeEvidenceHeavy
	default:
		return RegimeStraightforward
	}
}

// Fold implements eq (1): observations -> hashable Signature.
func Fold(ctx TaskContext, h *HandoffState, b BeliefVector, cfg Config) Signature {
	bins := cfg.BeliefBins
	return Signature{
		Regime:         detectRegime(ctx, h, b),
		Mask:           h.Mask(),
		CorrectnessB:   bucket(b.Correctness, bins),
		UncertaintyB:   bucket(b.Uncertainty, bins),
		ContradictionB: bucket(b.Contradiction, bins),
		EvidenceB:      bucket(b.Evidence, bins),
	}
}
