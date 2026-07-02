// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package integrity

import (
	"strings"
	"testing"
)

func findByKind(fs []Finding, kind FindingKind) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func TestCleanTextNoFindings(t *testing.T) {
	text := "The liability cap is twelve (12) months of fees paid under this Agreement. " +
		"Either party may terminate on thirty days' notice — subject to §4.2(b)."
	if fs := ScanText(text); fs != nil {
		t.Fatalf("clean text produced findings: %+v", fs)
	}
	if !Clean(nil) {
		t.Error("Clean(nil) = false")
	}
}

func TestForeignLanguageTextIsNotFlagged(t *testing.T) {
	// Whole words in a single non-Latin script are legitimate foreign text.
	text := "Die Vertragsparteien vereinbaren: настоящий договор регулируется правом Швейцарии. Καλημέρα."
	if fs := ScanText(text); fs != nil {
		t.Fatalf("pure foreign-script text flagged: %+v", fs)
	}
}

func TestHomoglyphCyrillicInLatinWord(t *testing.T) {
	// "Pаyment" with Cyrillic а (U+0430).
	text := "The Pаyment shall be due within thirty days."
	fs := ScanText(text)
	homo := findByKind(fs, KindHomoglyph)
	if len(homo) != 1 {
		t.Fatalf("got %d homoglyph findings, want 1: %+v", len(homo), fs)
	}
	f := homo[0]
	if f.Severity != SeverityCritical {
		t.Errorf("severity = %s, want critical", f.Severity)
	}
	if f.Count != 1 {
		t.Errorf("count = %d, want 1", f.Count)
	}
	if !strings.Contains(f.Detail, "U+0430") || !strings.Contains(f.Detail, "Cyrillic") ||
		!strings.Contains(f.Detail, "looks like 'a'") {
		t.Errorf("detail = %q", f.Detail)
	}
	if !strings.Contains(f.Sample, "Pаyment") {
		t.Errorf("sample = %q, want it to carry the affected word", f.Sample)
	}
	if f.Offset != strings.Index(text, "а") {
		t.Errorf("offset = %d, want %d", f.Offset, strings.Index(text, "а"))
	}
}

func TestHomoglyphGreekAndAggregation(t *testing.T) {
	// Greek omicron (U+03BF) substituted in two words, three times total →
	// one aggregated finding with count 3.
	text := "The cοmpany shall indemnify the cοntractοr in full."
	fs := ScanText(text)
	homo := findByKind(fs, KindHomoglyph)
	if len(homo) != 1 {
		t.Fatalf("got %d homoglyph findings, want 1 aggregated: %+v", len(homo), fs)
	}
	if homo[0].Count != 3 {
		t.Errorf("count = %d, want 3", homo[0].Count)
	}
	if homo[0].Severity != SeverityCritical {
		t.Errorf("severity = %s, want critical", homo[0].Severity)
	}
}

func TestHomoglyphFullwidth(t *testing.T) {
	text := "Total: ＄100 payable to Acme Corp." // fullwidth dollar is punctuation, not in a word
	// A fullwidth letter inside a Latin word:
	text2 := "The Supplｉer warrants the goods." // fullwidth ｉ (U+FF49)
	fs := ScanText(text2)
	homo := findByKind(fs, KindHomoglyph)
	if len(homo) != 1 || homo[0].Severity != SeverityCritical {
		t.Fatalf("fullwidth letter in Latin word: %+v", fs)
	}
	if !strings.Contains(homo[0].Detail, "fullwidth") {
		t.Errorf("detail = %q", homo[0].Detail)
	}
	_ = text
}

func TestInvisibleCharsAggregated(t *testing.T) {
	// A run of ZWSPs sprinkled through a clause → ONE finding with a count,
	// not one finding per character.
	text := "No\u200B waiver\u200B of\u200B any\u200B breach\u200B shall be deemed a waiver."
	fs := ScanText(text)
	inv := findByKind(fs, KindInvisibleChar)
	if len(inv) != 1 {
		t.Fatalf("got %d invisible_char findings, want 1 aggregated: %+v", len(inv), fs)
	}
	f := inv[0]
	if f.Count != 5 {
		t.Errorf("count = %d, want 5", f.Count)
	}
	if f.Severity != SeverityWarning {
		t.Errorf("severity = %s, want warning", f.Severity)
	}
	if !strings.Contains(f.Detail, "zero-width space") || !strings.Contains(f.Detail, "U+200B") {
		t.Errorf("detail = %q", f.Detail)
	}
	if !strings.Contains(f.Sample, "<U+200B>") {
		t.Errorf("sample does not render the invisible char visibly: %q", f.Sample)
	}
}

