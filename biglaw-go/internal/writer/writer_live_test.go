// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package writer

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// TestLiveWrite runs the real multi-pass writer over a completed task's findings on
// the configured (local) model, proving the synthesis is no longer empty. Skipped
// unless WRITER_LIVE=1; needs a backend at API (default :3101) and TASK_ID set, plus
// the same LOCAL_INFERENCE_* env the backend uses.
//
//	WRITER_LIVE=1 TASK_ID=<id> go test ./internal/writer/ -run TestLiveWrite -v
func TestLiveWrite(t *testing.T) {
	if os.Getenv("WRITER_LIVE") == "" {
		t.Skip("set WRITER_LIVE=1 (and TASK_ID, LOCAL_INFERENCE_*) to run the live writer test")
	}
	api := os.Getenv("API")
	if api == "" {
		api = "http://localhost:3101"
	}
	taskID := os.Getenv("TASK_ID")
	if taskID == "" {
		t.Fatal("TASK_ID required")
	}

	resp, err := http.Get(api + "/tasks/" + taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var task types.Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if len(task.Findings) == 0 {
		t.Fatalf("task %s has no findings", taskID)
	}

	wf := make([]Finding, 0, len(task.Findings))
	for _, f := range task.Findings {
		item := Finding{ID: f.ID, Content: f.Content, Agent: f.AgentName, Round: f.Round,
			Grounded: f.EvidenceStatus == types.EvidenceGrounded, Note: f.EvidenceNote}
		if len(f.Citations) > 0 {
			item.Evidence = f.Citations[0].Quote
			item.Source = f.Citations[0].Source
		}
		wf = append(wf, item)
	}

	cfg := config.Load()
	embed := embeddings.NewClient(cfg)
	provReg := providers.NewRegistry(cfg)
	tier := types.TierRoot
	model := routing.SelectModel(cfg, routing.SelectParams{Tier: &tier, TaskType: routing.TaskSynthesis})
	prov, err := provReg.Get(model)
	if err != nil {
		t.Fatal(err)
	}

	w := New(embed, prov, routing.ResolveModelID(model), Options{
		Temperature:       cfg.LLMTemperature,
		InputBudgetTokens: 5000,
	})
	out, err := w.Write(task.Description, string(task.WorkflowType), wf)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n===== WRITER OUTPUT (%d findings → %d chars) =====\n%s\n===== END =====", len(wf), len(out), out)
	if strings.TrimSpace(out) == "" {
		t.Fatal("writer produced EMPTY output")
	}
	if len(out) < 400 {
		t.Errorf("output suspiciously short (%d chars) — drafters may have failed", len(out))
	}
	if !strings.Contains(out, "##") {
		t.Errorf("expected section headings in the deliverable")
	}
}
