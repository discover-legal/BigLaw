// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/ontology"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// BELO analytic layer (Layer 3). The score's remaining ceiling is the DEFENSE ISSUES — the
// analytic reasoning the rubric asks for (scienter element, criminal exposure, statute of
// limitations) that no amount of extraction or drafting produces on its own. They are DERIVED
// here from the grounded evidence: the conducts and the authorities they violate map, via a
// curated knowledge base, onto the defense angles a lawyer would raise.
//
// TRIGGER DISCIPLINE — the July-3 Haiku regression: the derivation used to read ONLY the
// graph's `violates` claims, which come mostly from the LLM spine pass. On that run the spine
// model resolved to a dead local endpoint (BELO_SPINE_MODEL + a placeholder
// LOCAL_INFERENCE_URL made localize() rewrite it to "local:…"), every spine call failed
// silently, zero typed triples landed, and the whole analytic layer collapsed to the single
// unconditional § 2462 note. The fix is a deterministic floor: the charged authorities are
// ALSO harvested straight from the charging documents' verbatim text (regex over accusation
// sentences — no model call), so the templates fire whenever charged authorities exist,
// regardless of model tier, spine health, or matter mode. Graph claims still enrich the
// derivation (conduct labels, dates, quotes) when present.

const defenseDocTokenBudget = 20000 // ~ the charging doc; same bound as the spine pass

// ─── Element rules (requiresElement knowledge base) ───────────────────────────

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
	{regexp.MustCompile(`(?i)\b207\b`),
		"Section 207 (materially false or misleading statements in a filing) requires the misstatement to have been made WILLFULLY — a state-of-mind element the defense can contest on the Form ADV disclosures."},
	{regexp.MustCompile(`204A-1|(?i)code of ethics`),
		"Rule 204A-1 (Code of Ethics / personal-trading reporting) is implicated by the access-person reporting failures; late or inaccurate personal-trading reports are a direct, if lower-severity, violation."},
	{regexp.MustCompile(`204-2|(?i)books and records`),
		"The books-and-records charge under Rule 204-2 is a strict-liability recordkeeping requirement — intent is not an element, so it is among the harder allegations to defend on the merits and easier to concede while contesting the fraud counts."},
}

// ─── Template B: mental-state (scienter) mapping ───────────────────────────────

// mentalStatePair expresses the general pattern: two charged provisions over the same conduct
// that carry DISTINCT mental states. The canonical instance is Advisers Act § 206(1)
// (scienter) vs § 206(2) (negligence); further pairs (e.g. Exchange Act 10(b) vs Securities
// Act 17(a)(2)/(3)) slot in as entries.
type mentalStatePair struct {
	a, b           *regexp.Regexp
	aName, bName   string
	aState, bState string
	distinction    string
}

var mentalStatePairs = []mentalStatePair{
	{
		a: regexp.MustCompile(`206\s*\(\s*1\s*\)`), b: regexp.MustCompile(`206\s*\(\s*2\s*\)`),
		aName: "Section 206(1)", bName: "Section 206(2)",
		aState: "scienter (intent to defraud, or recklessness)", bState: "negligence",
		distinction: "The referral charges BOTH Section 206(1) — which requires scienter (intent) — AND Section 206(2) — which requires only negligence. This distinction is material to the defense: the 206(1) count can be contested on scienter grounds even where the negligence-based 206(2) count may stand on the same facts.",
	},
}

// ─── Template C: limitations rule ──────────────────────────────────────────────

// solIssue is the standing limitations defense for SEC civil-penalty actions; the per-conduct
// JOINS below apply its window to the specific dated conduct in the record.
const solIssue = "SEC civil monetary penalties are subject to the five-year statute of limitations under 28 U.S.C. § 2462. Any conduct occurring more than five years before the action is commenced may be time-barred for penalty purposes (disgorgement runs on a separate clock), so the Review-Period dates should be checked against the filing date."

// disgorgementCaveat is the separate-clock caveat every per-conduct time-bar conclusion carries.
const disgorgementCaveat = "Disgorgement runs on a separate clock — 15 U.S.C. § 78u(d)(8) (NDAA 2021) allows ten years for scienter-based violations (five otherwise), and equitable relief carries a ten-year window — so a § 2462 penalty bar does not extinguish disgorgement exposure for the same conduct."

// ─── Template A: criminal-parallel exposure ────────────────────────────────────

