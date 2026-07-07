// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tool-level tests for check_document_integrity (both input modes, bad
// input) and the integrity wiring inside respond_to_redline. No network, no
// models — check_document_integrity makes no model calls at all, and the
// redline test reuses the scripted fake server from negotiate_test.go.

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/integrity"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/textdiff"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// writeDocx builds a .docx from paragraphs inside the registry's output dir.
func writeDocx(t *testing.T, dir, name string, paras ...string) string {
	t.Helper()
	b := ooxml.NewBuilder()
	for _, p := range paras {
		b.Paragraph(p)
	}
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("build docx: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write docx: %v", err)
	}
	return path
}

// fakeKS is a minimal agents.KnowledgeStore for document_id mode.
type fakeKS struct{ docs map[string]string }

func (f *fakeKS) Search(string, string, int) ([]types.SearchResult, error) { return nil, nil }
func (f *fakeKS) GetFullText(docID string) (string, error)                 { return f.docs[docID], nil }
func (f *fakeKS) GetByID(string) *types.Document                           { return nil }

func execIntegrity(t *testing.T, reg *Registry, input map[string]interface{}, ctx agents.ToolContext) map[string]interface{} {
	t.Helper()
	res, err := reg.Execute("check_document_integrity", input, ctx)
	if err != nil {
		t.Fatalf("check_document_integrity returned a hard error: %v", err)
	}
	out, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("result is %T, want map", res)
	}
	return out
}

func TestCheckIntegrityPathModeFindsObfuscation(t *testing.T) {
	reg := newNegotiateTestRegistry(t, "http://127.0.0.1:0")
	// Cyrillic а in "Pаyment" plus a zero-width space.
	writeDocx(t, reg.cfg.PDF.OutputDir, "shady.docx",
		"The Pаyment shall be due within thirty\u200Bdays.")

	out := execIntegrity(t, reg, map[string]interface{}{"path": "shady.docx"}, agents.ToolContext{})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("ok = false: %v", out["error"])
	}
	if clean, _ := out["clean"].(bool); clean {
		t.Error("clean = true on an obfuscated document")
	}
	findings, _ := out["findings"].([]integrity.Finding)
	var homo, inv bool
	for _, f := range findings {
		if f.Kind == integrity.KindHomoglyph {
			homo = true
		}
		if f.Kind == integrity.KindInvisibleChar {
			inv = true
		}
	}
	if !homo || !inv {
		t.Errorf("findings missing homoglyph/invisible: %+v", findings)
	}
	summary, _ := out["summary"].(string)
	if !strings.Contains(summary, "Integrity issues found") {
		t.Errorf("summary = %q", summary)
	}
	if _, present := out["unmarkedChanges"]; present {
		t.Error("unmarkedChanges present without a prior version")
	}
}

func TestCheckIntegrityPathModeCleanDocument(t *testing.T) {
	reg := newNegotiateTestRegistry(t, "http://127.0.0.1:0")
	writeDocx(t, reg.cfg.PDF.OutputDir, "clean.docx",
		"The liability cap is twelve (12) months of fees paid under this Agreement.")

	out := execIntegrity(t, reg, map[string]interface{}{"path": "clean.docx"}, agents.ToolContext{})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("ok = false: %v", out["error"])
	}
	if clean, _ := out["clean"].(bool); !clean {
		t.Errorf("clean = false on a clean document: %v", out["findings"])
	}
	if summary, _ := out["summary"].(string); !strings.Contains(summary, "Document is clean") {
		t.Errorf("summary = %q", summary)
	}
}

