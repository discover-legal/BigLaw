// SPDX-License-Identifier: Apache-2.0
package orchestrator

import (
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/ontology"
)

func TestDefenseIssuesFor(t *testing.T) {
	auth := "Section 206(1) of the Advisers Act | Section 206(2) | Section 209(e) | Rule 204-2(a)(3) | Section 207"
	got := defenseIssuesFor(auth)
	blob := strings.ToLower(strings.Join(got, "\n"))
	for _, want := range []string{"scienter", "negligence", "18 u.s.c. § 1519", "statute of limitations", "204-2"} {
		if !strings.Contains(blob, strings.ToLower(want)) {
			t.Errorf("expected a defense issue mentioning %q; got:\n%s", want, strings.Join(got, "\n---\n"))
		}
	}
	// The explicit 206(1)-vs-206(2) scienter DISTINCTION (a named rubric point) must fire when both charged.
	if !strings.Contains(blob, "both section 206(1)") {
		t.Error("expected the explicit 206(1)-vs-206(2) scienter distinction")
	}
	if defenseIssuesFor("") != nil {
		t.Error("empty authorities should yield no issues")
	}
	// 206(2) alone must NOT emit the both-charged distinction.
	if strings.Contains(strings.ToLower(strings.Join(defenseIssuesFor("Section 206(2)"), " ")), "both section 206(1)") {
		t.Error("the distinction should require BOTH provisions")
	}
}

// ─── Trigger regression (July-3 Haiku run) ─────────────────────────────────────
//
// On the July-3 Haiku release run the spine pass silently produced ZERO typed triples
// (its model resolved to a dead local endpoint), so the graph carried no subsection-level
// `violates` claims and the analytic layer collapsed to the lone § 2462 note. The fix is
// the deterministic charging-document floor: with NO graph claims at all, the templates
// must still fire from the referral's verbatim text.
func TestDefenseIssues_FireFromChargingDocWithoutGraph(t *testing.T) {
	doc := `NOTICE OF ENFORCEMENT REFERRAL
This referral notice is dated June 5, 2026.
The Division of Examinations alleges that Whitmore Capital Advisors violated Section 206(1) and Section 206(2) of the Advisers Act through cherry-picking trade allocations.
The Division further alleges obstruction of the examination: Whitmore instructed Delgado to "clean up the shared drive" and directed the deletion of trade allocation spreadsheets during the pending examination.`
	ctx := defenseContext{DocText: doc, Allegations: []string{"Cherry-Picking Trade Allocations", "Obstruction of Examination"}}
	ctx.Auth = authorityText(ctx)
	if !strings.Contains(ctx.Auth, "206(1)") || !strings.Contains(ctx.Auth, "206(2)") {
		t.Fatalf("charged authorities not harvested from the charging doc: %q", ctx.Auth)
	}
	blob := strings.ToLower(strings.Join(renderDerivedIssues(analyseDefense(ctx)), "\n"))
	for _, want := range []string{"scienter", "1519", "1505", "2462"} {
		if !strings.Contains(blob, want) {
			t.Errorf("with no graph claims, expected the doc-derived issues to mention %q; got:\n%s", want, blob)
		}
	}
}

// A pure compliance document (no accusatory sentences) must NOT fire enforcement defense
// issues even though it cites rules — the charged-context gate.
func TestDefenseIssues_ComplianceDocDoesNotFire(t *testing.T) {
	doc := `COMPLIANCE MANUAL EXCERPT
Access Persons shall submit initial holdings reports within ten (10) calendar days under Rule 204A-1.
The firm maintains required books and records in accordance with its retention schedule.`
	ctx := defenseContext{DocText: doc}
	ctx.Auth = authorityText(ctx)
	if got := analyseDefense(ctx); got != nil {
		t.Errorf("a compliance manual with no accusatory sentences should derive nothing; got %d issues", len(got))
	}
}

// ─── Template A — criminal-parallel exposure (C-034) ──────────────────────────