// criminalParallelIssue encodes the statutory knowledge (not retrieval): civil obstruction /
// records-destruction conduct carries parallel federal criminal exposure.
const criminalParallelIssue = "The alleged obstruction / records destruction carries PARALLEL CRIMINAL exposure beyond the civil counts: 18 U.S.C. § 1519 (destroying, altering, or falsifying records with intent to impede, obstruct, or influence the investigation or proper administration of any matter within federal agency jurisdiction — a felony carrying up to 20 years; § 1519 reaches conduct 'in relation to or in contemplation of' such a matter, so no proceeding need have been pending) and 18 U.S.C. § 1505 (corruptly obstructing a pending federal agency proceeding, such as an SEC examination — up to 5 years). SEC Enforcement routinely refers obstruction and spoliation conduct to DOJ under parallel-proceedings practice; counsel should assume a criminal referral is possible, evaluate Fifth Amendment implications for any individual testimony, and coordinate the Wells / examination response accordingly."

// ─── Deterministic scans over the charging documents' verbatim text ────────────

var (
	// reChargedContext marks a sentence as ACCUSATORY — citations harvested only from such
	// sentences count as "charged" authorities. This keeps a compliance manual's neutral rule
	// cites ("Access Persons must … under Rule 204A-1") from firing enforcement defense issues.
	reChargedContext = regexp.MustCompile(`(?i)\balleg|\bviolat|\bcharg|\bcount\b|\bthe division\b|\breferral\b|\benforcement\b|\bfraud|\bwillful`)
	// reCitation harvests authority citations: "Section 206(1)", "Rule 204-2(a)(3)",
	// "28 U.S.C. § 2462", "§ 1519", "Item 11.A".
	reCitation = regexp.MustCompile(`(?i)(?:section|rule)\s+\d[\w.\-]*(?:\([a-z0-9]+\))*|§+\s*\d[\w.\-]*(?:\([a-z0-9]+\))*|\b\d+\s+u\.s\.c\.[\s§]*\d*[\w().\-]*|\bitem\s+\d+[\w.]*`)
	// reObstructionConduct spots evidence-destruction conduct language: a destruction verb near
	// a records noun (either order), or the word obstruct itself, or the 209(e) charge.
	reObstructionConduct = regexp.MustCompile(`(?i)\bobstruct|209\s*\(\s*e\s*\)|\b(?:delet|destro|alter|wip|purg|shred)\w*[^.\n]{0,80}\b(?:record|document|file|spreadsheet|log|drive|email|evidence)|\b(?:record|document|file|spreadsheet|log|drive|email)s?\b[^.\n]{0,80}\b(?:delet|destro|alter|wip|purg|shred)\w*`)
	// reDirective + reQuotedSpan find ambiguous quoted instructions ("clean up the shared drive").
	reDirective  = regexp.MustCompile(`(?i)\b(?:instruct|direct|told|tell|ask|order|request)`)
	reQuotedSpan = regexp.MustCompile(`["\x{201C}]([^"\x{201C}\x{201D}\n]{6,140})["\x{201D}]`)
	// reAnchorContext marks a sentence that can carry the FILING ANCHOR date.
	reAnchorContext = regexp.MustCompile(`(?i)\breferral\b|\bnotice\b|\bcommenc|\bfiled\b|\bdated\b|\binitiat`)
	// Date tokens: quarters, "first quarter of", month(-day)-year, bare years.
	reQuarter     = regexp.MustCompile(`(?i)\bQ([1-4])[\s-]?((?:19|20)\d\d)\b`)
	reQuarterWord = regexp.MustCompile(`(?i)\b(first|second|third|fourth)\s+quarter\s+of\s+((?:19|20)\d\d)\b`)
	reMonthYear   = regexp.MustCompile(`(?i)\b(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept?|oct|nov|dec)\.?\s+(?:\d{1,2},?\s+)?((?:19|20)\d\d)\b`)
	reBareYear    = regexp.MustCompile(`\b((?:19|20)\d\d)\b`)
)

// defenseContext is everything the analytic templates read: the charged-authority text, the
// charging documents' verbatim text, the graph's BELO claims, and the task's allegation
// headings (the conduct-label fallback when the typed graph is sparse).
type defenseContext struct {
	Auth        string
	DocText     string
	Claims      []ontology.Claim
	Allegations []string
}

