// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Generic writing-sample extractor — port of src/services/writingSamples.ts.
// Accepts LinkedIn export ZIP/CSV, DOCX, PDF, generic CSV, and plain
// text/Markdown buffers and returns a flat list of writing samples suitable
// for tone analysis. Never returns an error — malformed input yields an
// empty slice.

package services

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/linkedin"
)

// Sample source types, matching the TS ToneProfile sourceType values.
const (
	SourceLinkedInExport = "linkedin_export"
	SourceWritingSamples = "writing_samples"
)

// minSampleLen is the minimum character length for a paragraph to count as a
// writing sample (MIN_PARA_LEN in the TS extractor).
const minSampleLen = 80

// maxDocxXMLBytes caps the inflated word/document.xml read (zip-bomb guard,
// same 50 MB limit as the LinkedIn parser).
const maxDocxXMLBytes = 50 * 1024 * 1024

// pdfExtractTimeout bounds the python3 subprocess for PDF extraction.
const pdfExtractTimeout = 30 * time.Second

// ExtractWritingSamples extracts writing samples from an uploaded file,
// dispatching on content magic and file extension. It never returns an
// error — malformed input yields an empty slice.
func ExtractWritingSamples(filename string, data []byte) []string {
	samples, _ := ExtractWritingSamplesWithSource(filename, data)
	return samples
}

// ExtractWritingSamplesWithSource is ExtractWritingSamples plus the detected
// source type: SourceLinkedInExport when LinkedIn post columns are found,
// SourceWritingSamples for everything else.
func ExtractWritingSamplesWithSource(filename string, data []byte) (samples []string, sourceType string) {
	// Belt and braces: the dispatcher below must never take the caller down.
	defer func() {
		if recover() != nil {
			samples, sourceType = nil, SourceWritingSamples
		}
	}()

	sourceType = SourceWritingSamples
	ext := strings.ToLower(filepath.Ext(filename))
	isZip := len(data) >= 4 &&
		data[0] == 0x50 && data[1] == 0x4b && data[2] == 0x03 && data[3] == 0x04

	switch {
	case isZip && ext != ".docx":
		// Try LinkedIn export first (Shares.csv / Posts and Articles.csv),
		// then fall back to DOCX-style extraction (word/document.xml).
		if posts := linkedin.ParseLinkedInExport(data); len(posts) > 0 {
			return posts, SourceLinkedInExport
		}
		return extractFromDocx(data), SourceWritingSamples

	case ext == ".docx" || isZip:
		return extractFromDocx(data), SourceWritingSamples

	case ext == ".pdf" || bytes.HasPrefix(data, []byte("%PDF-")):
		return extractFromPDF(data), SourceWritingSamples

	case ext == ".csv":
		// Try LinkedIn column names first, then generic CSV extraction.
		if posts := linkedin.ParseLinkedInExport(data); len(posts) > 0 {
			return posts, SourceLinkedInExport
		}
		return extractFromGenericCSV(string(data)), SourceWritingSamples

	default:
		// Plain text / Markdown / anything else.
		return splitIntoParagraphs(string(data)), SourceWritingSamples
	}
}

// ─── Paragraph splitter ───────────────────────────────────────────────────────

var (
	paraSplitRe  = regexp.MustCompile(`\n[ \t]*\n+`)
	whitespaceRe = regexp.MustCompile(`\s+`)
)

// splitIntoParagraphs splits text on blank lines, collapses internal
// whitespace, and keeps paragraphs of at least minSampleLen characters.
func splitIntoParagraphs(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, p := range paraSplitRe.Split(text, -1) {
		p = strings.TrimSpace(whitespaceRe.ReplaceAllString(p, " "))
		if len(p) >= minSampleLen {
			out = append(out, p)
		}
	}
	return out
}

// ─── DOCX extraction ──────────────────────────────────────────────────────────

var (
	docxParaRe = regexp.MustCompile(`<w:p[ >]`)
	docxBrRe   = regexp.MustCompile(`<w:br[^>]*>`)
	xmlTagRe   = regexp.MustCompile(`<[^>]+>`)
)

