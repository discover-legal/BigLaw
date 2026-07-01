// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package tools

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCraftedDocx writes a minimal .docx with the given document.xml body
// into dir, so anchoring can be tested against run layouts Word produces
// (e.g. a phrase split across two runs).
func writeCraftedDocx(t *testing.T, dir, name, body string) string {
	t.Helper()
	docXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		body +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr></w:body></w:document>`
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, p := range [][2]string{
		{"[Content_Types].xml", "<Types/>"},
		{"_rels/.rels", "<Relationships/>"},
		{"word/document.xml", docXML},
	} {
		w, err := zw.Create(p[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(p[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func edit(find, replace, before, after string) map[string]interface{} {
	return map[string]interface{}{
		"find":           find,
		"replace":        replace,
		"context_before": before,
		"context_after":  after,
		"reason":         "test edit",
	}
}

// ─── Acceptance §8.3: generate → edit round-trip ──────────────────────────────

func TestEditDocumentRoundTrip(t *testing.T) {
	r, _ := newDocToolsRegistry(t)
	src := generateSampleDoc(t, r, nil) // contains "The margin is 2.5 percent over SONIA."

	res := execTool(t, r, "edit_document", map[string]interface{}{
		"path":   src,
		"author": "Reviewing Partner",
		"edits": []interface{}{
			edit("2.5 percent", "3.0 percent", "The margin is ", " over SONIA."),
		},
	})
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("edit_document failed: %v", res)
	}
	if got := res["appliedCount"].(int); got != 1 {
		t.Errorf("appliedCount = %d, want 1", got)
	}
	if got := res["errorCount"].(int); got != 0 {
		t.Errorf("errorCount = %d, want 0", got)
	}
	out := res["outputPath"].(string)
	if !strings.HasSuffix(out, ".redlined.docx") {
		t.Errorf("output %q does not end in .redlined.docx", out)
	}
	wantStem := strings.TrimSuffix(src, ".docx") + ".redlined.docx"
	if out != wantStem {
		t.Errorf("output = %q, want sibling %q", out, wantStem)
	}

	doc, names := readDocxPart(t, out, "word/document.xml")
	if !strings.Contains(strings.Join(names, "|"), "word/document.xml") {
		t.Fatal("redlined output is not a valid .docx")
	}
	if n := strings.Count(doc, "<w:ins "); n != 1 {
		t.Errorf("want exactly 1 insertion revision, got %d", n)
	}
	if n := strings.Count(doc, "<w:del "); n != 1 {
		t.Errorf("want exactly 1 deletion revision, got %d", n)
	}
	for _, snippet := range []string{
		`w:author="Reviewing Partner"`,
		`<w:delText xml:space="preserve">2.5 percent</w:delText>`,
		">3.0 percent</w:t>",
	} {
		if !strings.Contains(doc, snippet) {
			t.Errorf("redlined document.xml missing %q", snippet)
		}
	}
}

// ─── Acceptance §8.4: anchoring tolerance ─────────────────────────────────────

func TestEditAnchoringAcrossRuns(t *testing.T) {
	r, root := newDocToolsRegistry(t)
	// The find target "1,000,000" begins in one run and ends in another.
	src := writeCraftedDocx(t, root, "split-runs.docx",
		`<w:p><w:r><w:t>The purchase price is USD 1,0</w:t></w:r>`+
			`<w:r><w:t>00,000 payable at closing.</w:t></w:r></w:p>`)

	res := execTool(t, r, "edit_document", map[string]interface{}{
		"path": src,
		"edits": []interface{}{
			edit("1,000,000", "1,250,000", "purchase price is USD ", " payable at closing"),
		},
	})
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("edit_document failed: %v", res)
	}
	if got := res["appliedCount"].(int); got != 1 {
		t.Fatalf("appliedCount = %d, want 1 (errors: %v)", got, res["errors"])
	}
	doc, _ := readDocxPart(t, res["outputPath"].(string), "word/document.xml")
	if !strings.Contains(doc, "1,250,000") {
		t.Error("replacement text missing from redlined output")
	}
	if strings.Count(doc, "<w:del ") != 2 { // one per affected run
		t.Errorf("cross-run deletion should split per run, got %d w:del", strings.Count(doc, "<w:del "))
	}
}

func TestEditAnchoringCurlyQuotes(t *testing.T) {
	r, root := newDocToolsRegistry(t)
	// Document uses curly quotes; the edit uses straight quotes.
	src := writeCraftedDocx(t, root, "curly.docx",
		`<w:p><w:r><w:t>The term “Confidential Information” excludes public data.</w:t></w:r></w:p>`)

	res := execTool(t, r, "edit_document", map[string]interface{}{
		"path": src,
		"edits": []interface{}{
			edit(`"Confidential Information"`, `"Proprietary Information"`, "The term ", " excludes public data"),
		},
	})
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("edit_document failed: %v", res)
	}
	if got := res["appliedCount"].(int); got != 1 {
		t.Fatalf("appliedCount = %d, want 1 (errors: %v)", got, res["errors"])
	}
	anns := res["annotations"].([]map[string]interface{})
	if anns[0]["matchedBy"] != "normalized" {
		t.Errorf("matchedBy = %v, want normalized", anns[0]["matchedBy"])
	}
	doc, _ := readDocxPart(t, res["outputPath"].(string), "word/document.xml")
	if !strings.Contains(doc, "Proprietary Information") {
		t.Error("replacement missing from redlined output")
	}
}

// ─── Acceptance §8.5: an unfindable anchor does not abort the rest ────────────

func TestEditMissLandsInErrorsWithoutAborting(t *testing.T) {
	r, _ := newDocToolsRegistry(t)
	src := generateSampleDoc(t, r, nil)

	res := execTool(t, r, "edit_document", map[string]interface{}{
		"path": src,
		"edits": []interface{}{
			edit("no such phrase anywhere", "x", "nonexistent before", "nonexistent after"),
			edit("guarantee from the parent", "guarantee from the sponsor", "assets", "."),
		},
	})
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("edit_document failed outright: %v", res)
	}
	if got := res["appliedCount"].(int); got != 1 {
		t.Errorf("appliedCount = %d, want 1 (errors: %v)", got, res["errors"])
	}
	if got := res["errorCount"].(int); got != 1 {
		t.Errorf("errorCount = %d, want 1", got)
	}
	errs := res["errors"].([]map[string]interface{})
	if len(errs) != 1 || errs[0]["find"] != "no such phrase anywhere" {
		t.Errorf("errors = %v", errs)
	}
	anns := res["annotations"].([]map[string]interface{})
	if len(anns) != 1 || anns[0]["find"] != "guarantee from the parent" {
		t.Errorf("annotations = %v", anns)
	}
}

func TestEditDocumentRejectsEmptyEdits(t *testing.T) {
	r, _ := newDocToolsRegistry(t)
	src := generateSampleDoc(t, r, nil)
	res := execTool(t, r, "edit_document", map[string]interface{}{
		"path":  src,
		"edits": []interface{}{},
	})
	if ok, _ := res["ok"].(bool); ok {
		t.Errorf("expected rejection of empty edits, got %v", res)
	}
}

// ─── locateEdit unit coverage ─────────────────────────────────────────────────

func TestLocateEditDisambiguatesByContext(t *testing.T) {
	text := "the fee is 5% for advice and the fee is 5% for filing"
	start, end, strategy, ok := locateEdit(text, "5%", "the fee is ", " for filing")
	if !ok {
		t.Fatal("locateEdit failed")
	}
	if strategy != "exact" {
		t.Errorf("strategy = %q, want exact", strategy)
	}
	if text[start:end] != "5%" || start != strings.LastIndex(text, "5%") {
		t.Errorf("picked span [%d,%d) = %q; want the second occurrence", start, end, text[start:end])
	}
}

func TestLocateEditContextOnlyFallback(t *testing.T) {
	text := "delivery within  fourteen (14)  days of notice"
	// find does not appear (different wording), but the contexts pin the span.
	start, end, strategy, ok := locateEdit(text, "fifteen (15)", "delivery within ", " days of notice")
	if !ok {
		t.Fatal("locateEdit failed")
	}
	if strategy != "context" {
		t.Errorf("strategy = %q, want context", strategy)
	}
	if got := strings.TrimSpace(text[start:end]); got != "fourteen (14)" {
		t.Errorf("span = %q, want the text between the contexts", text[start:end])
	}
}
