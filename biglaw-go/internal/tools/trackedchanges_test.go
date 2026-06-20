// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Unit tests for the DOCX tracked-changes engine (trackedchanges.go) using
// small in-memory .docx fixtures: insertion, deletion, replacement, edits
// spanning multiple runs, ambiguity, whitespace tolerance, and no-match.

package tools

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
)

// ─── Fixture helpers ──────────────────────────────────────────────────────────

func fixtureRun(text string) string {
	return `<w:r><w:t xml:space="preserve">` + docxEscape(text) + `</w:t></w:r>`
}

func fixturePara(inner ...string) string {
	return "<w:p>" + strings.Join(inner, "") + "</w:p>"
}

func fixtureDocx(t *testing.T, bodyXML string) []byte {
	t.Helper()
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		bodyXML + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr></w:body></w:document>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`},
		{"word/document.xml", document},
	}
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			t.Fatalf("zip create %s: %v", p.name, err)
		}
		if _, err := w.Write([]byte(p.body)); err != nil {
			t.Fatalf("zip write %s: %v", p.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func documentXMLOf(t *testing.T, docxBytes []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(docxBytes), int64(len(docxBytes)))
	if err != nil {
		t.Fatalf("output is not a zip: %v", err)
	}
	entry := findZipEntry(zr, "word/document.xml")
	if entry == nil {
		t.Fatalf("output has no word/document.xml")
	}
	raw, err := readZipEntry(entry)
	if err != nil {
		t.Fatalf("read document.xml: %v", err)
	}
	return string(raw)
}

func acceptedText(t *testing.T, docxBytes []byte) string {
	t.Helper()
	text, err := extractDocxBodyText(docxBytes)
	if err != nil {
		t.Fatalf("extractDocxBodyText: %v", err)
	}
	return text
}

// ─── Replacement ──────────────────────────────────────────────────────────────

func TestApplyTrackedEditsReplacement(t *testing.T) {
	doc := fixtureDocx(t, fixturePara(fixtureRun("The party shall pay within 30 days of the invoice date.")))

	out, changes, errs, err := applyTrackedEdits(doc, []EditInput{{
		Find: "30 days", Replace: "45 days",
		ContextBefore: "pay within ", ContextAfter: " of the invoice",
		Reason: "Extend payment terms",
	}}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no edit errors, got %+v", errs)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	// collapseDiff strips the common " days" suffix → minimal tracked range.
	if changes[0].DeletedText != "30" || changes[0].InsertedText != "45" {
		t.Errorf("expected minimal diff 30→45, got %q→%q", changes[0].DeletedText, changes[0].InsertedText)
	}
	if changes[0].DelID == "" || changes[0].InsID == "" {
		t.Errorf("expected both delId and insId, got %+v", changes[0])
	}

	got := acceptedText(t, out)
	want := "The party shall pay within 45 days of the invoice date."
	if got != want {
		t.Errorf("accepted view = %q, want %q", got, want)
	}

	xml := documentXMLOf(t, out)
	if !strings.Contains(xml, `w:author="Big Michael"`) {
		t.Errorf("expected default author attribute, xml: %s", xml)
	}
	if !strings.Contains(xml, "<w:delText") || !strings.Contains(xml, "<w:ins ") {
		t.Errorf("expected w:delText and w:ins in output xml: %s", xml)
	}
	if !strings.Contains(xml, `w:date="`) {
		t.Errorf("expected w:date attribute in output xml")
	}
}

// ─── Deletion ─────────────────────────────────────────────────────────────────

func TestApplyTrackedEditsDeletion(t *testing.T) {
	doc := fixtureDocx(t, fixturePara(fixtureRun("This constitutes a material breach of the Agreement.")))

	out, changes, errs, err := applyTrackedEdits(doc, []EditInput{{
		Find: "material ", Replace: "",
		ContextBefore: "constitutes a ", ContextAfter: "breach",
	}}, "Reviewer")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no edit errors, got %+v", errs)
	}
	if len(changes) != 1 || changes[0].DeletedText != "material " || changes[0].InsertedText != "" {
		t.Fatalf("expected pure deletion of 'material ', got %+v", changes)
	}
	if changes[0].DelID == "" || changes[0].InsID != "" {
		t.Errorf("pure deletion should have only delId, got %+v", changes[0])
	}

	got := acceptedText(t, out)
	want := "This constitutes a breach of the Agreement."
	if got != want {
		t.Errorf("accepted view = %q, want %q", got, want)
	}
	if xml := documentXMLOf(t, out); !strings.Contains(xml, `w:author="Reviewer"`) {
		t.Errorf("expected custom author attribute")
	}
}

// ─── Pure insertion ───────────────────────────────────────────────────────────

