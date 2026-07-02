// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Engine tests: registration (lineage assembly, idempotency) and timeline
// building over a synthetic three-round negotiation — v1 ours (clean), v2
// theirs WITH tracked changes (attributed via revparse), v3 theirs CLEAN with
// one silent modification (attributed via textdiff). No network: the drift
// test speaks to an in-process httptest server on the OpenAI-compatible chat
// wire format (same pattern as internal/tools/tabreview_test.go).

package redtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Registration ─────────────────────────────────────────────────────────────

func TestRegisterVersionLineageAndIdempotency(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepo()

	v1, err := RegisterVersion(ctx, repo, RegisterOpts{Text: "twelve (12) months", Source: "ours", Author: "Big Michael"})
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}
	if v1.Round != 1 || v1.LineageID == "" || v1.ParentID != "" || v1.Source != "ours" {
		t.Errorf("v1 = %+v, want round 1 root of a fresh lineage", v1)
	}

	v2, err := RegisterVersion(ctx, repo, RegisterOpts{Text: "thirty-six (36) months", Source: "theirs", ParentID: v1.ID})
	if err != nil {
		t.Fatalf("register v2: %v", err)
	}
	if v2.Round != 2 || v2.LineageID != v1.LineageID || v2.ParentID != v1.ID {
		t.Errorf("v2 = %+v, want round 2 child of v1", v2)
	}

	// Re-registering identical content is idempotent — and attaches decisions
	// after the fact.
	again, err := RegisterVersion(ctx, repo, RegisterOpts{
		Text: "thirty-six (36) months", Source: "theirs", LineageID: v1.LineageID,
		Decisions: []map[string]string{{"disposition": "reject"}},
	})
	if err != nil {
		t.Fatalf("re-register v2: %v", err)
	}
	if again.ID != v2.ID {
		t.Errorf("idempotency broken: got new version %s, want %s", again.ID, v2.ID)
	}
	if !strings.Contains(string(again.Decisions), `"reject"`) {
		t.Errorf("decisions not attached on idempotent re-register: %q", again.Decisions)
	}

	// Joining by lineage ID parents on the latest version.
	v3, err := RegisterVersion(ctx, repo, RegisterOpts{Text: "twenty-four (24) months", Source: "ours", LineageID: v1.LineageID})
	if err != nil {
		t.Fatalf("register v3: %v", err)
	}
	if v3.Round != 3 || v3.ParentID != v2.ID {
		t.Errorf("v3 = %+v, want round 3 child of v2", v3)
	}

	lineage, err := repo.ListLineage(ctx, v1.LineageID)
	if err != nil || len(lineage) != 3 {
		t.Fatalf("lineage: len=%d err=%v, want 3", len(lineage), err)
	}
	if _, found, _ := repo.FindVersionByHash(ctx, HashBytes([]byte("twelve (12) months"))); !found {
		t.Error("FindVersionByHash misses a text-registered version")
	}

	// Unknown parent is an error, not a silent new lineage.
	if _, err := RegisterVersion(ctx, repo, RegisterOpts{Text: "x", ParentID: "v-none"}); err == nil {
		t.Error("register with unknown parent should fail")
	}
	// Nil repository degrades to ErrUnavailable.
	if _, err := RegisterVersion(ctx, nil, RegisterOpts{Text: "x"}); err != ErrUnavailable {
		t.Errorf("nil repo err = %v, want ErrUnavailable", err)
	}
}

func TestRegisterVersionExtractsDocxText(t *testing.T) {
	dir := t.TempDir()
	b := ooxml.NewBuilder()
	b.Paragraph("The liability cap is twelve (12) months of fees.")
	data, err := b.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "msa.docx")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := RegisterVersion(context.Background(), store.NewMemoryRepo(), RegisterOpts{Path: path, Source: "upload"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !strings.Contains(v.Text, "twelve (12) months of fees") {
		t.Errorf("docx text not extracted: %q", v.Text)
	}
	if v.ContentHash != HashBytes(data) {
		t.Errorf("content hash should be the file-byte hash")
	}
}

// ─── Timeline: the three-round synthetic lineage ─────────────────────────────

// paragraphs of the v1 draft. The numbered lines double as positional-bucket
// headings for the provider-less build.
var v1Paras = []string{
	"MASTER SERVICES AGREEMENT",
	"1. LIMITATION OF LIABILITY",
	"The liability cap is twelve (12) months of fees paid under this Agreement.",
	"2. GOVERNING LAW",
	"This Agreement is governed by the laws of England and Wales.",
}

func writeDocx(t *testing.T, path string, paras []string) {
	t.Helper()
	b := ooxml.NewBuilder()
	for _, p := range paras {
		b.Paragraph(p)
	}
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("build docx: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write docx: %v", err)
	}
}