// deriveDefenseIssues assembles the defense context for a task — graph claims when the graph
// exists, PLUS the deterministic charging-document floor — and derives the analytic defense
// issues. It never depends on the spine model having succeeded.
func (o *Orchestrator) deriveDefenseIssues(task *types.Task) []string {
	ctx := defenseContext{Allegations: task.Allegations}
	if g := o.evidenceGraph(task.ID); g != nil {
		ctx.Claims = g.Claims()
	}
	ctx.DocText = strings.Join(o.chargingDocChunks(task, defenseDocTokenBudget), "\n")
	ctx.Auth = authorityText(ctx)
	return renderDerivedIssues(analyseDefense(ctx))
}

// defenseIssuesFor maps authority text alone onto the analytic defense issues it raises — the
// pure element-rule core, kept for callers/tests that have only the authority blob.
func defenseIssuesFor(auth string) []string {
	return renderDerivedIssues(analyseDefense(defenseContext{Auth: auth}))
}

// authorityText unions the graph's violated-authority claims with the CHARGED citations
// harvested deterministically from the charging documents (citations appearing in accusatory
// sentences). The doc scan is the retrieval-independent floor.
func authorityText(ctx defenseContext) string {
	var b strings.Builder
	for _, c := range ctx.Claims {
		if c.P == "violates" {
			b.WriteString(c.O)
			b.WriteString(" | ")
		}
	}
	for _, frag := range fragments(ctx.DocText) {
		if !reChargedContext.MatchString(frag) {
			continue
		}
		for _, cite := range reCitation.FindAllString(frag, -1) {
			b.WriteString(cite)
			b.WriteString(" | ")
		}
	}
	return b.String()
}

// analyseDefense is the pure, testable core: defense context in, derived issues out.
// Gated on charged authorities existing — a matter citing no authority in an accusatory
// context (e.g. a pure compliance/compare matter) derives nothing.
func analyseDefense(ctx defenseContext) []ontology.DerivedIssue {
	if strings.TrimSpace(ctx.Auth) == "" {
		return nil
	}
	var out []ontology.DerivedIssue
	out = append(out, mentalStateIssues(ctx)...) // Template B (+ the named distinction)
	for _, r := range elementRules {
		if r.match.MatchString(ctx.Auth) {
			out = append(out, ontology.DerivedIssue{Kind: ontology.ElementGapKind, Text: r.issue})
		}
	}
	out = append(out, criminalParallelIssues(ctx)...) // Template A
	out = append(out, limitationsIssues(ctx)...)      // Template C (rule + per-conduct joins)
	out = append(out, steelmanIssues(ctx)...)         // Template D
	return out
}

// renderDerivedIssues renders and dedupes the derived issues, preserving order.
func renderDerivedIssues(issues []ontology.DerivedIssue) []string {
	var out []string
	seen := map[string]bool{}
	for _, is := range issues {
		k := strings.ToLower(strings.TrimSpace(is.Text))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, is.Text)
	}
	return out
}

// ─── Template A — criminal-parallel exposure (C-034) ──────────────────────────

// criminalParallelIssues fires when the record shows obstruction / records-destruction
// conduct: the 209(e)/obstruction charge in the authorities, destruction language in the
// charging documents, an alteredRecords claim, or destruction language in any claim quote.
func criminalParallelIssues(ctx defenseContext) []ontology.DerivedIssue {
	quote := ""
	hit := reObstructionConduct.MatchString(ctx.Auth)
	if !hit {
		for _, frag := range fragments(ctx.DocText) {
			if reObstructionConduct.MatchString(frag) {
				hit, quote = true, strings.TrimSpace(frag)
				break
			}
		}
	}
	if !hit {
		for _, c := range ctx.Claims {
			if c.P == "alteredRecords" || reObstructionConduct.MatchString(c.Quote) {
				hit, quote = true, c.Quote
				break
			}
		}
	}
	if !hit {
		return nil
	}
	return []ontology.DerivedIssue{{Kind: ontology.CriminalExposureKind, Text: criminalParallelIssue, Quote: quote}}
}

// ─── Template B — scienter mapping (C-037) ─────────────────────────────────────

