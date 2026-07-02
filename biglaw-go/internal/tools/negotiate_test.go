// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// End-to-end test for respond_to_redline: an opposing-counsel marked-up docx
// goes in, the countered response document comes out. No network, no real
// model — the provider registry is pointed at an in-process httptest server
// speaking the OpenAI-compatible chat wire format (same pattern as
// tabreview_test.go); classify vs judge calls are told apart by model ID.

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/negotiate"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

// newNegotiateFakeServer serves scripted classify/judge replies. With the
// test config's model stack unset, the extraction tier resolves to
// "qwen-turbo" (classify) and the drafting tier to "qwen-plus" (judge).
func newNegotiateFakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Model    string `json:"model"`
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

		var reply string
		if body.Model == "qwen-turbo" { // extraction tier → classify
			if strings.Contains(user, "thirty-six (36)") {
				reply = `{"clauseType":"Limitation of liability"}`
			} else {
				reply = `{"clauseType":"Governing law"}`
			}
		} else { // drafting tier → judge
			if strings.Contains(user, "thirty-six (36)") {
				reply = `{"disposition":"reject","rationale":"Falls below the client floor on liability cap; original language restored.","counterText":""}`
			} else {
				reply = `{"disposition":"accept","rationale":"Market-standard conflict-of-laws carve-out; no playbook position — judged on reasonableness."}`
			}
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]interface{}{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 150, "completion_tokens": 40},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func newNegotiateTestRegistry(t *testing.T, serverURL string) *Registry {
	t.Helper()
	cfg := &config.Config{}
	cfg.Model.PrimaryURL = serverURL
	cfg.Model.PrimaryKey = "test-key"
	cfg.PDF.OutputDir = t.TempDir()
	cfg.Persistence.PlaybooksFile = filepath.Join(t.TempDir(), "playbooks.json")
	return NewRegistry(cfg, providers.NewRegistry(cfg), nil, nil)
}

// buildOpposingDocx writes a two-paragraph agreement into the output dir and
// marks it up as opposing counsel: one substitution (to be rejected) and one
// insertion (to be accepted).
func buildOpposingDocx(t *testing.T, dir string) string {
	t.Helper()
	b := ooxml.NewBuilder()
	b.Paragraph("The liability cap is twelve (12) months of fees paid under this Agreement.")
	b.Paragraph("This Agreement is governed by the laws of England and Wales.")
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("build docx: %v", err)
	}
	src := filepath.Join(dir, "msa.docx")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatalf("write docx: %v", err)
	}

	doc, err := ooxml.OpenFile(src)
	if err != nil {
		t.Fatalf("open docx: %v", err)
	}
	oc := ooxml.NewRevisions("Opposing Counsel", time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC))
	text := doc.Text()
	i := strings.Index(text, "twelve (12)")
	if err := doc.ApplyTracked(i, i+len("twelve (12)"), "thirty-six (36)", oc); err != nil {
		t.Fatalf("opposing substitution: %v", err)
	}
	text = doc.Text()
	j := strings.Index(text, "England and Wales") + len("England and Wales")
	if err := doc.ApplyTracked(j, j, ", excluding its conflict of laws rules", oc); err != nil {
		t.Fatalf("opposing insertion: %v", err)
	}
	if err := doc.SaveFile(src); err != nil {
		t.Fatalf("save marked-up docx: %v", err)
	}
	return src
}

