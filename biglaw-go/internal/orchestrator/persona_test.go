// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestRootSystemPromptPersonaOverlay(t *testing.T) {
	o := &Orchestrator{rootAgentDef: types.AgentDefinition{SystemPrompt: "ROOT."}}
	o.SetWorkflowPersonas(map[types.WorkflowType]string{
		types.WorkflowAdversarial: "Attack every finding.",
		types.WorkflowReview:      "   ", // blank persona → no overlay
	})

	// No task / no persona for the type / blank persona → bare root prompt.
	for _, task := range []*types.Task{
		nil,
		{WorkflowType: types.WorkflowRoundtable},
		{WorkflowType: types.WorkflowReview},
	} {
		if got := o.rootSystemPrompt(task); got != "ROOT." {
			t.Errorf("task %+v: want bare root prompt, got %q", task, got)
		}
	}

	got := o.rootSystemPrompt(&types.Task{WorkflowType: types.WorkflowAdversarial})
	if !strings.HasPrefix(got, "ROOT.") {
		t.Fatalf("root prompt must stay authoritative (first), got %q", got)
	}
	if !strings.Contains(got, "WORKFLOW PERSONA (adversarial)") || !strings.Contains(got, "Attack every finding.") {
		t.Fatalf("persona overlay missing, got %q", got)
	}
}

func TestRootSystemPromptNilPersonas(t *testing.T) {
	o := &Orchestrator{rootAgentDef: types.AgentDefinition{SystemPrompt: "ROOT."}}
	if got := o.rootSystemPrompt(&types.Task{WorkflowType: types.WorkflowAdversarial}); got != "ROOT." {
		t.Fatalf("nil persona map should be a no-op, got %q", got)
	}
}
