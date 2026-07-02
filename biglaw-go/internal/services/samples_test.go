// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tests for the generic writing-sample extractor (samples.go).

package services

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

const longPara1 = "This memorandum addresses the principal regulatory considerations arising from the proposed acquisition, including merger control thresholds and sector-specific approvals."
const longPara2 = "Counsel should note that the indemnification provisions in the draft agreement deviate materially from the firm playbook position on liability caps and survival periods."

// buildDocx creates a minimal in-memory DOCX containing the given paragraphs.
func buildDocx(t *testing.T, paragraphs []string) []byte {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		sb.WriteString(`<w:p w:rsidR="0"><w:r><w:t>` + p + `</w:t></w:r></w:p>`)
	}
	sb.WriteString(`</w:body></w:document>`)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(sb.String())); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractWritingSamples_Docx(t *testing.T) {
	data := buildDocx(t, []string{longPara1, "short", longPara2})

	samples, source := ExtractWritingSamplesWithSource("brief.docx", data)
	if source != SourceWritingSamples {
		t.Errorf("source = %q, want %q", source, SourceWritingSamples)
	}
	if len(samples) != 2 {
		t.Fatalf("got %d samples, want 2: %#v", len(samples), samples)
	}
	if samples[0] != longPara1 || samples[1] != longPara2 {
		t.Errorf("unexpected samples: %#v", samples)
	}
}

func TestExtractWritingSamples_DocxWithoutExtension(t *testing.T) {
	// A ZIP that is not a LinkedIn export should fall through to DOCX parsing.
	data := buildDocx(t, []string{longPara1})
	samples, source := ExtractWritingSamplesWithSource("upload.zip", data)
	if source != SourceWritingSamples || len(samples) != 1 {
		t.Errorf("got %d samples (source %q), want 1 writing_samples", len(samples), source)
	}
}

func TestExtractWritingSamples_PlainText(t *testing.T) {
	text := longPara1 + "\n\ntoo short\n\n" + longPara2 + "\r\n\r\nspanning\nlines but still much too short overall"
	samples := ExtractWritingSamples("notes.md", []byte(text))
	if len(samples) != 2 {
		t.Fatalf("got %d samples, want 2: %#v", len(samples), samples)
	}
}

func TestExtractWritingSamples_GenericCSV(t *testing.T) {
	csv := "id,comment\n" +
		"1,\"" + longPara1 + "\"\n" +
		"2,\"" + longPara2 + "\"\n"
	samples, source := ExtractWritingSamplesWithSource("feedback.csv", []byte(csv))
	if source != SourceWritingSamples {
		t.Errorf("source = %q, want %q", source, SourceWritingSamples)
	}
	if len(samples) != 2 || samples[0] != longPara1 {
		t.Fatalf("column scoring failed: %#v", samples)
	}
}

func TestExtractWritingSamples_LinkedInCSV(t *testing.T) {
	csv := "Date,ShareCommentary,Visibility\n" +
		"2026-01-01,\"" + longPara1 + "\",PUBLIC\n"
	samples, source := ExtractWritingSamplesWithSource("Shares.csv", []byte(csv))
	if source != SourceLinkedInExport {
		t.Errorf("source = %q, want %q", source, SourceLinkedInExport)
	}
	if len(samples) != 1 || samples[0] != longPara1 {
		t.Errorf("unexpected samples: %#v", samples)
	}
}

func TestExtractWritingSamples_MalformedInput(t *testing.T) {
	cases := map[string][]byte{
		"garbage.zip":  {0x50, 0x4b, 0x03, 0x04, 0xde, 0xad, 0xbe, 0xef},
		"empty.docx":   {},
		"corrupt.docx": []byte("not a zip at all"),
		"missing.pdf":  []byte("%PDF-1.7 garbage"),
		"empty.csv":    []byte(""),
	}
	for name, data := range cases {
		if samples := ExtractWritingSamples(name, data); len(samples) != 0 {
			t.Errorf("%s: expected no samples, got %#v", name, samples)
		}
	}
}

func TestSplitIntoParagraphs_FiltersShort(t *testing.T) {
	got := splitIntoParagraphs("short one\n\n" + longPara1 + "\n\n\n" + longPara2)
	if len(got) != 2 {
		t.Fatalf("got %d paragraphs, want 2: %#v", len(got), got)
	}
}
