// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package clients

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	normSuffixRe = regexp.MustCompile(`\b(inc|llc|llp|ltd|limited|corp|corporation|co|company|plc|gmbh|sa|nv|lp|group|holdings)\b`)
)

// NormalizeName is the conflict-matching form of an entity or person name:
// NFKD-folded with diacritics dropped (José → jose), invisible format
// characters dropped (zero-width spaces/joiners), apostrophes removed
// (O'Brien / O’Brien → obrien), every other non-alphanumeric run turned into
// a space (O'Brien-Smith → obrien smith), lowercased, common corporate
// suffixes (Inc/LLC/Ltd/…) stripped and whitespace collapsed. This lets
// "Acme Inc." match "Acme Corporation". Conflict checks should err toward
// flagging, so over-matching is acceptable (it just triggers human review).
func NormalizeName(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Cf, r):
			// combining mark (diacritic) or invisible format character
		case r == '\'' || r == '’' || r == '‘' || r == 'ʼ' || r == '`' || r == '´':
			// apostrophes join: O'Brien → obrien
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteByte(' ')
		}
	}
	out := normSuffixRe.ReplaceAllString(b.String(), " ")
	return strings.Join(strings.Fields(out), " ")
}

// normName is the package-internal alias the conflict check uses.
func normName(s string) string { return NormalizeName(s) }

// nameForms returns the comparison forms of a normalised name: the name
// itself, its tokens sorted (surname-first "Smith, Robert" ≡ "Robert
// Smith"), and the sorted tokens without single-letter initials ("Robert J.
// Smith" ≡ "Robert Smith"). Empty forms are omitted.
func nameForms(n string) []string {
	toks := strings.Fields(n)
	sorted := append([]string(nil), toks...)
	sort.Strings(sorted)
	var noInitials []string
	for _, t := range sorted {
		if len([]rune(t)) > 1 {
			noInitials = append(noInitials, t)
		}
	}
	forms := []string{n, strings.Join(sorted, " ")}
	if ni := strings.Join(noInitials, " "); ni != "" {
		forms = append(forms, ni)
	}
	return forms
}

// NamesMatch reports whether two raw names refer to the same party for
// conflict purposes: a comparison form of one contains (or is contained in)
// the same form of the other, or the two are equal once all spacing is
// removed ("Mary O Brien Smith" ≡ "Mary O'Brien-Smith"). Empty names never
// match.
func NamesMatch(a, b string) bool {
	na, nb := normName(a), normName(b)
	if na == "" || nb == "" {
		return false
	}
	if strings.ReplaceAll(na, " ", "") == strings.ReplaceAll(nb, " ", "") {
		return true
	}
	fa, fb := nameForms(na), nameForms(nb)
	for i := 0; i < len(fa) && i < len(fb); i++ { // like-for-like forms only
		if strings.Contains(fa[i], fb[i]) || strings.Contains(fb[i], fa[i]) {
			return true
		}
	}
	return false
}
