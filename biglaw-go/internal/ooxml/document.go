// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxDecompressedBytes caps the total decompressed size of an opened archive
// so a hostile ZIP cannot exhaust memory.
const maxDecompressedBytes = 200 << 20 // 200 MB

// part is one archive member, kept verbatim in original order.
type part struct {
	name string
	data []byte
}

// Document is an opened .docx. word/document.xml is held as mutable XML text;
// every other part is preserved byte-for-byte, in the order it was read, so a
// round-trip touches nothing but the main document part.
type Document struct {
	parts  []part
	docXML string
}

// Open parses a .docx from bytes.
func Open(data []byte) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a valid .docx (zip): %w", err)
	}
	d := &Document{}
	var total int64
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxDecompressedBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		if total += int64(len(b)); total > maxDecompressedBytes {
			return nil, fmt.Errorf("refusing to open: decompressed size exceeds %d bytes", int64(maxDecompressedBytes))
		}
		if f.Name == "word/document.xml" {
			d.docXML = string(b)
		}
		d.parts = append(d.parts, part{name: f.Name, data: b})
	}
	if d.docXML == "" {
		return nil, fmt.Errorf("not a .docx: word/document.xml missing")
	}
	return d, nil
}

// OpenFile parses a .docx from disk.
func OpenFile(path string) (*Document, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Open(b)
}

// DocumentXML returns the current main document part.
func (d *Document) DocumentXML() string { return d.docXML }

