// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// Carried authorship vectors (v1 writer/authorship_test.go), against the renderer's polish.

func TestNoProcessLeaks_FallbackPath(t *testing.T) {
	in := "Evidence on point for this matter; see the quoted source. The Division alleges excess profits of $7,800,000 to Oceanic Fund I LP [F1].\n" +
		"These must be extracted from the full referral notice and exhibits. Bellini received undisclosed compensation of $438,000 [F2].\n" +
		"The extent of coverage should be verified to determine if detail gap exists."
	out := polishSection("Summary", in)
	for _, leak := range []string{"Evidence on point for this matter", "see the quoted source", "must be extracted from the full", "detail gap", "should be verified to determine"} {
		if strings.Contains(out, leak) {
			t.Errorf("process language leaked: %q\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "$7,800,000") || !strings.Contains(out, "$438,000") {
		t.Errorf("substance dropped:\n%s", out)
	}
}

func TestStripProcessSentences_KeepsSubstance(t *testing.T) {
	out := stripProcessSentences("The $822,000 differential suggests additional alleged violations. These must be extracted from the full referral notice and exhibits.")
	if strings.Contains(out, "must be extracted") || !strings.Contains(out, "$822,000 differential") {
		t.Fatalf("%q", out)
	}
}

func TestPolishSection_TrimsTruncatedTail(t *testing.T) {
	out := polishSection("Form ADV Disclosures", "The allegation is supported by the record. Requires detailed comparison of the Form ADV against actual practices documented in trading logs and")
	if strings.HasSuffix(strings.TrimSpace(out), "and") || !strings.Contains(out, "supported by the record.") {
		t.Fatalf("%q", out)
	}
	if got := polishSection("X", "documented in trading logs and"); strings.TrimSpace(got) != "" {
		t.Fatalf("a pure fragment survived: %q", got)
	}
}

func TestPolishSection_DropsOrphansAndDuplicateTitle(t *testing.T) {
	in := strings.Join([]string{"Failure to Maintain Required Books and Records", "Chief Compliance Officer", "Code of Ethics",
		"The Division alleges that WCA failed to maintain required books and records under Rule 204-2."}, "\n")
	out := polishSection("Failure to Maintain Required Books and Records", in)
	if strings.HasPrefix(out, "Failure to Maintain") || strings.Contains(out, "Chief Compliance Officer") || strings.Contains(out, "Code of Ethics") || !strings.Contains(out, "Rule 204-2.") {
		t.Fatalf("%q", out)
	}
}

func TestDedupeDocBlocks(t *testing.T) {
	para := "The total alleged ill-gotten gains across all violation categories are $8,622,000, comprising cherry-picking profits, undisclosed compensation, and excess commissions attributable to the scheme."
	bullet := "**Key figures:**\n- Total alleged ill-gotten gains / client harm: $8,622,000 (referral.docx)"
	out := dedupeDocBlocks(strings.Join([]string{"## Cherry-Picking", para, bullet, "## Obstruction", para, bullet}, "\n\n"))
	if strings.Count(out, "$8,622,000, comprising") != 1 || strings.Count(out, "- Total alleged ill-gotten gains") != 1 || !strings.Contains(out, "## Obstruction") {
		t.Fatalf("%s", out)
	}
}

// The renderer substitutes a drafter's handles outside quoted text only, from the handle set that
// drafter was given (FOLLOWUPS PR 4a L10: codenames shift as the ledger grows).
func TestHandlesSubstitutedOutsideQuotesOnly(t *testing.T) {
	figs := []Fig{{"{Zephyr}", "F1", "$644,910"}}
	out := substituteOutsideQuotes(`Greystone claims {Zephyr}; the record says “the {Zephyr} code is reserved” and "{Zephyr} stays".`, figs)
	if out != `Greystone claims $644,910; the record says “the {Zephyr} code is reserved” and "{Zephyr} stays".` {
		t.Fatalf("%s", out)
	}
}

func TestRendererUsesDrafterHandleSet(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "2. Damages\nGreystone claims damages of $64,491 for unpaid invoices.\n", "1. Bond\nTalbot posted a bond of $9,000,000 with the court.\n")
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	// F1 ($64,491) gets {Zephyr} when I1 is drafted; a later, more salient figure ($9,000,000) would
	// take {Zephyr} in a recomputed set, but a codename keeps its figure for the run (it may sit in
	// I1's summary, which I2's drafter reads): F2 gets the next free one, {Quasar}.
	e, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Damages?", "Conduct"), issue("I2", "Bond?", "RedFlag"))), calls(extract(d1, "damages", "I1")), calls(draft("I1", "F1")),
			calls(extract(d2, "bond", "I2")), calls(draft("I2", "F2")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("Greystone claims damages of $64,491.", d1, "Greystone claims damages of $64,491 for unpaid invoices", val("Greystone", "damages", "$64,491", "Money"))),
			facts(fd("Talbot posted a $9,000,000 bond.", d2, "Talbot posted a bond of $9,000,000", val("Talbot", "bond", "$9,000,000", "Money")))},
		"draft_section": {section("Greystone claims {Zephyr} [F1].", "Damages {Zephyr} [F1]."), section("Talbot posted a bond of {Quasar} [F2], against the {Zephyr} claimed [F1].", "s2")},
	}), Config{})
	task := f.submit(0)
	f.step(context.Background())
	v, _ := e.Get(context.Background(), task.ID)
	var second string
	for _, c := range fake.Calls() {
		if c.Agent == "draft_section" {
			second = fmt.Sprint(c.Messages[1].Content)
		}
	}
	if !strings.Contains(second, `{"fid":"F2","handle":"{Quasar}"`) || !strings.Contains(second, "Damages {Zephyr} [F1].") {
		t.Fatalf("the second drafter's request: %s", second)
	}
	if !strings.Contains(deref(v.Output), "Greystone claims $64,491 [F1].") || !strings.Contains(deref(v.Output), "bond of $9,000,000 [F2], against the $64,491 claimed [F1].") {
		t.Fatalf("%s\n%s\n%q", deref(v.Output), f.steps(v.ID), f.outputs(v.ID, "finish"))
	}
}

// TestCurrencyFromQuote (4a r2 L2): a money value's currency is the symbol its quote puts beside
// it, not another figure's; none shown is "" (USD).
func TestCurrencyFromQuote(t *testing.T) {
	t.Parallel()
	q := "A fee of €1,200 and $5 per page, or 40 CHF, plus £30."
	for _, c := range [][2]string{{"1,200", "EUR"}, {"5", ""}, {"40", "CHF"}, {"£30", "GBP"}, {"7", ""}} {
		if got := currencyOf(c[0], q); got != c[1] {
			t.Errorf("currencyOf(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}