func TestCriminalParallel_FromObstructionConduct(t *testing.T) {
	src := `Obstruction of Examination committed by Whitmore, in violation of Section 209(e): Whitmore instructed the deletion of trade allocation spreadsheets during the pending examination.`
	g := evidencegraph.New()
	if !g.AddTriple("Obstruction of Examination", "Conduct", "violates", "Section 209(e)", "Authority", "",
		"in violation of Section 209(e)", "referral", src) {
		t.Fatal("fixture triple rejected")
	}
	if !g.AddTriple("Whitmore", "Party", "alteredRecords", "trade allocation spreadsheets", "Instrument", "",
		"Whitmore instructed the deletion of trade allocation spreadsheets during the pending examination", "referral", src) {
		t.Fatal("fixture alteredRecords triple rejected")
	}
	ctx := defenseContext{Claims: g.Claims()}
	ctx.Auth = authorityText(ctx)
	issues := analyseDefense(ctx)
	var crim *ontology.DerivedIssue
	for i := range issues {
		if issues[i].Kind == ontology.CriminalExposureKind {
			crim = &issues[i]
			break
		}
	}
	if crim == nil {
		t.Fatalf("obstruction conduct did not derive a criminal-exposure issue; got:\n%s", strings.Join(renderDerivedIssues(issues), "\n---\n"))
	}
	low := strings.ToLower(crim.Text)
	for _, want := range []string{"18 u.s.c. § 1519", "18 u.s.c. § 1505", "doj", "fifth amendment"} {
		if !strings.Contains(low, want) {
			t.Errorf("criminal-exposure issue missing %q:\n%s", want, crim.Text)
		}
	}
}

// ─── Template B — scienter mapping (C-037) ─────────────────────────────────────

func TestScienterMapping_FlagsUnspecifiedConduct(t *testing.T) {
	doc := `The Division alleges violations of Section 206(1) and Section 206(2) of the Advisers Act.
The cherry-picking trade allocations were undertaken intentionally, in violation of Section 206(1).
The Division also alleges misleading Form ADV disclosures.`
	ctx := defenseContext{
		DocText:     doc,
		Allegations: []string{"Cherry-Picking Trade Allocations", "Misleading Form ADV Disclosures"},
	}
	ctx.Auth = authorityText(ctx)
	var mapping string
	for _, is := range analyseDefense(ctx) {
		if is.Kind == ontology.MentalStateKind && strings.Contains(is.Text, "Scienter mapping") {
			mapping = is.Text
			break
		}
	}
	if mapping == "" {
		t.Fatal("both 206(1) and 206(2) charged with conducts present — expected a scienter-mapping issue")
	}
	if !strings.Contains(mapping, "Cherry-Picking Trade Allocations: the charging document ties this conduct to Section 206(1)") {
		t.Errorf("explicitly mapped conduct not tied to 206(1):\n%s", mapping)
	}
	if !strings.Contains(mapping, "Misleading Form ADV Disclosures: the charging document does NOT specify") {
		t.Errorf("unmapped conduct not flagged as unspecified:\n%s", mapping)
	}
	if !strings.Contains(strings.ToLower(mapping), "defense point") {
		t.Errorf("ambiguous mapping must be surfaced as a defense point:\n%s", mapping)
	}
}

// ─── Template C — limitations-to-conduct join (C-040) ──────────────────────────

func TestLimitationsJoin_PerConductTimeBar(t *testing.T) {
	src := `The cherry-picking trade allocations occurred during Q1 2021.`
	g := evidencegraph.New()
	if !g.AddTriple("Cherry-Picking Trade Allocations", "Conduct", "occurredDuring", "Q1 2021", "Event", "",
		"occurred during Q1 2021", "referral", src) {
		t.Fatal("fixture occurredDuring triple rejected")
	}
	doc := `The Division alleges the conduct violated Section 206(1) of the Advisers Act.
This referral notice is dated June 5, 2026.`
	ctx := defenseContext{Claims: g.Claims(), DocText: doc}
	ctx.Auth = authorityText(ctx)
	var join string
	for _, is := range analyseDefense(ctx) {
		if is.Kind == ontology.LimitationsKind && is.About == "Cherry-Picking Trade Allocations" {
			join = is.Text
			break
		}
	}
	if join == "" {
		t.Fatal("dated conduct + § 2462 context — expected a per-conduct limitations join")
	}
	low := strings.ToLower(join)
	for _, want := range []string{"q1 2021", "2462", "march 2026", "outside", "time-barred", "disgorgement", "78u(d)(8)"} {
		if !strings.Contains(low, want) {
			t.Errorf("limitations join missing %q:\n%s", want, join)
		}
	}
	// Conduct inside the window must not be called time-barred: Q1 2023 closes March 2028,
	// after the June 2026 anchor.
	src2 := `The improper transfers occurred during Q1 2023.`
	g2 := evidencegraph.New()
	if !g2.AddTriple("Improper Transfers", "Conduct", "occurredDuring", "Q1 2023", "Event", "",
		"occurred during Q1 2023", "referral", src2) {
		t.Fatal("fixture triple rejected")
	}
	ctx2 := defenseContext{Claims: g2.Claims(), DocText: doc}
	ctx2.Auth = authorityText(ctx2)
	for _, is := range analyseDefense(ctx2) {
		if is.Kind == ontology.LimitationsKind && is.About == "Improper Transfers" {
			if strings.Contains(strings.ToLower(is.Text), "outside") {
				t.Errorf("in-window conduct wrongly marked outside the window:\n%s", is.Text)
			}
			if !strings.Contains(strings.ToLower(is.Text), "within the window") {
				t.Errorf("in-window conduct should be stated as within the window:\n%s", is.Text)
			}
		}
	}
}

