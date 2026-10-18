// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package ooxml reads the subset of ECMA-376 WordprocessingML that BigLaw needs: a .docx is a ZIP
// whose word/document.xml holds the text, with footnotes and endnotes beside it. (Ported from v1
// internal/ooxml; the writer, the XML escapers and the byte-for-byte re-zip land with their
// vectors in PR 3d.)
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
	"unicode"
	"unicode/utf8"
)

// maxDecompressed (128 MiB) caps the total decompressed size of an opened archive, so a ZIP bomb cannot
// exhaust memory. (A variable only so the test can lower it.)
var maxDecompressed int64 = 128 << 20

// textParts are the parts Text reads; Open decompresses nothing else.
var textParts = map[string]string{"word/document.xml": "", "word/footnotes.xml": "Footnotes", "word/endnotes.xml": "Endnotes"}

// Document is an opened .docx: the XML of its text parts, by part name.
type Document struct{ parts map[string]string }

// ErrNotDocx means the bytes are not a ZIP with a word/document.xml part.
var ErrNotDocx = errors.New("ooxml: not a .docx (a ZIP archive with word/document.xml)")

// Open parses a .docx from bytes.
func Open(data []byte) (*Document, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrNotDocx
	}
	d, total := &Document{parts: map[string]string{}}, int64(0)
	for _, f := range zr.File {
		if _, ok := textParts[f.Name]; !ok {
			continue
		}
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
		d.parts[f.Name] = string(b)
	}
	if _, ok := d.parts["word/document.xml"]; !ok {
		return nil, ErrNotDocx
	}
	return d, nil
}

// wml is the WordprocessingML namespace: elements are matched by namespace URI, whatever prefix a
// document binds it to. maxDepth bounds element nesting (the decoder keeps a stack per level).
const (
	wml      = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	maxDepth = 256
)

// Text is the visible text, in one linear pass of an XML decoder: the content of every <w:t> in
// document order (character references decoded), a tab for <w:tab/>, a line break for <w:br/> and
// <w:cr/>, U+2011 for <w:noBreakHyphen/>, a <w:sym/>'s character unless it is a private-use
// (Symbol-font) code, "\n" after each paragraph. A run's text counts when the run closes; hidden runs
// (<w:vanish/>), deleted text (<w:delText>) and markup-compatibility fallbacks (a copy of the
// choice) are left out. Footnotes and endnotes follow the body, each as "[id] text", under a
// "Footnotes"/"Endnotes" line.
func (d *Document) Text() (string, error) {
	var b strings.Builder
	if err := text(&b, d.parts["word/document.xml"]); err != nil {
		return "", err
	}
	for _, name := range []string{"word/footnotes.xml", "word/endnotes.xml"} {
		var notes strings.Builder
		if err := text(&notes, d.parts[name]); err != nil {
			return "", err
		}
		if strings.TrimSpace(notes.String()) != "" {
			b.WriteString("\n" + textParts[name] + "\n" + notes.String())
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
			case "tab", "br", "cr", "noBreakHyphen":
				if inRun {
					run.WriteString(map[string]string{"tab": "\t", "br": "\n", "cr": "\n", "noBreakHyphen": "\u2011"}[t.Name.Local])
				}
			case "sym": // a symbol-font character: kept when it is a Unicode character, not a private-use code
				if c, err := strconv.ParseUint(attr(t, "char"), 16, 32); inRun && err == nil && utf8.ValidRune(rune(c)) && !unicode.Is(unicode.Co, rune(c)) && c >= 0x20 {
					run.WriteRune(rune(c))
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
