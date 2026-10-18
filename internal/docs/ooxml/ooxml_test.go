// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// The reader's v1 ooxml vectors (4 of 12; the Builder, ApplyTracked and revision vectors come
// with the writer).

func readZipParts(t *testing.T, data []byte) ([]string, map[string]string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	var names []string
	contents := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		names = append(names, f.Name)
		contents[f.Name] = string(b)
	}
	return names, contents
}

func buildZip(t *testing.T, parts [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, _ := zw.Create(p[0])
		w.Write([]byte(p[1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const docXMLWrapper = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>%BODY%<w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr></w:body></w:document>`

func docWithBody(t *testing.T, body string) *Document {
	t.Helper()
	d, err := Open(buildZip(t, [][2]string{{"[Content_Types].xml", "<Types/>"}, {"word/document.xml", strings.ReplaceAll(docXMLWrapper, "%BODY%", body)}}))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestEscapeUnescapeRoundTrip(t *testing.T) {
	in := `a & b < c > "d" 'e'`
	if got := Unescape(Escape(in)); got != in {
		t.Errorf("round trip = %q", got)
	}
	if got := Unescape("&amp;lt;"); got != "&lt;" {
		t.Errorf("double-escaped decode = %q", got)
	}
}

func TestRoundTripPreservesPartsAndOrder(t *testing.T) {
	src := buildZip(t, [][2]string{{"docProps/core.xml", "<coreProperties/>"},
		{"word/document.xml", strings.ReplaceAll(docXMLWrapper, "%BODY%", "<w:p><w:r><w:t>hello</w:t></w:r></w:p>")},
		{"word/styles.xml", "<styles/>"}, {"[Content_Types].xml", "<Types/>"}, {"_rels/.rels", "<Relationships/>"}})
	d, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	srcNames, srcContents := readZipParts(t, src)
	outNames, outContents := readZipParts(t, out)
	if strings.Join(srcNames, "|") != strings.Join(outNames, "|") {
		t.Fatalf("part order changed: %v -> %v", srcNames, outNames)
	}
	for name, want := range srcContents {
		if outContents[name] != want {
			t.Errorf("part %s not preserved", name)
		}
	}
}

func TestOpenRejectsNonDocx(t *testing.T) {
	for _, data := range [][]byte{[]byte("plain text, not a zip"), buildZip(t, [][2]string{{"other.xml", "<x/>"}})} {
		if _, err := Open(data); !errors.Is(err, ErrNotDocx) {
			t.Errorf("Open(%.20q): %v, want ErrNotDocx", data, err)
		}
	}
}

// TestOpenRefusesZipBomb: an archive decompressing past maxDecompressed is refused while reading,
// not after.
func TestOpenRefusesZipBomb(t *testing.T) {
	old := maxDecompressed
	maxDecompressed = 4 << 20
	t.Cleanup(func() { maxDecompressed = old })
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	w.Write([]byte("<w:document/>"))
	w, _ = zw.Create("word/media/bomb.bin")
	zeros := make([]byte, 1<<20)
	for range maxDecompressed>>20 + 1 {
		w.Write(zeros)
	}
	zw.Close()
	if buf.Len() > 64<<10 {
		t.Fatalf("the bomb is %d bytes compressed", buf.Len())
	}
	if _, err := Open(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "decompresses to more than") {
		t.Errorf("zip bomb: %v", err)
	}
}

func TestTextJoinsRunsAndSeparatesParagraphs(t *testing.T) {
	d := docWithBody(t, `<w:p><w:r><w:t>alpha </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>beta</w:t></w:r></w:p>`+
		`<w:p><w:r><w:t>gamma &amp; delta</w:t></w:r></w:p>`+
		`<w:p><w:ins w:id="1"><w:r><w:t xml:space="preserve">inserted </w:t></w:r></w:ins><w:del w:id="2"><w:r><w:delText>deleted</w:delText></w:r></w:del>`+
		`<w:r><w:tab/><w:t/><w:t>end</w:t></w:r><w:r/><w:r><w:t>unterminated`)
	if got := d.Text(); got != "alpha beta\ngamma & delta\ninserted end" {
		t.Errorf("Text() = %q", got)
	}
}
