// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The writer's vectors (PR 3d, README § Track changes): fixtures are built here from their XML so
// each test shows what it exercises; libreoffice-origin.docx under testdata is the one real file
// (made by LibreOffice from a text file; see testdata/README.md).

var when = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

const (
	contentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`
	rootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`
	docRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`
	styles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style></w:styles>`
)

// fullDocx is a complete, Word-openable .docx around body, plus extra parts.
func fullDocx(t *testing.T, body string, extra ...[2]string) []byte {
	t.Helper()
	parts := [][2]string{{"[Content_Types].xml", contentTypes}, {"_rels/.rels", rootRels}, {"word/_rels/document.xml.rels", docRels},
		{"word/document.xml", strings.ReplaceAll(docXMLWrapper, "%BODY%", body)}, {"word/styles.xml", styles}}
	return buildZip(t, append(parts, extra...))
}

func openAll(t *testing.T, data []byte) *Document {
	t.Helper()
	d, err := OpenAll(data)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// walked opens data, walks v and returns the written archive.
func walked(t *testing.T, data []byte, v Visitor) []byte {
	t.Helper()
	d := openAll(t, data)
	if err := d.Walk(v, NewRevisions("Horatiu Druma-Strugariu", when)); err != nil {
		t.Fatal(err)
	}
	out, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	wellFormed(t, out)
	return out
}

// decided is the text of data with every tracked change accepted (or rejected).
func decided(t *testing.T, data []byte, accept bool) string {
	t.Helper()
	d := openAll(t, data)
	v, err := d.Decide(accept, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Walk(v, NewRevisions("x", when)); err != nil {
		t.Fatal(err)
	}
	return textOf(t, d)
}

func textOf(t *testing.T, d *Document) string {
	t.Helper()
	s, err := d.Text()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// wellFormed parses every XML part of the archive and checks no <w:ins>/<w:del> nests in another.
func wellFormed(t *testing.T, data []byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".xml") && !strings.HasSuffix(f.Name, ".rels") {
			continue
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		dec := xml.NewDecoder(bytes.NewReader(b))
		depth := 0
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s is not well-formed: %v\n%s", f.Name, err, b)
			}
			switch x := tok.(type) {
			case xml.StartElement:
				if x.Name.Space == wml && (x.Name.Local == "ins" || x.Name.Local == "del") && len(x.Attr) > 1 {
					if depth++; depth > 1 {
						t.Fatalf("%s nests a tracked change in another:\n%s", f.Name, b)
					}
				}
			case xml.EndElement:
				if x.Name.Space == wml && (x.Name.Local == "ins" || x.Name.Local == "del") && depth > 0 {
					depth--
				}
			}
		}
	}
}

func partOf(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			return string(b)
		}
	}
	return ""
}