func TestApplyTrackedEditsPureInsertion(t *testing.T) {
	doc := fixtureDocx(t, fixturePara(fixtureRun("The Supplier shall notify the Buyer of any defect.")))

	out, changes, errs, err := applyTrackedEdits(doc, []EditInput{{
		Find: "", Replace: "promptly ",
		ContextBefore: "Supplier shall ", ContextAfter: "notify",
	}}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no edit errors, got %+v", errs)
	}
	if len(changes) != 1 || changes[0].InsertedText != "promptly " || changes[0].DeletedText != "" {
		t.Fatalf("expected pure insertion of 'promptly ', got %+v", changes)
	}
	if changes[0].InsID == "" || changes[0].DelID != "" {
		t.Errorf("pure insertion should have only insId, got %+v", changes[0])
	}

	got := acceptedText(t, out)
	want := "The Supplier shall promptly notify the Buyer of any defect."
	if got != want {
		t.Errorf("accepted view = %q, want %q", got, want)
	}
	// A pure insertion must not emit any deletion markup.
	if xml := documentXMLOf(t, out); strings.Contains(xml, "<w:del ") {
		t.Errorf("pure insertion should not produce w:del, xml: %s", xml)
	}
}

// Pure insertion with no anchoring context must be rejected.
func TestApplyTrackedEditsInsertionRequiresContext(t *testing.T) {
	doc := fixtureDocx(t, fixturePara(fixtureRun("Some text.")))
	_, changes, errs, err := applyTrackedEdits(doc, []EditInput{{Find: "", Replace: "x"}}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(changes) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Reason, "context_before or context_after") {
		t.Fatalf("expected context-required error, got changes=%+v errs=%+v", changes, errs)
	}
}

// ─── Text spanning multiple runs ──────────────────────────────────────────────

