// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package ooxml reads the subset of ECMA-376 WordprocessingML that BigLaw needs: a .docx is a ZIP
// whose word/document.xml holds the text. Open keeps every part in its original order so a later
// writer can round-trip the archive untouched. (Ported from v1 internal/ooxml; the writer and the
// tracked-change parse/apply land with their vectors in a later slice of PR 3.)
package ooxml

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// maxDecompressed (128 MiB) caps the total decompressed size of an opened archive, so a ZIP bomb cannot
// exhaust memory. (A variable only so the test can lower it.)
var maxDecompressed int64 = 128 << 20

var (
	escaper   = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	unescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&") // one pass: "&amp;lt;" → "&lt;"
)

// Escape escapes the five predefined XML entities; Unescape decodes them.
func Escape(s string) string   { return escaper.Replace(s) }
func Unescape(s string) string { return unescaper.Replace(s) }

type part struct {
	name string
	data []byte
}

// Document is an opened .docx: word/document.xml as text, every other part kept byte for byte.
type Document struct {
	parts  []part
	docXML string
}

// ErrNotDocx means the bytes are not a ZIP with a word/document.xml part.
var ErrNotDocx = errors.New("ooxml: not a .docx (a ZIP archive with word/document.xml)")

// Open parses a .docx from bytes.
func Open(data []byte) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrNotDocx
	}
	d, total, found := &Document{}, int64(0), false
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: part %q cannot be read", ErrNotDocx, f.Name)
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxDecompressed-total+1))
		rc.Close()
		if total += int64(len(b)); total > maxDecompressed {
			return nil, fmt.Errorf("ooxml: refusing a .docx that decompresses to more than %d bytes", maxDecompressed)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: part %q is corrupt", ErrNotDocx, f.Name)
		}
		if f.Name == "word/document.xml" {
			d.docXML, found = string(b), true
		}
		d.parts = append(d.parts, part{f.Name, b})
	}
	if !found {
		return nil, ErrNotDocx
	}
	return d, nil
}

// Bytes re-zips the archive in the original part order, every part byte for byte.
func (d *Document) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range d.parts {
		data := p.data
		if p.name == "word/document.xml" {
			data = []byte(d.docXML)
		}
		w, err := zw.Create(p.name)
		if err == nil {
			_, err = w.Write(data)
		}
		if err != nil {
			return nil, err
		}
	}
	err := zw.Close()
	return buf.Bytes(), err
}

// Text is the visible text: the decoded content of every <w:t> in document order, with "\n"
// after each paragraph. Deleted text (<w:delText>) is not visible and is left out.
func (d *Document) Text() string {
	var b strings.Builder
	xml := d.docXML
	for i := 0; i < len(xml); {
		r, p := nextRunOpen(xml, i), strings.Index(xml[i:], "</w:p>")
		if p >= 0 {
			p += i
		}
		if p >= 0 && (r < 0 || p < r) {
			b.WriteByte('\n')
			i = p + len("</w:p>")
			continue
		}
		if r < 0 {
			break
		}
		end := strings.Index(xml[r:], "</w:r>")
		if end < 0 {
			break
		}
		end += r + len("</w:r>")
		b.WriteString(runText(xml[r:end]))
		i = end
	}
	return b.String()
}

// nextRunOpen finds the next "<w:r>" or "<w:r …>" at or after from (not <w:rPr> or <w:r/>).
func nextRunOpen(xml string, from int) int {
	for i := from; ; {
		j := strings.Index(xml[i:], "<w:r")
		if j < 0 {
			return -1
		}
		if k := i + j + len("<w:r"); k < len(xml) && (xml[k] == '>' || xml[k] == ' ') {
			return i + j
		}
		i += j + 1
	}
}

// runText concatenates and decodes the <w:t> elements of one run (not <w:tab/>, <w:t/>).
func runText(run string) string {
	var b strings.Builder
	for i := 0; ; {
		j := strings.Index(run[i:], "<w:t")
		if j < 0 {
			return b.String()
		}
		k := i + j + len("<w:t")
		if k >= len(run) || run[k] != '>' && run[k] != ' ' {
			i = k // a longer name such as <w:tab/>, or <w:t/>
			continue
		}
		gt := strings.IndexByte(run[k:], '>')
		if gt < 0 {
			return b.String()
		}
		if gt += k; run[gt-1] == '/' { // <w:t attrs/>
			i = gt + 1
			continue
		}
		e := strings.Index(run[gt+1:], "</w:t>")
		if e < 0 {
			return b.String()
		}
		b.WriteString(Unescape(run[gt+1 : gt+1+e]))
		i = gt + 1 + e + len("</w:t>")
	}
}
