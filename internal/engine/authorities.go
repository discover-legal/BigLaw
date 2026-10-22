// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"fmt"
	"regexp"
	"strings"
)

// External authorities (G5, ported from v1 orchestrator/authorities.go): quotes are verified
// against the matter's documents, but case law and statutes live outside them, so a model can
// salt a memo with plausible citations no layer checks. Every authority the record does not
// contain is marked where it first appears and listed in an appendix. There is no live citator.

const authorityMarker = " [EXTERNAL AUTHORITY — NOT IN RECORD; VERIFY BEFORE RELIANCE]"

const caseParty = `[A-Z][A-Za-z'’&\-]*(?:\s[A-Z][A-Za-z'’&\-]*){0,4}(?:,?\s(?:Inc|Corp|Co|Ltd|LLC|LLP|L\.P)\.?)?`

var (
	caseCite = regexp.MustCompile(`\*?` + caseParty + `\s+v\.?\s+` + caseParty + `\*?` +
		`(?:,?\s+\d{1,4}\s+[A-Z][\w.]*\s?\d{0,5}[a-z]{0,2}\s+\d{1,5})?(?:\s+\(\d{4}\))?`)
	statuteCite = regexp.MustCompile(`(?:\d+\s+)?(?:[A-Z][\w.]*\s+){0,3}(?:Law|Code|Rules?|Regulation|C\.?F\.?R\.?|U\.?S\.?C\.?|C\.?P\.?L\.?R\.?|NYLL|NYCRR|FLSA)\s*§+\s*[\d][\d.\-()a-zA-Z]*`)
	citeTail    = regexp.MustCompile(`,?\s+\d.*$`)
	corpSuffix  = regexp.MustCompile(`,?\s+(?:inc|corp|co|ltd|llc|llp|l\.p)\.?$`)
)

// authorityKey reduces a citation for lookup: a case's two party names, or a statute with its
// section sign and spacing normalised.
func authorityKey(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "*", "")
	if strings.Contains(s, " v. ") || strings.Contains(s, " v ") {
		return strings.TrimSpace(corpSuffix.ReplaceAllString(strings.TrimSpace(citeTail.ReplaceAllString(s, "")), ""))
	}
	s = strings.Trim(strings.ReplaceAll(strings.ReplaceAll(s, "§§", "§"), "§ ", "§"), ")]},.;")
	return strings.Join(strings.Fields(s), " ")
}

// flagExternalAuthorities marks each authority in body that corpus (the matter's documents) does
// not contain, at its first occurrence, and appends the list. A clean body is returned unchanged.
func flagExternalAuthorities(body, corpus string) (string, []string) {
	corpus = strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(corpus), "§§", "§"), "§ ", "§")
	var list []string
	seen := map[string]bool{}
	for _, m := range append(caseCite.FindAllString(body, -1), statuteCite.FindAllString(body, -1)...) {
		d := strings.TrimSuffix(strings.Trim(strings.TrimSpace(m), "*,;"), ".")
		for strings.HasSuffix(d, ")") && strings.Count(d, ")") > strings.Count(d, "(") {
			d = strings.TrimSuffix(d, ")")
		}
		k := authorityKey(d)
		if k == "" || seen[k] || strings.Contains(corpus, k) {
			continue
		}
		seen[k] = true
		list = append(list, d)
		if i := strings.Index(body, d); i >= 0 {
			body = body[:i+len(d)] + authorityMarker + body[i+len(d):]
		}
	}
	if len(list) == 0 {
		return body, nil
	}
	n := len(list)
	app := fmt.Sprintf("\n\n## Authorities Requiring Verification\n\nThe following %d legal %s cited above %s not appear in the matter record and %s NOT verified by BigLaw. Confirm each against a citator before any reliance:\n\n",
		n, plural(n, "authority", "authorities"), plural(n, "does", "do"), plural(n, "was", "were"))
	for _, d := range list {
		app += "- " + mdSafe(d, true) + "\n"
	}
	return strings.TrimRight(body, "\n") + app, list
}
