// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// readZipParts returns the archive's part names in order and a name→content map.
func readZipParts(t *testing.T, data []byte) ([]string, map[string]string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("output is not a valid zip: %v", err)
	}
	names := make([]string, 0, len(zr.File))
	contents := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		names = append(names, f.Name)
		contents[f.Name] = string(b)
	}
	return names, contents
}

// buildZip assembles a zip from ordered name/content pairs.
func buildZip(t *testing.T, parts [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.Create(p[0])
		if err != nil {
			t.Fatalf("create %s: %v", p[0], err)
		}
		if _, err := w.Write([]byte(p[1])); err != nil {
			t.Fatalf("write %s: %v", p[0], err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

const docXMLWrapper = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>%BODY%<w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr></w:body></w:document>`

func docWithBody(t *testing.T, body string) *Document {
	t.Helper()
	xml := strings.ReplaceAll(docXMLWrapper, "%BODY%", body)
	data := buildZip(t, [][2]string{
		{"[Content_Types].xml", "<Types/>"},
		{"_rels/.rels", "<Relationships/>"},
		{"word/document.xml", xml},
	})
	d, err := Open(data)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return d
}

// ─── Writer ───────────────────────────────────────────────────────────────────

func TestBuilderProducesValidDocx(t *testing.T) {
	b := NewBuilder()
	b.Heading(1, "Asset Purchase Agreement")
	b.Heading(2, "Warranties & Indemnities") // exercises escaping
	b.Paragraph("The Seller warrants that it has good title.")
	b.Bullet("no encumbrances")
	b.PageBreak()
	b.Table([]string{"Index", "Clause"}, [][]string{{"1", "2.1"}, {"2", "3.4(b)"}})
	b.Spacer()

	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	names, contents := readZipParts(t, data)
	want := []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("part %d = %q, want %q", i, names[i], n)
		}
	}
	doc := contents["word/document.xml"]
	for _, snippet := range []string{
		"Asset Purchase Agreement",
		"Warranties &amp; Indemnities",
		"•  no encumbrances",
		`<w:br w:type="page"/>`,
		"<w:tbl>", "<w:tblBorders>", "3.4(b)",
		`<w:pgSz w:w="11906" w:h="16838"/>`,
	} {
		if !strings.Contains(doc, snippet) {
			t.Errorf("document.xml missing %q", snippet)
		}
	}
	if strings.Contains(doc, `w:orient="landscape"`) {
		t.Error("portrait document must not carry landscape orientation")
	}
}

func TestBuilderLandscapeSection(t *testing.T) {
	b := NewBuilder()
	b.SetLandscape(true)
	b.Paragraph("wide content")
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	_, contents := readZipParts(t, data)
	doc := contents["word/document.xml"]
	if !strings.Contains(doc, `w:orient="landscape"`) {
		t.Error("missing landscape orientation attribute")
	}
	if !strings.Contains(doc, `<w:pgSz w:w="16838" w:h="11906"`) {
		t.Error("landscape page size not swapped")
	}
}

func TestBuilderSkipsHeaderlessTable(t *testing.T) {
	b := NewBuilder()
	b.Table(nil, [][]string{{"orphan"}})
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	_, contents := readZipParts(t, data)
	if strings.Contains(contents["word/document.xml"], "<w:tbl>") {
		t.Error("table without headers must be skipped")
	}
}

func TestEscapeUnescapeRoundTrip(t *testing.T) {
	in := `a & b < c > "d" 'e'`
	if got := Unescape(Escape(in)); got != in {
		t.Errorf("round trip = %q, want %q", got, in)
	}
	if got := Unescape("&amp;lt;"); got != "&lt;" {
		t.Errorf("double-escaped decode = %q, want %q", got, "&lt;")
	}
}

// ─── Round-trip ───────────────────────────────────────────────────────────────

func TestRoundTripPreservesPartsAndOrder(t *testing.T) {
	// Deliberately odd part order plus extra parts the writer never emits.
	src := buildZip(t, [][2]string{
		{"docProps/core.xml", "<coreProperties/>"},
		{"word/document.xml", strings.ReplaceAll(docXMLWrapper, "%BODY%", "<w:p><w:r><w:t>hello</w:t></w:r></w:p>")},
		{"word/styles.xml", "<styles/>"},
		{"[Content_Types].xml", "<Types/>"},
		{"_rels/.rels", "<Relationships/>"},
	})
	d, err := Open(src)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	out, err := d.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	srcNames, srcContents := readZipParts(t, src)
	outNames, outContents := readZipParts(t, out)
	if strings.Join(srcNames, "|") != strings.Join(outNames, "|") {
		t.Fatalf("part order changed: %v -> %v", srcNames, outNames)
	}
	for name, want := range srcContents {
		if outContents[name] != want {
			t.Errorf("part %s not preserved byte-for-byte", name)
		}
	}
}

func TestOpenRejectsNonDocx(t *testing.T) {
	if _, err := Open([]byte("plain text, not a zip")); err == nil {
		t.Error("expected error for non-zip input")
	}
	noDoc := buildZip(t, [][2]string{{"other.xml", "<x/>"}})
	if _, err := Open(noDoc); err == nil {
		t.Error("expected error for zip without word/document.xml")
	}
}

// ─── Text extraction ──────────────────────────────────────────────────────────

func TestTextJoinsRunsAndSeparatesParagraphs(t *testing.T) {
	d := docWithBody(t,
		`<w:p><w:r><w:t>alpha </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>beta</w:t></w:r></w:p>`+
			`<w:p><w:r><w:t>gamma &amp; delta</w:t></w:r></w:p>`)
	text := d.Text()
	if !strings.Contains(text, "alpha beta") {
		t.Errorf("runs not joined: %q", text)
	}
	if !strings.Contains(text, "gamma & delta") {
		t.Errorf("entities not decoded: %q", text)
	}
	if strings.Contains(text, "betagamma") {
		t.Errorf("paragraphs not separated: %q", text)
	}
}

// ─── Tracked-change surgery ───────────────────────────────────────────────────

func fixedRevisions() *Revisions {
	return NewRevisions("Test Reviewer", time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC))
}

func TestApplyTrackedSubstitutionWithinOneRun(t *testing.T) {
	d := docWithBody(t, `<w:p><w:r><w:t>The deposit is ten percent of the price.</w:t></w:r></w:p>`)
	text := d.Text()
	start := strings.Index(text, "ten")
	if err := d.ApplyTracked(start, start+len("ten"), "fifteen", fixedRevisions()); err != nil {
		t.Fatalf("ApplyTracked: %v", err)
	}
	xml := d.DocumentXML()
	for _, snippet := range []string{
		`<w:del w:id="1" w:author="Test Reviewer" w:date="2026-07-01T09:00:00Z">`,
		`<w:delText xml:space="preserve">ten</w:delText>`,
		`<w:ins w:id="2" w:author="Test Reviewer" w:date="2026-07-01T09:00:00Z">`,
		">fifteen</w:t>",
	} {
		if !strings.Contains(xml, snippet) {
			t.Errorf("document.xml missing %q\nxml: %s", snippet, xml)
		}
	}
	// The visible text now reads with the insertion and without the deletion.
	after := d.Text()
	if !strings.Contains(after, "The deposit is fifteen percent") {
		t.Errorf("visible text wrong after edit: %q", after)
	}
	// The result still round-trips as a valid archive.
	out, err := d.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if _, err := Open(out); err != nil {
		t.Fatalf("edited output no longer opens: %v", err)
	}
}

func TestApplyTrackedAcrossRunBoundary(t *testing.T) {
	d := docWithBody(t,
		`<w:p><w:r><w:t>Payment due in thir</w:t></w:r><w:r><w:rPr><w:i/></w:rPr><w:t>ty days from closing.</w:t></w:r></w:p>`)
	text := d.Text()
	start := strings.Index(text, "thirty days")
	if start < 0 {
		t.Fatalf("test phrase not indexed: %q", text)
	}
	if err := d.ApplyTracked(start, start+len("thirty days"), "sixty days", fixedRevisions()); err != nil {
		t.Fatalf("ApplyTracked: %v", err)
	}
	xml := d.DocumentXML()
	if !strings.Contains(xml, `<w:delText xml:space="preserve">thir</w:delText>`) ||
		!strings.Contains(xml, `<w:delText xml:space="preserve">ty days</w:delText>`) {
		t.Errorf("deletion did not split across the two runs:\n%s", xml)
	}
	if strings.Count(xml, "<w:ins ") != 1 {
		t.Errorf("want exactly one insertion, got %d", strings.Count(xml, "<w:ins "))
	}
	// The second run's italics survive on its kept suffix.
	if !strings.Contains(xml, "<w:rPr><w:i/></w:rPr>") {
		t.Error("run properties lost during surgery")
	}
	after := d.Text()
	if !strings.Contains(after, "Payment due in sixty days from closing.") {
		t.Errorf("visible text wrong after cross-run edit: %q", after)
	}
}

func TestApplyTrackedPureDeletionAndInsertion(t *testing.T) {
	d := docWithBody(t, `<w:p><w:r><w:t>strictly confidential material</w:t></w:r></w:p>`)
	text := d.Text()
	start := strings.Index(text, "strictly ")
	if err := d.ApplyTracked(start, start+len("strictly "), "", fixedRevisions()); err != nil {
		t.Fatalf("pure deletion: %v", err)
	}
	if strings.Contains(d.DocumentXML(), "<w:ins ") {
		t.Error("pure deletion must not emit an insertion")
	}

	d2 := docWithBody(t, `<w:p><w:r><w:t>governed by the laws of England</w:t></w:r></w:p>`)
	text2 := d2.Text()
	pos := strings.Index(text2, "England") + len("England")
	if err := d2.ApplyTracked(pos, pos, " and Wales", fixedRevisions()); err != nil {
		t.Fatalf("pure insertion: %v", err)
	}
	if strings.Contains(d2.DocumentXML(), "<w:del ") {
		t.Error("pure insertion must not emit a deletion")
	}
	if !strings.Contains(d2.Text(), "England and Wales") {
		t.Errorf("insertion not visible: %q", d2.Text())
	}
}

func TestApplyTrackedRejectsParagraphSpan(t *testing.T) {
	d := docWithBody(t,
		`<w:p><w:r><w:t>end of one.</w:t></w:r></w:p><w:p><w:r><w:t>start of two</w:t></w:r></w:p>`)
	text := d.Text()
	start := strings.Index(text, "one.")
	end := strings.Index(text, "start") + len("start")
	if err := d.ApplyTracked(start, end, "x", fixedRevisions()); err == nil {
		t.Error("expected error for a span crossing a paragraph boundary")
	}
}

func TestRevisionIDsAreMonotonicAndUnique(t *testing.T) {
	d := docWithBody(t, `<w:p><w:r><w:t>one two three four five</w:t></w:r></w:p>`)
	rev := fixedRevisions()
	for _, word := range []string{"two", "four"} {
		text := d.Text()
		start := strings.Index(text, word)
		if err := d.ApplyTracked(start, start+len(word), strings.ToUpper(word), rev); err != nil {
			t.Fatalf("edit %q: %v", word, err)
		}
	}
	xml := d.DocumentXML()
	seen := map[string]bool{}
	rest := xml
	for {
		i := strings.Index(rest, `w:id="`)
		if i < 0 {
			break
		}
		rest = rest[i+len(`w:id="`):]
		j := strings.Index(rest, `"`)
		id := rest[:j]
		if seen[id] {
			t.Errorf("revision id %s reused", id)
		}
		seen[id] = true
	}
	if len(seen) != 4 { // two deletions + two insertions
		t.Errorf("want 4 distinct revision ids, got %d", len(seen))
	}
}