// buildThreeRoundLineage registers: v1 ours (clean), v2 theirs with a tracked
// substitution by "Opposing Counsel", v3 theirs clean with one silent
// modification. Returns the repo and lineage ID.
func buildThreeRoundLineage(t *testing.T) (*store.MemoryRepo, string) {
	t.Helper()
	ctx := context.Background()
	repo := store.NewMemoryRepo()
	dir := t.TempDir()

	// v1 — ours, clean.
	v1Path := filepath.Join(dir, "msa.docx")
	writeDocx(t, v1Path, v1Paras)
	v1, err := RegisterVersion(ctx, repo, RegisterOpts{Path: v1Path, Source: "ours", Author: "Big Michael"})
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}

	// v2 — theirs, WITH a tracked substitution twelve → thirty-six.
	v2Path := filepath.Join(dir, "msa.v2.docx")
	writeDocx(t, v2Path, v1Paras)
	doc, err := ooxml.OpenFile(v2Path)
	if err != nil {
		t.Fatalf("open v2: %v", err)
	}
	oc := ooxml.NewRevisions("Opposing Counsel", time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC))
	text := doc.Text()
	i := strings.Index(text, "twelve (12)")
	if err := doc.ApplyTracked(i, i+len("twelve (12)"), "thirty-six (36)", oc); err != nil {
		t.Fatalf("apply tracked: %v", err)
	}
	if err := doc.SaveFile(v2Path); err != nil {
		t.Fatalf("save v2: %v", err)
	}
	v2, err := RegisterVersion(ctx, repo, RegisterOpts{Path: v2Path, Source: "theirs", ParentID: v1.ID})
	if err != nil {
		t.Fatalf("register v2: %v", err)
	}

	// v3 — theirs, CLEAN, silently swapping the governing law.
	v3Paras := make([]string, len(v1Paras))
	copy(v3Paras, v1Paras)
	v3Paras[2] = strings.Replace(v3Paras[2], "twelve (12)", "thirty-six (36)", 1) // v2's change, accepted
	v3Paras[4] = strings.Replace(v3Paras[4], "England and Wales", "the State of Delaware", 1)
	v3Path := filepath.Join(dir, "msa.v3.docx")
	writeDocx(t, v3Path, v3Paras)
	if _, err := RegisterVersion(ctx, repo, RegisterOpts{Path: v3Path, Source: "theirs", ParentID: v2.ID}); err != nil {
		t.Fatalf("register v3: %v", err)
	}
	return repo, v1.LineageID
}

func findClauseEvent(tl *Timeline, match func(ClauseEvent) bool) (string, *ClauseEvent) {
	for _, c := range tl.Clauses {
		for i := range c.Events {
			if match(c.Events[i]) {
				return c.Clause, &c.Events[i]
			}
		}
	}
	return "", nil
}

func TestBuildTimelineThreeRoundsProviderless(t *testing.T) {
	repo, lineageID := buildThreeRoundLineage(t)

	// Zero model calls: no provider, no playbook.
	tl, err := BuildTimeline(context.Background(), repo, lineageID, BuildOpts{})
	if err != nil {
		t.Fatalf("BuildTimeline: %v", err)
	}
	if tl.Rounds != 3 || len(tl.Versions) != 3 {
		t.Fatalf("rounds=%d versions=%d, want 3/3", tl.Rounds, len(tl.Versions))
	}

	// Round 2: the tracked substitution, attributed to its author via revparse.
	clause2, ev2 := findClauseEvent(tl, func(e ClauseEvent) bool { return e.Round == 2 })
	if ev2 == nil {
		t.Fatal("no round-2 event")
	}
	if !ev2.ViaTrackedChange || ev2.Actor != "Opposing Counsel" || ev2.Kind != "substitution" ||
		ev2.FromText != "twelve (12)" || ev2.ToText != "thirty-six (36)" {
		t.Errorf("round-2 event = %+v, want tracked substitution by Opposing Counsel", ev2)
	}
	// Positional bucketing lands the event under its numbered heading.
	if !strings.Contains(clause2, "LIMITATION OF LIABILITY") {
		t.Errorf("round-2 clause bucket = %q, want the LIMITATION OF LIABILITY heading", clause2)
	}

	// Round 3: the silent modification, attributed to the version's side via
	// textdiff.
	clause3, ev3 := findClauseEvent(tl, func(e ClauseEvent) bool { return e.Round == 3 })
	if ev3 == nil {
		t.Fatal("no round-3 event")
	}
	if ev3.ViaTrackedChange || ev3.Actor != "theirs" || ev3.Kind != "substitution" ||
		!strings.Contains(ev3.FromText, "England and Wales") || !strings.Contains(ev3.ToText, "Delaware") {
		t.Errorf("round-3 event = %+v, want silent substitution attributed to theirs", ev3)
	}
	if !strings.Contains(clause3, "GOVERNING LAW") {
		t.Errorf("round-3 clause bucket = %q, want the GOVERNING LAW heading", clause3)
	}
	// v3 also carries v2's accepted change in its visible text — but v2's
	// visible text already had it, so it must NOT re-register as a round-3
	// move.
	if _, ev := findClauseEvent(tl, func(e ClauseEvent) bool {
		return e.Round == 3 && strings.Contains(e.ToText, "thirty-six")
	}); ev != nil {
		t.Errorf("accepted round-2 change re-reported at round 3: %+v", ev)
	}

	// No provider → drift unknown everywhere; current language located.
	for _, c := range tl.Clauses {
		if c.Drift == nil || c.Drift.Status != DriftUnknown {
			t.Errorf("clause %q drift = %+v, want unknown without a provider", c.Clause, c.Drift)
		}
	}
	if _, ev := findClauseEvent(tl, func(e ClauseEvent) bool { return e.Round == 3 }); ev != nil {
		for _, c := range tl.Clauses {
			if strings.Contains(c.Clause, "GOVERNING LAW") && !strings.Contains(c.CurrentText, "Delaware") {
				t.Errorf("currentText = %q, want the Delaware paragraph", c.CurrentText)
			}
		}
	}

	// Unknown lineage → ErrNotFound.
	if _, err := BuildTimeline(context.Background(), repo, "lin-none", BuildOpts{}); err != ErrNotFound {
		t.Errorf("unknown lineage err = %v, want ErrNotFound", err)
	}
}