// extractFromDocx pulls prose paragraphs out of a DOCX buffer. DOCX files are
// ZIPs; we read word/document.xml and strip the XML, using <w:p> elements as
// paragraph delimiters (mirrors the TS extractFromDocx).
func extractFromDocx(data []byte) []string {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	for _, f := range r.File {
		if filepath.Base(f.Name) != "document.xml" {
			continue
		}
		if f.UncompressedSize64 > maxDocxXMLBytes {
			return nil
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		raw, err := io.ReadAll(io.LimitReader(rc, maxDocxXMLBytes))
		rc.Close()
		if err != nil {
			return nil
		}
		xml := string(raw)
		// Insert newlines at paragraph boundaries before stripping tags.
		xml = docxParaRe.ReplaceAllString(xml, "\n\n<w:p ")
		xml = docxBrRe.ReplaceAllString(xml, "\n")
		plain := xmlTagRe.ReplaceAllString(xml, "")
		return splitIntoParagraphs(plain)
	}
	return nil
}

// docxFullText returns the complete prose text of a DOCX (every paragraph,
// no minimum-length filter), used for document ingest where short headings and
// single-line clauses matter. Paragraph boundaries become blank lines so the
// downstream classifier and search see real structure. Returns "" on a
// malformed archive.
func docxFullText(data []byte) string {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ""
	}
	for _, f := range r.File {
		if filepath.Base(f.Name) != "document.xml" {
			continue
		}
		if f.UncompressedSize64 > maxDocxXMLBytes {
			return ""
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		raw, err := io.ReadAll(io.LimitReader(rc, maxDocxXMLBytes))
		rc.Close()
		if err != nil {
			return ""
		}
		xml := string(raw)
		xml = docxParaRe.ReplaceAllString(xml, "\n\n<w:p ")
		xml = docxBrRe.ReplaceAllString(xml, "\n")
		plain := xmlTagRe.ReplaceAllString(xml, "")
		// Collapse runs of blank lines but keep paragraph separation.
		plain = strings.ReplaceAll(plain, "\r\n", "\n")
		var out []string
		for _, line := range strings.Split(plain, "\n") {
			out = append(out, strings.TrimRight(line, " \t"))
		}
		return strings.TrimSpace(strings.Join(out, "\n"))
	}
	return ""
}

// ─── Generic CSV extraction ───────────────────────────────────────────────────

// extractFromGenericCSV pulls samples from a non-LinkedIn CSV: it scores each
// column by average cell length (skipping the header row) and uses the
// text-richest column; if no column dominates, it joins all cells per row.
func extractFromGenericCSV(text string) []string {
	rows := linkedin.ParseCSV(text)
	if len(rows) < 2 {
		return nil
	}
	dataRows := rows[1:]

	colCount := 0
	for _, r := range dataRows {
		if len(r) > colCount {
			colCount = len(r)
		}
	}
	if colCount == 0 {
		return nil
	}

	best, bestAvg := 0, -1.0
	for c := 0; c < colCount; c++ {
		total := 0
		for _, r := range dataRows {
			if c < len(r) {
				total += len(strings.TrimSpace(r[c]))
			}
		}
		avg := float64(total) / float64(len(dataRows))
		if avg > bestAvg {
			best, bestAvg = c, avg
		}
	}

	var out []string
	if bestAvg >= minSampleLen {
		// A dominant text column — use it directly.
		for _, r := range dataRows {
			if best < len(r) {
				if t := strings.TrimSpace(r[best]); len(t) >= minSampleLen {
					out = append(out, t)
				}
			}
		}
		return out
	}

	// No dominant column — join all cells in each row as one sample.
	for _, r := range dataRows {
		var cells []string
		for _, cell := range r {
			if t := strings.TrimSpace(cell); t != "" {
				cells = append(cells, t)
			}
		}
		if joined := strings.Join(cells, " "); len(joined) >= minSampleLen {
			out = append(out, joined)
		}
	}
	return out
}

// ─── PDF extraction ───────────────────────────────────────────────────────────

// extractFromPDF shells out to the Python PyMuPDF backend
// (scripts/pdf_tools.py extract_text), like the TS tools/pdf.ts did. If
// python3 or the script is unavailable, it returns an empty slice.
func extractFromPDF(data []byte) []string {
	python := os.Getenv("PDF_PYTHON_BIN")
	if python == "" {
		python = "python3"
	}
	if _, err := exec.LookPath(python); err != nil {
		return nil
	}
	script := os.Getenv("PDF_TOOLS_SCRIPT")
	if script == "" {
		script = filepath.Join("scripts", "pdf_tools.py")
	}
	if _, err := os.Stat(script); err != nil {
		return nil
	}

	tmp := filepath.Join(os.TempDir(), "tone-import-"+uuid.New().String()+".pdf")
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return nil
	}
	defer os.Remove(tmp)

	args, err := json.Marshal(map[string]string{"path": tmp})
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), pdfExtractTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, python, script, "extract_text", string(args)).Output()
	if err != nil {
		return nil
	}

	var result struct {
		Pages []struct {
			Text string `json:"text"`
		} `json:"pages"`
	}
	if json.Unmarshal(out, &result) != nil {
		return nil
	}
	var pages []string
	for _, p := range result.Pages {
		pages = append(pages, p.Text)
	}
	return splitIntoParagraphs(strings.Join(pages, "\n\n"))
}
