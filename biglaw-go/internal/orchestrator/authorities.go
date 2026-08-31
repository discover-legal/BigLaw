// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// External-authority flagging. The citation gate mechanically verifies quotes
// against the MATTER documents; case law and statutes live outside its
// jurisdiction, so a model can salt a deliverable with plausible-looking
// citations ("Morgan v. Groupon, Inc., 27 N.Y.3d 423") that no layer checks —
// observed on a real matter run, where most such citations were fabricated or
// misattributed. Until live citator verification is wired into synthesis,
// every legal authority that does not appear in the matter record is marked in
// place and listed in a closing appendix, so no reader mistakes an unverified
// citation for a checked one.

package orchestrator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/types"
)

const authorityMarker = " [EXTERNAL AUTHORITY — NOT IN RECORD; VERIFY BEFORE RELIANCE]"

// caseParty is one side of a case caption: capitalized words (no embedded
// periods, so a sentence boundary ends the name) with an optional corporate
// suffix ("Groupon, Inc."). reCaseCitation joins two parties around "v." with
// an optional reporter cite, year, and slip-op tail.
const caseParty = `[A-Z][A-Za-z'’&\-]*(?:\s[A-Z][A-Za-z'’&\-]*){0,4}(?:,?\s(?:Inc|Corp|Co|Ltd|LLC|LLP|L\.P)\.?)?`

var reCaseCitation = regexp.MustCompile(
	`\*?` + caseParty + `\s+v\.?\s+` + caseParty + `\*?` +
		`(?:,?\s+\d{1,4}\s+[A-Z][\w.]*\s?\d{0,5}[a-z]{0,2}\s+\d{1,5})?(?:\s+\(\d{4}\))?(?:,?\s+\d{4}\s+NY\s+Slip\s+Op\s+\d+)?`)

// reStatuteCitation matches act-plus-section references ("NYLL §193",
// "29 C.F.R. § 541.100", "28 U.S.C. § 2462", "New York Labor Law §740").
var reStatuteCitation = regexp.MustCompile(
	`(?:\d+\s+)?(?:[A-Z][\w.]*\s+){0,3}(?:Law|Code|Rules?|Regulation|C\.?F\.?R\.?|U\.?S\.?C\.?|C\.?P\.?L\.?R\.?|NYLL|NYCRR|NYCL|NYWCL|NYSHRL|MWHL|FLSA)\s*§+\s*[\d][\d.\-()a-zA-Z]*`)

// flagExternalAuthorities marks every legal authority cited in body that does
// not appear in the matter corpus (the task's source documents, lowercased).
// The first occurrence of each unique authority gets an inline marker; all of
// them are listed in a closing "Authorities Requiring Verification" section.
// Case names that ARE in the record (e.g. the matter caption) pass untouched,
// as do contract-internal section references (§7(d) — no act token).
func flagExternalAuthorities(body, corpus string) string {
	if strings.TrimSpace(body) == "" {
		return body
	}
	corpus = strings.ToLower(corpus)
	type hit struct{ display, key string }
	var unique []hit
	seen := map[string]bool{}
	collect := func(matches []string) {
		for _, m := range matches {
			display := strings.Trim(strings.TrimSpace(m), "*,;")
			// A sentence-final period is punctuation, not part of the cite
			// ("NYLL §193." → "NYLL §193"); real cites end in digits, a year
			// paren, or a corporate-suffix period already trimmed above.
			display = strings.TrimSuffix(display, ".")
			if display == "" {
				continue
			}
			// Dedupe on the normalized core (case name without reporter/year),
			// so "Morgan v. Groupon, Inc., 27 N.Y.3d 423" and a later bare
			// "Morgan v. Groupon" count as one authority.
			core := normalizeAuthority(strings.ToLower(display))
			if core == "" || seen[core] {
				continue
			}
			// In-record authorities are verified by presence: the corpus
			// carries the reference (matter caption, quoted clause).
			if strings.Contains(corpus, core) {
				continue
			}
			seen[core] = true
			unique = append(unique, hit{display: display, key: core})
		}
	}
	collect(reCaseCitation.FindAllString(body, -1))
	collect(reStatuteCitation.FindAllString(body, -1))
	if len(unique) == 0 {
		return body
	}

	for _, h := range unique {
		// Mark the first literal occurrence only; repeats read from the appendix.
		if i := strings.Index(body, h.display); i >= 0 {
			end := i + len(h.display)
			body = body[:end] + authorityMarker + body[end:]
		}
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n## Authorities Requiring Verification\n\n")
	b.WriteString(fmt.Sprintf("The following %d legal authorit%s cited above do not appear in the matter record and were NOT verified by this system. Confirm each against a citator before any reliance:\n\n",
		len(unique), map[bool]string{true: "y is", false: "ies"}[len(unique) == 1]))
	for _, h := range unique {
		b.WriteString("- " + h.display + "\n")
	}
	return b.String()
}

// normalizeAuthority reduces an authority reference for corpus lookup: the
// core case name ("x v. y" without reporter/year) or the compacted statute
// reference, so formatting differences don't defeat an honest match.
var (
	reAuthorityTail   = regexp.MustCompile(`,?\s+\d.*$`)
	reCorporateSuffix = regexp.MustCompile(`,?\s+(?:inc|corp|co|ltd|llc|llp|l\.p)\.?$`)
)

func normalizeAuthority(key string) string {
	key = strings.ReplaceAll(key, "*", "")
	if strings.Contains(key, " v. ") || strings.Contains(key, " v ") {
		// Keep only the two party names around the "v." — reporters, years,
		// and corporate suffixes won't reliably appear in matter documents
		// even for the matter's own caption.
		key = reAuthorityTail.ReplaceAllString(key, "")
		key = reCorporateSuffix.ReplaceAllString(strings.TrimSpace(key), "")
		return strings.TrimSpace(key)
	}
	return strings.Join(strings.Fields(key), " ")
}

// matterCorpus concatenates the task's source documents (plus titles) for
// authority lookup.
func (o *Orchestrator) matterCorpus(task *types.Task) string {
	var b strings.Builder
	for _, docID := range task.DocumentIDs {
		if text, err := o.knowledge.GetFullText(docID); err == nil {
			b.WriteString(text)
			b.WriteString("\n")
		}
		if doc := o.knowledge.GetByID(docID); doc != nil {
			b.WriteString(doc.Title)
			b.WriteString("\n")
		}
	}
	return b.String()
}