func partNames(t *testing.T, data []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

// replaceAll is a visitor that replaces find with repl wherever a run holds it whole.
func replaceAll(find, repl string) Visitor {
	return func(r Run) ([]Op, error) {
		if r.Skip != "" {
			return nil, nil
		}
		var ops []Op
		for i := strings.Index(r.Text, find); i >= 0; {
			ops = append(ops, Op{Kind: Replace, Start: i, End: i + len(find), Text: repl})
			j := strings.Index(r.Text[i+len(find):], find)
			if j < 0 {
				break
			}
			i += len(find) + j
		}
		return ops, nil
	}
}

const (
	bodyPlain    = `<w:p><w:r><w:t>The Supplier shall deliver the Goods within 30 days.</w:t></w:r></w:p><w:p><w:r><w:t>Payment is due within 60 days of invoice.</w:t></w:r></w:p>`
	bodySplit    = `<w:p><w:r><w:t xml:space="preserve">The Supplier </w:t></w:r><w:r><w:t>shall</w:t></w:r><w:r><w:t xml:space="preserve"> deliver the </w:t></w:r><w:r><w:t>Goods</w:t></w:r><w:r><w:t xml:space="preserve"> within 30 days.</w:t></w:r></w:p>`
	bodyMixed    = `<w:p><w:r><w:t xml:space="preserve">Payment is </w:t></w:r><w:r><w:rPr><w:b/><w:i/></w:rPr><w:t>due</w:t></w:r><w:r><w:rPr><w:u w:val="single"/></w:rPr><w:t xml:space="preserve"> within 60 days</w:t></w:r><w:r><w:t>.</w:t></w:r></w:p>`
	bodyNumbered = `<w:p><w:pPr><w:pStyle w:val="ListParagraph"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="3"/></w:numPr><w:ind w:left="720"/><w:rPr><w:b/></w:rPr></w:pPr><w:r><w:rPr><w:b/></w:rPr><w:t>Term.</w:t></w:r><w:r><w:t xml:space="preserve"> This Agreement lasts two years.</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="3"/></w:numPr><w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr></w:pPr><w:r><w:t>Termination.</w:t></w:r></w:p>`
	bodyTable    = `<w:p><w:r><w:t>Schedule</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Item</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Price</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>Widget</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>$100</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:p><w:r><w:t>End.</w:t></w:r></w:p>`
	bodyOpposing = `<w:p><w:r><w:t xml:space="preserve">The Supplier shall deliver within </w:t></w:r>` +
		`<w:del w:id="7" w:author="Opposing Counsel" w:date="2026-10-01T09:00:00Z"><w:r><w:delText>30</w:delText></w:r></w:del>` +
		`<w:ins w:id="8" w:author="Opposing Counsel" w:date="2026-10-01T09:00:00Z"><w:r><w:t>90</w:t></w:r></w:ins>` +
		`<w:r><w:t xml:space="preserve"> days</w:t></w:r><w:ins w:id="9" w:author="Opposing Counsel" w:date="2026-10-01T09:00:00Z"><w:r><w:t xml:space="preserve"> of the order</w:t></w:r></w:ins><w:r><w:t>.</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:rPr><w:del w:id="10" w:author="Opposing Counsel" w:date="2026-10-01T09:00:00Z"/></w:rPr></w:pPr><w:del w:id="11" w:author="Opposing Counsel" w:date="2026-10-01T09:00:00Z"><w:r><w:delText>Time is of the essence.</w:delText></w:r></w:del></w:p>` +
		`<w:p><w:r><w:t>Payment is due within 60 days.</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:rPr><w:ins w:id="12" w:author="Second Author" w:date="2026-10-02T09:00:00Z"/></w:rPr></w:pPr><w:ins w:id="13" w:author="Second Author" w:date="2026-10-02T09:00:00Z"><w:r><w:t>Late payment bears interest.</w:t></w:r></w:ins></w:p>` +
		`<w:p><w:r><w:t>Signed.</w:t></w:r></w:p>`
	bodyFields = `<w:p><w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>1</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r><w:r><w:t xml:space="preserve"> of the Agreement</w:t></w:r></w:p>` +
		`<w:p><w:sdt><w:sdtPr><w:alias w:val="Party"/></w:sdtPr><w:sdtContent><w:r><w:t>Acme Ltd</w:t></w:r></w:sdtContent></w:sdt><w:r><w:t xml:space="preserve"> is the Supplier.</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>See </w:t></w:r><w:r><w:footnoteReference w:id="1"/></w:r><w:r><w:t xml:space="preserve"> below.</w:t></w:r></w:p>`
	bodyQuotes = `<w:p><w:r><w:t>The “Effective Date” is the Supplier’s choice.</w:t></w:r></w:p>`
)

// TestRoundTripPreservesPartsAndOrder (AC-1): every part but the ones the writer changes comes
// back byte-identical, in order; with no op at all only settings.xml (Track Changes on) differs.
func TestRoundTripPreservesPartsAndOrder(t *testing.T) {
	for name, body := range map[string]string{"plain": bodyPlain, "split": bodySplit, "mixed": bodyMixed, "numbered": bodyNumbered, "table": bodyTable, "opposing": bodyOpposing, "fields": bodyFields} {
		in := fullDocx(t, body, [2]string{"word/media/image1.png", "\x89PNG\r\n\x1a\n not really"}, [2]string{"docProps/core.xml", "<cp/>"})
		d := openAll(t, in)
		same, err := d.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(partNames(t, in), partNames(t, same)) {
			t.Errorf("%s: part order changed: %v", name, partNames(t, same))
		}
		for _, p := range partNames(t, in) {
			if partOf(t, in, p) != partOf(t, same, p) {
				t.Errorf("%s: part %s changed on a plain round trip", name, p)
			}
		}
		out := walked(t, in, func(Run) ([]Op, error) { return nil, nil })
		if got := partNames(t, out); !slices.Equal(got, append(partNames(t, in), "word/settings.xml")) {
			t.Errorf("%s: parts after an empty walk: %v", name, got)
		}
		for _, p := range partNames(t, in) {
			if p != "[Content_Types].xml" && p != "word/_rels/document.xml.rels" && partOf(t, in, p) != partOf(t, out, p) {
				t.Errorf("%s: part %s changed on an empty walk", name, p)
			}
		}
	}
}

// TestWalkVisitsEveryRunInOrder (AC-2): one Run per <w:r>, in order, whose offsets tile Text().
func TestWalkVisitsEveryRunInOrder(t *testing.T) {
	for name, body := range map[string]string{"split": bodySplit, "mixed": bodyMixed, "table": bodyTable, "opposing": bodyOpposing, "fields": bodyFields} {
		d := openAll(t, fullDocx(t, body))
		text := textOf(t, d)
		at, para, idx := 0, -1, 0
		err := d.Walk(func(r Run) ([]Op, error) {
			if r.Index == -1 {
				return nil, nil
			}
			if r.Para < para || r.Para == para && r.Index != idx+1 || r.Para > para && r.Index != 0 {
				t.Errorf("%s: run (%d, %d) after (%d, %d)", name, r.Para, r.Index, para, idx)
			}
			para, idx = r.Para, r.Index
			if r.Start < at || text[r.Start:r.Start+len(r.Text)] != r.Text {
				t.Errorf("%s: run (%d, %d) %q at %d does not tile the text (at %d)", name, r.Para, r.Index, r.Text, r.Start, at)
			}
			at = r.Start + len(r.Text)
			return nil, nil
		}, NewRevisions("x", when))
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestWriterTextEqualsReaderText (AC-6): the scan's text is the reader's (the body's part of it).
func TestWriterTextEqualsReaderText(t *testing.T) {
	notes := `<w:footnotes xmlns:w="` + wml + `"><w:footnote w:id="1"><w:p><w:r><w:t>A note.</w:t></w:r></w:p></w:footnote></w:footnotes>`
	for name, body := range map[string]string{"plain": bodyPlain, "split": bodySplit, "mixed": bodyMixed, "numbered": bodyNumbered, "table": bodyTable, "opposing": bodyOpposing, "fields": bodyFields, "quotes": bodyQuotes,
		"fidelity": `<w:p><w:r><w:t>1.1</w:t><w:tab/><w:t>Defs</w:t><w:br/><w:t>x&#8217;s</w:t><w:noBreakHyphen/><w:sym w:char="2713"/></w:r><w:r><w:rPr><w:vanish/></w:rPr><w:t>hidden</w:t></w:r><!-- c --><w:r><mc:AlternateContent xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><mc:Choice Requires="wps"><w:t>box</w:t></mc:Choice><mc:Fallback><w:t>box</w:t></mc:Fallback></mc:AlternateContent></w:r></w:p><w:p/><w:p><w:r><w:t>unterminated`} {
		d := openAll(t, fullDocx(t, body, [2]string{"word/footnotes.xml", notes}))
		text := textOf(t, d)
		ix, err := scan(d.parts["word/document.xml"])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(text, ix.text) || text[len(ix.text):] != "\nFootnotes\n[1] A note.\n" {
			t.Errorf("%s: reader %q, writer %q", name, text, ix.text)
		}
	}
	for _, src := range []string{"<w:document xmlns:w=\"" + wml + "\"><w:body><w:p><w:r>", "<w:document xmlns:w=\"" + wml + "\"><w:body><w:p><w:r><w:t>unterminated", "<w:document><w:body></w:p>"} {
		d := &Document{parts: map[string]string{"word/document.xml": src}}
		_, rerr := d.Text()
		_, serr := scan(src)
		if (rerr == nil) != (serr == nil) {
			t.Errorf("reader and scanner disagree on %q: %v / %v", src, rerr, serr)
		}
	}
}

// TestReplaceInsideRunKeepsProps (AC-3): a replace in a bold run gives <w:del> and <w:ins> the
// run's properties, and the kept prefix and suffix keep them too; accept-all reads the new text.
func TestReplaceInsideRunKeepsProps(t *testing.T) {
	in := fullDocx(t, bodyMixed)
	out := walked(t, in, replaceAll("60 days", "45 days"))
	doc := partOf(t, out, "word/document.xml")
	want := `<w:r><w:rPr><w:u w:val="single"/></w:rPr><w:t xml:space="preserve"> within </w:t></w:r><w:del w:id="1" w:author="Horatiu Druma-Strugariu" w:date="2026-11-03T10:00:00Z"><w:r><w:rPr><w:u w:val="single"/></w:rPr><w:delText xml:space="preserve">60 days</w:delText></w:r></w:del><w:ins w:id="2" w:author="Horatiu Druma-Strugariu" w:date="2026-11-03T10:00:00Z"><w:r><w:rPr><w:u w:val="single"/></w:rPr><w:t xml:space="preserve">45 days</w:t></w:r></w:ins>`
	if !strings.Contains(doc, want) {
		t.Errorf("document.xml:\n%s\nwant it to hold\n%s", doc, want)
	}
	if got := decided(t, out, true); got != "Payment is due within 45 days.\n" {
		t.Errorf("accepted: %q", got)
	}
	if got := decided(t, out, false); got != "Payment is due within 60 days.\n" {
		t.Errorf("rejected: %q", got)
	}
}

// TestReplaceAcrossRunsAttachesInsertToLast (AC-4): a span over three runs is three deletions and
// one insertion after the last; accepted, the text is the intended one.
func TestReplaceAcrossRunsAttachesInsertToLast(t *testing.T) {
	in := fullDocx(t, bodySplit)
	// "shall deliver the Goods" spans runs 1..3: the visitor is what applyEdits does per run
	start, end := len("The Supplier "), len("The Supplier shall deliver the Goods")
	out := walked(t, in, func(r Run) ([]Op, error) {
		s, e := max(start, r.Start), min(end, r.Start+len(r.Text))
		if s >= e {
			return nil, nil
		}
		op := Op{Kind: Delete, Start: s - r.Start, End: e - r.Start}
		if e == end {
			op.Kind, op.Text = Replace, "must supply the Products"
		}
		return []Op{op}, nil
	})
	doc := partOf(t, out, "word/document.xml")
	if n := strings.Count(doc, "<w:del "); n != 3 {
		t.Errorf("%d deletions, want 3:\n%s", n, doc)
	}
	if n := strings.Count(doc, "<w:ins "); n != 1 || strings.Index(doc, "<w:ins ") < strings.LastIndex(doc, "<w:del ") {
		t.Errorf("%d insertions, want 1 after the last deletion:\n%s", n, doc)
	}
	if got := decided(t, out, true); got != "The Supplier must supply the Products within 30 days.\n" {
		t.Errorf("accepted: %q", got)
	}
}

// TestNoNestedRevisions (AC-5): a change inside another author's insertion closes and reopens
// it; the other author's changes stay as they were; an insertion beside a deleted run too.
func TestNoNestedRevisions(t *testing.T) {
	in := fullDocx(t, bodyOpposing)
	out := walked(t, in, func(r Run) ([]Op, error) {
		switch {
		case r.Text == "90":
			return []Op{{Kind: Replace, Start: 0, End: 2, Text: "45"}}, nil
		case r.Deleted == "30":
			return []Op{{Kind: InsertAfter, Text: " (thirty)"}}, nil
		}
		return nil, nil
	})
	doc := partOf(t, out, "word/document.xml")
	for _, keep := range []string{`<w:del w:id="7" w:author="Opposing Counsel" w:date="2026-10-01T09:00:00Z"><w:r><w:delText>30</w:delText></w:r></w:del>`,
		`<w:ins w:id="9" w:author="Opposing Counsel" w:date="2026-10-01T09:00:00Z"><w:r><w:t xml:space="preserve"> of the order</w:t></w:r></w:ins>`} {
		if !strings.Contains(doc, keep) {
			t.Errorf("the other author's change was touched:\n%s", doc)
		}
	}
	revs, err := openAll(t, out).Revisions()
	if err != nil {
		t.Fatal(err)
	}
	var authors []string
	for _, r := range revs {
		authors = append(authors, r.Author+":"+r.Kind)
	}
	// BigLaw's insertion after the deleted "30" and its 90→45 are adjacent, so they read as one substitution
	want := []string{"Opposing Counsel:deletion", "Horatiu Druma-Strugariu:substitution", "Opposing Counsel:insertion", "Opposing Counsel:deletion", "Second Author:insertion"}
	if !slices.Equal(authors, want) {
		t.Errorf("revisions after the edit: %v\nwant %v", authors, want)
	}
	if strings.Contains(doc, `<w:ins w:id="8" w:author="Opposing Counsel" w:date="2026-10-01T09:00:00Z"></w:ins>`) {
		t.Errorf("an empty reopened element was left:\n%s", doc)
	}
	if got := decided(t, out, true); got != "The Supplier shall deliver within  (thirty)45 days of the order.\nPayment is due within 60 days.\nLate payment bears interest.\nSigned.\n" {
		t.Errorf("accepted: %q", got)
	}
}

// TestRevisionIDsAboveExisting (AC-7): new ids start above the highest w:id already in the file.
func TestRevisionIDsAboveExisting(t *testing.T) {
	out := walked(t, fullDocx(t, bodyOpposing+`<w:p><w:bookmarkStart w:id="40" w:name="x"/><w:r><w:t>Bookmarked.</w:t></w:r><w:bookmarkEnd w:id="40"/></w:p>`), replaceAll("60", "45"))
	doc := partOf(t, out, "word/document.xml")
	if !strings.Contains(doc, `<w:del w:id="41" `) || !strings.Contains(doc, `<w:ins w:id="42" `) {
		t.Errorf("ids:\n%s", doc)
	}
}

// TestInsertParagraphCopiesPPr (AC-8): an inserted paragraph takes the anchor's numbering and
// indentation (never its section break), with a marked paragraph mark and the asking run's text
// properties.
func TestInsertParagraphCopiesPPr(t *testing.T) {
	out := walked(t, fullDocx(t, bodyNumbered), func(r Run) ([]Op, error) {
		if r.LastInPara && r.Index >= 0 {
			return []Op{{Kind: InsertParagraphAfter, Text: "Renewal. It renews yearly."}}, nil
		}
		return nil, nil
	})
	doc := partOf(t, out, "word/document.xml")
	want := `<w:p><w:pPr><w:pStyle w:val="ListParagraph"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="3"/></w:numPr><w:ind w:left="720"/><w:rPr><w:ins w:id="1" w:author="Horatiu Druma-Strugariu" w:date="2026-11-03T10:00:00Z"/><w:b/></w:rPr></w:pPr><w:ins w:id="2" w:author="Horatiu Druma-Strugariu" w:date="2026-11-03T10:00:00Z"><w:r><w:t xml:space="preserve">Renewal. It renews yearly.</w:t></w:r></w:ins></w:p>`
	if !strings.Contains(doc, want) {
		t.Errorf("document.xml:\n%s\nwant\n%s", doc, want)
	}
	if strings.Count(doc, "<w:sectPr") != strings.Count(bodyNumbered, "<w:sectPr")+1 { // the paragraph's and the body's
		t.Errorf("the section break was copied:\n%s", doc)
	}
	if got := decided(t, out, true); got != "Term. This Agreement lasts two years.\nRenewal. It renews yearly.\nTermination.\nRenewal. It renews yearly.\n" {
		t.Errorf("accepted: %q", got)
	}
	if got := decided(t, out, false); got != "Term. This Agreement lasts two years.\nTermination.\n" {
		t.Errorf("rejected: %q", got)
	}
	// rejecting an inserted mark joins the paragraph with the next, which keeps its own properties
	split := walked(t, fullDocx(t, `<w:p><w:pPr><w:jc w:val="center"/><w:rPr><w:ins w:id="5" w:author="A" w:date="2026-10-01T09:00:00Z"/></w:rPr></w:pPr><w:r><w:t>One</w:t></w:r></w:p><w:p><w:pPr><w:ind w:left="100"/></w:pPr><w:r><w:t>Two</w:t></w:r></w:p>`), func(Run) ([]Op, error) { return nil, nil })
	d := openAll(t, split)
	v, _ := d.Decide(false, nil)
	if err := d.Walk(v, NewRevisions("x", when)); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Part("word/document.xml"); !strings.Contains(got, `<w:p><w:pPr><w:ind w:left="100"/></w:pPr><w:r><w:t>One</w:t></w:r><w:r><w:t>Two</w:t></w:r></w:p>`) {
		t.Errorf("joined: %s", got)
	}
}

// TestDeleteParagraphMarksParagraphMark (AC-9): every run is deleted and the mark carries w:del;
// rejected, the paragraph is back; accepted, it is gone.
func TestDeleteParagraphMarksParagraphMark(t *testing.T) {
	out := walked(t, fullDocx(t, bodyNumbered), func(r Run) ([]Op, error) {
		if r.Para == 0 && r.Index == 0 {
			return []Op{{Kind: DeleteParagraph}}, nil
		}
		return nil, nil
	})
	doc := partOf(t, out, "word/document.xml")
	if !strings.Contains(doc, `<w:pPr><w:pStyle w:val="ListParagraph"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="3"/></w:numPr><w:ind w:left="720"/><w:rPr><w:del w:id="1" w:author="Horatiu Druma-Strugariu" w:date="2026-11-03T10:00:00Z"/><w:b/></w:rPr></w:pPr><w:del w:id="2" `) ||
		!strings.Contains(doc, `<w:delText xml:space="preserve"> This Agreement lasts two years.</w:delText>`) {
		t.Errorf("document.xml:\n%s", doc)
	}
	if got := decided(t, out, true); got != "Termination.\n" {
		t.Errorf("accepted: %q", got)
	}
	if got := decided(t, out, false); got != "Term. This Agreement lasts two years.\nTermination.\n" {
		t.Errorf("rejected: %q", got)
	}
}

// TestCommentCreatesThreeParts (AC-10): the first comment creates word/comments.xml, its
// relationship and its content type; a second adds only a comment; existing comments are kept
// and new ids are above theirs.
func TestCommentCreatesThreeParts(t *testing.T) {
	commentOn := func(text string) Visitor {
		return func(r Run) ([]Op, error) {
			if strings.Contains(r.Text, text) {
				return []Op{{Kind: Comment, Text: "Playbook: 30 days is our floor.\nSee clause 4."}}, nil
			}
			return nil, nil
		}
	}
	out := walked(t, fullDocx(t, bodyPlain), commentOn("30 days"))
	if !slices.Contains(partNames(t, out), "word/comments.xml") {
		t.Fatalf("parts: %v", partNames(t, out))
	}
	rels, ct, cm, doc := partOf(t, out, "word/_rels/document.xml.rels"), partOf(t, out, "[Content_Types].xml"), partOf(t, out, "word/comments.xml"), partOf(t, out, "word/document.xml")
	if !strings.Contains(rels, `<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="comments.xml"/>`) {
		t.Errorf("rels: %s", rels)
	}
	if !strings.Contains(ct, `<Override PartName="/word/comments.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.comments+xml"/>`) {
		t.Errorf("content types: %s", ct)
	}
	if !strings.Contains(cm, `<w:comment w:id="1" w:author="Horatiu Druma-Strugariu" w:date="2026-11-03T10:00:00Z" w:initials="HD"><w:p><w:r><w:annotationRef/></w:r><w:r><w:t xml:space="preserve">Playbook: 30 days is our floor.</w:t></w:r></w:p><w:p><w:r><w:t xml:space="preserve">See clause 4.</w:t></w:r></w:p></w:comment>`) {
		t.Errorf("comments: %s", cm)
	}
	if !strings.Contains(doc, `<w:commentRangeStart w:id="1"/><w:r><w:t xml:space="preserve">The Supplier shall deliver the Goods within 30 days.</w:t></w:r><w:commentRangeEnd w:id="1"/><w:r><w:commentReference w:id="1"/></w:r>`) {
		t.Errorf("document: %s", doc)
	}
	again := walked(t, out, commentOn("60 days"))
	if n := strings.Count(partOf(t, again, "word/comments.xml"), "<w:comment "); n != 2 {
		t.Errorf("%d comments after the second walk, want 2", n)
	}
	if n := strings.Count(partOf(t, again, "word/_rels/document.xml.rels"), "comments.xml"); n != 1 {
		t.Errorf("the relationship was added %d times", n)
	}
	if !strings.Contains(partOf(t, again, "word/comments.xml"), `<w:comment w:id="2" `) {
		t.Errorf("the second comment's id is not above the first:\n%s", partOf(t, again, "word/comments.xml"))
	}
}

// TestRevisionsParseInsDelAndParagraphMarks (AC-11): kinds, authors, text, offsets, context and
// ids, with a deletion beside an insertion by one author read as one substitution and a deleted
// paragraph with its mark as one deletion.
func TestRevisionsParseInsDelAndParagraphMarks(t *testing.T) {
	revs, err := openAll(t, fullDocx(t, bodyOpposing)).Revisions()
	if err != nil {
		t.Fatal(err)
	}
	// Text(): "The Supplier shall deliver within 90 days of the order.\n" (56) + "\n" (the deleted
	// paragraph, 57) + "Payment is due within 60 days.\n" (88) + "Late payment bears interest.\n" (117)
	want := []Revision{
		{ID: 7, Kind: "substitution", Author: "Opposing Counsel", Date: "2026-10-01T09:00:00Z", Inserted: "90", Deleted: "30", Start: 34, End: 36, Before: "The Supplier shall deliver within ", After: " days of the order.\n\nPayment is due with", IDs: []int{7, 8}},
		{ID: 9, Kind: "insertion", Author: "Opposing Counsel", Date: "2026-10-01T09:00:00Z", Inserted: " of the order", Start: 41, End: 54, IDs: []int{9}},
		{ID: 11, Kind: "deletion", Author: "Opposing Counsel", Date: "2026-10-01T09:00:00Z", Deleted: "Time is of the essence.\n", Start: 56, End: 56, After: "\nPayment is due within 60 days.\nLate pay", IDs: []int{11, 10}},
		{ID: 13, Kind: "insertion", Author: "Second Author", Date: "2026-10-02T09:00:00Z", Inserted: "Late payment bears interest.\n", Start: 88, End: 117, IDs: []int{13, 12}},
	}
	if len(revs) != len(want) {
		t.Fatalf("%d revisions: %+v", len(revs), revs)
	}
	for i := range want {
		if revs[i].ID != want[i].ID || revs[i].Kind != want[i].Kind || revs[i].Author != want[i].Author || revs[i].Inserted != want[i].Inserted || revs[i].Deleted != want[i].Deleted ||
			revs[i].Start != want[i].Start || revs[i].End != want[i].End || !slices.Equal(revs[i].IDs, want[i].IDs) {
			t.Errorf("revision %d:\n got %+v\nwant %+v", i, revs[i], want[i])
		}
	}
	if revs[0].Before != want[0].Before || revs[0].After != want[0].After || revs[2].After != want[2].After {
		t.Errorf("context: %q %q / %q", revs[0].Before, revs[0].After, revs[2].After)
	}
	if _, err := openAll(t, fullDocx(t, bodyOpposing)).Decide(true, []int{99}); err == nil {
		t.Error("an unknown id was accepted")
	}
}

// TestAcceptRejectEquivalence (AC-12): accept-all reads the reader's text (what Word shows with
// changes accepted), reject-all the text before the changes; a subset by id leaves the rest.
func TestAcceptRejectEquivalence(t *testing.T) {
	in := fullDocx(t, bodyOpposing)
	if got := decided(t, in, true); got != "The Supplier shall deliver within 90 days of the order.\nPayment is due within 60 days.\nLate payment bears interest.\nSigned.\n" {
		t.Errorf("accepted: %q", got)
	}
	if got := decided(t, in, false); got != "The Supplier shall deliver within 30 days.\nTime is of the essence.\nPayment is due within 60 days.\nSigned.\n" {
		t.Errorf("rejected: %q", got)
	}
	d := openAll(t, in)
	v, err := d.Decide(false, []int{8}) // the substitution 30→90, by either of its ids
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Walk(v, NewRevisions("x", when)); err != nil {
		t.Fatal(err)
	}
	if got := textOf(t, d); got != "The Supplier shall deliver within 30 days of the order.\n\nPayment is due within 60 days.\nLate payment bears interest.\nSigned.\n" { // the deleted paragraph still stands, empty
		t.Errorf("one rejected: %q", got)
	}
	out, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	wellFormed(t, out)
	revs, _ := openAll(t, out).Revisions()
	if len(revs) != 3 {
		t.Errorf("%d revisions left, want 3: %+v", len(revs), revs)
	}
}

// TestFieldsAndSdtSkipped (AC-13): runs in a field, a content control or holding a reference are
// reported skipped with their reason and left untouched; the rest of the paragraph is editable.
func TestFieldsAndSdtSkipped(t *testing.T) {
	var skips []string
	in := fullDocx(t, bodyFields)
	out := walked(t, in, func(r Run) ([]Op, error) {
		if r.Skip != "" {
			skips = append(skips, r.Skip+":"+r.Text)
			return nil, nil
		}
		if strings.Contains(r.Text, "Supplier") {
			return []Op{{Kind: Replace, Start: 0, End: len(r.Text), Text: " is the Buyer."}}, nil
		}
		return nil, nil
	})
	if want := []string{"field:", "field:", "field:", "field:1", "field:", "sdt:Acme Ltd", "complex:"}; !slices.Equal(skips, want) {
		t.Errorf("skips %q, want %q", skips, want)
	}
	doc := partOf(t, out, "word/document.xml")
	if !strings.Contains(doc, `<w:sdtContent><w:r><w:t>Acme Ltd</w:t></w:r></w:sdtContent>`) || !strings.Contains(doc, `<w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>1</w:t></w:r>`) || !strings.Contains(doc, `<w:footnoteReference w:id="1"/>`) {
		t.Errorf("a skipped run was touched:\n%s", doc)
	}
	if got := decided(t, out, true); got != "1 of the Agreement\nAcme Ltd is the Buyer.\nSee  below.\n" {
		t.Errorf("accepted: %q", got)
	}
	d := openAll(t, in)
	if err := d.Walk(func(r Run) ([]Op, error) {
		if r.Skip == "sdt" {
			return []Op{{Kind: Delete, Start: 0, End: 1}}, nil
		}
		return nil, nil
	}, NewRevisions("x", when)); err == nil || !strings.Contains(err.Error(), "sdt") {
		t.Errorf("an edit of a skipped run: %v", err)
	}
	if _, ok := d.changed["word/document.xml"]; ok {
		t.Error("a refused walk changed the document")
	}
}

// TestEscapesAndPreservesSpace (AC-14): markup characters, leading and trailing spaces, tabs
// and line breaks survive the round trip; every written w:t and w:delText preserves space.
func TestEscapesAndPreservesSpace(t *testing.T) {
	in := fullDocx(t, `<w:p><w:r><w:t xml:space="preserve"> A &amp; B &lt;C&gt; </w:t><w:tab/><w:t>D</w:t><w:br/><w:t xml:space="preserve">"E" </w:t></w:r></w:p>`)
	out := walked(t, in, replaceAll("D", "<D & \"d\">\tx"))
	doc := partOf(t, out, "word/document.xml")
	for _, s := range []string{`<w:t xml:space="preserve"> A &amp; B &lt;C&gt; </w:t><w:tab/></w:r>`, `<w:delText xml:space="preserve">D</w:delText>`, `<w:t xml:space="preserve">&lt;D &amp; "d"&gt;</w:t><w:tab/><w:t xml:space="preserve">x</w:t>`, `<w:br/><w:t xml:space="preserve">"E" </w:t>`} {
		if !strings.Contains(doc, s) {
			t.Errorf("document.xml lacks %s:\n%s", s, doc)
		}
	}
	if strings.Contains(doc, "<w:t>") || strings.Contains(doc, "<w:delText>") {
		t.Errorf("a written text element does not preserve space:\n%s", doc)
	}
	if got := decided(t, out, true); got != " A & B <C> \t<D & \"d\">\tx\n\"E\" \n" {
		t.Errorf("accepted: %q", got)
	}
	if Escape("a\x00b\x01<&>\"'") != `a b &lt;&amp;&gt;"'` {
		t.Errorf("Escape: %q", Escape("a\x00b\x01<&>\"'"))
	}
}

// TestRenderDeterministic (AC-17's writer half): the same walk twice is the same bytes.
func TestRenderDeterministic(t *testing.T) {
	in := fullDocx(t, bodyOpposing)
	a := walked(t, in, replaceAll("60", "45"))
	time.Sleep(10 * time.Millisecond)
	b := walked(t, in, replaceAll("60", "45"))
	if !bytes.Equal(a, b) {
		t.Error("two walks differ")
	}
}

// TestTrackRevisionsSetOnRender (AC-25): settings.xml gets w:trackRevisions, created with its
// relationship and content type when absent, extended when present, left alone when it has it.
func TestTrackRevisionsSetOnRender(t *testing.T) {
	none := walked(t, fullDocx(t, bodyPlain), replaceAll("60", "45"))
	if s := partOf(t, none, "word/settings.xml"); s != xmlHeader+`<w:settings xmlns:w="`+wml+`"><w:trackRevisions/></w:settings>` {
		t.Errorf("created settings: %q", s)
	}
	if !strings.Contains(partOf(t, none, "word/_rels/document.xml.rels"), `Target="settings.xml"`) || !strings.Contains(partOf(t, none, "[Content_Types].xml"), `PartName="/word/settings.xml"`) {
		t.Error("the created settings part is not related")
	}
	existing := `<?xml version="1.0"?><w:settings xmlns:w="` + wml + `"><w:zoom w:percent="100"/></w:settings>`
	some := walked(t, fullDocx(t, bodyPlain, [2]string{"word/settings.xml", existing}), replaceAll("60", "45"))
	if s := partOf(t, some, "word/settings.xml"); s != `<?xml version="1.0"?><w:settings xmlns:w="`+wml+`"><w:trackRevisions/><w:zoom w:percent="100"/></w:settings>` {
		t.Errorf("extended settings: %q", s)
	}
	on := `<?xml version="1.0"?><w:settings xmlns:w="` + wml + `"><w:trackRevisions w:val="true"/></w:settings>`
	had := walked(t, fullDocx(t, bodyPlain, [2]string{"word/settings.xml", on}), replaceAll("60", "45"))
	if s := partOf(t, had, "word/settings.xml"); s != on {
		t.Errorf("settings that had it: %q", s)
	}
}

// TestWalkRefusals: overlapping spans, an insertion of nothing, accepting a plain run, a span out
// of range and a read-only document are errors that change nothing.
func TestWalkRefusals(t *testing.T) {
	for name, v := range map[string]Visitor{
		"overlap": func(r Run) ([]Op, error) {
			return []Op{{Kind: Delete, Start: 0, End: 5}, {Kind: Delete, Start: 3, End: 6}}, nil
		},
		"nothing":   func(r Run) ([]Op, error) { return []Op{{Kind: InsertAfter}}, nil },
		"plain":     func(r Run) ([]Op, error) { return []Op{{Kind: Accept}}, nil },
		"range":     func(r Run) ([]Op, error) { return []Op{{Kind: Delete, Start: 0, End: 1000}}, nil },
		"no mark":   func(r Run) ([]Op, error) { return []Op{{Kind: AcceptMark}}, nil },
		"visitor's": func(r Run) ([]Op, error) { return nil, io.ErrUnexpectedEOF },
	} {
		d := openAll(t, fullDocx(t, bodyPlain))
		if err := d.Walk(v, NewRevisions("x", when)); err == nil {
			t.Errorf("%s: no error", name)
		} else if d.changed != nil {
			t.Errorf("%s: the document changed: %v", name, err)
		}
	}
	ro, _ := Open(fullDocx(t, bodyPlain))
	if err := ro.Walk(replaceAll("60", "45"), NewRevisions("x", when)); err != ErrReadOnly {
		t.Errorf("read-only walk: %v", err)
	}
	if _, err := ro.Bytes(); err != ErrReadOnly {
		t.Errorf("read-only bytes: %v", err)
	}
}

// TestLibreOfficeOriginRoundTrip: the one real fixture (LibreOffice's parts, prefixes and order)
// round-trips, its text is the reader's, an edit lands and accept-all reads it.
func TestLibreOfficeOriginRoundTrip(t *testing.T) {
	in, err := os.ReadFile(filepath.Join("testdata", "libreoffice-origin.docx"))
	if err != nil {
		t.Skip("testdata/libreoffice-origin.docx is missing")
	}
	d := openAll(t, in)
	text := textOf(t, d)
	if !strings.Contains(text, "within 30 days") {
		t.Fatalf("fixture text: %q", text)
	}
	out := walked(t, in, replaceAll("30 days", "45 days"))
	if got := decided(t, out, true); !strings.Contains(got, "within 45 days") || strings.Contains(got, "30 days") {
		t.Errorf("accepted: %q", got)
	}
	if got := decided(t, out, false); got != text {
		t.Errorf("rejected: %q\nwant %q", got, text)
	}
	for _, p := range partNames(t, in) {
		if p != "word/document.xml" && p != "word/settings.xml" && partOf(t, in, p) != partOf(t, out, p) {
			t.Errorf("part %s changed", p)
		}
	}
}

// TestRenderedFilesOpenInLibreOffice: every rendered fixture converts with LibreOffice headless
// (a malformed part would fail the conversion, as Word would ask to repair). Skipped where soffice
// is not installed, unless BIGLAW_REQUIRE_SOFFICE is set.
func TestRenderedFilesOpenInLibreOffice(t *testing.T) {
	soffice, err := exec.LookPath("soffice")
	if err != nil {
		if os.Getenv("BIGLAW_REQUIRE_SOFFICE") != "" {
			t.Fatal("soffice is not installed and BIGLAW_REQUIRE_SOFFICE is set")
		}
		t.Skip("soffice is not installed")
	}
	dir := t.TempDir()
	files := map[string][]byte{
		"replace.docx":  walked(t, fullDocx(t, bodyMixed), replaceAll("60 days", "45 days")),
		"opposing.docx": walked(t, fullDocx(t, bodyOpposing), replaceAll("60", "45")),
		"comment.docx":  walked(t, fullDocx(t, bodyPlain), func(r Run) ([]Op, error) { return []Op{{Kind: Comment, Text: "A comment."}}, nil }),
		"paragraph.docx": walked(t, fullDocx(t, bodyNumbered), func(r Run) ([]Op, error) {
			return []Op{{Kind: InsertParagraphAfter, Text: "New clause."}, {Kind: DeleteParagraph}}, nil
		}),
		"accepted.docx": nil,
	}
	d := openAll(t, fullDocx(t, bodyOpposing))
	v, _ := d.Decide(true, nil)
	_ = d.Walk(v, NewRevisions("x", when))
	files["accepted.docx"], _ = d.Bytes()
	if lo, err := os.ReadFile(filepath.Join("testdata", "libreoffice-origin.docx")); err == nil {
		files["libreoffice.docx"] = walked(t, lo, replaceAll("30 days", "45 days"))
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--headless", "--convert-to", "pdf", "--outdir", dir}
	for name := range files {
		args = append(args, filepath.Join(dir, name))
	}
	cmd := exec.Command(soffice, args...)
	cmd.Env = append(os.Environ(), "HOME="+dir) // its own profile: never the user's running instance
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("soffice: %v\n%s", err, out)
	}
	for name := range files {
		if _, err := os.Stat(filepath.Join(dir, strings.TrimSuffix(name, ".docx")+".pdf")); err != nil {
			t.Errorf("%s did not convert", name)
		}
	}
}
