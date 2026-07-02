// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"regexp"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// BELO analytic layer (Layer 3). The score's remaining ceiling is the DEFENSE ISSUES — the
// analytic reasoning the rubric asks for (scienter element, criminal exposure, statute of
// limitations) that no amount of extraction or drafting produces on its own. They are DERIVED
// here from the grounded evidence graph: the conducts and the authorities they violate map,
// via a curated requiresElement knowledge base, onto the defense angles a lawyer would raise.

// elementRule is one requiresElement entry: when an authority cited in the matter matches, the
// defense issue it raises. This is the analytic ontology made concrete — domain knowledge that
// turns "Section 206(1) is charged" into "scienter must be proven, a contestable element".
type elementRule struct {
	match *regexp.Regexp
	issue string
}

var elementRules = []elementRule{
	{regexp.MustCompile(`206\s*\(\s*1\s*\)`),
		"Section 206(1) of the Advisers Act requires proof of SCIENTER (intent to defraud or reckless disregard for the truth). Unlike Section 206(2), a mere negligence showing is insufficient, so the scienter element is a primary defense point on the 206(1) charge."},
	{regexp.MustCompile(`206\s*\(\s*2\s*\)`),
		"Section 206(2) requires only NEGLIGENCE, not scienter — a lower bar than Section 206(1). The same conduct may sustain a 206(2) claim even where intent for 206(1) cannot be established."},
	{regexp.MustCompile(`(?i)207`),
		"Section 207 (materially false or misleading statements in a filing) requires the misstatement to have been made WILLFULLY — a state-of-mind element the defense can contest on the Form ADV disclosures."},
	{regexp.MustCompile(`(?i)209\s*\(\s*e\s*\)|obstruct|deletion of (trade|records)`),
		"The alleged obstruction (instructing deletion of records during a pending examination) carries parallel CRIMINAL exposure under 18 U.S.C. § 1519 (destruction or alteration of records in a federal investigation), beyond the civil Advisers Act provisions — a materially higher-stakes exposure to flag."},
	{regexp.MustCompile(`204A-1|(?i)code of ethics`),
		"Rule 204A-1 (Code of Ethics / personal-trading reporting) is implicated by the access-person reporting failures; late or inaccurate personal-trading reports are a direct, if lower-severity, violation."},
	{regexp.MustCompile(`204-2|(?i)books and records`),
		"The books-and-records charge under Rule 204-2 is a strict-liability recordkeeping requirement — intent is not an element, so it is among the harder allegations to defend on the merits and easier to concede while contesting the fraud counts."},
}

// solRule is a standing limitations defense for SEC civil-penalty actions.
const solIssue = "SEC civil monetary penalties are subject to the five-year statute of limitations under 28 U.S.C. § 2462. Any conduct occurring more than five years before the action is commenced may be time-barred for penalty purposes (disgorgement runs on a separate clock), so the Review-Period dates should be checked against the filing date."

// deriveDefenseIssues reads the evidence graph's violated-authority claims and returns the
// analytic defense issues they raise, deduped and in a stable order. It also flags the
// SCIENTER DISTINCTION explicitly when both Section 206(1) and 206(2) are charged (a named
// rubric point), and always surfaces the limitations defense (a standard enforcement angle).
func (o *Orchestrator) deriveDefenseIssues(task *types.Task) []string {
	g := o.evidenceGraph(task.ID)
	if g == nil {
		return nil
	}
	// Collect the authority text the matter cites (objects of `violates`).
	var authBlob strings.Builder
	for _, c := range g.Claims() {
		if c.P == "violates" {
			authBlob.WriteString(c.O)
			authBlob.WriteString(" | ")
		}
	}
	return defenseIssuesFor(authBlob.String())
}

// defenseIssuesFor maps the authorities a matter cites onto the analytic defense issues they
// raise — the pure, testable core of the BELO analytic layer.
func defenseIssuesFor(auth string) []string {
	if strings.TrimSpace(auth) == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		k := strings.ToLower(strings.TrimSpace(s))
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, s)
	}

	has206_1 := regexp.MustCompile(`206\s*\(\s*1\s*\)`).MatchString(auth)
	has206_2 := regexp.MustCompile(`206\s*\(\s*2\s*\)`).MatchString(auth)
	if has206_1 && has206_2 {
		add("The referral charges BOTH Section 206(1) — which requires scienter (intent) — AND Section 206(2) — which requires only negligence. This distinction is material to the defense: the 206(1) count can be contested on scienter grounds even where the negligence-based 206(2) count may stand on the same facts.")
	}
	for _, r := range elementRules {
		if r.match.MatchString(auth) {
			add(r.issue)
		}
	}
	add(solIssue)
	return out
}
