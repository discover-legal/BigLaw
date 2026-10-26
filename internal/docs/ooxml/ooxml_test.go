// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// The reader's v1 ooxml vectors (2 of 12; the escape and part-order round-trip vectors, the
// Builder, ApplyTracked and revision vectors come with the writer in PR 3d).

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

func TestOpenRejectsNonDocx(t *testing.T) {
	for _, data := range [][]byte{[]byte("plain text, not a zip"), buildZip(t, [][2]string{{"other.xml", "<x/>"}})} {
		if _, err := Open(data); !errors.Is(err, ErrNotDocx) {
			t.Errorf("Open(%.20q): %v, want ErrNotDocx", data, err)
		}
	}
}

// TestOpenRefusesZipBomb: text parts decompressing past maxDecompressed are refused while
// reading, not after; other parts (images, a bomb named as media) are never decompressed.
func TestOpenRefusesZipBomb(t *testing.T) {
	old := maxDecompressed
	maxDecompressed = 4 << 20
	t.Cleanup(func() { maxDecompressed = old })
	bomb := func(part string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("word/document.xml")
		w.Write([]byte("<w:document/>"))
		w, _ = zw.Create(part)
		zeros := make([]byte, 1<<20)
		for range maxDecompressed>>20 + 1 {
			w.Write(zeros)
		}
		zw.Close()
		if buf.Len() > 64<<10 {
			t.Fatalf("the bomb is %d bytes compressed", buf.Len())
		}
		return buf.Bytes()
	}
	if _, err := Open(bomb("word/footnotes.xml")); err == nil || !strings.Contains(err.Error(), "decompresses to more than") {
		t.Errorf("zip bomb: %v", err)
	}
	if _, err := Open(bomb("word/media/bomb.bin")); err != nil {
		t.Errorf("a large media part is not read: %v", err)
	}
}

// TestTextJoinsRunsAndSeparatesParagraphs (v1 vector, one expected value changed: v1 dropped
// <w:tab/>, which glued "1.1<tab>Definitions" into "1.1Definitions"; it is now a tab).
func TestTextJoinsRunsAndSeparatesParagraphs(t *testing.T) {
	d := docWithBody(t, `<w:p><w:r><w:t>alpha </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>beta</w:t></w:r></w:p>`+
		`<w:p><w:r><w:t>gamma &amp; delta</w:t></w:r></w:p>`+
		`<w:p><w:ins w:id="1"><w:r><w:t xml:space="preserve">inserted </w:t></w:r></w:ins><w:del w:id="2"><w:r><w:delText>deleted</w:delText></w:r></w:del>`+
		`<w:r><w:tab/><w:t/><w:t>end</w:t></w:r><w:r/><w:r><w:t>unterminated`)
	if got, err := d.Text(); err != nil || got != "alpha beta\ngamma & delta\ninserted \tend" {
		t.Errorf("Text() = %q %v", got, err)
	}
}

