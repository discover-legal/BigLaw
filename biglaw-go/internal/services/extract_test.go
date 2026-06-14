// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package services

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// minimalDocx builds a valid .docx (zip + word/document.xml) carrying the given
// paragraphs.
func minimalDocx(paras ...string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paras {
		sb.WriteString("<w:p><w:r><w:t>" + p + "</w:t></w:r></w:p>")
	}
	sb.WriteString(`</w:body></w:document>`)
	w, _ := zw.Create("word/document.xml")
	_, _ = w.Write([]byte(sb.String()))
	_ = zw.Close()
	return buf.Bytes()
}

func newTextExtractor() *DocumentExtractor {
	// No providers, no python: exercises the pure-Go branches and graceful
	// degradation only.
	return NewDocumentExtractor(nil, "", nil, "", "", "")
}

func TestExtractPlainText(t *testing.T) {
	res := newTextExtractor().Extract("memo.txt", []byte("PRIVILEGED. Preserve all documents for the Acme matter."))
	if res.Method != MethodPlain {
		t.Errorf("method = %q, want plain", res.Method)
	}
	if !strings.Contains(res.Text, "Acme") {
		t.Errorf("text not preserved: %q", res.Text)
	}
}

func TestExtractDOCX(t *testing.T) {
	doc := minimalDocx("NON-DISCLOSURE AGREEMENT", "Confidential for five years.")
	res := newTextExtractor().Extract("nda.docx", doc)
	if res.Method != MethodTextLayer {
		t.Errorf("method = %q, want text-layer", res.Method)
	}
	if !strings.Contains(res.Text, "NON-DISCLOSURE") || !strings.Contains(res.Text, "five years") {
		t.Errorf("docx text incomplete: %q", res.Text)
	}
}

func TestExtractImageWithoutVisionDegrades(t *testing.T) {
	// 1x1 PNG header is enough for magic-byte detection.
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	res := newTextExtractor().Extract("scan.png", png)
	if res.Method != MethodNone {
		t.Errorf("method = %q, want none (no vision configured)", res.Method)
	}
	if len(res.Notes) == 0 || !strings.Contains(strings.ToLower(strings.Join(res.Notes, " ")), "vision") {
		t.Errorf("expected a vision-configuration note, got %v", res.Notes)
	}
}

func TestSniffImageMIME(t *testing.T) {
	cases := map[string][]byte{
		"image/png":  {0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a},
		"image/jpeg": {0xff, 0xd8, 0xff, 0xe0},
		"image/gif":  []byte("GIF89a..."),
	}
	for want, data := range cases {
		if got := sniffImageMIME(data); got != want {
			t.Errorf("sniffImageMIME = %q, want %q", got, want)
		}
	}
}
