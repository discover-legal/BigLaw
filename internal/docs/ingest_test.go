// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"regexp"
	"runtime"
	"runtime/metrics"
	"strings"
	"testing"
	"time"
)

// TestMain lets this test binary act as the PDF reader child (MaybeRunChild).
func TestMain(m *testing.M) {
	MaybeRunChild()
	os.Exit(m.Run())
}

// minimalDocx builds a .docx (zip + word/document.xml) holding the paragraphs (v1 vector helper).
func minimalDocx(paras ...string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paras {
		sb.WriteString("<w:p><w:r><w:t>" + p + "</w:t></w:r></w:p>")
	}
	sb.WriteString(`</w:body></w:document>`)
	w, _ := zw.Create("word/document.xml")
	w.Write([]byte(sb.String()))
	zw.Close()
	return buf.Bytes()
}

// makePDF writes a PDF with one page per entry: text pages draw their text in Helvetica; an
// entry "" is a scanned page (an 8×8 JPEG image XObject painted, no text operators) and "-" a blank
// page (no text, no image).
func makePDF(pages ...string) []byte { return makePDFCount(len(pages), pages...) }

func makePDFCount(count int, pages ...string) []byte {
	var ps []pdfPage
	for _, p := range pages {
		switch p {
		case "":
			ps = append(ps, pdfPage{Image: dctImage(jpegOf(8, 8, 0))})
		case "-":
			ps = append(ps, pdfPage{})
		default:
			ps = append(ps, pdfPage{Text: p})
		}
	}
	return buildPDF(count, ps...)
}

// pdfPage is one page of buildPDF: Text drawn in Helvetica, and/or Image (an XObject's dictionary
// and stream, e.g. dctImage) painted as /Im1.
type pdfPage struct{ Text, Image string }

func buildPDF(count int, pages ...pdfPage) []byte {
	var objs []string
	add := func(s string) int { objs = append(objs, s); return len(objs) }
	add("") // 1: catalog, filled below
	add("") // 2: pages
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	var kids []string
	for _, pg := range pages {
		content, xo := "", ""
		if pg.Image != "" {
			content, xo = "q 612 0 0 792 0 0 cm /Im1 Do Q\n", fmt.Sprintf(" /XObject << /Im1 %d 0 R >>", add(pg.Image))
		}
		if pg.Text != "" {
			content += "BT /F1 12 Tf 72 720 Td (" + pg.Text + ") Tj ET"
		}
		c := add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
		p := add(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 %d 0 R >>%s >> >>", c, font, xo))
		kids = append(kids, fmt.Sprintf("%d 0 R", p))
	}
	objs[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), count)
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

// dctImage is an image XObject holding JPEG data as a /DCTDecode stream (filter overrides it).
func dctImage(jpg []byte, filter ...string) string {
	f := "/DCTDecode"
	if len(filter) > 0 {
		f = filter[0]
	}
	return fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 8 /Height 8 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter %s /Length %d >>\nstream\n%s\nendstream", f, len(jpg), jpg)
}

// jpegOf is a w×h grey JPEG; seed varies its pixels, so two images differ.
func jpegOf(w, h, seed int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i*7 + seed*31)
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func TestExtractPlainText(t *testing.T) {
	x, err := Extract(t.Context(), []byte("PRIVILEGED. Preserve all documents for the Acme matter."))
	if err != nil || x.Method != "text" || !strings.Contains(x.Text, "Acme") || x.NoText {
		t.Errorf("plain text: %+v %v", x, err)
	}
	for _, bin := range [][]byte{{0, 1, 2, 3}, {0x89, 'P', 'N', 'X', 0}, []byte("a\x00b"), []byte("\xff\xfe\x00b")} {
		if _, err := Extract(t.Context(), bin); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Extract(%q) = %v, want ErrUnsupported (never an empty document)", bin, err)
		}
	}
	if x, err := Extract(t.Context(), []byte{0xff, 0xd8, 0xff, 0xe0, 0}); err != nil || x.Method != "vision" || !x.NoText || len(x.Scans) != 1 {
		t.Errorf("an image is one page to transcribe (PR 3c), never text: %+v %v", x, err)
	}
	if x, err := Extract(t.Context(), []byte("bell\a esc\x1b[31m red")); err != nil || x.Text != "bell  esc [31m red" { // r1 R11
		t.Errorf("control characters in text are cleaned for storage: %q %v", x.Text, err)
	}
	if x, err := Extract(t.Context(), []byte(" \n\t")); err != nil || !x.NoText || len(x.Notes) == 0 {
		t.Errorf("white space only is flagged: %+v %v", x, err)
	}
}