func TestApplyTrackedEditsSpansMultipleRuns(t *testing.T) {
	// The find string straddles three separate w:r elements.
	doc := fixtureDocx(t, fixturePara(
		fixtureRun("The Purchase Pri"),
		fixtureRun("ce shall be USD 1,0"),
		fixtureRun("00,000 payable in cash at Closing."),
	))

	out, changes, errs, err := applyTrackedEdits(doc, []EditInput{{
		Find: "USD 1,000,000", Replace: "USD 2,500,000",
		ContextBefore: "shall be ", ContextAfter: " payable",
	}}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no edit errors, got %+v", errs)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}

	got := acceptedText(t, out)
	want := "The Purchase Price shall be USD 2,500,000 payable in cash at Closing."
	if got != want {
		t.Errorf("accepted view = %q, want %q", got, want)
	}
	// Untouched text before/after the span must survive verbatim.
	if !strings.Contains(got, "The Purchase Price shall be") || !strings.Contains(got, "at Closing.") {
		t.Errorf("surrounding text mangled: %q", got)
	}
}

// ─── No match ─────────────────────────────────────────────────────────────────

func TestApplyTrackedEditsNoMatch(t *testing.T) {
	original := "The Agreement is governed by the laws of England and Wales."
	doc := fixtureDocx(t, fixturePara(fixtureRun(original)))

	out, changes, errs, err := applyTrackedEdits(doc, []EditInput{{
		Find: "laws of Delaware", Replace: "laws of New York",
		ContextBefore: "governed by the ", ContextAfter: ".",
	}}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes, got %+v", changes)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Reason, "Could not locate") {
		t.Fatalf("expected a 'Could not locate' error, got %+v", errs)
	}
	if got := acceptedText(t, out); got != original {
		t.Errorf("document must be unchanged on no-match, got %q", got)
	}
}

// ─── Ambiguous match ──────────────────────────────────────────────────────────

func TestApplyTrackedEditsAmbiguous(t *testing.T) {
	doc := fixtureDocx(t, fixturePara(
		fixtureRun("The Seller shall indemnify the Buyer and the Seller shall hold harmless the Buyer."),
	))

	_, changes, errs, err := applyTrackedEdits(doc, []EditInput{{
		Find: "the Buyer", Replace: "the Purchaser",
	}}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(changes) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Reason, "Ambiguous match") {
		t.Fatalf("expected ambiguity error, got changes=%+v errs=%+v", changes, errs)
	}
}

// ─── Whitespace / smart-character tolerance ───────────────────────────────────

func TestApplyTrackedEditsWhitespaceTolerantAnchor(t *testing.T) {
	// Document uses an NBSP between "Section" and "4.2"; the model's find
	// string uses a plain space. The normalizing matcher must still anchor,
	// and the deleted text must preserve the document's original NBSP.
	doc := fixtureDocx(t, fixturePara(fixtureRun("Section\u00A04.2 shall not apply to Affiliates.")))

	out, changes, errs, err := applyTrackedEdits(doc, []EditInput{{
		Find: "Section 4.2", Replace: "Clause 4.2",
		ContextAfter: " shall not apply",
	}}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("expected no edit errors, got %+v", errs)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if !strings.Contains(changes[0].DeletedText, "Section") {
		t.Errorf("deletedText should carry original document text, got %q", changes[0].DeletedText)
	}
	got := acceptedText(t, out)
	if !strings.HasPrefix(got, "Clause") || !strings.Contains(got, "4.2 shall not apply") {
		t.Errorf("accepted view = %q, want Clause 4.2 …", got)
	}
}

// ─── Pre-existing tracked changes (accepted view) ─────────────────────────────

func TestApplyTrackedEditsOverExistingIns(t *testing.T) {
	// A paragraph that already contains a w:ins (id 5): the matcher must see
	// its text in accepted view, and new w:ids must start above 5.
	body := "<w:p>" +
		fixtureRun("The fee is ") +
		`<w:ins w:id="5" w:author="Earlier" w:date="2026-01-01T00:00:00Z">` + fixtureRun("EUR 100 ") + `</w:ins>` +
		fixtureRun("per month.") +
		"</w:p>"
	doc := fixtureDocx(t, body)

	if got := acceptedText(t, doc); got != "The fee is EUR 100 per month." {
		t.Fatalf("fixture accepted view = %q", got)
	}

	out, changes, errs, err := applyTrackedEdits(doc, []EditInput{{
		Find: "EUR 100", Replace: "EUR 250",
		ContextBefore: "fee is ", ContextAfter: " per month",
	}}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(errs) != 0 || len(changes) != 1 {
		t.Fatalf("expected 1 clean change, got changes=%+v errs=%+v", changes, errs)
	}
	// New w:ids must be allocated above the pre-existing max (5).
	if id, convErr := strconv.Atoi(changes[0].DelID); convErr != nil || id <= 5 {
		t.Errorf("new w:id %q should be an integer > 5", changes[0].DelID)
	}
	got := acceptedText(t, out)
	want := "The fee is EUR 250 per month."
	if got != want {
		t.Errorf("accepted view = %q, want %q", got, want)
	}
}

// ─── Multiple edits in one batch ──────────────────────────────────────────────

func TestApplyTrackedEditsBatch(t *testing.T) {
	doc := fixtureDocx(t,
		fixturePara(fixtureRun("The term is 12 months."))+
			fixturePara(fixtureRun("Notice period: 30 days.")),
	)

	out, changes, errs, err := applyTrackedEdits(doc, []EditInput{
		{Find: "12 months", Replace: "24 months", ContextBefore: "term is "},
		{Find: "30 days", Replace: "60 days", ContextBefore: "Notice period: "},
		{Find: "does not exist", Replace: "x"},
	}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 applied changes, got %+v", changes)
	}
	if len(errs) != 1 || errs[0].Index != 2 {
		t.Fatalf("expected 1 error at index 2, got %+v", errs)
	}
	got := acceptedText(t, out)
	want := "The term is 24 months.\nNotice period: 60 days."
	if got != want {
		t.Errorf("accepted view = %q, want %q", got, want)
	}
}

// Overlapping edits in the same paragraph: the second must be rejected.
func TestApplyTrackedEditsOverlapRejected(t *testing.T) {
	doc := fixtureDocx(t, fixturePara(fixtureRun("Payment due within thirty (30) days.")))

	_, changes, errs, err := applyTrackedEdits(doc, []EditInput{
		{Find: "thirty (30) days", Replace: "sixty (60) days", ContextBefore: "within "},
		{Find: "(30)", Replace: "(45)", ContextBefore: "thirty "},
	}, "")
	if err != nil {
		t.Fatalf("applyTrackedEdits: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 applied change, got %+v", changes)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Reason, "Overlaps") {
		t.Fatalf("expected overlap error, got %+v", errs)
	}
}

// ─── edit_document tool (registry level) ──────────────────────────────────────

func TestEditDocumentTool(t *testing.T) {
	outDir := t.TempDir()
	cfg := &config.Config{}
	cfg.PDF.OutputDir = outDir
	reg := NewRegistry(cfg, nil, nil, nil)
	// registerAll does not yet wire the docx tool groups; register explicitly.
	reg.registerTrackedChangesTools()

	src := filepath.Join(outDir, "agreement.docx")
	if err := os.WriteFile(src, fixtureDocx(t, fixturePara(fixtureRun("Liability is capped at GBP 1m."))), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := reg.tools["edit_document"]
	if tool == nil {
		t.Fatal("edit_document not registered")
	}
	res, err := tool.Exec(map[string]interface{}{
		"path": "agreement.docx",
		"edits": []interface{}{map[string]interface{}{
			"find": "GBP 1m", "replace": "GBP 5m",
			"context_before": "capped at ", "context_after": ".",
		}},
	}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("edit_document: %v", err)
	}
	m := res.(map[string]interface{})
	if m["ok"] != true {
		t.Fatalf("edit_document not ok: %+v", m)
	}
	outPath, _ := m["outputPath"].(string)
	if !strings.HasSuffix(outPath, "agreement.redlined.docx") {
		t.Errorf("unexpected output path %q", outPath)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read redlined output: %v", err)
	}
	if got := acceptedText(t, data); got != "Liability is capped at GBP 5m." {
		t.Errorf("accepted view = %q", got)
	}

	// Path traversal: an absolute path outside the output dir is re-anchored
	// to the output dir basename, never read from its original location.
	res2, err := tool.Exec(map[string]interface{}{
		"path":  "/etc/passwd.docx",
		"edits": []interface{}{map[string]interface{}{"find": "a", "replace": "b", "context_before": "", "context_after": ""}},
	}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("edit_document traversal probe: %v", err)
	}
	if res2.(map[string]interface{})["ok"] != false {
		t.Errorf("expected traversal probe to fail cleanly, got %+v", res2)
	}
}
