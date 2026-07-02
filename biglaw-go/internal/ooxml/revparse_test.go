// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Round-trip tests for tracked-change parsing: documents built with our own
// writer, marked up via ApplyTracked as "opposing counsel", then parsed back
// — kinds, authors, texts, spans, and the reconstructed baseline must all
// survive the trip through a real .docx archive.

package ooxml

import (
	"strings"
	"testing"
	"time"
)

func TestParseRevisionsRoundTrip(t *testing.T) {
	b := NewBuilder()
	b.Heading(1, "Master Services Agreement")
	b.Paragraph("The liability cap is twelve (12) months of fees paid under this Agreement.")
	b.Paragraph("Either party may terminate for convenience on thirty days notice.")
	b.Paragraph("This Agreement is governed by the laws of England and Wales.")
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	doc, err := Open(data)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	original := doc.Text()

	when := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	rev := NewRevisions("Opposing Counsel", when)
	apply := func(find, replace string) {
		t.Helper()
		text := doc.Text()
		idx := strings.Index(text, find)
		if idx < 0 {
			t.Fatalf("%q not found in document text", find)
		}
		if err := doc.ApplyTracked(idx, idx+len(find), replace, rev); err != nil {
			t.Fatalf("ApplyTracked(%q): %v", find, err)
		}
	}
	apply("twelve (12)", "thirty-six (36)") // substitution
	apply("for convenience ", "")           // pure deletion
	text := doc.Text()
	pos := strings.Index(text, "England and Wales") + len("England and Wales")
	if err := doc.ApplyTracked(pos, pos, ", excluding its conflict of laws rules", rev); err != nil {
		t.Fatalf("ApplyTracked insertion: %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes after markup: %v", err)
	}
	reopened, err := Open(out)
	if err != nil {
		t.Fatalf("reopen marked-up docx: %v", err)
	}

	if got := reopened.BaselineText(); got != original {
		t.Errorf("BaselineText != original text\n got: %q\nwant: %q", got, original)
	}

	revs := reopened.ParseRevisions()
	if len(revs) != 3 {
		t.Fatalf("parsed %d revisions, want 3: %+v", len(revs), revs)
	}
	byKind := map[RevisionKind]Revision{}
	for _, rv := range revs {
		if rv.Author != "Opposing Counsel" {
			t.Errorf("revision author = %q, want %q", rv.Author, "Opposing Counsel")
		}
		if rv.Date != when.Format(time.RFC3339) {
			t.Errorf("revision date = %q, want %q", rv.Date, when.Format(time.RFC3339))
		}
		byKind[rv.Kind] = rv
	}

	sub, ok := byKind[RevSubstitution]
	if !ok {
		t.Fatal("no substitution parsed")
	}
	if sub.DeletedText != "twelve (12)" || sub.InsertedText != "thirty-six (36)" {
		t.Errorf("substitution texts = del %q / ins %q", sub.DeletedText, sub.InsertedText)
	}
	if !strings.HasSuffix(sub.ContextBefore, "liability cap is ") {
		t.Errorf("substitution ContextBefore = %q, want suffix %q", sub.ContextBefore, "liability cap is ")
	}
	if !strings.HasPrefix(sub.ContextAfter, " months of fees") {
		t.Errorf("substitution ContextAfter = %q, want prefix %q", sub.ContextAfter, " months of fees")
	}
	visible := reopened.Text()
	if got := visible[sub.VisibleStart:sub.VisibleEnd]; got != "thirty-six (36)" {
		t.Errorf("substitution visible span = %q, want %q", got, "thirty-six (36)")
	}
	baseline := reopened.BaselineText()
	if got := baseline[sub.BaselineStart:sub.BaselineEnd]; got != "twelve (12)" {
		t.Errorf("substitution baseline span = %q, want %q", got, "twelve (12)")
	}

	del, ok := byKind[RevDeletion]
	if !ok {
		t.Fatal("no deletion parsed")
	}
	if del.DeletedText != "for convenience " || del.InsertedText != "" {
		t.Errorf("deletion texts = del %q / ins %q", del.DeletedText, del.InsertedText)
	}
	if got := baseline[del.BaselineStart:del.BaselineEnd]; got != "for convenience " {
		t.Errorf("deletion baseline span = %q", got)
	}
	if del.VisibleStart != del.VisibleEnd {
		t.Errorf("pure deletion visible span [%d, %d) should be zero-width", del.VisibleStart, del.VisibleEnd)
	}
	if got := visible[del.VisibleStart:]; !strings.HasPrefix(got, "on thirty days notice") {
		t.Errorf("deletion point lands before %q, want %q", got[:min(30, len(got))], "on thirty days notice")
	}

	ins, ok := byKind[RevInsertion]
	if !ok {
		t.Fatal("no insertion parsed")
	}
	if ins.InsertedText != ", excluding its conflict of laws rules" || ins.DeletedText != "" {
		t.Errorf("insertion texts = ins %q / del %q", ins.InsertedText, ins.DeletedText)
	}
	if !strings.HasSuffix(ins.ContextBefore, "England and Wales") {
		t.Errorf("insertion ContextBefore = %q", ins.ContextBefore)
	}
	if got := visible[ins.VisibleStart:ins.VisibleEnd]; got != ins.InsertedText {
		t.Errorf("insertion visible span = %q", got)
	}
	if ins.BaselineStart != ins.BaselineEnd {
		t.Errorf("pure insertion baseline span [%d, %d) should be zero-width", ins.BaselineStart, ins.BaselineEnd)
	}
}

