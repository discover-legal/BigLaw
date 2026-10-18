// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"strings"
	"testing"
)

// The five v1 pageindex vectors, against the flat pre-order list (a section "nests under" another
// when it lies inside the other's [Start, End) at a deeper level).

const numberedContract = `MASTER SERVICES AGREEMENT

1. Definitions
In this Agreement the following terms have the meanings set out below.
1.1 "Services" means the services described in Schedule A.
1.2 "Fees" means the amounts payable, comprising:
(a) the monthly retainer; and
(b) any of the following pass-through costs:
(i) travel expenses; and
(ii) third-party software fees.
2. Term
This Agreement commences on the Effective Date.
2.1 Renewal occurs automatically unless notice is given.
`

const articleContract = `SHARE PURCHASE AGREEMENT

RECITALS
WHEREAS the Seller owns the Shares;
WHEREAS the Buyer wishes to purchase the Shares;
NOW, THEREFORE the parties agree as follows:

ARTICLE I Definitions and Interpretation
Section 1 Defined Terms
"Closing" means the completion of the sale.
Section 2 Interpretation
Headings are for convenience only.

ARTICLE II Purchase and Sale
Section 1 Sale of Shares
The Seller shall sell the Shares to the Buyer.
`

const flatDocument = `This memorandum addresses the question of liability arising from the
incident on the loading dock. There is no formal clause structure here;
it is running prose intended to be read top to bottom without sections.

A second paragraph continues the analysis and reaches a conclusion.`

// checkTiling asserts the invariant: own texts join to the input, spans are well formed, and
// every subtree contains exactly the sections that follow it at a deeper level.
func checkTiling(t *testing.T, src string, secs []Section) {
	t.Helper()
	var b strings.Builder
	for i, s := range secs {
		if s.Start < 0 || s.Start > s.BodyEnd || s.BodyEnd > s.End || s.End > len(src) || i > 0 && s.Start != secs[i-1].BodyEnd {
			t.Fatalf("section %d %q: bad span %d/%d/%d (len %d)", i, s.Number, s.Start, s.BodyEnd, s.End, len(src))
		}
		b.WriteString(src[s.Start:s.BodyEnd])
	}
	if b.String() != src {
		t.Fatalf("reconstruction mismatch\n got: %q\nwant: %q", b.String(), src)
	}
}

func find(secs []Section, number string) int {
	for i, s := range secs {
		if s.Number == number {
			return i
		}
	}
	return -1
}

// under reports whether section c lies in p's subtree.
func under(secs []Section, c, p int) bool {
	return c >= 0 && p >= 0 && secs[c].Start >= secs[p].Start && secs[c].Start < secs[p].End && secs[c].Level > secs[p].Level
}

func TestReconstruction(t *testing.T) {
	for _, src := range []string{numberedContract, articleContract, flatDocument, "", "\n\n\n", "1. One\r\nbody line\r\n2. Two\r\nmore\r\n",
		"1. One\nbody\n2. Two\nlast line no newline", "ARTICLE I Scope", "   leading spaces and a 2026 year mid sentence then more text",
		"# Title\n\n## ARTICLE I — DEFINITIONS\n\n1.1 \"Day\" means a day.\n\n**Section 2 Term.** Two years.\n| a | b |\n"} {
		checkTiling(t, src, Parse(src))
	}
}

func TestNumberedNesting(t *testing.T) {
	secs := Parse(numberedContract)
	checkTiling(t, numberedContract, secs)
	s1, s12, a, b := find(secs, "1"), find(secs, "1.2"), find(secs, "(a)"), find(secs, "(b)")
	if s1 < 0 || !secs[s1].Defs || secs[s1].Scheme != "decimal" {
		t.Fatalf(`section "1" Definitions: %+v`, secs)
	}
	if !under(secs, find(secs, "1.1"), s1) || !under(secs, s12, s1) {
		t.Error("1.1 and 1.2 nest under 1")
	}
	if !under(secs, a, s12) || !under(secs, b, s12) || secs[a].Scheme != "alpha" {
		t.Error("(a) and (b) nest under 1.2")
	}
	i, ii := find(secs, "(i)"), find(secs, "(ii)")
	if !under(secs, i, b) || !under(secs, ii, b) || secs[i].Scheme != "roman" {
		t.Error("(i) and (ii) nest under (b), roman")
	}
	if got := numberedContract[secs[s1].Start:secs[s1].End]; !strings.HasPrefix(got, "1. Definitions") || !strings.HasSuffix(got, "third-party software fees.\n") {
		t.Errorf("section 1 with its subsections spans %q", got)
	}
	if got := numberedContract[secs[a].Start:secs[a].End]; got != "(a) the monthly retainer; and\n" {
		t.Errorf("(a) spans %q", got)
	}
	if under(secs, find(secs, "2"), s1) || !under(secs, find(secs, "2.1"), find(secs, "2")) {
		t.Error("2 is a sibling of 1, and 2.1 nests under 2")
	}
	if !(secs[s1].Level < secs[find(secs, "1.1")].Level && secs[s12].Level < secs[a].Level && secs[a].Level < secs[i].Level) {
		t.Error("levels are monotone with depth")
	}
	if secs[0].Level != 0 || secs[0].Number != "" || !strings.HasPrefix(numberedContract[secs[0].Start:secs[0].BodyEnd], "MASTER") {
		t.Errorf("the title line is a level-0 preface: %+v", secs[0])
	}
}