// Bytes re-zips the archive, preserving part order and every part other than
// word/document.xml byte-for-byte.
func (d *Document) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range d.parts {
		w, err := zw.Create(p.name)
		if err != nil {
			return nil, err
		}
		data := p.data
		if p.name == "word/document.xml" {
			data = []byte(d.docXML)
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SaveFile writes the archive to disk.
func (d *Document) SaveFile(path string) error {
	b, err := d.Bytes()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ─── Text index ───────────────────────────────────────────────────────────────

// runSegment maps one text-bearing <w:r> element to its slice of the
// document's visible text.
type runSegment struct {
	text     string // decoded visible text of the run (its <w:t> elements)
	docStart int    // byte offset of text within Text()
	xmlStart int    // byte offset of "<w:r" within docXML
	xmlEnd   int    // byte offset just past "</w:r>"
	runProps string // the run's <w:rPr>…</w:rPr> block, "" if none
}

// Text returns the visible text of the document: the concatenated content of
// every <w:t> element, with "\n" between paragraphs. Deleted text
// (<w:delText>) is not visible and is excluded.
func (d *Document) Text() string {
	text, _ := d.index()
	return text
}

// index walks docXML once, building the visible text and the run segments
// that back it. Paragraph closes contribute a "\n" separator owned by no
// segment, so a match can never silently span a paragraph boundary.
func (d *Document) index() (string, []runSegment) {
	xml := d.docXML
	var text strings.Builder
	var segs []runSegment
	i := 0
	for i < len(xml) {
		rOpen := nextRunOpen(xml, i)
		pClose := strings.Index(xml[i:], "</w:p>")
		if pClose >= 0 {
			pClose += i
		}
		if rOpen < 0 && pClose < 0 {
			break
		}
		if pClose >= 0 && (rOpen < 0 || pClose < rOpen) {
			text.WriteByte('\n')
			i = pClose + len("</w:p>")
			continue
		}
		rClose := strings.Index(xml[rOpen:], "</w:r>")
		if rClose < 0 {
			break
		}
		rEnd := rOpen + rClose + len("</w:r>")
		runXML := xml[rOpen:rEnd]
		if t := runText(runXML); t != "" {
			segs = append(segs, runSegment{
				text:     t,
				docStart: text.Len(),
				xmlStart: rOpen,
				xmlEnd:   rEnd,
				runProps: runProps(runXML),
			})
			text.WriteString(t)
		}
		i = rEnd
	}
	return text.String(), segs
}

// nextRunOpen finds the next "<w:r>" or "<w:r …>" open tag at or after from.
// The character check rejects lookalikes such as <w:rPr> and self-closing
// empty runs (<w:r/>).
func nextRunOpen(xml string, from int) int {
	for i := from; ; {
		j := strings.Index(xml[i:], "<w:r")
		if j < 0 {
			return -1
		}
		j += i
		k := j + len("<w:r")
		if k < len(xml) && (xml[k] == '>' || xml[k] == ' ') {
			return j
		}
		i = j + 1
	}
}

// runText concatenates and decodes the <w:t> content of one run element.
func runText(runXML string) string {
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(runXML[i:], "<w:t")
		if j < 0 {
			break
		}
		j += i
		k := j + len("<w:t")
		if k >= len(runXML) {
			break
		}
		switch runXML[k] {
		case '>': // <w:t>…</w:t>
			e := strings.Index(runXML[k+1:], "</w:t>")
			if e < 0 {
				return b.String()
			}
			b.WriteString(Unescape(runXML[k+1 : k+1+e]))
			i = k + 1 + e + len("</w:t>")
		case ' ': // <w:t attrs>…</w:t> or <w:t attrs/>
			c := strings.Index(runXML[k:], ">")
			if c < 0 {
				return b.String()
			}
			c += k
			if runXML[c-1] == '/' {
				i = c + 1
				continue
			}
			e := strings.Index(runXML[c+1:], "</w:t>")
			if e < 0 {
				return b.String()
			}
			b.WriteString(Unescape(runXML[c+1 : c+1+e]))
			i = c + 1 + e + len("</w:t>")
		case '/': // <w:t/>
			i = k + 2
		default: // a longer element name, e.g. <w:tab/>
			i = k
		}
	}
	return b.String()
}

// runProps extracts the run's <w:rPr> block so surgery can preserve the
// original character formatting on the pieces it re-emits.
func runProps(runXML string) string {
	s := strings.Index(runXML, "<w:rPr")
	if s < 0 {
		return ""
	}
	c := strings.Index(runXML[s:], ">")
	if c < 0 {
		return ""
	}
	if runXML[s+c-1] == '/' { // self-closing <w:rPr/>
		return runXML[s : s+c+1]
	}
	e := strings.Index(runXML[s:], "</w:rPr>")
	if e < 0 {
		return ""
	}
	return runXML[s : s+e+len("</w:rPr>")]
}

// ─── Tracked-change surgery ───────────────────────────────────────────────────

// ApplyTracked replaces the visible-text span [start, end) with replacement as
// a Word tracked change. The covered characters become <w:del> runs and the
// replacement becomes a single <w:ins> run inserted at the deletion point;
// text outside the span stays in its original runs. start == end performs a
// pure insertion; replacement == "" performs a pure deletion. The span may
// cross run boundaries but not a paragraph boundary. Offsets are byte offsets
// into Text().
func (d *Document) ApplyTracked(start, end int, replacement string, rev *Revisions) error {
	if start < 0 || end < start {
		return fmt.Errorf("invalid span [%d, %d)", start, end)
	}
	if start == end && replacement == "" {
		return fmt.Errorf("edit is a no-op: empty span and empty replacement")
	}
	text, segs := d.index()
	if end > len(text) {
		return fmt.Errorf("span [%d, %d) out of range (text length %d)", start, end, len(text))
	}

	if start == end {
		return d.insertAt(start, replacement, segs, rev)
	}

	// Collect the slice of each run covered by the span.
	type cut struct {
		seg    runSegment
		ls, le int // local byte offsets within seg.text
	}
	var cuts []cut
	covered := 0
	for _, s := range segs {
		segEnd := s.docStart + len(s.text)
		if segEnd <= start || s.docStart >= end {
			continue
		}
		ls := max(0, start-s.docStart)
		le := min(len(s.text), end-s.docStart)
		cuts = append(cuts, cut{seg: s, ls: ls, le: le})
		covered += le - ls
	}
	if covered != end-start {
		return fmt.Errorf("edit span crosses a paragraph boundary")
	}

	// Rewrite the affected runs from last to first so earlier XML offsets
	// stay valid. Each run splits into kept-prefix, a <w:del> for its covered
	// slice, and kept-suffix; the <w:ins> lands right after the final <w:del>.
	for ci := len(cuts) - 1; ci >= 0; ci-- {
		c := cuts[ci]
		mid := rev.Deletion(c.seg.text[c.ls:c.le], c.seg.runProps)
		if ci == len(cuts)-1 && replacement != "" {
			mid += rev.Insertion(replacement, c.seg.runProps)
		}
		newXML := plainRun(c.seg.text[:c.ls], c.seg.runProps) + mid + plainRun(c.seg.text[c.le:], c.seg.runProps)
		d.docXML = d.docXML[:c.seg.xmlStart] + newXML + d.docXML[c.seg.xmlEnd:]
	}
	return nil
}

// insertAt splits the run containing the insertion point and places a
// <w:ins> between the two halves.
func (d *Document) insertAt(pos int, replacement string, segs []runSegment, rev *Revisions) error {
	for _, s := range segs {
		segEnd := s.docStart + len(s.text)
		if pos < s.docStart || pos > segEnd {
			continue
		}
		if pos == s.docStart && s.docStart != 0 {
			// Prefer attaching to the end of the preceding segment; only take
			// a leading position when this is genuinely the first text.
			continue
		}
		ls := pos - s.docStart
		newXML := plainRun(s.text[:ls], s.runProps) +
			rev.Insertion(replacement, s.runProps) +
			plainRun(s.text[ls:], s.runProps)
		d.docXML = d.docXML[:s.xmlStart] + newXML + d.docXML[s.xmlEnd:]
		return nil
	}
	// Fall back to any segment whose start matches (e.g. pos == 0).
	for _, s := range segs {
		if pos == s.docStart {
			newXML := rev.Insertion(replacement, s.runProps) + plainRun(s.text, s.runProps)
			d.docXML = d.docXML[:s.xmlStart] + newXML + d.docXML[s.xmlEnd:]
			return nil
		}
	}
	return fmt.Errorf("insertion point %d does not fall within any text run", pos)
}

// plainRun renders an untracked run with the given properties; empty text
// yields nothing.
func plainRun(text, props string) string {
	if text == "" {
		return ""
	}
	return "<w:r>" + props + `<w:t xml:space="preserve">` + Escape(text) + "</w:t></w:r>"
}