// TestParseRevisionsAdjacencyRules verifies the substitution-merge guardrails:
// a del/ins pair merges only when same-author AND with nothing untouched
// between them.
func TestParseRevisionsAdjacencyRules(t *testing.T) {
	attrs := func(id int, author string) string {
		return `w:id="` + string(rune('0'+id)) + `" w:author="` + author + `" w:date="2026-01-01T00:00:00Z"`
	}
	body := `<w:p>` +
		`<w:r><w:t xml:space="preserve">Cap at </w:t></w:r>` +
		`<w:del ` + attrs(1, "Alice") + `><w:r><w:delText>12</w:delText></w:r></w:del>` +
		`<w:ins ` + attrs(2, "Bob") + `><w:r><w:t>36</w:t></w:r></w:ins>` +
		`<w:r><w:t xml:space="preserve"> months. Notice within </w:t></w:r>` +
		`<w:del ` + attrs(3, "Alice") + `><w:r><w:delText>30</w:delText></w:r></w:del>` +
		`<w:r><w:t xml:space="preserve"> days</w:t></w:r>` +
		`<w:ins ` + attrs(4, "Alice") + `><w:r><w:t> at most</w:t></w:r></w:ins>` +
		`</w:p>`
	d := docWithBody(t, body)

	if got, want := d.BaselineText(), "Cap at 12 months. Notice within 30 days\n"; got != want {
		t.Errorf("baseline = %q, want %q", got, want)
	}
	revs := d.ParseRevisions()
	if len(revs) != 4 {
		t.Fatalf("parsed %d revisions, want 4 unmerged: %+v", len(revs), revs)
	}
	wantKinds := []RevisionKind{RevDeletion, RevInsertion, RevDeletion, RevInsertion}
	wantAuthors := []string{"Alice", "Bob", "Alice", "Alice"}
	for i, rv := range revs {
		if rv.Kind != wantKinds[i] || rv.Author != wantAuthors[i] {
			t.Errorf("revision %d = %s by %s, want %s by %s", i, rv.Kind, rv.Author, wantKinds[i], wantAuthors[i])
		}
	}
}

// TestParseRevisionsMergesSubstitution verifies del+ins and ins+del pairs by
// the same author with nothing between them each collapse to one
// substitution, and that a deletion split across two <w:del> elements
// coalesces.
func TestParseRevisionsMergesSubstitution(t *testing.T) {
	a := `w:author="Alice" w:date="2026-01-01T00:00:00Z"`
	body := `<w:p>` +
		`<w:r><w:t xml:space="preserve">Cap at </w:t></w:r>` +
		`<w:del w:id="1" ` + a + `><w:r><w:delText>12</w:delText></w:r></w:del>` +
		`<w:ins w:id="2" ` + a + `><w:r><w:t>36</w:t></w:r></w:ins>` +
		`<w:r><w:t xml:space="preserve"> months</w:t></w:r>` +
		`</w:p><w:p>` +
		`<w:r><w:t xml:space="preserve">Notice within </w:t></w:r>` +
		`<w:ins w:id="3" ` + a + `><w:r><w:t>ten</w:t></w:r></w:ins>` +
		`<w:del w:id="4" ` + a + `><w:r><w:delText>thi</w:delText></w:r></w:del>` +
		`<w:del w:id="5" ` + a + `><w:r><w:delText>rty</w:delText></w:r></w:del>` +
		`<w:r><w:t xml:space="preserve"> days</w:t></w:r>` +
		`</w:p>`
	d := docWithBody(t, body)

	revs := d.ParseRevisions()
	if len(revs) != 2 {
		t.Fatalf("parsed %d revisions, want 2 substitutions: %+v", len(revs), revs)
	}
	first := revs[0]
	if first.Kind != RevSubstitution || first.DeletedText != "12" || first.InsertedText != "36" {
		t.Errorf("first revision = %+v, want substitution 12→36", first)
	}
	second := revs[1]
	if second.Kind != RevSubstitution || second.DeletedText != "thirty" || second.InsertedText != "ten" {
		t.Errorf("second revision = %+v, want substitution thirty→ten (ins-first order, split del coalesced)", second)
	}
	if got, want := d.BaselineText(), "Cap at 12 months\nNotice within thirty days\n"; got != want {
		t.Errorf("baseline = %q, want %q", got, want)
	}
	if got, want := d.Text(), "Cap at 36 months\nNotice within ten days\n"; got != want {
		t.Errorf("visible = %q, want %q", got, want)
	}
}
