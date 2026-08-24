// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package flavour

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func def(id string, tier int, skills ...string) types.AgentDefinition {
	return types.AgentDefinition{ID: id, Tier: types.AgentTier(tier), Skills: skills}
}

func ids(defs []types.AgentDefinition) map[string]bool {
	m := map[string]bool{}
	for _, d := range defs {
		m[d.ID] = true
	}
	return m
}

func TestFilterAgentsRules(t *testing.T) {
	f := &Flavour{ID: "t", Agents: AgentFilter{
		IncludeSkills: []string{"litigation", "clinic-*"},
		IncludeAgents: []string{"kept-by-id"},
		ExcludeAgents: []string{"dropped-by-id", "root"},
	}}
	bench := []types.AgentDefinition{
		def("root", 0, "task-planning"),         // spine: exclude cannot touch it
		def("manager", 1, "coordination"),       // spine
		def("lit", 2, "litigation", "case-law"), // skill match
		def("clinic", 2, "clinic-intake"),       // wildcard match
		def("nda", 2, "nda-review"),             // no match → dropped
		def("kept-by-id", 2, "m&a"),             // includeAgents wins
		def("dropped-by-id", 2, "litigation"),   // excludeAgents wins over skills
		def("tool", 3, "web-search"),            // T3 always seated
	}
	got := ids(f.FilterAgents(bench))
	want := []string{"root", "manager", "lit", "clinic", "kept-by-id", "tool"}
	if len(got) != len(want) {
		t.Fatalf("seated %v, want %v", got, want)
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("%s should be seated", id)
		}
	}
}

func TestFilterAgentsNoSkillFilterKeepsAll(t *testing.T) {
	f := &Flavour{ID: "t"}
	bench := []types.AgentDefinition{def("a", 2, "x"), def("b", 3)}
	if n := len(f.FilterAgents(bench)); n != 2 {
		t.Fatalf("empty filter should keep all, kept %d", n)
	}
}

func TestFilterTemplates(t *testing.T) {
	f := &Flavour{ID: "t", Templates: []string{"keep-me"}}
	ts := []types.TaskTemplate{{ID: "keep-me"}, {ID: "drop-me"}}
	got := f.FilterTemplates(ts)
	if len(got) != 1 || got[0].ID != "keep-me" {
		t.Fatalf("got %v", got)
	}
	// Empty template list = expose all.
	if n := len((&Flavour{ID: "t"}).FilterTemplates(ts)); n != 2 {
		t.Fatalf("empty template filter should keep all, kept %d", n)
	}
}

func TestLoadFullAndEmptyAreNoOps(t *testing.T) {
	for _, v := range []string{"", "full", "FULL", "  "} {
		f, err := Load(".", v)
		if err != nil || f != nil {
			t.Fatalf("Load(%q) = %v, %v; want nil, nil", v, f, err)
		}
	}
}

func TestLoadMissingAndInvalid(t *testing.T) {
	if _, err := Load(t.TempDir(), "nope"); err == nil {
		t.Fatal("missing flavour should error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"name":"no id"}`), 0o644)
	if _, err := Load(".", bad); err == nil {
		t.Fatal("flavour without id should error")
	}
}

// TestFamilyLawPresetAgainstRealBench loads the shipped family-law flavour and
// applies it to the real agent definitions. It pins the intent of the preset:
// the corporate deal stack is out, the disputes/advisory core is in, and the
// spine survives intact. Run from the package dir; the flavours/ dir sits at
// the repo root, two levels up from biglaw-go.
func TestFamilyLawPresetAgainstRealBench(t *testing.T) {
	f, err := Load(filepath.Join("..", "..", ".."), "family-law")
	if err != nil {
		t.Fatalf("load shipped preset: %v", err)
	}
	seated := f.FilterAgents(agents.ALL_AGENT_DEFINITIONS)
	got := ids(seated)

	for _, id := range []string{
		"root-orchestrator", "research-manager", // spine
		"litigation-disputes-analyst", "case-law-precedent-analyst",
		"arbitration-adr-analyst", "chronology-builder", "tax-analyst",
		"real-estate-property-analyst", "client-advice-memo-drafter",
		"litigation-brief-drafter", "web-search-agent", // T3
		// the dedicated family bench
		"custody-parenting-analyst", "child-support-analyst",
		"spousal-support-analyst", "property-division-analyst",
		"marital-agreements-drafter", "protection-order-analyst",
		"family-procedure-analyst", "parentage-adoption-analyst",
	} {
		if !got[id] {
			t.Errorf("family-law should seat %s", id)
		}
	}
	for _, id := range []string{
		"nda-triager", "redline-engine-agent", "playbook-specialist",
		"capital-markets-analyst", "patent-prosecution-analyst",
		"tabular-diligence-reviewer", "closing-checklist-driver",
	} {
		if got[id] {
			t.Errorf("family-law should NOT seat %s", id)
		}
	}
	// The preset must be a real trim: meaningfully smaller than the full bench.
	if len(seated) >= len(agents.ALL_AGENT_DEFINITIONS)*2/3 {
		t.Errorf("family-law seats %d of %d agents — not much of a trim",
			len(seated), len(agents.ALL_AGENT_DEFINITIONS))
	}
	t.Logf("family-law seats %d of %d agents", len(seated), len(agents.ALL_AGENT_DEFINITIONS))
}

// TestFamilyLawPresetAgainstLavernBench applies the shipped preset to the
// Lavern agents. Their configs carry specialties (mapped to Skills by the
// adapter), so a flavour should seat the relevant ones — mediation, plain
// language, research — and drop the corporate stack, rather than dropping all
// 68 as untagged.
func TestFamilyLawPresetAgainstLavernBench(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	f, err := Load(root, "family-law")
	if err != nil {
		t.Fatalf("load shipped preset: %v", err)
	}
	lavern, err := adapters.LoadLavernAgents(filepath.Join(root, "agents", "lavern"))
	if err != nil || len(lavern) == 0 {
		t.Fatalf("load lavern agents: %v (n=%d)", err, len(lavern))
	}
	for _, d := range lavern {
		if len(d.Skills) == 0 {
			t.Errorf("%s has no skills — adapter should map specialties", d.ID)
		}
	}
	got := ids(f.FilterAgents(lavern))
	for _, id := range []string{
		"lavern:dispute-resolution", "lavern:plain-language-specialist",
		"lavern:legal-researcher", "lavern:litigation-associate",
		"lavern:real-estate-counsel", "lavern:tax-counsel",
		"lavern:client-relations-partner",
	} {
		if !got[id] {
			t.Errorf("family-law should seat %s", id)
		}
	}
	for _, id := range []string{
		"lavern:ma-specialist", "lavern:capital-markets",
		"lavern:contract-specialist", "lavern:startup-counsel",
		"lavern:sanctions-specialist",
	} {
		if got[id] {
			t.Errorf("family-law should NOT seat %s", id)
		}
	}
	t.Logf("family-law seats %d of %d lavern agents", len(got), len(lavern))
}
