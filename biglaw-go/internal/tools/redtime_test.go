// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Redtime tool + hook tests: a respond_to_redline run (fake provider) accrues
// the inbound and response documents into a lineage with the decision summary
// attached, edit_document extends an existing lineage, and the
// register_document_version / get_redline_timeline tools round-trip. Reuses
// the negotiate test's fake server and opposing-docx builder (same package).

package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/redtime"
	"github.com/discover-legal/biglaw-go/internal/store"
)

// newRedtimeTestRegistry mirrors newNegotiateTestRegistry but wires a memory
// store as the review repository — the concrete store implements
// VersionRepository too, which is exactly how main wires production.
func newRedtimeTestRegistry(t *testing.T, serverURL string) (*Registry, *store.MemoryRepo) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Model.PrimaryURL = serverURL
	cfg.Model.PrimaryKey = "test-key"
	cfg.PDF.OutputDir = t.TempDir()
	cfg.Persistence.PlaybooksFile = filepath.Join(t.TempDir(), "playbooks.json")
	repo := store.NewMemoryRepo()
	return NewRegistry(cfg, providers.NewRegistry(cfg), nil, nil, repo), repo
}

func execRedtimeTool(t *testing.T, r *Registry, name string, input map[string]interface{}) map[string]interface{} {
	t.Helper()
	res, err := r.Execute(name, input, agents.ToolContext{TaskID: "task-rt"})
	if err != nil {
		t.Fatalf("%s returned an error: %v", name, err)
	}
	out, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("%s result is %T, want map", name, res)
	}
	return out
}

// TestRespondToRedlineRegistersVersions: the counter-redline hook creates two
// versions in one lineage — inbound theirs, response ours — with the decision
// summary attached to the response.
func TestRespondToRedlineRegistersVersions(t *testing.T) {
	srv := newNegotiateFakeServer(t)
	defer srv.Close()
	reg, repo := newRedtimeTestRegistry(t, srv.URL)
	buildOpposingDocx(t, reg.cfg.PDF.OutputDir)

	out := execRedtimeTool(t, reg, "respond_to_redline", map[string]interface{}{"path": "msa.docx"})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("respond_to_redline failed: %v", out["error"])
	}
	lineage, ok := out["lineage"].(map[string]interface{})
	if !ok || lineage == nil {
		t.Fatalf("no lineage card on the result: %v", out["lineage"])
	}
	lineageID, _ := lineage["lineageId"].(string)
	if lineageID == "" {
		t.Fatal("lineage card carries no lineageId")
	}

	ctx := context.Background()
	versions, err := repo.ListLineage(ctx, lineageID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("lineage has %d versions (err=%v), want 2", len(versions), err)
	}
	inbound, response := versions[0], versions[1]
	if inbound.Source != "theirs" || inbound.Round != 1 || inbound.Author != "Opposing Counsel" {
		t.Errorf("inbound version = %+v, want theirs round 1 by Opposing Counsel", inbound)
	}
	if response.Source != "ours" || response.Round != 2 || response.ParentID != inbound.ID {
		t.Errorf("response version = %+v, want ours round 2 child of inbound", response)
	}
	if !strings.HasSuffix(response.Path, "msa.response.docx") {
		t.Errorf("response path = %q", response.Path)
	}
	// The decision summary rides on the response version.
	if !strings.Contains(string(response.Decisions), `"reject"`) ||
		!strings.Contains(string(response.Decisions), `"accept"`) {
		t.Errorf("decisions not attached: %s", response.Decisions)
	}
	// The inbound's visible text (insertions accepted) was stored.
	if !strings.Contains(inbound.Text, "thirty-six (36)") {
		t.Errorf("inbound text = %q, want the opposing visible text", inbound.Text)
	}

	// The timeline over that lineage annotates the opposing moves with the
	// negotiation decisions (fake classifier labels the clause buckets).
	tlOut := execRedtimeTool(t, reg, "get_redline_timeline", map[string]interface{}{"lineage_id": lineageID})
	if okFlag, _ := tlOut["ok"].(bool); !okFlag {
		t.Fatalf("get_redline_timeline failed: %v", tlOut["error"])
	}
	tl, ok := tlOut["timeline"].(*redtime.Timeline)
	if !ok {
		t.Fatalf("timeline is %T", tlOut["timeline"])
	}
	var rejected, counterMove bool
	for _, c := range tl.Clauses {
		for _, e := range c.Events {
			if e.Round == 1 && e.Decision == "reject" && e.Actor == "Opposing Counsel" &&
				c.Clause == "Limitation of liability" {
				rejected = true
			}
			if e.Round == 2 && e.Actor == defaultRedlineAuthor && e.ViaTrackedChange {
				counterMove = true
			}
		}
	}
	if !rejected {
		t.Errorf("no rejected opposing move under Limitation of liability; clauses: %+v", tl.Clauses)
	}
	if !counterMove {
		t.Errorf("BigLaw's round-2 counter move missing; clauses: %+v", tl.Clauses)
	}
}

