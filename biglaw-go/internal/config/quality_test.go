// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// The BIGLAW_QUALITY preset sets every booster's default; each booster's own
// env var wins over the preset; "balanced" reproduces the historical
// defaults exactly.

package config

import "testing"

func TestQualityPresetBalancedIsHistoricalDefault(t *testing.T) {
	c := Load()
	if c.Quality.Preset != "balanced" {
		t.Fatalf("default preset = %q, want balanced", c.Quality.Preset)
	}
	for name, got := range map[string]bool{
		"RoundGoals":       c.Quality.RoundGoals,
		"MemoryDigest":     c.Quality.MemoryDigest,
		"Descriptors":      c.Quality.Descriptors,
		"StagedExtraction": c.Quality.StagedExtraction,
		"EvidenceGraph":    c.Quality.EvidenceGraph,
		"SpineExtraction":  c.Quality.SpineExtraction,
		"Figures":          c.Quality.Figures,
		"CrossDoc":         c.Quality.CrossDoc,
		"Deviations":       c.Quality.Deviations,
		"Specialists":      c.Quality.Specialists,
		"SpecificsSweep":   c.Quality.SpecificsSweep,
		"WriterMultipass":  c.Quality.WriterMultipass,
		"RAGEnrichment":    c.Quality.RAGEnrichment,
		"Debate":           c.Debate.AdversarialEnabled,
		"GateNotes":        c.ClientVoice.GateNotes,
		"Reentry":          c.ReentrantMachinery,
	} {
		if !got {
			t.Errorf("balanced: %s should default on", name)
		}
	}
	if c.Debate.VerificationPasses != 10 {
		t.Errorf("balanced: verification passes = %d, want 10", c.Debate.VerificationPasses)
	}
	if c.Drafting.DyTopo {
		t.Error("balanced: DyTopo drafting should default off")
	}
}

func TestQualityPresetFastTurnsBoostersOff(t *testing.T) {
	t.Setenv("BIGLAW_QUALITY", "fast")
	c := Load()
	if c.Quality.Preset != "fast" {
		t.Fatalf("preset = %q, want fast", c.Quality.Preset)
	}
	for name, got := range map[string]bool{
		"RoundGoals":       c.Quality.RoundGoals,
		"MemoryDigest":     c.Quality.MemoryDigest,
		"StagedExtraction": c.Quality.StagedExtraction,
		"EvidenceGraph":    c.Quality.EvidenceGraph,
		"SpineExtraction":  c.Quality.SpineExtraction,
		"Figures":          c.Quality.Figures,
		"CrossDoc":         c.Quality.CrossDoc,
		"Deviations":       c.Quality.Deviations,
		"Specialists":      c.Quality.Specialists,
		"SpecificsSweep":   c.Quality.SpecificsSweep,
		"WriterMultipass":  c.Quality.WriterMultipass,
		"RAGEnrichment":    c.Quality.RAGEnrichment,
		"Debate":           c.Debate.AdversarialEnabled,
		"GateNotes":        c.ClientVoice.GateNotes,
		"Reentry":          c.ReentrantMachinery,
		"DyTopoDrafting":   c.Drafting.DyTopo,
	} {
		if got {
			t.Errorf("fast: %s should default off", name)
		}
	}
	if c.Debate.VerificationPasses != 0 {
		t.Errorf("fast: verification passes = %d, want 0", c.Debate.VerificationPasses)
	}
	// Descriptors stay on even in fast: tiny, parallel, and DyTopo routing
	// quality depends on them.
	if !c.Quality.Descriptors {
		t.Error("fast: descriptors should stay on")
	}
	// The citation gate is free (no model calls) and is not preset-gated.
	if !c.Debate.CitationRequired {
		t.Error("fast: citation gate should stay on")
	}
}

func TestQualityPresetMaxEnablesDrafting(t *testing.T) {
	t.Setenv("BIGLAW_QUALITY", "max")
	c := Load()
	if !c.Drafting.DyTopo {
		t.Error("max: DyTopo drafting should default on")
	}
	if !c.Quality.StagedExtraction || c.Debate.VerificationPasses != 10 {
		t.Error("max: boosters should be on")
	}
}

func TestExplicitEnvBeatsPreset(t *testing.T) {
	t.Setenv("BIGLAW_QUALITY", "fast")
	t.Setenv("QUALITY_ROUND_GOALS", "true")
	t.Setenv("QUALITY_STAGED_EXTRACTION", "true")
	t.Setenv("DEBATE_VERIFICATION_PASSES", "4")
	t.Setenv("DEBATE_ADVERSARIAL_ENABLED", "true")
	c := Load()
	if !c.Quality.RoundGoals || !c.Quality.StagedExtraction {
		t.Error("explicit QUALITY_* env must beat the fast preset")
	}
	if c.Debate.VerificationPasses != 4 {
		t.Errorf("explicit passes = %d, want 4", c.Debate.VerificationPasses)
	}
	if !c.Debate.AdversarialEnabled {
		t.Error("explicit DEBATE_ADVERSARIAL_ENABLED must beat the fast preset")
	}
	// Un-overridden boosters still follow the preset.
	if c.Quality.CrossDoc {
		t.Error("crossdoc should still follow the fast preset")
	}
}

func TestUnknownPresetFallsBackToBalanced(t *testing.T) {
	t.Setenv("BIGLAW_QUALITY", "turbo")
	c := Load()
	if c.Quality.Preset != "balanced" {
		t.Fatalf("unknown preset resolved to %q, want balanced", c.Quality.Preset)
	}
}