func TestExtractDOCX(t *testing.T) {
	x, err := Extract(t.Context(), minimalDocx("NON-DISCLOSURE AGREEMENT", "Confidential for five years &amp; more."))
	if err != nil || x.Method != "text_layer" || x.Text != "NON-DISCLOSURE AGREEMENT\nConfidential for five years & more.\n" {
		t.Errorf("docx: %+v %v", x, err)
	}
	if x, err := Extract(t.Context(), minimalDocx()); err != nil || !x.NoText || len(x.Notes) == 0 {
		t.Errorf("a .docx without text is flagged: %+v %v", x, err)
	}
	var xlsx bytes.Buffer
	zw := zip.NewWriter(&xlsx)
	w, _ := zw.Create("xl/workbook.xml")
	w.Write([]byte("<workbook/>"))
	zw.Close()
	if _, err := Extract(t.Context(), xlsx.Bytes()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a ZIP that is not a .docx: %v", err)
	}
	if _, err := Extract(t.Context(), []byte("PK\x03\x04 truncated")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a damaged ZIP: %v", err)
	}
}

func TestExtractPDFTextLayer(t *testing.T) {
	x, err := Extract(t.Context(), makePDF("The Lessee shall pay rent monthly.", "Second page clause."))
	if err != nil || x.Method != "text_layer" || x.Pages != 2 || x.NoText || !strings.Contains(x.Text, "The Lessee shall pay rent monthly.") ||
		!strings.Contains(x.Text, "\f") || !strings.Contains(x.Text, "Second page") {
		t.Fatalf("text PDF: %+v %v", x, err)
	}
	if x, err := Extract(t.Context(), makePDF("Only this page has text.", "", "And this one.", "")); err != nil || x.NoText || len(x.Notes) != 1 ||
		x.Notes[0] != "2 of 4 pages have no text layer (scanned images?) and were not read: page 2, 4." {
		t.Errorf("a half-scanned PDF keeps its text and says some pages may be scans: %+v %v", x, err)
	}
	pages := append([]string{"Only the first page has text."}, make([]string, 12)...) // r1 R5: the count is every blank page
	if x, err := Extract(t.Context(), makePDF(pages...)); err != nil || len(x.Notes) != 1 ||
		x.Notes[0] != "12 of 13 pages have no text layer (scanned images?) and were not read: page 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, …." {
		t.Errorf("12 blank pages: %q %v", x.Notes, err)
	}
	if x, err := Extract(t.Context(), makePDF(`NUL\000and\001controls \377`)); err != nil || strings.TrimSpace(x.Text) != "NUL and controls \u00ff" {
		t.Errorf("control characters in a text layer are cleaned for storage: %q %v", x.Text, err)
	}
	for _, bad := range [][]byte{[]byte("%PDF-1.4\ngarbage"), makePDF()} {
		if _, err := Extract(t.Context(), bad); !errors.Is(err, ErrUnreadable) {
			t.Errorf("damaged or empty PDF: %v, want ErrUnreadable", err)
		}
	}
	if _, err := Extract(t.Context(), makePDFCount(999999999, "x")); !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "999999999 pages") {
		t.Errorf("a PDF claiming a billion pages: %v", err)
	}
}