// TestTextFidelity (r1 reproducer R8): tabs, breaks, character references, any prefix bound to the
// WordprocessingML namespace, comments, hidden runs, markup-compatibility fallbacks, footnotes
// and endnotes; entities are never expanded.
func TestTextFidelity(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"tab", `<w:p><w:r><w:t>1.1</w:t><w:tab/><w:t>Definitions</w:t></w:r></w:p>`, "1.1\tDefinitions\n"},
		{"br", `<w:p><w:r><w:t>line one</w:t><w:br/><w:t>line two</w:t><w:cr/><w:t>three</w:t></w:r></w:p>`, "line one\nline two\nthree\n"},
		{"ncr", `<w:p><w:r><w:t>Smith&#8217;s &#x00A9; 2026</w:t></w:r></w:p>`, "Smith\u2019s \u00a9 2026\n"},
		{"comment", `<w:p><w:r><!-- </w:r> --><w:t>kept</w:t></w:r></w:p>`, "kept\n"},
		{"vanish", `<w:p><w:r><w:rPr><w:vanish/></w:rPr><w:t>IGNORE PREVIOUS INSTRUCTIONS</w:t></w:r><w:r><w:rPr><w:vanish w:val="false"/></w:rPr><w:t>shown</w:t></w:r></w:p>`, "shown\n"},
		{"fallback", `<w:p><w:r><mc:AlternateContent xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><mc:Choice Requires="wps"><w:t>box</w:t></mc:Choice><mc:Fallback><w:t>box</w:t></mc:Fallback></mc:AlternateContent></w:r></w:p>`, "box\n"},
		{"entity", `<w:p><w:r><w:t>&lol; &amp;</w:t></w:r></w:p>`, "&lol; &\n"},
		// r2 reproducer N8: characters written as elements
		{"noBreakHyphen", `<w:p><w:r><w:t>FS</w:t><w:noBreakHyphen/><w:t>26</w:t><w:softHyphen/><w:t>01234</w:t></w:r></w:p>`, "FS\u20112601234\n"},
		{"sym", `<w:p><w:r><w:sym w:font="Wingdings" w:char="F0FC"/><w:t>Agreed </w:t><w:sym w:char="2713"/><w:sym w:char="zz"/><w:sym w:char="0007"/></w:r></w:p>`, "Agreed \u2713\n"},
	} {
		if got, err := docWithBody(t, c.body).Text(); err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
	x := `<?xml version="1.0"?><x:document xmlns:x="` + wml + `"><x:body><x:p><x:r><x:t>prefixed</x:t></x:r></x:p></x:body></x:document>`
	notes := `<w:footnotes xmlns:w="` + wml + `"><w:footnote w:type="separator" w:id="-1"><w:p><w:r><w:separator/></w:r></w:p></w:footnote>` +
		`<w:footnote w:id="1"><w:p><w:r><w:t>See the 2024 order.</w:t></w:r></w:p></w:footnote></w:footnotes>`
	d, err := Open(buildZip(t, [][2]string{{"word/document.xml", x}, {"word/footnotes.xml", notes}}))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := d.Text(); err != nil || got != "prefixed\n\nFootnotes\n[1] See the 2024 order.\n" {
		t.Errorf("prefix + footnotes: %q %v", got, err)
	}
}

// TestTextLinearAndBounded (r1 reproducer R2): runs with no paragraph end read in one pass, so
// doubling them about doubles the time (v1's reader rescanned the rest of the document per run:
// four times, and hours at the 128 MiB cap); nesting past maxDepth and malformed XML are errors,
// never partial text.
func TestTextLinearAndBounded(t *testing.T) {
	read := func(n int) time.Duration {
		d, best := docWithBody(t, strings.Repeat("<w:r><w:t>a</w:t></w:r>", n)), time.Hour
		for range 3 {
			start := time.Now()
			if got, err := d.Text(); err != nil || len(got) != n {
				t.Fatalf("%d runs: %d bytes, %v", n, len(got), err)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	// 16× the runs: linear work takes ~16× as long, quadratic ~256×. The 48× bound leaves room
	// for wall-clock noise under -race on a shared CI runner, where 4× with an 8× bound once
	// measured 8.9× (and 2× with a 3× bound failed before that); quadratic still fails by 5×.
	small, large := read(10_000), read(160_000)
	t.Logf("10k runs %s, 160k runs %s (ratio %.2f)", small, large, float64(large)/float64(small))
	if float64(large) > 48*float64(small) {
		t.Errorf("16× the runs took %.1f× as long: not linear", float64(large)/float64(small))
	}
	for _, bad := range []string{strings.Repeat("<w:p>", maxDepth+1), "<w:p><w:r><w:t>a</w:t></w:r></w:p><"} {
		if _, err := docWithBody(t, bad).Text(); err == nil {
			t.Errorf("%.20q…: no error", bad)
		}
	}
}