// mentalStateIssues emits, for each charged mental-state pair: the named distinction, and a
// per-conduct mapping — which alleged conduct the charging document ties to which provision,
// with an explicit defense flag when the document leaves the mapping unspecified.
func mentalStateIssues(ctx defenseContext) []ontology.DerivedIssue {
	var out []ontology.DerivedIssue
	for _, p := range mentalStatePairs {
		if !p.a.MatchString(ctx.Auth) || !p.b.MatchString(ctx.Auth) {
			continue
		}
		out = append(out, ontology.DerivedIssue{Kind: ontology.MentalStateKind, Text: p.distinction})
		conducts := conductLabels(ctx)
		if len(conducts) == 0 {
			continue
		}
		var lines []string
		unmapped := 0
		for _, c := range conducts {
			switch {
			case sentenceJoins(ctx.DocText, c, p.a):
				lines = append(lines, fmt.Sprintf("- %s: the charging document ties this conduct to %s, so %s must be proven for it.", c, p.aName, p.aState))
			case sentenceJoins(ctx.DocText, c, p.b):
				lines = append(lines, fmt.Sprintf("- %s: the charging document ties this conduct to %s (a %s standard).", c, p.bName, p.bState))
			default:
				unmapped++
				lines = append(lines, fmt.Sprintf("- %s: the charging document does NOT specify whether this conduct is charged under %s (%s) or only %s (%s).", c, p.aName, p.aState, p.bName, p.bState))
			}
		}
		text := fmt.Sprintf("Scienter mapping — %s requires %s while %s requires only %s; each alleged conduct must be mapped to the mental state it is charged under:\n%s",
			p.aName, p.aState, p.bName, p.bState, strings.Join(lines, "\n"))
		if unmapped > 0 {
			text += fmt.Sprintf("\nWhere the charging document leaves the mapping unspecified (%d of %d conducts above), that ambiguity is itself a defense point: demand that the Division commit to which conduct supports the scienter-based count, and contest %s as to any conduct it attributes to %s.",
				unmapped, len(conducts), p.aState, p.aName)
		}
		out = append(out, ontology.DerivedIssue{Kind: ontology.MentalStateKind, Text: text})
	}
	return out
}

// ─── Template C — limitations-to-conduct join (C-040) ──────────────────────────

const maxLimitationsJoins = 6

// maxJoinsPerConduct caps the § 2462 joins emitted for ONE conduct label — the anti-spray
// bound: a label the doc scan dates six ways must not yield six near-identical paragraphs.
const maxJoinsPerConduct = 2

// limitationsIssues always states the § 2462 rule (the standing enforcement angle) and, when
// the record carries DATED conduct, joins the five-year window to each dated conduct — the
// inference the pipeline used to state as a rule but never apply. Emissions are capped per
// authority (maxLimitationsJoins) AND per conduct label, and near-identical joins (same
// label, same window month) are deduped.
func limitationsIssues(ctx defenseContext) []ontology.DerivedIssue {
	out := []ontology.DerivedIssue{{Kind: ontology.LimitationsKind, Text: solIssue}}
	anchor, anchorRaw := filingAnchor(ctx.DocText)
	joins := 0
	perLabel := map[string]int{}
	seenWindow := map[string]bool{}
	for _, dc := range datedConducts(ctx) {
		if joins >= maxLimitationsJoins {
			break
		}
		lk := strings.ToLower(dc.label)
		wk := lk + "|" + dc.date.plusYears(5).display()
		if perLabel[lk] >= maxJoinsPerConduct || seenWindow[wk] {
			continue
		}
		perLabel[lk]++
		seenWindow[wk] = true
		joins++
		windowClose := dc.date.plusYears(5)
		txt := fmt.Sprintf("%s — the record dates this conduct to %s; under 28 U.S.C. § 2462 the five-year civil-penalty window for it closes around %s.", dc.label, dc.raw, windowClose.display())
		if anchorRaw != "" {
			if anchor.after(windowClose) {
				txt += fmt.Sprintf(" Measured against the %s filing anchor in the record, this conduct falls OUTSIDE the five-year window — civil penalties for it are presumptively time-barred (verify the date the action is actually commenced; tolling agreements may extend the window).", anchorRaw)
			} else {
				txt += fmt.Sprintf(" Measured against the %s filing anchor in the record, this conduct falls within the window for penalties.", anchorRaw)
			}
		}
		txt += " " + disgorgementCaveat
		out = append(out, ontology.DerivedIssue{Kind: ontology.LimitationsKind, About: dc.label, Text: txt, Quote: dc.quote})
	}
	return out
}

// ─── Template D — steelman the innocent reading (C-060) ────────────────────────

const maxSteelmanIssues = 3

