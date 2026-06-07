// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// appendRaw appends raw bytes to a file (test helper, shared across the package).
func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestDocxBuilderProducesValidZip(t *testing.T) {
	d := &docxBuilder{}
	d.Heading(1, "Matter Status — M-001")
	d.Para("A normal paragraph with <special> & \"chars\".")
	d.Heading(2, "Risks")
	d.Bullet("High risk item")
	d.Labeled("Owner", "Jane Partner")

	b, err := d.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}

	required := map[string]bool{
		"[Content_Types].xml": false,
		"_rels/.rels":         false,
		"word/document.xml":   false,
	}
	var docXML string
	for _, f := range zr.File {
		if _, ok := required[f.Name]; ok {
			required[f.Name] = true
		}
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			raw, _ := io.ReadAll(rc)
			rc.Close()
			docXML = string(raw)
		}
	}
	for name, found := range required {
		if !found {
			t.Errorf("missing required part %q", name)
		}
	}

	// Special characters must be XML-escaped, not raw.
	if strings.Contains(docXML, "<special>") {
		t.Error("special characters were not escaped in document.xml")
	}
	if !strings.Contains(docXML, "&lt;special&gt;") {
		t.Error("expected escaped &lt;special&gt; in document.xml")
	}
	if !strings.Contains(docXML, "Matter Status") {
		t.Error("heading text missing from document.xml")
	}
}

func TestRenderDOCXFromReport(t *testing.T) {
	r := newReport("M-001", "2026-06-07")
	r.Summary = "Things are progressing."
	b, err := RenderDOCX(r)
	if err != nil {
		t.Fatalf("RenderDOCX: %v", err)
	}
	if _, err := zip.NewReader(bytes.NewReader(b), int64(len(b))); err != nil {
		t.Fatalf("RenderDOCX did not produce a valid zip: %v", err)
	}

	// Write it out under the temp dir to confirm it lands as a file.
	path := filepath.Join(t.TempDir(), "out.docx")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("docx file not written: %v", err)
	}
}
