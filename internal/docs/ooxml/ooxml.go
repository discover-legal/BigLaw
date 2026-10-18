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
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
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

// wml is the WordprocessingML namespace: elements are matched by namespace URI, whatever prefix a
// document binds it to. maxDepth bounds element nesting (the decoder keeps a stack per level).
const (
	wml      = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	maxDepth = 256
)

// Text is the visible text, in one linear pass of an XML decoder: the content of every <w:t> in
// document order (character references decoded), a tab for <w:tab/>, a line break for <w:br/> and
// <w:cr/>, "\n" after each paragraph. A run's text counts when the run closes; hidden runs
// (<w:vanish/>), deleted text (<w:delText>) and markup-compatibility fallbacks (a copy of the
// choice) are left out. Footnotes and endnotes follow the body, each as "[id] text", under a
// "Footnotes"/"Endnotes" line.
func (d *Document) Text() (string, error) {
	var b strings.Builder
	if err := text(&b, d.docXML); err != nil {
		return "", err
	}
	for _, p := range d.parts {
		label := map[string]string{"word/footnotes.xml": "Footnotes", "word/endnotes.xml": "Endnotes"}[p.name]
		var notes strings.Builder
		if label == "" {
			continue
		} else if err := text(&notes, string(p.data)); err != nil {
			return "", err
		}
		if strings.TrimSpace(notes.String()) != "" {
			b.WriteString("\n" + label + "\n" + notes.String())
		}
	}
	return b.String(), nil
}

func text(b *strings.Builder, src string) error {
	dec := xml.NewDecoder(strings.NewReader(src))
	dec.Strict = false // an undeclared entity stays literal text; entities are never expanded
	var run strings.Builder
	inRun, inT, hidden, depth := false, false, false, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("ooxml: the document XML is malformed: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth++; depth > maxDepth {
				return fmt.Errorf("ooxml: the document XML nests deeper than %d elements", maxDepth)
			}
			id, idErr := strconv.Atoi(attr(t, "id"))
			note := t.Name.Space == wml && (t.Name.Local == "footnote" || t.Name.Local == "endnote")
			if note && (idErr != nil || id <= 0) || t.Name.Local == "Fallback" && t.Name.Space == "http://schemas.openxmlformats.org/markup-compatibility/2006" {
				depth-- // a separator note, or the fallback copy of an mc:Choice
				if err := dec.Skip(); err != nil {
					return fmt.Errorf("ooxml: the document XML is malformed: %w", err)
				}
				continue
			} else if note {
				b.WriteString("[" + strconv.Itoa(id) + "] ")
			} else if t.Name.Space != wml {
				continue
			}
			switch val := attr(t, "val"); t.Name.Local {
			case "r":
				inRun, hidden = true, false
				run.Reset()
			case "t":
				inT = inRun
			case "tab", "br", "cr":
				if inRun {
					run.WriteString(map[string]string{"tab": "\t", "br": "\n", "cr": "\n"}[t.Name.Local])
				}
			case "vanish":
				hidden = hidden || inRun && val != "0" && val != "false" && val != "off"
			}
		case xml.EndElement:
			depth--
			switch {
			case t.Name.Space != wml:
			case t.Name.Local == "t":
				inT = false
			case t.Name.Local == "r":
				if inRun && !hidden {
					b.WriteString(run.String())
				}
				inRun, inT = false, false
			case t.Name.Local == "p":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inT {
				run.Write(t)
			}
		}
	}
}

func attr(e xml.StartElement, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local && a.Name.Space == wml {
			return a.Value
		}
	}
	return ""
}