func TestAllInvisibleKindsDetected(t *testing.T) {
	text := "a\u200Bb c\u200Cd e\u200Df g\u2060h i\uFEFFj k\u00ADl"
	fs := ScanText(text)
	inv := findByKind(fs, KindInvisibleChar)
	if len(inv) != 6 {
		t.Fatalf("got %d invisible_char findings, want 6 (one per char): %+v", len(inv), fs)
	}
}

func TestBidiControlCritical(t *testing.T) {
	// RLO attack: "$100" can be made to display as "$001" etc.
	text := "The fee is \u202E001,000\u202C dollars."
	fs := ScanText(text)
	bidi := findByKind(fs, KindBidiControl)
	if len(bidi) != 2 { // RLO and PDF, one each
		t.Fatalf("got %d bidi findings, want 2: %+v", len(bidi), fs)
	}
	for _, f := range bidi {
		if f.Severity != SeverityCritical {
			t.Errorf("bidi severity = %s, want critical", f.Severity)
		}
	}
	if !strings.Contains(bidi[0].Detail, "RLO") {
		t.Errorf("first bidi detail = %q, want RLO named", bidi[0].Detail)
	}
	if !Clean(fs) {
		// sanity: bidi means not clean
	} else {
		t.Error("Clean() = true despite critical bidi findings")
	}
}

func TestBidiIsolatesDetected(t *testing.T) {
	text := "Amount \u2066reversed\u2069 here."
	fs := ScanText(text)
	if got := len(findByKind(fs, KindBidiControl)); got != 2 {
		t.Fatalf("got %d bidi findings, want 2 (LRI + PDI): %+v", got, fs)
	}
}

func TestMixedScriptWordWithoutConfusable(t *testing.T) {
	// Cyrillic д (U+0434) has no Latin lookalike in the curated table, so a
	// Latin word carrying it is mixed-script, not homoglyph.
	text := "The worд here is odd."
	fs := ScanText(text)
	if len(findByKind(fs, KindHomoglyph)) != 0 {
		t.Fatalf("non-confusable rune reported as homoglyph: %+v", fs)
	}
	mixed := findByKind(fs, KindMixedScriptWord)
	if len(mixed) != 1 {
		t.Fatalf("got %d mixed_script_word findings, want 1: %+v", len(mixed), fs)
	}
	if mixed[0].Severity != SeverityWarning {
		t.Errorf("severity = %s, want warning", mixed[0].Severity)
	}
	if !strings.Contains(mixed[0].Detail, "Cyrillic") {
		t.Errorf("detail = %q", mixed[0].Detail)
	}
}

func TestAccentedLatinIsClean(t *testing.T) {
	text := "Force majeure: grève, façade, Zürich, naïveté, São Paulo."
	if fs := ScanText(text); fs != nil {
		t.Fatalf("accented Latin flagged: %+v", fs)
	}
}

func TestWorstSeverityAndSummarize(t *testing.T) {
	text := "Pаyment\u200B due \u202Enow\u202C."
	fs := ScanText(text)
	if WorstSeverity(fs) != SeverityCritical {
		t.Errorf("WorstSeverity = %s, want critical", WorstSeverity(fs))
	}
	if Clean(fs) {
		t.Error("Clean = true, want false")
	}
	s := Summarize(fs)
	if !strings.Contains(s, "critical") || !strings.Contains(s, "homoglyph") || !strings.Contains(s, "bidi_control") {
		t.Errorf("Summarize = %q", s)
	}
	if Summarize(nil) != "no obfuscation findings" {
		t.Errorf("Summarize(nil) = %q", Summarize(nil))
	}
}

func TestFindingsSortedByOffset(t *testing.T) {
	text := "start \u202Ebidi\u202C then Pаyment and\u200B end"
	fs := ScanText(text)
	for i := 1; i < len(fs); i++ {
		if fs[i-1].Offset > fs[i].Offset {
			t.Fatalf("findings not sorted by offset: %+v", fs)
		}
	}
}
