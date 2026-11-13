// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The scanner is the one reading of word/document.xml both the reader (Text) and the writer
// (Walk) share: a single linear pass of an XML decoder that reports every run with its visible
// text and the byte span of its <w:r> element, every paragraph with its span and properties, and
// every tracked-change element a run sits in. Text is the runs' text joined with "\n" after each
// paragraph, so what the ingest stores and what the writer indexes are the same bytes by
// construction (TestWriterTextEqualsReaderText pins it on every fixture).

// run is one text-bearing <w:r> as scanned.
type run struct {
	text     string // visible text (the reader's rules: w:t content, tab, br, cr, noBreakHyphen, sym)
	delText  string // a deleted run's w:delText (not visible)
	docStart int    // byte offset of text within the body's text
	xmlStart int    // byte offset of "<w:r" within the part
	xmlEnd   int    // byte offset just past "</w:r>"
	props    string // the raw <w:rPr>…</w:rPr>, "" if none
	para     int    // paragraph ordinal
	hidden   bool   // <w:vanish/>: not in the text
	complex  bool   // holds children the writer cannot reproduce (drawings, fields, references, symbols)
	skip     string // why the writer leaves it alone: field, sdt, move, complex, hidden; "" when editable
	rev      *revEl // the enclosing <w:ins> or <w:del>, nil for plain text
}

// revEl is one <w:ins>, <w:del> element (or a paragraph mark's) with its attributes and span.
type revEl struct {
	kind         string // ins or del
	id           int
	author, date string
	open         string // the raw open tag, to reopen it around a change inside it
	xmlStart     int    // offset of "<w:ins"
	xmlEnd       int    // just past "</w:ins>" (set at its close)
	openEnd      int    // just past the open tag
	closeStart   int    // offset of "</w:ins>"
	mark         bool   // a paragraph mark's revision (w:pPr/w:rPr/w:ins|del, an empty element)
}

// para is one <w:p> as scanned.
type para struct {
	xmlStart, xmlEnd int    // the <w:p>…</w:p> span
	openEnd          int    // just past the <w:p …> open tag
	pPr              string // the raw <w:pPr>…</w:pPr>, "" if none
	pPrStart, pPrEnd int    // its span ("" : both openEnd)
	textStart        int    // byte offset of the paragraph's first text within the body's text
	textEnd          int    // offset of its "\n"
	cell             int    // which table cell or body it is in (merging never crosses one)
	markRev          *revEl // the paragraph mark's w:ins or w:del, nil for none
	firstRun, nRuns  int    // its runs in the index
}

// index is the scan of a part.
type index struct {
	text  string
	runs  []run
	paras []para
	maxID int // the highest w:id attribute value seen on any element
}