// imperativeLeads is the lexicon of verbs a quoted housekeeping/records directive leads
// with. A quoted span is a DIRECTIVE only when it reads as an instruction — not merely any
// quoted string in a sentence with a directive verb ("directed trades to \"Lakeshore
// Trading\"" quotes an entity name; "instructed \"Delgado\"" quotes a recipient; neither
// is an instruction to steelman).
var imperativeLeads = map[string]bool{
	"clean": true, "remove": true, "delete": true, "destroy": true, "wipe": true,
	"shred": true, "purge": true, "erase": true, "clear": true, "dispose": true,
	"get": true, "move": true, "take": true, "send": true, "keep": true, "stop": true,
	"make": true, "put": true, "throw": true, "archive": true, "scrub": true,
	"drop": true, "cancel": true, "hold": true,
}

// isImperativeDirective reports whether a quoted span is a genuine instruction: at least
// three words, leading with an imperative verb (optionally after "please"). Quoted proper
// nouns ("Lakeshore Trading", "Delgado") fail both tests.
func isImperativeDirective(span string) bool {
	fs := strings.Fields(span)
	if len(fs) < 3 {
		return false
	}
	first := strings.ToLower(strings.Trim(fs[0], `"'.,;:`))
	if first == "please" {
		first = strings.ToLower(strings.Trim(fs[1], `"'.,;:`))
	}
	return imperativeLeads[first]
}

// steelmanIssues fires on conduct resting on ambiguous quoted DIRECTIVES: a sentence with a
// directive verb (the human-recipient context) carrying an embedded quoted span that itself
// reads as an instruction. A defense memo must state the innocent interpretation alongside
// the inculpatory one, and what discovery would distinguish them. One issue per distinct
// imperative verb — "clean up the shared drive" and "clean up of legacy files…" are the
// same instruction quoted twice, not two defense points.
func steelmanIssues(ctx defenseContext) []ontology.DerivedIssue {
	var out []ontology.DerivedIssue
	seen := map[string]bool{}
	seenVerb := map[string]bool{}
	scan := func(text string) {
		for _, frag := range fragments(text) {
			if len(out) >= maxSteelmanIssues {
				return
			}
			if !reDirective.MatchString(frag) {
				continue
			}
			for _, m := range reQuotedSpan.FindAllStringSubmatch(frag, -1) {
				span := strings.TrimSpace(m[1])
				if !isImperativeDirective(span) {
					continue
				}
				verb := strings.ToLower(strings.Trim(strings.Fields(span)[0], `"'.,;:`))
				k := strings.ToLower(span)
				if seen[k] || seenVerb[verb] {
					continue
				}
				seen[k] = true
				seenVerb[verb] = true
				out = append(out, ontology.DerivedIssue{
					Kind:  ontology.InnocentReadingKind,
					About: span,
					Quote: span,
					Text:  fmt.Sprintf("Steelman the innocent reading — the record quotes the instruction \"%s\" inculpatorily, but on its face the instruction can also be read innocently: routine file/records housekeeping or ordinary-course IT cleanup, which firms direct regularly without any intent to impede an examination. A defense memo must state that reading, not only the inculpatory one. What would distinguish the two readings in discovery: (i) the timing of the instruction relative to notice of the examination or investigation; (ii) whether the deletions were selective (targeting responsive records) or part of a general cleanup; (iii) whether backups or retention copies were preserved; (iv) whether the instruction followed a pre-existing document-retention schedule; and (v) the recipient's contemporaneous understanding of what was being asked.", span),
				})
				if len(out) >= maxSteelmanIssues {
					return
				}
			}
		}
	}
	scan(ctx.DocText)
	for _, c := range ctx.Claims {
		if len(out) >= maxSteelmanIssues {
			break
		}
		scan(c.Quote)
	}
	return out
}

// ─── Conduct / date plumbing ───────────────────────────────────────────────────

const maxConductLabels = 12

// conductLabels returns the matter's conduct labels: the typed graph's Issue-domain subjects
// when present, else the task's allegation headings (which survive even when the spine pass
// produced no typed triples).
func conductLabels(ctx defenseContext) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || len(out) >= maxConductLabels {
			return
		}
		if k := strings.ToLower(s); !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	for _, c := range ctx.Claims {
		switch c.P {
		case "committedBy", "violates", "harmed", "occurredDuring":
			if c.SClass.IsA(ontology.Issue) {
				add(c.S)
			}
		}
	}
	if len(out) == 0 {
		for _, a := range ctx.Allegations {
			add(a)
		}
	}
	return out
}