func TestCheckIntegrityUnmarkedChanges(t *testing.T) {
	reg := newNegotiateTestRegistry(t, "http://127.0.0.1:0")
	dir := reg.cfg.PDF.OutputDir
	const p1 = "The liability cap is twelve (12) months of fees."
	const p2 = "Either party may terminate on thirty days notice."
	writeDocx(t, dir, "v1.docx", p1, p2)

	// Received: silent edit (thirty → ten) baked into the underlying text,
	// plus one honest tracked change on top.
	writeDocx(t, dir, "received.docx", p1, strings.Replace(p2, "thirty", "ten", 1))
	doc, err := ooxml.OpenFile(filepath.Join(dir, "received.docx"))
	if err != nil {
		t.Fatalf("open received: %v", err)
	}
	rev := ooxml.NewRevisions("Opposing Counsel", time.Now().UTC())
	text := doc.Text()
	i := strings.Index(text, "twelve (12)")
	if err := doc.ApplyTracked(i, i+len("twelve (12)"), "twenty-four (24)", rev); err != nil {
		t.Fatalf("ApplyTracked: %v", err)
	}
	if err := doc.SaveFile(filepath.Join(dir, "received.docx")); err != nil {
		t.Fatalf("save received: %v", err)
	}

	out := execIntegrity(t, reg, map[string]interface{}{
		"path":               "received.docx",
		"prior_version_path": "v1.docx",
	}, agents.ToolContext{})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("ok = false: %v", out["error"])
	}
	if clean, _ := out["clean"].(bool); clean {
		t.Error("clean = true despite a silent edit")
	}
	um, _ := out["unmarkedChanges"].(map[string]interface{})
	if um == nil {
		t.Fatal("unmarkedChanges missing")
	}
	hunks, _ := um["hunks"].([]textdiff.Hunk)
	if count, _ := um["count"].(int); count == 0 || len(hunks) == 0 {
		t.Fatalf("no unmarked changes reported: %v", um)
	}
	for _, h := range hunks {
		if strings.Contains(h.OurText, "twelve") || strings.Contains(h.TheirText, "twenty-four") {
			t.Errorf("tracked change leaked into unmarked report: %+v", h)
		}
	}
	if summary, _ := out["summary"].(string); !strings.Contains(summary, "unmarked change") {
		t.Errorf("summary = %q", summary)
	}
}

func TestCheckIntegrityTxtPriorVersion(t *testing.T) {
	reg := newNegotiateTestRegistry(t, "http://127.0.0.1:0")
	dir := reg.cfg.PDF.OutputDir
	const para = "Either party may terminate on thirty days notice."
	if err := os.WriteFile(filepath.Join(dir, "v1.txt"), []byte(para), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}
	writeDocx(t, dir, "received.docx", para) // unchanged → clean

	out := execIntegrity(t, reg, map[string]interface{}{
		"path":               "received.docx",
		"prior_version_path": "v1.txt",
	}, agents.ToolContext{})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("ok = false: %v", out["error"])
	}
	if clean, _ := out["clean"].(bool); !clean {
		t.Errorf("clean = false on an unchanged document: %v", out)
	}
	um, _ := out["unmarkedChanges"].(map[string]interface{})
	if um == nil {
		t.Fatal("unmarkedChanges missing despite prior version")
	}
	if count, _ := um["count"].(int); count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestCheckIntegrityDocumentIDMode(t *testing.T) {
	reg := newNegotiateTestRegistry(t, "http://127.0.0.1:0")
	ks := &fakeKS{docs: map[string]string{
		"doc-1": "The fee is \u202E000,001\u202C dollars.",
		"doc-2": "Perfectly ordinary text.",
	}}

	out := execIntegrity(t, reg, map[string]interface{}{"document_id": "doc-1"},
		agents.ToolContext{KnowledgeStore: ks})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("ok = false: %v", out["error"])
	}
	if clean, _ := out["clean"].(bool); clean {
		t.Error("clean = true on bidi-laced content")
	}

	out = execIntegrity(t, reg, map[string]interface{}{"document_id": "doc-2"},
		agents.ToolContext{KnowledgeStore: ks})
	if clean, _ := out["clean"].(bool); !clean {
		t.Errorf("clean = false on clean content: %v", out["findings"])
	}
}

func TestCheckIntegrityBadInput(t *testing.T) {
	reg := newNegotiateTestRegistry(t, "http://127.0.0.1:0")
	ks := &fakeKS{docs: map[string]string{}}

	cases := []struct {
		name  string
		input map[string]interface{}
		ctx   agents.ToolContext
	}{
		{"neither path nor document_id", map[string]interface{}{}, agents.ToolContext{}},
		{"both path and document_id", map[string]interface{}{"path": "a.docx", "document_id": "d1"}, agents.ToolContext{}},
		{"prior with document_id", map[string]interface{}{"document_id": "d1", "prior_version_path": "v1.docx"}, agents.ToolContext{KnowledgeStore: ks}},
		{"path escaping the output root", map[string]interface{}{"path": "../outside.docx"}, agents.ToolContext{}},
		{"non-docx path", map[string]interface{}{"path": "notes.txt"}, agents.ToolContext{}},
		{"unknown document_id", map[string]interface{}{"document_id": "missing"}, agents.ToolContext{KnowledgeStore: ks}},
		{"document_id without knowledge store", map[string]interface{}{"document_id": "d1"}, agents.ToolContext{}},
	}
	for _, tc := range cases {
		out := execIntegrity(t, reg, tc.input, tc.ctx)
		if okFlag, _ := out["ok"].(bool); okFlag {
			t.Errorf("%s: ok = true, want structured failure", tc.name)
		} else if msg, _ := out["error"].(string); msg == "" {
			t.Errorf("%s: no error message", tc.name)
		}
	}
}