// TestEditDocumentExtendsLineage: edit_document registers its output as the
// next "ours" version when the input is tracked — and stays silent when not.
func TestEditDocumentExtendsLineage(t *testing.T) {
	srv := newNegotiateFakeServer(t)
	defer srv.Close()
	reg, repo := newRedtimeTestRegistry(t, srv.URL)
	src := buildOpposingDocx(t, reg.cfg.PDF.OutputDir)

	edit := []interface{}{map[string]interface{}{
		"find": "England and Wales", "replace": "Scotland",
		"context_before": "governed by the laws of", "context_after": ", excluding",
	}}

	// Untracked input → no lineage forced into existence.
	out := execRedtimeTool(t, reg, "edit_document", map[string]interface{}{"path": "msa.docx", "edits": edit})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("edit_document failed: %v", out["error"])
	}
	if out["lineage"] != nil {
		t.Errorf("one-off edit created a lineage: %v", out["lineage"])
	}

	// Register the source, edit again → the output extends the lineage.
	reg2 := execRedtimeTool(t, reg, "register_document_version", map[string]interface{}{"path": "msa.docx", "source": "ours"})
	if okFlag, _ := reg2["ok"].(bool); !okFlag {
		t.Fatalf("register_document_version failed: %v", reg2["error"])
	}
	out = execRedtimeTool(t, reg, "edit_document", map[string]interface{}{"path": "msa.docx", "edits": edit})
	lineage, _ := out["lineage"].(map[string]interface{})
	if lineage == nil {
		t.Fatalf("tracked edit did not extend the lineage: %v", out)
	}
	versions, err := repo.ListLineage(context.Background(), lineage["lineageId"].(string))
	if err != nil || len(versions) != 2 {
		t.Fatalf("lineage has %d versions (err=%v), want 2", len(versions), err)
	}
	if versions[1].Source != "ours" || !strings.HasSuffix(versions[1].Path, "msa.redlined.docx") {
		t.Errorf("edited version = %+v", versions[1])
	}
	if !strings.Contains(versions[1].Text, "Scotland") {
		t.Errorf("edited version text = %q, want the applied edit visible", versions[1].Text)
	}
	_ = src
}

// TestRedtimeToolsDegradeWithoutVersionStore: a nil review repository (or one
// without version support) yields structured errors, and the hooks no-op.
func TestRedtimeToolsDegradeWithoutVersionStore(t *testing.T) {
	srv := newNegotiateFakeServer(t)
	defer srv.Close()
	reg := newNegotiateTestRegistry(t, srv.URL) // reviews repo = nil
	buildOpposingDocx(t, reg.cfg.PDF.OutputDir)

	out := execRedtimeTool(t, reg, "register_document_version", map[string]interface{}{"path": "msa.docx"})
	if okFlag, _ := out["ok"].(bool); okFlag {
		t.Error("register_document_version should fail without a version store")
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "version tracking unavailable") {
		t.Errorf("error = %q, want a version-tracking-unavailable message", msg)
	}
	out = execRedtimeTool(t, reg, "get_redline_timeline", map[string]interface{}{"lineage_id": "x"})
	if okFlag, _ := out["ok"].(bool); okFlag {
		t.Error("get_redline_timeline should fail without a version store")
	}

	// The negotiation itself still succeeds — the hook no-ops.
	out = execRedtimeTool(t, reg, "respond_to_redline", map[string]interface{}{"path": "msa.docx"})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("respond_to_redline must not fail when version tracking is unavailable: %v", out["error"])
	}
	if out["lineage"] != nil {
		t.Errorf("lineage should be null without a version store: %v", out["lineage"])
	}
}

// TestGetRedlineTimelineByPath: the lineage resolves from a registered
// version's path.
func TestGetRedlineTimelineByPath(t *testing.T) {
	srv := newNegotiateFakeServer(t)
	defer srv.Close()
	reg, _ := newRedtimeTestRegistry(t, srv.URL)
	buildOpposingDocx(t, reg.cfg.PDF.OutputDir)

	out := execRedtimeTool(t, reg, "respond_to_redline", map[string]interface{}{"path": "msa.docx"})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("respond_to_redline failed: %v", out["error"])
	}

	tlOut := execRedtimeTool(t, reg, "get_redline_timeline", map[string]interface{}{"path": "msa.docx"})
	if okFlag, _ := tlOut["ok"].(bool); !okFlag {
		t.Fatalf("get_redline_timeline by path failed: %v", tlOut["error"])
	}
	if tl, _ := tlOut["timeline"].(*redtime.Timeline); tl == nil || tl.Rounds != 2 {
		t.Errorf("timeline by path = %+v, want the 2-round lineage", tlOut["timeline"])
	}

	// Unknown path → structured not-found error.
	tlOut = execRedtimeTool(t, reg, "get_redline_timeline", map[string]interface{}{"path": "nope.docx"})
	if okFlag, _ := tlOut["ok"].(bool); okFlag {
		t.Error("unknown path should not resolve to a timeline")
	}
}