// recDate is a month-resolution record date. Month 0 means year-only; parsing resolves
// quarters and bare years to their LATEST month (quarter end, December) so time-bar
// conclusions are conservative: we only say "outside the window" when even the latest
// reading of the date is more than the window before the anchor.
type recDate struct {
	Year, Month int
}

func (d recDate) plusYears(n int) recDate { return recDate{Year: d.Year + n, Month: d.Month} }

func (d recDate) after(o recDate) bool {
	dm, om := d.Month, o.Month
	if dm == 0 {
		dm = 12
	}
	if om == 0 {
		om = 12
	}
	return d.Year*12+dm > o.Year*12+om
}

var monthNames = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}

func (d recDate) display() string {
	if d.Month >= 1 && d.Month <= 12 {
		return fmt.Sprintf("%s %d", monthNames[d.Month-1], d.Year)
	}
	return fmt.Sprintf("the end of %d", d.Year)
}

var monthIndex = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var quarterWordIndex = map[string]int{"first": 1, "second": 2, "third": 3, "fourth": 4}

// dateToken is one date mention: its verbatim surface form and its parsed month-resolution value.
type dateToken struct {
	raw string
	d   recDate
}

// parseRecDates extracts every date token from s, most specific first (a span matched by a
// quarter or month pattern is not re-counted as a bare year).
func parseRecDates(s string) []dateToken {
	var out []dateToken
	add := func(raw string, d recDate) {
		out = append(out, dateToken{raw: strings.TrimSpace(raw), d: d})
	}
	consumed := s
	for _, m := range reQuarter.FindAllStringSubmatch(s, -1) {
		q, y := int(m[1][0]-'0'), atoiYear(m[2])
		add(m[0], recDate{Year: y, Month: q * 3})
		consumed = strings.Replace(consumed, m[0], "", 1)
	}
	for _, m := range reQuarterWord.FindAllStringSubmatch(s, -1) {
		add(m[0], recDate{Year: atoiYear(m[2]), Month: quarterWordIndex[strings.ToLower(m[1])] * 3})
		consumed = strings.Replace(consumed, m[0], "", 1)
	}
	for _, m := range reMonthYear.FindAllStringSubmatch(consumed, -1) {
		mo := monthIndex[strings.ToLower(m[1])[:3]]
		add(m[0], recDate{Year: atoiYear(m[2]), Month: mo})
		consumed = strings.Replace(consumed, m[0], "", 1)
	}
	for _, m := range reBareYear.FindAllStringSubmatch(consumed, -1) {
		add(m[1], recDate{Year: atoiYear(m[1])})
	}
	return out
}

func atoiYear(s string) int {
	y := 0
	for _, r := range s {
		y = y*10 + int(r-'0')
	}
	return y
}

// datedConduct is one (conduct, date) pair the limitations window joins to.
type datedConduct struct {
	label, raw, quote string
	date              recDate
}

// reProceduralDateCtx marks a date as PROCEDURAL — an examination/referral/filing/response
// milestone, not a date the conduct occurred on. Joining the § 2462 window to such dates
// produced the template spray the partner review flagged (a five-year "window" computed
// from the EXAM START date, the referral date, a compliance-review year-end). Note
// "Review Period" is deliberately absent: the review period IS the conduct window.
var reProceduralDateCtx = regexp.MustCompile(`(?i)\b(examination|deficiency\s+letter|referral|enforcement\s+notice|compliance\s+review|annual\s+review|engagement|deadline|service\s+of|written\s+response|response\s+period|filed|filing|notice\s+is\s+dated|letter\s+dated)\b`)

// proceduralDate reports whether a specific date token in a fragment sits in procedural
// context — procedural wording within a window around the token.
func proceduralDate(frag, raw string) bool {
	i := strings.Index(frag, raw)
	if i < 0 {
		return reProceduralDateCtx.MatchString(frag)
	}
	lo := i - 80
	if lo < 0 {
		lo = 0
	}
	hi := i + len(raw) + 40
	if hi > len(frag) {
		hi = len(frag)
	}
	return reProceduralDateCtx.MatchString(frag[lo:hi])
}