// pdfWith writes a one-page PDF whose page dictionary carries extra (offsets stay right).
func pdfWith(extra string) []byte {
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Junk " + extra + " >>",
		"<< /Length 16 >>\nstream\nBT (hello) Tj ET\nendstream"}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	var off []int
	for i, o := range objs {
		off = append(off, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range off {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

// TestHostilePDFContained (red team): a PDF nested deep enough to overflow the parser's stack (a
// fatal error recover cannot catch) is refused as unreadable and the server process lives; a PDF
// that takes too long is killed at the timeout; a cancelled request kills its reader.
func TestHostilePDFContained(t *testing.T) {
	if x, err := Extract(t.Context(), pdfWith("[[[]]]")); err != nil || !strings.Contains(x.Text, "hello") {
		t.Fatalf("the control PDF: %+v %v", x, err)
	}
	deep := strings.Repeat("[", 3_000_000) + strings.Repeat("]", 3_000_000)
	if _, err := Extract(t.Context(), pdfWith(deep)); !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "hostile") {
		t.Fatalf("a stack-overflow PDF: %v", err)
	}
	oldT := childTimeout
	childTimeout = time.Millisecond
	_, err := Extract(t.Context(), pdfWith("[]"))
	childTimeout = oldT
	if !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "took longer") {
		t.Errorf("a slow PDF: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond) // the request ends mid-parse
	start := time.Now()
	_, err = Extract(ctx, loopPDF())
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 1300*time.Millisecond {
		t.Errorf("a looping PDF whose request ends: %v after %s, want the reader killed at once", err, time.Since(start))
	}
	old := maxText
	maxText = 40
	_, err = extractPDF(makePDF("twenty-five characters..", "twenty-five characters.."))
	_, errDocx := parse(minimalDocx(strings.Repeat("x", 41)))           // a .docx's text is capped too (red team r1)
	_, errText := Extract(t.Context(), []byte(strings.Repeat("x", 41))) // and an upload of text (r2 N1)
	_, errPosted := (&Service{}).IngestText(t.Context(), "a.txt", strings.Repeat("x", 41))
	maxText = old
	for _, err := range []error{errText, errPosted} {
		if !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "larger than 40 bytes") {
			t.Errorf("text past the cap: %v", err)
		}
	}
	if !errors.Is(errDocx, ErrUnreadable) || !strings.Contains(errDocx.Error(), "larger than 40 bytes") {
		t.Errorf("a .docx whose text is past the cap: %v", errDocx)
	}
	if !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), "larger than 40 bytes") {
		t.Errorf("a PDF whose text expands past the cap: %v", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	cancel()
	if _, err := Extract(ctx, pdfWith("[]")); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled request: %v", err)
	}
}

// TestScannedPDFFlaggedNoText: a PDF with no text layer ingests with NoText and a visible note,
// keeps its original file, and is never silently empty.
func TestScannedPDFFlaggedNoText(t *testing.T) {
	t.Parallel()
	s := newService(t) // no vision model: a scan is stored, waiting, and said so (PR 3c)
	d, err := s.Ingest(t.Context(), "scan.pdf", "application/pdf", bytes.NewReader(makePDF("", "")))
	if !errors.Is(err, ErrVisionNotConfigured) || !d.NoText || d.Pages != 2 || len(d.Notes) != 1 || d.Notes[0] != pendingNote || d.FileID == "" || d.Status != "needs_transcription" {
		t.Fatalf("scanned PDF: %+v %v", d, err)
	}
	got, err := s.Get(t.Context(), []string{d.ID}, d.ID)
	if err != nil || !got.NoText || len(got.Notes) != 1 || got.Notes[0] != d.Notes[0] || got.Pages != 2 || got.Status != "needs_transcription" || got.Chunks != 0 {
		t.Fatalf("stored: %+v %v", got, err)
	}
	if data, err := fileData(s, []string{d.ID}, d.ID); err != nil || !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Errorf("the original is kept: %v", err)
	}
	blank, err := s.Ingest(t.Context(), "blank.pdf", "application/pdf", bytes.NewReader(makePDF("-", "-")))
	if err != nil || !blank.NoText || len(blank.Notes) != 1 || !strings.Contains(blank.Notes[0], "no text layer") || blank.Status != "ready" || blank.Method != "text_layer" {
		t.Fatalf("a PDF with nothing on it is stored as noText with a note: %+v %v", blank, err)
	}
	text, err := s.Ingest(t.Context(), "lease.pdf", "application/pdf", bytes.NewReader(makePDF("The Lessee shall pay rent.")))
	if err != nil || text.NoText || len(text.Notes) != 0 {
		t.Errorf("a text PDF is not flagged: %+v %v", text, err)
	}
}

// TestIngestRefusalLeavesNothing: a file that cannot be read is refused with its typed error and
// leaves no file, document, section or chunk behind.
func TestIngestRefusalLeavesNothing(t *testing.T) {
	t.Parallel()
	s := newService(t)
	for _, c := range []struct {
		data []byte
		want error
	}{{[]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, ErrUnreadable}, {[]byte("%PDF-1.7 broken"), ErrUnreadable}, {[]byte("a\x00b"), ErrUnsupported},
		{[]byte("II*\x00\x08\x00"), ErrScanEncoding}, {buildPDF(1, pdfPage{Image: dctImage(jpegOf(8, 8, 0), "/CCITTFaxDecode")}), ErrScanEncoding}} {
		if _, err := s.Ingest(t.Context(), "x.bin", "application/octet-stream", bytes.NewReader(c.data)); !errors.Is(err, c.want) {
			t.Errorf("ingest %q: %v, want %v", c.data, err, c.want)
		}
	}
	for _, table := range []string{"files", "documents", "sections", "chunks"} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows after refused uploads (%v)", table, n, err)
		}
	}
	if _, err := s.IngestText(t.Context(), "a/b.txt", "x"); err == nil {
		t.Error("IngestText applies the file-name policy")
	}
	if _, err := s.IngestText(t.Context(), "nul.txt", "a\x00b"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("IngestText refuses NUL: %v", err)
	}
	if d, err := s.IngestText(t.Context(), "pdfish.txt", "%PDF-1.4 is how a PDF starts"); err != nil || d.Method != "text" || d.NoText {
		t.Errorf("posted text that starts like a PDF is text: %+v %v", d, err)
	}
}