func TestRespondToRedlineEndToEnd(t *testing.T) {
	srv := newNegotiateFakeServer(t)
	defer srv.Close()
	reg := newNegotiateTestRegistry(t, srv.URL)
	buildOpposingDocx(t, reg.cfg.PDF.OutputDir)

	res, err := reg.Execute("respond_to_redline", map[string]interface{}{"path": "msa.docx"}, agents.ToolContext{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("respond_to_redline returned an error: %v", err)
	}
	out, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("result is %T, want map", res)
	}
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("ok = false: %v", out["error"])
	}
	if got, _ := out["changesParsed"].(int); got != 2 {
		t.Errorf("changesParsed = %v, want 2", out["changesParsed"])
	}

	outputPath, _ := out["outputPath"].(string)
	if !strings.HasSuffix(outputPath, "msa.response.docx") {
		t.Errorf("outputPath = %q, want <stem>.response.docx next to the input", outputPath)
	}

	decisions, _ := out["decisions"].([]negotiate.Decision)
	if len(decisions) != 2 {
		t.Fatalf("got %d decisions, want 2", len(decisions))
	}
	if decisions[0].Disposition != negotiate.DispositionReject ||
		decisions[0].Kind != "substitution" ||
		decisions[0].ClauseType != "Limitation of liability" {
		t.Errorf("decision 0 = %+v, want rejected Limitation of liability substitution", decisions[0])
	}
	if decisions[0].Rationale == "" || decisions[0].Author != "Opposing Counsel" {
		t.Errorf("decision 0 card incomplete: %+v", decisions[0])
	}
	if decisions[1].Disposition != negotiate.DispositionAccept || decisions[1].Kind != "insertion" {
		t.Errorf("decision 1 = %+v, want accepted insertion", decisions[1])
	}

	counts, _ := out["counts"].(map[string]int)
	if counts["accepted"] != 1 || counts["rejected"] != 1 || counts["countered"] != 0 || counts["review"] != 0 {
		t.Errorf("counts = %v, want accepted:1 rejected:1", counts)
	}

	// The response document must be a valid .docx (valid zip with a main part).
	resp, err := ooxml.OpenFile(outputPath)
	if err != nil {
		t.Fatalf("response document does not open as a .docx: %v", err)
	}

	// Rejection restored the original language visibly; the opposing number
	// is tracked-deleted (no longer visible).
	visible := resp.Text()
	if !strings.Contains(visible, "twelve (12)") {
		t.Errorf("rejected change did not restore the baseline language; visible text: %q", visible)
	}
	if strings.Contains(visible, "thirty-six (36)") {
		t.Errorf("opposing language still visible after rejection: %q", visible)
	}
	// The accepted insertion is untouched and still visible.
	if !strings.Contains(visible, ", excluding its conflict of laws rules") {
		t.Errorf("accepted opposing insertion went missing: %q", visible)
	}

	// The counter is a genuine tracked change authored by BigLaw, and the
	// accepted opposing insertion still stands under opposing authorship.
	rrevs := resp.ParseRevisions()
	var bigMichaelCounter, opposingInsertionStands bool
	for _, rv := range rrevs {
		if rv.Author == defaultRedlineAuthor && rv.Kind == ooxml.RevInsertion && rv.InsertedText == "twelve (12)" {
			bigMichaelCounter = true
		}
		if rv.Author == "Opposing Counsel" && rv.Kind == ooxml.RevInsertion &&
			rv.InsertedText == ", excluding its conflict of laws rules" {
			opposingInsertionStands = true
		}
	}
	if !bigMichaelCounter {
		t.Errorf("no counter-revision authored by %q restoring %q; revisions: %+v", defaultRedlineAuthor, "twelve (12)", rrevs)
	}
	if !opposingInsertionStands {
		t.Errorf("accepted opposing insertion was disturbed; revisions: %+v", rrevs)
	}
}

// TestRespondToRedlineRejectsEscapingPath keeps the tool inside the document
// output root.
func TestRespondToRedlineRejectsEscapingPath(t *testing.T) {
	srv := newNegotiateFakeServer(t)
	defer srv.Close()
	reg := newNegotiateTestRegistry(t, srv.URL)

	res, err := reg.Execute("respond_to_redline", map[string]interface{}{"path": "../outside.docx"}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	out, _ := res.(map[string]interface{})
	if okFlag, _ := out["ok"].(bool); okFlag {
		t.Error("path traversal outside the output root was not rejected")
	}
}

// TestRespondToRedlineNoChanges: a clean document yields an empty decision
// set rather than an error.
func TestRespondToRedlineNoChanges(t *testing.T) {
	srv := newNegotiateFakeServer(t)
	defer srv.Close()
	reg := newNegotiateTestRegistry(t, srv.URL)

	b := ooxml.NewBuilder()
	b.Paragraph("Nothing was changed in this document.")
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("build docx: %v", err)
	}
	src := filepath.Join(reg.cfg.PDF.OutputDir, "clean.docx")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatalf("write docx: %v", err)
	}

	res, err := reg.Execute("respond_to_redline", map[string]interface{}{"path": "clean.docx"}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("respond_to_redline returned an error: %v", err)
	}
	out, _ := res.(map[string]interface{})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("ok = false on a clean document: %v", out["error"])
	}
	if got, _ := out["changesParsed"].(int); got != 0 {
		t.Errorf("changesParsed = %v, want 0", out["changesParsed"])
	}
	if decisions, _ := out["decisions"].([]negotiate.Decision); len(decisions) != 0 {
		t.Errorf("decisions = %d, want 0", len(decisions))
	}
}
