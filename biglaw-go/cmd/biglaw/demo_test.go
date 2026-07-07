// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tests for `biglaw demo`. The orchestration pieces that need no model are
// unit-tested directly (argv routing, playbook seeding, edit anchoring, grid
// rendering); the full four-beat tour runs end-to-end against an in-process
// httptest server speaking the OpenAI-compatible chat wire format, following
// the fake-provider pattern from internal/tools/tabreview_test.go.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/playbook"
)

// ─── argv routing ─────────────────────────────────────────────────────────────

func TestDemoRequested(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"biglaw"}, false},
		{[]string{"biglaw", "demo"}, true},
		{[]string{"biglaw", "demo", "extra"}, true},
		{[]string{"biglaw", "serve"}, false},
		{[]string{"biglaw", "--demo"}, false},
	}
	for _, c := range cases {
		if got := demoRequested(c.args); got != c.want {
			t.Errorf("demoRequested(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// ─── playbook seeding ─────────────────────────────────────────────────────────

// The demo playbook file must be valid JSON in the exact shape the playbook
// store reads, and the cascade must resolve the two demo clause positions.
func TestDemoPlaybookRoundTripsThroughStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo-playbook.json")
	if err := writeDemoPlaybook(path); err != nil {
		t.Fatalf("writeDemoPlaybook: %v", err)
	}

	store := playbook.New(path)
	if err := store.Init(); err != nil {
		t.Fatalf("playbook store rejected the demo file: %v", err)
	}

	indem := store.Resolve("Indemnification cap", playbook.ResolveOpts{})
	if indem == nil {
		t.Fatal("cascade did not resolve the Indemnification cap position")
	}
	redlines := strings.ToLower(strings.Join(indem.EffectiveEntry.RedLines, " "))
	if !strings.Contains(redlines, "uncapped") && !strings.Contains(redlines, "unlimited") {
		t.Errorf("indemnification red lines should forbid uncapped indemnity, got %v", indem.EffectiveEntry.RedLines)
	}

	notice := store.Resolve("Notice period", playbook.ResolveOpts{})
	if notice == nil {
		t.Fatal("cascade did not resolve the Notice period position")
	}
	if !strings.Contains(notice.EffectiveEntry.RedLines[0], "five (5) Business Days") {
		t.Errorf("notice-period red line should hold the five-day floor, got %v", notice.EffectiveEntry.RedLines)
	}
}

// ─── sample content integrity ─────────────────────────────────────────────────

// Every opposing edit must anchor into the base clause text exactly —
// find preceded by context_before — and the two red-line edits must target
// text that is unique in the document, so edit_document locates them
// unambiguously.
func TestDemoOpposingEditsAnchorIntoBaseClauses(t *testing.T) {
	body := demoClauseIndemnity + "\n" + demoClauseCure + "\n" + demoClauseAssignment
	for i, e := range demoOpposingEdits() {
		if e.Find == "" || e.Replace == "" {
			t.Errorf("edit %d: find and replace must both be non-empty", i)
		}
		anchored := e.ContextBefore + e.Find
		if !strings.Contains(body, anchored) {
			t.Errorf("edit %d: %q does not anchor into the base clause text", i, anchored)
		}
		if n := strings.Count(body, e.Find); n != 1 {
			t.Errorf("edit %d: find %q occurs %d times in the base clauses; want exactly 1", i, e.Find, n)
		}
	}
}

// The seeded agreement must actually carry the figures the tabular-review
// columns ask about, so the extraction beat has something concrete to find.
func TestDemoAgreementCarriesTheKeyFigures(t *testing.T) {
	for _, figure := range []string{
		"$45,000,000",            // facility amount (Facility amount column)
		"2.75%",                  // applicable margin (Interest rate column)
		"MERIDIAN DATA SYSTEMS",  // borrower (Parties column)
		"FIRST HARBOR BANK",      // administrative agent (Parties column)
		"ten (10) Business Days", // covenant cure period (Events of default column)
		"Event of Default",       // events of default article
	} {
		if !strings.Contains(demoAgreementText, figure) {
			t.Errorf("sample agreement is missing %q", figure)
		}
	}
}

// ─── grid rendering ───────────────────────────────────────────────────────────

func TestRenderReviewGridFormatsACannedMatrix(t *testing.T) {
	res := map[string]interface{}{
		"rows": []interface{}{
			map[string]interface{}{
				"document": "Demo Credit Agreement",
				"cells": []interface{}{
					map[string]interface{}{
						"column":  "Facility amount",
						"flag":    "green",
						"summary": "$45,000,000 revolving facility [[page:1||quote:aggregate principal amount not exceeding $45,000,000]]",
					},
					map[string]interface{}{
						"column":  "Poison pill",
						"flag":    "grey",
						"summary": "Not Found",
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	renderReviewGrid(&buf, parseReviewGrid(res), false)
	got := buf.String()

	for _, want := range []string{
		"Demo Credit Agreement",
		"Facility amount",
		"[green ]",
		"$45,000,000 revolving facility",
		"[grey  ]",
		"Not Found",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("grid output missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[[page:") {
		t.Errorf("citation markers must be stripped from terminal output:\n%s", got)
	}
}

// ─── provider preflight ───────────────────────────────────────────────────────

// A default hosted endpoint with no API key must fail fast with the friendly
// setup message — before anything is seeded or written.
func TestDemoFailsFastWithoutAProvider(t *testing.T) {
	cfg := &config.Config{}
	cfg.Model.PrimaryURL = "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"
	cfg.Model.PrimaryKey = ""
	outDir := t.TempDir()
	cfg.PDF.OutputDir = outDir

	var buf bytes.Buffer
	if code := runDemoWithConfig(cfg, &buf, false); code != 1 {
		t.Fatalf("expected exit code 1 without a provider, got %d\noutput:\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "QWEN_API_KEY") {
		t.Errorf("failure message should tell the user what to set:\n%s", buf.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "demo-playbook.json")); !os.IsNotExist(err) {
		t.Error("nothing should be seeded when the provider preflight fails")
	}
}

// ─── end-to-end against a fake provider ───────────────────────────────────────

// newDemoFakeModelServer serves every model call the demo makes, dispatching
// on distinctive markers in the user message: tabular cells ("FIELD:"),
// clause classification ("KIND:"), and negotiation judgment
// ("OPPOSING CHANGE:").
func newDemoFakeModelServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var user string
		for _, m := range body.Messages {
			if m.Role == "user" {
				var s string
				_ = json.Unmarshal(m.Content, &s)
				user = s
			}
		}

		reply := `{"summary":"$45,000,000 revolving credit facility [[page:1||quote:aggregate principal amount not exceeding $45,000,000]]","flag":"green","reasoning":"Clearly stated in Section 2.01."}`
		switch {
		case strings.Contains(user, "OPPOSING CHANGE:"):
			reply = `{"disposition":"reject","rationale":"Crosses a firm red line; original language restored.","counterText":""}`
		case strings.Contains(user, "KIND:"):
			reply = `{"clauseType":"Indemnification cap"}`
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]interface{}{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 100, "completion_tokens": 40},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// TestDemoEndToEnd runs the whole four-beat tour against the fake provider
// and asserts the narrative and every artifact land where promised.
func TestDemoEndToEnd(t *testing.T) {
	srv := newDemoFakeModelServer(t)
	defer srv.Close()

	cfg := &config.Config{}
	cfg.Model.PrimaryURL = srv.URL
	cfg.Model.PrimaryKey = "test-key"
	// Keep the knowledge store's embedding path off the network: the "local"
	// embedder points at an unreachable URL and fails fast; ingest tolerates it.
	cfg.Local.LocalEmbeddings = true
	cfg.Local.OllamaURL = "http://127.0.0.1:1"
	outDir := t.TempDir()
	cfg.PDF.OutputDir = outDir

	var buf bytes.Buffer
	if code := runDemoWithConfig(cfg, &buf, false); code != 0 {
		t.Fatalf("demo exited %d\noutput:\n%s", code, buf.String())
	}
	out := buf.String()

	for _, want := range []string{
		"[1/4]", "[2/4]", "[3/4]", "[4/4]",
		"Facility amount",  // grid column rendered
		"$45,000,000",      // extracted value on screen
		"Opposing Counsel", // markup narrative
		"REJECT",           // decision card disposition
		"Next steps",       // closing block
	} {
		if !strings.Contains(out, want) {
			t.Errorf("demo output missing %q\noutput:\n%s", want, out)
		}
	}

	for _, artifact := range []string{
		"demo-playbook.json",
		"demo-tabular-review.docx",
		"demo-cp-checklist.docx",
		"demo-base-clauses.docx",
		"demo-base-clauses.redlined.docx",
		"demo-base-clauses.redlined.response.docx",
	} {
		if _, err := os.Stat(filepath.Join(outDir, artifact)); err != nil {
			t.Errorf("expected artifact %s: %v", artifact, err)
		}
	}
}