func TestNormalizeSourceKey(t *testing.T) {
	for in, want := range map[string]string{"SEC-Referral-Notice.docx": "sec-referral-notice", "docs/sub/Compliance Manual.pdf": "compliance manual",
		"plain-id": "plain-id", `C:\vdr\Cap Table.CSV`: "cap table"} {
		if got := NormalizeSourceKey(in); got != want {
			t.Errorf("NormalizeSourceKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolvesByTitle (v1 gate vector): a model citing a document by its file name, with or without
// the extension, resolves to the document in scope; an ambiguous or out-of-scope name does not.
func TestResolvesByTitle(t *testing.T) {
	t.Parallel()
	s := newService(t)
	a := ingestText(t, s, "sec-referral-notice.docx", "the staff alleges substantial unauthorized trading by the firm")
	b := ingestText(t, s, "memo.txt", "x")
	c := ingestText(t, s, "memo.md", "y")
	for _, ref := range []string{"sec-referral-notice.docx", "SEC-Referral-Notice", "vdr/sec-referral-notice.docx", a.ID} {
		if id, err := s.Resolve(t.Context(), []string{a.ID, b.ID}, ref); err != nil || id != a.ID {
			t.Errorf("resolve %q: %q %v", ref, id, err)
		}
	}
	if _, err := s.Resolve(t.Context(), []string{a.ID, b.ID, c.ID}, "memo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an ambiguous name: %v", err)
	}
	v, err := s.VerifyQuote(t.Context(), []string{a.ID}, a.ID, "substantial unauthorized trading", nil)
	if err != nil || !v.Verified || v.Method != "exact" {
		t.Errorf("verify after resolve: %+v %v", v, err)
	}
}

// TestIngestMemoryBounded (r2 reproducer N1): the server process's own memory for one ingest at
// the text cap, escape-heavy text (what JSON and Postgres array literals double), as a PDF (read
// by the child) and as an uploaded text file. Peak live heap above the start, sampled every
// millisecond; README § Memory states these figures and the whole-ingest bound.
func TestIngestMemoryBounded(t *testing.T) {
	if testing.Short() || raceOn { // make test runs it in its non-race pass (7 × 16 MiB under -race: ~4 min)
		t.Skip("ingests 7 inputs of up to 16 MiB; run without -race and -short")
	}
	s := newService(t)
	// Each input is built inside its own measurement, so earlier inputs do not inflate the live
	// heap (and with it the garbage the collector lets accumulate before it runs).
	for _, c := range []struct {
		name, file string
		data       func() []byte
	}{
		{"pdf", "big.pdf", func() []byte { return textPDF((maxText-1<<20)*3/2, strings.Repeat(`\\"`, 500)) }},
		{"escapes", "big.txt", func() []byte { return []byte(strings.Repeat(`\"`+"x ", (maxText-1<<20)/4)) }},
		// r3 M1: line- and tab-dense text used to cost 16 B per line or header tab in the server.
		{"newlines", "big.txt", func() []byte { return bytes.Repeat([]byte("\n"), maxText) }},
		{"tab header", "big.txt", func() []byte { return []byte("h" + strings.Repeat("\t", maxText-16) + "\n1\t2\t3\n") }},
		{"long header cells", "big.txt", func() []byte {
			cell := strings.Repeat("w", maxText/4-8)
			return []byte("# " + cell + "\n" + cell + "\t" + cell + "\t" + cell + "\n1\t2\t3\n")
		}},
		{"x lines", "big.txt", func() []byte { return bytes.Repeat([]byte("x\n"), maxText/2) }},
		{"short lines", "big.txt", func() []byte { return bytes.Repeat([]byte("ten bytes\n"), maxText/10) }},
	} {
		name, data := c.name, c.data()
		runtime.GC()
		sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		metrics.Read(sample)
		base := sample[0].Value.Uint64()
		done, peaked := make(chan struct{}), make(chan uint64)
		go func() {
			peak := uint64(0)
			for {
				select {
				case <-done:
					peaked <- peak
					return
				case <-time.After(time.Millisecond):
					metrics.Read(sample)
					peak = max(peak, sample[0].Value.Uint64())
				}
			}
		}()
		start := time.Now()
		d, err := s.Ingest(t.Context(), c.file, "application/octet-stream", bytes.NewReader(data))
		close(done)
		peak := <-peaked
		growth := (peak - min(peak, base)) >> 20
		t.Logf("%s: %d-byte file, %d chunks, %s; server peak heap +%d MiB (%.1f× the %d MiB text cap)", name, len(data), d.Chunks, time.Since(start), growth,
			float64(growth)/float64(maxText>>20), maxText>>20)
		if err != nil || growth > 80 { // MiB: 5× the 16 MiB cap README states
			t.Errorf("%s: %v, peak heap +%d MiB", name, err, growth)
		}
	}
	if b, err := os.ReadFile("/proc/self/status"); err == nil { // Linux: this process's peak RSS so far
		t.Logf("test process %s", regexp.MustCompile(`VmHWM:\s*\d+ kB`).Find(b))
	}
}