func TestBuildTimelineFormattingOnlyRound(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepo()

	v1, err := RegisterVersion(ctx, repo, RegisterOpts{
		Text: "The parties agree that \"Confidential Information\" excludes public data - always.", Source: "ours",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Same language after Word's autocorrect: curly quotes, en dash,
	// non-breaking space, reflowed whitespace.
	if _, err := RegisterVersion(ctx, repo, RegisterOpts{
		Text: "The parties  agree that “Confidential Information” excludes public data – always.", Source: "theirs", ParentID: v1.ID,
	}); err != nil {
		t.Fatal(err)
	}

	tl, err := BuildTimeline(ctx, repo, v1.LineageID, BuildOpts{})
	if err != nil {
		t.Fatalf("BuildTimeline: %v", err)
	}
	if len(tl.Clauses) != 0 {
		t.Errorf("formatting-only round produced %d clause buckets: %+v", len(tl.Clauses), tl.Clauses)
	}
	if tl.Rounds != 2 {
		t.Errorf("rounds = %d, want 2", tl.Rounds)
	}
}

// ─── Drift with a fake provider ───────────────────────────────────────────────

// newDriftFakeServer serves scripted classify (extraction tier → qwen-turbo)
// and drift-judgment (drafting tier → qwen-plus) replies.
func newDriftFakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reply := `{"clauseType":"Limitation of liability"}`
		if body.Model == "qwen-plus" { // drafting tier → drift judgment
			reply = `{"status":"below","note":"Cap tripled to thirty-six months, beyond the firm's twelve-month standard."}`
		}
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]interface{}{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 120, "completion_tokens": 30},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestBuildTimelineDriftWithProvider(t *testing.T) {
	srv := newDriftFakeServer(t)
	defer srv.Close()

	// A firm playbook with a limitation-of-liability position.
	pbPath := filepath.Join(t.TempDir(), "playbooks.json")
	pbs := []types.Playbook{{
		ID: "pb-firm", Scope: types.PlaybookScopeFirm, Name: "Firm standard",
		Entries: []types.PlaybookEntry{{
			ClauseType:       "limitation_of_liability",
			StandardPosition: "Liability capped at twelve (12) months of fees.",
			RedLines:         []string{"No uncapped liability"},
		}},
	}}
	raw, err := json.Marshal(pbs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pbPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repo := store.NewMemoryRepo()
	v1, err := RegisterVersion(ctx, repo, RegisterOpts{
		Text: "The liability cap is twelve (12) months of fees.", Source: "ours",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterVersion(ctx, repo, RegisterOpts{
		Text: "The liability cap is thirty-six (36) months of fees.", Source: "theirs", ParentID: v1.ID,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Model.PrimaryURL = srv.URL
	cfg.Model.PrimaryKey = "test-key"
	cfg.Persistence.PlaybooksFile = pbPath
	opts := OptsFromConfig(cfg, providers.NewRegistry(cfg), playbook.ResolveOpts{}, "task-drift")
	if opts.Provider == nil {
		t.Fatal("OptsFromConfig did not wire the fake provider")
	}

	tl, err := BuildTimeline(ctx, repo, v1.LineageID, opts)
	if err != nil {
		t.Fatalf("BuildTimeline: %v", err)
	}
	if len(tl.Clauses) != 1 {
		t.Fatalf("clauses = %d, want 1: %+v", len(tl.Clauses), tl.Clauses)
	}
	c := tl.Clauses[0]
	if c.Clause != "Limitation of liability" {
		t.Errorf("clause label = %q, want the model classification", c.Clause)
	}
	if c.Drift == nil || c.Drift.Status != DriftBelow || c.Drift.PlaybookTier != "firm" || c.Drift.Note == "" {
		t.Errorf("drift = %+v, want below at the firm tier with a note", c.Drift)
	}
}