// TestRespondToRedlineIntegrity: the negotiation tool always carries an
// integrity block — clean when the opposing paper only has tracked changes,
// flagged when a silent edit rode along — and never aborts on findings.
func TestRespondToRedlineIntegrity(t *testing.T) {
	srv := newNegotiateFakeServer(t)
	defer srv.Close()
	reg := newNegotiateTestRegistry(t, srv.URL)
	dir := reg.cfg.PDF.OutputDir

	// Prior = the document as we sent it (matches buildOpposingDocx's base).
	writeDocx(t, dir, "prior.docx",
		"The liability cap is twelve (12) months of fees paid under this Agreement.",
		"This Agreement is governed by the laws of England and Wales.")
	buildOpposingDocx(t, dir) // msa.docx: tracked changes only

	res, err := reg.Execute("respond_to_redline", map[string]interface{}{
		"path":               "msa.docx",
		"prior_version_path": "prior.docx",
	}, agents.ToolContext{TaskID: "task-int"})
	if err != nil {
		t.Fatalf("respond_to_redline error: %v", err)
	}
	out, _ := res.(map[string]interface{})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("ok = false: %v", out["error"])
	}
	integ, _ := out["integrity"].(map[string]interface{})
	if integ == nil {
		t.Fatal("integrity block missing from respond_to_redline result")
	}
	if clean, _ := integ["clean"].(bool); !clean {
		t.Errorf("clean = false on a tracked-changes-only document: %+v", integ)
	}
	um, _ := integ["unmarkedChanges"].(map[string]interface{})
	if um == nil {
		t.Fatal("unmarkedChanges missing despite prior_version_path")
	}
	if count, _ := um["count"].(int); count != 0 {
		t.Errorf("unmarked count = %d, want 0", count)
	}

	// Now a received version with a silent edit baked in: the integrity block
	// must flag it while the negotiation still completes.
	writeDocx(t, dir, "sneaky.docx",
		"The liability cap is six (6) months of fees paid under this Agreement.", // silent!
		"This Agreement is governed by the laws of England and Wales.")
	doc, err := ooxml.OpenFile(filepath.Join(dir, "sneaky.docx"))
	if err != nil {
		t.Fatalf("open sneaky: %v", err)
	}
	rev := ooxml.NewRevisions("Opposing Counsel", time.Now().UTC())
	text := doc.Text()
	i := strings.Index(text, "England and Wales") + len("England and Wales")
	if err := doc.ApplyTracked(i, i, ", excluding its conflict of laws rules", rev); err != nil {
		t.Fatalf("ApplyTracked: %v", err)
	}
	if err := doc.SaveFile(filepath.Join(dir, "sneaky.docx")); err != nil {
		t.Fatalf("save sneaky: %v", err)
	}

	res, err = reg.Execute("respond_to_redline", map[string]interface{}{
		"path":               "sneaky.docx",
		"prior_version_path": "prior.docx",
	}, agents.ToolContext{TaskID: "task-int"})
	if err != nil {
		t.Fatalf("respond_to_redline error: %v", err)
	}
	out, _ = res.(map[string]interface{})
	if okFlag, _ := out["ok"].(bool); !okFlag {
		t.Fatalf("ok = false — integrity findings must not abort the run: %v", out["error"])
	}
	integ, _ = out["integrity"].(map[string]interface{})
	if clean, _ := integ["clean"].(bool); clean {
		t.Error("clean = true despite the silent liability-cap edit")
	}
	um, _ = integ["unmarkedChanges"].(map[string]interface{})
	if count, _ := um["count"].(int); count == 0 {
		t.Errorf("silent edit not reported: %+v", um)
	}
	hunks, _ := um["hunks"].([]textdiff.Hunk)
	var sawSilent bool
	for _, h := range hunks {
		if strings.Contains(h.OurText, "twelve") || strings.Contains(h.TheirText, "six") {
			sawSilent = true
		}
		if strings.Contains(h.TheirText, "excluding its conflict") {
			t.Errorf("tracked insertion leaked into unmarked report: %+v", h)
		}
	}
	if !sawSilent {
		t.Errorf("silent edit hunks missing: %+v", hunks)
	}
}