func TestArticleAndRecitalNesting(t *testing.T) {
	secs := Parse(articleContract)
	checkTiling(t, articleContract, secs)
	a1, a2 := find(secs, "ARTICLE I"), find(secs, "ARTICLE II")
	if a1 < 0 || a2 < 0 || secs[a1].Scheme != "article" || !secs[a1].Defs {
		t.Fatalf("ARTICLE I/II: %+v", secs)
	}
	var inA1 []string
	for c := range secs {
		if under(secs, c, a1) {
			inA1 = append(inA1, secs[c].Number)
			if secs[c].Scheme != "section" {
				t.Errorf("child %q of ARTICLE I: scheme %q", secs[c].Number, secs[c].Scheme)
			}
		}
	}
	if strings.Join(inA1, ",") != "Section 1,Section 2" {
		t.Errorf("ARTICLE I holds %v", inA1)
	}
	if find(secs, "WHEREAS") < 0 || find(secs, "NOW, THEREFORE") < 0 || secs[find(secs, "WHEREAS")].Scheme != "recital" {
		t.Error("WHEREAS and NOW, THEREFORE are recital sections")
	}
	if !under(secs, find(secs, "WHEREAS"), find(secs, "")+1) {
		t.Error("WHEREAS nests under RECITALS")
	}
}

func TestFlatDocument(t *testing.T) {
	secs := Parse(flatDocument)
	if len(secs) != 1 || secs[0].Number != "" || secs[0].Scheme != "" || secs[0].Level != 0 || flatDocument[secs[0].Start:secs[0].BodyEnd] != flatDocument {
		t.Fatalf("a flat document is one unnumbered section: %+v", secs)
	}
}

func TestSchemes(t *testing.T) {
	src := "Schedule A Pricing\nrate card\nExhibit 1 Form of Notice\ntemplate\n§ 5 Governing Law\nNew York law applies.\n"
	secs := Parse(src)
	checkTiling(t, src, secs)
	if find(secs, "Schedule A") < 0 || find(secs, "Exhibit 1") < 0 {
		t.Errorf("schedule labels: %+v", secs)
	}
	if i := find(secs, "Section 5"); i < 0 || secs[i].Scheme != "section" || secs[i].Title != "Governing Law" {
		t.Errorf("§ 5 parses as Section 5: %+v", secs)
	}
}

// TestMarkdownHeadings (new in v2): Markdown and bold headings are classified by their text, an
// unnumbered "#" heading nests by its depth, and figures or years at a line start are not headings.
func TestMarkdownHeadings(t *testing.T) {
	src := "# STOCK PURCHASE AGREEMENT\n\nintro\n\n## ARTICLE I — DEFINITIONS\n\n1.1 \"Day\" means a day.\n\n## Background\n\ntext\n\n**3.4 Capitalization.** 6,250,000 shares.\n" +
		"2026 was a year.\n10/21/2026 service.\n2400000,Common Stock\n"
	secs := Parse(src)
	checkTiling(t, src, secs)
	var got []string
	for _, s := range secs {
		got = append(got, s.Scheme+":"+s.Number+":"+s.Title)
	}
	want := "heading::STOCK PURCHASE AGREEMENT|article:ARTICLE I:DEFINITIONS|decimal:1.1:\"Day\" means a day|recital::Background|decimal:3.4:Capitalization. 6,250,000 shares"
	if strings.Join(got, "|") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
	if !under(secs, find(secs, "1.1"), find(secs, "ARTICLE I")) {
		t.Error("1.1 nests under the Markdown ARTICLE I")
	}
}
