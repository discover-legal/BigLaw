// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

const lavernFixture = `[
  {"id": "lavern:mediator", "name": "Mediator", "tier": "specialist",
   "role": "Mediation specialist who finds settlement pathways.",
   "specialties": ["mediation", "settlement"], "systemPrompt": "You mediate."},
  {"id": "lavern:orchestrator-adversarial", "name": "Adversarial Orchestrator",
   "tier": "orchestrator", "promptOnly": true,
   "role": "Orchestrator Adversarial (prompt-only).",
   "specialties": ["workflow-orchestration"], "systemPrompt": "Attack every finding."},
  {"id": "lavern:orchestrator-counsel", "name": "Counsel Orchestrator",
   "tier": "orchestrator", "promptOnly": true,
   "role": "Orchestrator Counsel (prompt-only).",
   "specialties": ["workflow-orchestration"], "systemPrompt": "Advise like senior counsel."},
  {"id": "lavern:orchestrator", "name": "Generic Orchestrator",
   "tier": "orchestrator", "promptOnly": true,
   "role": "Orchestrator (prompt-only).",
   "specialties": ["workflow-orchestration"], "systemPrompt": "Generic — no workflow type."}
]`

func writeLavernFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agents.json"), []byte(lavernFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadLavernAgentsSkipsPromptOnly(t *testing.T) {
	defs, err := LoadLavernAgents(writeLavernFixture(t))
	if err != nil {
		t.Fatalf("LoadLavernAgents: %v", err)
	}
	if len(defs) != 1 || defs[0].ID != "lavern:mediator" {
		t.Fatalf("want only the mediator seated, got %+v", defs)
	}
	if len(defs[0].Skills) != 2 || defs[0].Skills[0] != "mediation" {
		t.Fatalf("specialties should map to Skills, got %v", defs[0].Skills)
	}
	if defs[0].Description != "Mediation specialist who finds settlement pathways." {
		t.Fatalf("prose role should become the description, got %q", defs[0].Description)
	}
}

func TestLoadLavernOrchestratorPrompts(t *testing.T) {
	personas, err := LoadLavernOrchestratorPrompts(writeLavernFixture(t))
	if err != nil {
		t.Fatalf("LoadLavernOrchestratorPrompts: %v", err)
	}
	if len(personas) != 2 {
		t.Fatalf("want 2 personas (generic orchestrator ignored), got %d: %v", len(personas), personas)
	}
	if personas[types.WorkflowAdversarial] != "Attack every finding." {
		t.Errorf("adversarial persona missing, got %q", personas[types.WorkflowAdversarial])
	}
	// Regression: "counsel" must map to WorkflowCounsel, not fall through to
	// roundtable and clobber another persona.
	if personas[types.WorkflowCounsel] != "Advise like senior counsel." {
		t.Errorf("counsel persona missing/misrouted, got %q", personas[types.WorkflowCounsel])
	}
	if _, ok := personas[types.WorkflowRoundtable]; ok {
		t.Error("no roundtable persona in fixture — counsel fell through the type map")
	}
}

func TestLoadLavernOrchestratorPromptsMissingDir(t *testing.T) {
	personas, err := LoadLavernOrchestratorPrompts(filepath.Join(t.TempDir(), "nope"))
	if err != nil || personas != nil {
		t.Fatalf("missing dir should be a silent no-op, got %v, %v", personas, err)
	}
}