// Partner-review regression: the § 2462 window must join ONLY to conduct dates. An exam
// start date, a referral date, or a compliance-review year-end must never yield a
// time-bar paragraph, and one conduct label is capped at maxJoinsPerConduct joins.
func TestLimitationsJoin_ConductDatesOnly(t *testing.T) {
	doc := `The Division alleges the trade allocation scheme violated Section 206(1) of the Advisers Act.
The Division of Examinations commenced its examination of the trade allocation scheme on March 11, 2024.
Following the formal referral to Enforcement on October 18, 2024, the trade allocation scheme was described in the notice.
The trade allocation scheme operated from June 2022 through November 2023, when Bellini directed the block trades.
This referral notice is dated June 5, 2026.`
	ctx := defenseContext{DocText: doc, Allegations: []string{"Trade Allocation Scheme"}}
	ctx.Auth = authorityText(ctx)
	var joins []ontology.DerivedIssue
	for _, is := range analyseDefense(ctx) {
		if is.Kind == ontology.LimitationsKind && is.About != "" {
			joins = append(joins, is)
		}
	}
	if len(joins) == 0 {
		t.Fatal("genuine conduct dates present — expected at least one limitations join")
	}
	if len(joins) > maxJoinsPerConduct {
		t.Errorf("per-conduct cap breached: %d joins for one label", len(joins))
	}
	blob := strings.ToLower(joins[0].Text)
	for _, is := range joins {
		lt := strings.ToLower(is.Text)
		if strings.Contains(lt, "march 11, 2024") || strings.Contains(lt, "october 18, 2024") {
			t.Errorf("procedural date joined to the limitations window:\n%s", is.Text)
		}
	}
	if !strings.Contains(blob, "june 2022") && !strings.Contains(blob, "november 2023") {
		t.Errorf("conduct dates not joined:\n%s", blob)
	}
	// Graph path: only the conduct-dating predicate (occurredDuring) supplies dates — a
	// date buried in an unrelated claim's quote must not join.
	g := evidencegraph.New()
	src := `The referral describing the trade allocation scheme was issued on October 18, 2024.`
	if !g.AddTriple("Trade Allocation Scheme", "Conduct", "violates", "Section 206(1)", "Authority", "",
		"The referral describing the trade allocation scheme was issued on October 18, 2024", "referral", src) {
		t.Fatal("fixture triple rejected")
	}
	ctx2 := defenseContext{Claims: g.Claims(), DocText: "The Division alleges violations of Section 206(1)."}
	ctx2.Auth = authorityText(ctx2)
	for _, is := range analyseDefense(ctx2) {
		if is.Kind == ontology.LimitationsKind && is.About != "" {
			t.Errorf("non-conduct-dated claim produced a limitations join:\n%s", is.Text)
		}
	}
}

// ─── Template D — steelman the innocent reading (C-060) ────────────────────────

