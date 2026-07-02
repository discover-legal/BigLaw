// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Unmarked-change detection — the sneaky-edit problem. Opposing counsel
// returns a draft where some edits are tracked changes and others were made
// silently. The received document's BaselineText() (every tracked change
// rejected) should read EXACTLY like the version we last sent; any difference
// between the two is an edit not accounted for by tracked changes.

package integrity

import (
	"strings"

	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/textdiff"
)

// UnmarkedReport is the result of CompareVersions. Hunks are the non-equal
// word-level diff hunks between the sent text and the received document's
// baseline — each one an unmarked (silent) change; a substitution appears as
// a delete hunk followed by an insert hunk. Clean is true when no unmarked
// change was found. Obfuscation carries a ScanText pass over the received
// document's visible text, so one call answers the whole trust question.
type UnmarkedReport struct {
	Hunks       []textdiff.Hunk `json:"hunks"`
	Count       int             `json:"count"`
	Clean       bool            `json:"clean"`
	Obfuscation []Finding       `json:"obfuscation,omitempty"`
}

// CompareVersions diffs sentText (the version we last sent) against the
// received document's baseline (tracked changes rejected). Every difference
// is a change the counterparty made WITHOUT marking it. The received
// document's visible text is also scanned for Unicode obfuscation.
func CompareVersions(sentText string, received *ooxml.Document) UnmarkedReport {
	hunks := textdiff.Changes(
		normalizeForCompare(sentText),
		normalizeForCompare(received.BaselineText()),
	)
	return UnmarkedReport{
		Hunks:       hunks,
		Count:       len(hunks),
		Clean:       len(hunks) == 0,
		Obfuscation: ScanText(received.Text()),
	}
}

// NormalizeForCompare exposes the comparison normalisation for other
// formatting-insensitive diffs — Redtime normalises successive document
// versions with it so reflow and smart-quote churn never read as negotiation
// moves.
func NormalizeForCompare(s string) string { return normalizeForCompare(s) }

// normalizeForCompare is applied IDENTICALLY to both sides before diffing so
// formatting-only differences never read as silent edits:
//   - curly quotes → straight quotes (Word's autocorrect rewrites these);
//   - typographic dashes (hyphen U+2010 … minus U+2212) → ASCII hyphen;
//   - non-breaking / thin spaces → plain space (whitespace RUNS are then
//     collapsed by the word-level diff's tokenisation, so line wrapping and
//     paragraph reflow are also invisible);
//   - zero-width characters, soft hyphens, and bidi controls are dropped —
//     they carry no visible text and are reported by ScanText instead of
//     polluting the diff.
//
// Hunk offsets consequently index the NORMALIZED strings — approximate with
// respect to the originals, which is what the Hunk contract promises.
func normalizeForCompare(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '‘', '’', '‚', '‛': // curly/low single quotes
			return '\''
		case '“', '”', '„', '‟': // curly/low double quotes
			return '"'
		case '\u00A0', '\u202F', '\u2007', '\u2009': // non-breaking / thin spaces
			return ' '
		case '\u2010', '\u2011', '\u2012', '–', '—', '\u2212': // dashes
			return '-'
		case '\u200B', '\u200C', '\u200D', '\u2060', '\uFEFF', '\u00AD': // invisibles
			return -1
		case '\u202A', '\u202B', '\u202C', '\u202D', '\u202E', // bidi embeddings/overrides
			'\u2066', '\u2067', '\u2068', '\u2069': // bidi isolates
			return -1
		}
		return r
	}, s)
}