// datedConducts joins each conduct label to the dates the record attaches to THE CONDUCT
// ITSELF — never to procedural milestones:
//   - graph claims: ONLY the conduct-dating predicate (occurredDuring) supplies dates; the
//     graph knows which dates are conduct dates, so no other claim field is mined;
//   - charging-document sentences mentioning the conduct: a date is joined only when its
//     local context is not procedural (exam start / referral / filing / response dates are
//     excluded).
//
// Deduped by (label, raw date).
func datedConducts(ctx defenseContext) []datedConduct {
	var out []datedConduct
	seen := map[string]bool{}
	add := func(label, raw, quote string, d recDate) {
		k := strings.ToLower(label + "|" + raw)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, datedConduct{label: label, raw: raw, quote: quote, date: d})
	}
	labels := conductLabels(ctx)
	for _, l := range labels {
		for _, c := range ctx.Claims {
			if c.P != "occurredDuring" || !strings.EqualFold(strings.TrimSpace(c.S), l) {
				continue
			}
			for _, field := range []string{c.O, c.Value} {
				for _, dt := range parseRecDates(field) {
					add(l, dt.raw, c.Quote, dt.d)
				}
			}
		}
	}
	for _, l := range labels {
		toks := labelTokens(l)
		for _, frag := range fragments(ctx.DocText) {
			if !tokensOverlap(frag, toks) {
				continue
			}
			for _, dt := range parseRecDates(frag) {
				if proceduralDate(frag, dt.raw) {
					continue
				}
				add(l, dt.raw, strings.TrimSpace(frag), dt.d)
			}
		}
	}
	return out
}

// filingAnchor finds the filing/referral anchor date in the charging documents: the LATEST
// full or month-resolution date in a sentence carrying anchor context (referral, notice,
// commenced, filed, dated). Returns the zero date and "" when none is found.
func filingAnchor(docText string) (recDate, string) {
	var best recDate
	raw := ""
	for _, frag := range fragments(docText) {
		if !reAnchorContext.MatchString(frag) {
			continue
		}
		for _, dt := range parseRecDates(frag) {
			if dt.d.Month == 0 {
				continue // a bare year is too weak to anchor a time-bar conclusion
			}
			if raw == "" || dt.d.after(best) {
				best, raw = dt.d, dt.raw
			}
		}
	}
	return best, raw
}

// ─── Text utilities ────────────────────────────────────────────────────────────

// fragments splits text into sentence-ish units for the locality joins. It does NOT split
// after an uppercase-letter abbreviation period ("U.S.C. § 2462" stays whole) — only after
// [.!?] preceded by a lowercase letter, digit, ')' or '"', or at newlines.
func fragments(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	start := 0
	flush := func(end int) {
		if f := strings.TrimSpace(s[start:end]); f != "" {
			out = append(out, f)
		}
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\n' || c == '\r':
			flush(i)
			start = i + 1
		case (c == '.' || c == '!' || c == '?') && i > start && i+1 < len(s) && (s[i+1] == ' ' || s[i+1] == '\t'):
			p := s[i-1]
			if (p >= 'a' && p <= 'z') || (p >= '0' && p <= '9') || p == ')' || p == '"' {
				flush(i + 1)
				start = i + 1
			}
		}
	}
	flush(len(s))
	return out
}

// labelTokens returns a conduct label's content tokens (length ≥ 4, lowercased).
func labelTokens(label string) []string {
	var out []string
	for _, t := range strings.Fields(strings.ToLower(label)) {
		t = strings.Trim(t, ".,;:()[]{}\"'`—-")
		if len(t) >= 4 {
			out = append(out, t)
		}
	}
	return out
}

// tokensOverlap reports whether frag contains at least two of the tokens (or all of them
// when there are fewer than two) — the locality join between a conduct label and a sentence.
func tokensOverlap(frag string, toks []string) bool {
	if len(toks) == 0 {
		return false
	}
	need := 2
	if len(toks) < need {
		need = len(toks)
	}
	f := strings.ToLower(frag)
	n := 0
	for _, t := range toks {
		if strings.Contains(f, t) {
			n++
			if n >= need {
				return true
			}
		}
	}
	return false
}

// sentenceJoins reports whether any charging-document sentence both cites the provision and
// mentions the conduct — the deterministic test for "the document itself maps this conduct
// to this count".
func sentenceJoins(docText, label string, cite *regexp.Regexp) bool {
	toks := labelTokens(label)
	for _, frag := range fragments(docText) {
		if cite.MatchString(frag) && tokensOverlap(frag, toks) {
			return true
		}
	}
	return false
}