func TestSteelman_QuotedInstruction(t *testing.T) {
	doc := `The Division alleges obstruction in violation of Section 209(e).
On March 3, 2024, Whitmore instructed Delgado to "clean up the shared drive".`
	ctx := defenseContext{DocText: doc}
	ctx.Auth = authorityText(ctx)
	var steel *ontology.DerivedIssue
	for _, is := range analyseDefense(ctx) {
		if is.Kind == ontology.InnocentReadingKind {
			steel = &is
			break
		}
	}
	if steel == nil {
		t.Fatal("quoted instruction with a directive verb — expected a steelman issue")
	}
	if steel.Quote != "clean up the shared drive" {
		t.Errorf("steelman issue not grounded on the verbatim quote: %q", steel.Quote)
	}
	low := strings.ToLower(steel.Text)
	for _, want := range []string{`"clean up the shared drive"`, "innocent", "discovery", "retention", "backups"} {
		if !strings.Contains(low, want) {
			t.Errorf("steelman issue missing %q:\n%s", want, steel.Text)
		}
	}
	// No fabricated steelman without a quoted communication.
	ctx2 := defenseContext{DocText: "The Division alleges violations of Section 206(2)."}
	ctx2.Auth = authorityText(ctx2)
	for _, is := range analyseDefense(ctx2) {
		if is.Kind == ontology.InnocentReadingKind {
			t.Errorf("steelman fired without any quoted communication:\n%s", is.Text)
		}
	}
}

// Partner-review regression: a quoted PROPER NOUN in a directive sentence is not an
// instruction — no steelman for "Lakeshore Trading" or "Delgado". Genuine imperative
// spans still fire, and near-duplicate spans sharing the lead verb collapse to one.
func TestSteelman_ProperNounNotDirective(t *testing.T) {
	doc := `The Division alleges violations of Section 206(1).
Bellini directed twenty-three block trades to "Lakeshore Trading" during the period.
Whitmore instructed "Delgado" regarding the servers.
Whitmore instructed Delgado to "clean up the shared drive" that week.
The email references the instruction to "clean up of legacy files on the shared drive" as well.`
	ctx := defenseContext{DocText: doc}
	ctx.Auth = authorityText(ctx)
	var spans []string
	for _, is := range analyseDefense(ctx) {
		if is.Kind == ontology.InnocentReadingKind {
			spans = append(spans, is.Quote)
		}
	}
	for _, s := range spans {
		if strings.EqualFold(s, "Lakeshore Trading") || strings.EqualFold(s, "Delgado") {
			t.Errorf("steelman fired on a quoted proper noun: %q", s)
		}
	}
	if len(spans) != 1 {
		t.Errorf("want exactly 1 steelman (imperative spans share the lead verb), got %d: %v", len(spans), spans)
	}
	if len(spans) == 1 && !strings.Contains(spans[0], "clean up") {
		t.Errorf("the genuine directive was not steelmanned: %v", spans)
	}
}

// ─── Plumbing ──────────────────────────────────────────────────────────────────

func TestFragments_KeepCitationsWhole(t *testing.T) {
	frags := fragments("Penalties are governed by 28 U.S.C. § 2462. Any conduct outside the window is barred.")
	if len(frags) != 2 {
		t.Fatalf("want 2 fragments, got %d: %q", len(frags), frags)
	}
	if !strings.Contains(frags[0], "28 U.S.C. § 2462") {
		t.Errorf("citation split across fragments: %q", frags[0])
	}
}

func TestParseRecDates(t *testing.T) {
	toks := parseRecDates("deletions in Q1 2021, amended in March 2022, and again in early 2023")
	if len(toks) != 3 {
		t.Fatalf("want 3 dates, got %d: %+v", len(toks), toks)
	}
	byRaw := map[string]recDate{}
	for _, tk := range toks {
		byRaw[tk.raw] = tk.d
	}
	if d := byRaw["Q1 2021"]; d.Year != 2021 || d.Month != 3 {
		t.Errorf("Q1 2021 parsed as %+v (want quarter-end March 2021)", d)
	}
	if d := byRaw["March 2022"]; d.Year != 2022 || d.Month != 3 {
		t.Errorf("March 2022 parsed as %+v", d)
	}
	if d := byRaw["2023"]; d.Year != 2023 || d.Month != 0 {
		t.Errorf("bare year parsed as %+v", d)
	}
	// Statute numbers must not parse as years.
	if got := parseRecDates("18 U.S.C. § 1519 and 28 U.S.C. § 2462"); len(got) != 0 {
		t.Errorf("statute numbers parsed as dates: %+v", got)
	}
}
