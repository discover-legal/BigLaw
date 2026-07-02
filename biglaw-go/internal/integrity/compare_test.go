// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// End-to-end unmarked-change detection over real .docx archives: a v1
// document built with the ooxml writer, a "received" version carrying one
// tracked change AND one silent edit — the detector must report exactly the
// silent edit, never the tracked one.

package integrity

import (
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/ooxml"
)

const (
	paraLiability   = "The liability cap is twelve (12) months of fees paid under this Agreement."
	paraTermination = "Either party may terminate for convenience on thirty days notice."
	paraGoverning   = "This Agreement is governed by the laws of England and Wales."
)

// buildDocx assembles paragraphs into an opened Document.
func buildDocx(t *testing.T, paras ...string) *ooxml.Document {
	t.Helper()
	b := ooxml.NewBuilder()
	for _, p := range paras {
		b.Paragraph(p)
	}
	data, err := b.Bytes()
	if err != nil {
		t.Fatalf("build docx: %v", err)
	}
	doc, err := ooxml.Open(data)
	if err != nil {
		t.Fatalf("open docx: %v", err)
	}
	return doc
}

// trackChange applies one tracked substitution as opposing counsel and
// round-trips the document through bytes so the test exercises real parsing.
func trackChange(t *testing.T, doc *ooxml.Document, find, replace string) *ooxml.Document {
	t.Helper()
	rev := ooxml.NewRevisions("Opposing Counsel", time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC))
	text := doc.Text()
	i := strings.Index(text, find)
	if i < 0 {
		t.Fatalf("%q not in document", find)
	}
	if err := doc.ApplyTracked(i, i+len(find), replace, rev); err != nil {
		t.Fatalf("ApplyTracked: %v", err)
	}
	data, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	reopened, err := ooxml.Open(data)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return reopened
}

func TestCompareVersionsDetectsOnlyTheSilentEdit(t *testing.T) {
	// v1 — what we sent.
	sent := buildDocx(t, paraLiability, paraTermination, paraGoverning)
	sentText := sent.Text()

	// Received — built from SILENTLY altered text ("thirty days" → "ten
	// days"), then one honest tracked change (liability 12 → 36) on top.
	silent := strings.Replace(paraTermination, "thirty days", "ten days", 1)
	received := buildDocx(t, paraLiability, silent, paraGoverning)
	received = trackChange(t, received, "twelve (12)", "thirty-six (36)")

	rep := CompareVersions(sentText, received)
	if rep.Clean {
		t.Fatal("Clean = true, but a silent edit was made")
	}
	if rep.Count != len(rep.Hunks) || rep.Count == 0 {
		t.Fatalf("count = %d, hunks = %d", rep.Count, len(rep.Hunks))
	}
	// The silent edit is reported…
	var sawDeleteThirty, sawInsertTen bool
	for _, h := range rep.Hunks {
		if strings.Contains(h.OurText, "thirty") {
			sawDeleteThirty = true
		}
		if strings.Contains(h.TheirText, "ten") {
			sawInsertTen = true
		}
		// …and the tracked change is NOT: neither its old nor its new text
		// may appear in any hunk.
		for _, s := range []string{h.OurText, h.TheirText} {
			if strings.Contains(s, "twelve") || strings.Contains(s, "thirty-six") {
				t.Errorf("tracked change leaked into unmarked report: %+v", h)
			}
		}
	}
	if !sawDeleteThirty || !sawInsertTen {
		t.Errorf("silent edit not fully reported: %+v", rep.Hunks)
	}
	if len(rep.Obfuscation) != 0 {
		t.Errorf("unexpected obfuscation findings: %+v", rep.Obfuscation)
	}
}

func TestCompareVersionsOnlyTrackedChangesIsClean(t *testing.T) {
	sent := buildDocx(t, paraLiability, paraTermination, paraGoverning)
	sentText := sent.Text()

	received := buildDocx(t, paraLiability, paraTermination, paraGoverning)
	received = trackChange(t, received, "twelve (12)", "thirty-six (36)")

	rep := CompareVersions(sentText, received)
	if !rep.Clean || rep.Count != 0 {
		t.Fatalf("tracked-changes-only document reported unmarked changes: %+v", rep.Hunks)
	}
}

func TestCompareVersionsIdenticalIsClean(t *testing.T) {
	sent := buildDocx(t, paraLiability, paraGoverning)
	received := buildDocx(t, paraLiability, paraGoverning)
	rep := CompareVersions(sent.Text(), received)
	if !rep.Clean || rep.Count != 0 {
		t.Fatalf("identical documents reported unmarked changes: %+v", rep.Hunks)
	}
}

// TestCompareVersionsFormattingOnlyIsClean: curly quotes, typographic dashes,
// non-breaking spaces, and zero-width characters in the received version must
// not read as silent edits — normalisation absorbs them.
func TestCompareVersionsFormattingOnlyIsClean(t *testing.T) {
	sentText := "The Supplier's obligations - \"Services\" - survive termination."
	received := buildDocx(t,
		"The Supplier’s obligations – “Services” – survive termination.")
	rep := CompareVersions(sentText, received)
	if rep.Count != 0 {
		t.Fatalf("formatting-only differences reported as unmarked changes: %+v", rep.Hunks)
	}
	// The zero-width variant: dropped from the diff but still reported by the
	// obfuscation scan.
	received2 := buildDocx(t,
		"The Supplier's obligations - \"Services\" - survive\u200B termination.")
	rep2 := CompareVersions(sentText, received2)
	if rep2.Count != 0 {
		t.Fatalf("zero-width char reported as unmarked change: %+v", rep2.Hunks)
	}
	if len(rep2.Obfuscation) == 0 {
		t.Error("zero-width char missed by the obfuscation pass")
	}
}

// TestCompareVersionsObfuscationInReceived: the received document's VISIBLE
// text (insertions accepted) is what gets scanned — a homoglyph smuggled in
// via a tracked insertion is still caught.
func TestCompareVersionsObfuscationInReceived(t *testing.T) {
	sent := buildDocx(t, paraLiability)
	received := buildDocx(t, paraLiability)
	// Tracked substitution whose NEW text carries a Cyrillic а.
	received = trackChange(t, received, "fees paid", "fees pаid")
	rep := CompareVersions(sent.Text(), received)
	var homo bool
	for _, f := range rep.Obfuscation {
		if f.Kind == KindHomoglyph {
			homo = true
		}
	}
	if !homo {
		t.Errorf("homoglyph in tracked insertion not caught: %+v", rep.Obfuscation)
	}
}