// scan reads src once. It is the reader's text() generalised: the same rules decide what is text.
func scan(src string) (*index, error) {
	dec := xml.NewDecoder(strings.NewReader(src))
	dec.Strict = false
	ix := &index{}
	var text, cur, delCur strings.Builder
	var (
		r                                           run
		p                                           *para
		inRun, inT, inDel, inProps, inPPr, inPPrRPr bool
		depth                                       int
		prev                                        int64 // the offset the current token starts at
		revs                                        []*revEl
		cell                                        int
		fieldOpen                                   int // fldChar begin … end depth, and <w:fldSimple> nesting
		sdt, move                                   int
		fieldEnd                                    bool
		propsStart, pPrStart                        int
		paraDepth                                   int
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("ooxml: the document XML is malformed: %w", err)
		}
		start, end := int(prev), int(dec.InputOffset())
		prev = dec.InputOffset()
		switch t := tok.(type) {
		case xml.StartElement:
			if depth++; depth > maxDepth {
				return nil, fmt.Errorf("ooxml: the document XML nests deeper than %d elements", maxDepth)
			}
			if t.Name.Local == "Fallback" && t.Name.Space == "http://schemas.openxmlformats.org/markup-compatibility/2006" {
				depth--
				if err := dec.Skip(); err != nil {
					return nil, fmt.Errorf("ooxml: the document XML is malformed: %w", err)
				}
				prev = dec.InputOffset()
				if inRun {
					r.complex = true
				}
				continue
			}
			if t.Name.Space != wml {
				if inRun {
					r.complex = true
				}
				continue
			}
			if id, err := strconv.Atoi(attr(t, "id")); err == nil && id > ix.maxID {
				ix.maxID = id
			}
			val := attr(t, "val")
			switch t.Name.Local {
			case "body", "tc", "txbxContent", "footnote", "endnote":
				cell++
			case "p":
				ix.paras = append(ix.paras, para{xmlStart: start, openEnd: end, pPrStart: end, pPrEnd: end, textStart: text.Len(), cell: cell, firstRun: len(ix.runs)})
				p, paraDepth = &ix.paras[len(ix.paras)-1], depth
			case "pPr":
				if p != nil && depth == paraDepth+1 {
					inPPr, pPrStart = true, start
				}
			case "rPr":
				if inRun {
					inProps, propsStart = true, start
				} else if inPPr {
					inPPrRPr = true
				}
			case "ins", "del":
				if inPPrRPr && p != nil {
					p.markRev = &revEl{kind: t.Name.Local, author: attr(t, "author"), date: attr(t, "date"), open: src[start:end], xmlStart: start, xmlEnd: end, mark: true}
					p.markRev.id, _ = strconv.Atoi(attr(t, "id"))
				} else if !inRun {
					e := &revEl{kind: t.Name.Local, author: attr(t, "author"), date: attr(t, "date"), open: src[start:end], xmlStart: start, openEnd: end}
					e.id, _ = strconv.Atoi(attr(t, "id"))
					revs = append(revs, e)
				}
			case "moveFrom", "moveTo":
				move++
			case "sdt":
				sdt++
			case "fldSimple":
				fieldOpen++
			case "r":
				r = run{xmlStart: start, para: len(ix.paras) - 1, docStart: text.Len()}
				if len(revs) > 0 {
					r.rev = revs[len(revs)-1]
				}
				inRun, inDel = true, false
				cur.Reset()
				delCur.Reset()
			case "t":
				inT = inRun
			case "delText":
				inDel = inRun
				inT = false
			case "tab", "br", "cr", "noBreakHyphen":
				if inRun {
					cur.WriteString(map[string]string{"tab": "\t", "br": "\n", "cr": "\n", "noBreakHyphen": "‑"}[t.Name.Local])
				}
			case "sym":
				if c, err := strconv.ParseUint(attr(t, "char"), 16, 32); inRun && err == nil && utf8.ValidRune(rune(c)) && !unicode.Is(unicode.Co, rune(c)) && c >= 0x20 {
					cur.WriteRune(rune(c))
				}
				if inRun {
					r.complex = true
				}
			case "vanish":
				if inProps {
					r.hidden = r.hidden || val != "0" && val != "false" && val != "off"
				}
			case "fldChar":
				if inRun {
					r.complex = true
					switch attr(t, "fldCharType") {
					case "begin":
						fieldOpen++
					case "end":
						fieldEnd = true // counted once the run closes: the end run is the field's too
					}
				}
			case "softHyphen", "lastRenderedPageBreak":
			default:
				if inRun && !inProps {
					r.complex = true // instrText, drawing, pict, object, footnoteReference, commentReference, ruby, ptab, …
				}
			}
		case xml.EndElement:
			depth--
			if t.Name.Space != wml {
				continue
			}
			switch t.Name.Local {
			case "t":
				inT = false
			case "delText":
				inDel = false
			case "rPr":
				if inProps {
					inProps, r.props = false, stripPropChange(src[propsStart:end])
				}
				inPPrRPr = false
			case "pPr":
				if inPPr && p != nil {
					inPPr, p.pPr, p.pPrStart, p.pPrEnd = false, src[pPrStart:end], pPrStart, end
				}
			case "ins", "del":
				if n := len(revs); n > 0 && !inRun && !inPPrRPr {
					revs[n-1].closeStart, revs[n-1].xmlEnd = start, end
					revs = revs[:n-1]
				}
			case "moveFrom", "moveTo":
				move = max(move-1, 0)
			case "sdt":
				sdt = max(sdt-1, 0)
			case "fldSimple":
				fieldOpen = max(fieldOpen-1, 0)
			case "r":
				if inRun {
					r.xmlEnd, r.delText = end, delCur.String()
					if !r.hidden {
						r.text = cur.String()
						text.WriteString(r.text)
					}
					switch {
					case r.hidden:
						r.skip = "hidden"
					case move > 0:
						r.skip = "move"
					case sdt > 0:
						r.skip = "sdt"
					case fieldOpen > 0:
						r.skip = "field"
					case r.complex:
						r.skip = "complex"
					}
					if r.rev != nil && r.rev.kind == "del" && r.skip == "" {
						r.skip = "deleted"
					}
					if p != nil {
						p.nRuns++
					}
					ix.runs = append(ix.runs, r)
					if fieldEnd {
						fieldOpen, fieldEnd = max(fieldOpen-1, 0), false
					}
				}
				inRun, inT, inDel = false, false, false
			case "p":
				if p != nil {
					p.textEnd, p.xmlEnd = text.Len(), end
					p = nil
				}
				text.WriteByte('\n')
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			} else if inDel {
				delCur.Write(t)
			}
		}
	}
	ix.text = text.String()
	return ix, nil
}

// stripPropChange drops a run's <w:rPrChange> (a formatting revision: copying it to new runs would
// duplicate its id) from a copied <w:rPr>.
func stripPropChange(props string) string {
	for {
		i := strings.Index(props, "<w:rPrChange")
		if i < 0 {
			return props
		}
		j := strings.Index(props[i:], "</w:rPrChange>")
		if j < 0 {
			if k := strings.Index(props[i:], "/>"); k >= 0 {
				props = props[:i] + props[i+k+2:]
				continue
			}
			return props[:i] + "</w:rPr>"
		}
		props = props[:i] + props[i+j+len("</w:rPrChange>"):]
	}
}

// Escape makes s safe as XML text: the markup characters become references, and characters XML
// 1.0 cannot carry (controls but tab, newline and return) become spaces. (An attribute value also
// needs its quotes escaped: Revisions.who.)
func Escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r', r == 0x7f, r == 0xfffe, r == 0xffff:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
