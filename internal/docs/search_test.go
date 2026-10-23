// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

func newService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(t.Context(), storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st)
}

func ingestText(t *testing.T, s *Service, name, text string) Document {
	t.Helper()
	d, err := s.IngestText(t.Context(), name, text)
	if err != nil {
		t.Fatalf("ingest %s: %v", name, err)
	}
	return d
}

func hitIDs(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.DocumentID+":"+h.Text)
	}
	return out
}

// TestRetrieveNeverLeavesMatter (G1): a document outside the scope that holds the query term is
// never returned by search, outline, section, verification, get or file.
func TestRetrieveNeverLeavesMatter(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := t.Context()
	in := ingestText(t, s, "ours.txt", "1. Scope\nThe supplier shall deliver widgets monthly.")
	out := ingestText(t, s, "theirs.txt", "1. Secret\nThe zanzibar settlement is confidential to another client.")
	scope := []string{in.ID}
	if hits, err := s.Search(ctx, scope, "zanzibar settlement confidential", 8, 0); err != nil || len(hits) != 0 {
		t.Errorf("search leaked: %v %v", hitIDs(hits), err)
	}
	if hits, _ := s.Search(ctx, []string{out.ID}, "zanzibar", 8, 0); len(hits) != 1 {
		t.Fatalf("the term is searchable in its own matter: %v", hitIDs(hits)) // the trap is live
	}
	if hits, _ := s.Search(ctx, []string{strings.ToUpper(out.ID)}, "zanzibar", 8, 0); len(hits) != 1 {
		t.Errorf("an upper-case id names the same document: %v", hitIDs(hits))
	}
	if _, err := s.Outline(ctx, []string{strings.ToUpper(out.ID)}, out.ID); err != nil {
		t.Errorf("an upper-case id names the same document for every read: %v", err)
	}
	nearMiss := out.ID[:35] + map[bool]string{true: "1", false: "0"}[out.ID[35] == '0'] // differs in the last hex digit, always (r1 M-3)
	for _, sc := range [][]string{scope, nil, {}, {"not-a-uuid"}, {nearMiss}, {"' OR true --"}} {
		if hits, err := s.Search(ctx, sc, "zanzibar", 8, 0); err != nil || len(hits) != 0 {
			t.Errorf("scope %v: search leaked %v %v", sc, hitIDs(hits), err)
		}
		if _, err := s.Outline(ctx, sc, out.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("scope %v: outline of an out-of-scope document: %v", sc, err)
		}
		if _, err := s.Section(ctx, sc, out.ID, "s0", 0, 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("scope %v: section of an out-of-scope document: %v", sc, err)
		}
		if _, err := s.VerifyQuote(ctx, sc, out.ID, "zanzibar settlement", nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("scope %v: verify against an out-of-scope document: %v", sc, err)
		}
		if _, err := s.Get(ctx, sc, out.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("scope %v: get: %v", sc, err)
		}
		if _, err := s.Resolve(ctx, sc, "theirs.txt"); !errors.Is(err, ErrNotFound) {
			t.Errorf("scope %v: resolve by name: %v", sc, err)
		}
	}
}

// TestSearchRareTermOutranksCommon (v1 bm25 vector re-pointed at tsvector): "law" is in every
// document, "antitrust" in one; the rare-term document ranks first.
func TestSearchRareTermOutranksCommon(t *testing.T) {
	t.Parallel()
	s := newService(t)
	var scope []string
	ids := map[string]string{}
	for _, d := range [][2]string{{"common", "law law law general principles overview"}, {"rare", "antitrust law specialized analysis"},
		{"filler1", "law contract drafting practice"}, {"filler2", "law procedure courtroom etiquette"}} {
		doc := ingestText(t, s, d[0]+".txt", d[1])
		scope, ids[doc.ID] = append(scope, doc.ID), d[0]
	}
	hits, err := s.Search(t.Context(), scope, "law antitrust", 10, 0)
	if err != nil || len(hits) != 4 {
		t.Fatalf("hits %v, %v", hitIDs(hits), err)
	}
	if ids[hits[0].DocumentID] != "rare" {
		t.Errorf("first hit %q, want the rare-term document: %v", ids[hits[0].DocumentID], hitIDs(hits))
	}
	if r := slices.IndexFunc(hits, func(h Hit) bool { return ids[h.DocumentID] == "common" }); r != 1 {
		t.Errorf("the repeated common term should rank second, at %d", r)
	}
}

// TestSearchRarityIsScoped (r1 prosecutor L-2): term rarity is counted over the scope only, so
// another matter's documents cannot reorder this one's results (a cross-matter side channel):
// "zeta" fills 20 out-of-scope documents, and inside the scope it ties with "omega", at exactly
// the rank it had before they existed (neither df nor N counts them).
func TestSearchRarityIsScoped(t *testing.T) {
	t.Parallel()
	s := newService(t)
	x := ingestText(t, s, "x.txt", "zeta report")
	y := ingestText(t, s, "y.txt", "omega report")
	alone, err := s.Search(t.Context(), []string{x.ID, y.ID}, "zeta omega", 8, 0)
	if err != nil || len(alone) != 2 {
		t.Fatal(alone, err)
	}
	for i := range 20 {
		ingestText(t, s, fmt.Sprintf("other%d.txt", i), "zeta zeta zeta")
	}
	hits, err := s.Search(t.Context(), []string{x.ID, y.ID}, "zeta omega", 8, 0)
	if err != nil || len(hits) != 2 || hits[0].DocumentID != x.ID || hits[0].Rank != hits[1].Rank || hits[0].Rank != alone[0].Rank {
		t.Errorf("hits %v %v, want a tie broken by chunk id (x first) at the rank they had before the other documents existed", hitIDs(hits), err)
	}
}

// TestSearchTopKDeterministic (v1 bm25 vector): equal ranks are ordered by chunk id, k caps the
// list, offset pages it, and repeated calls agree.
func TestSearchTopKDeterministic(t *testing.T) {
	t.Parallel()
	s := newService(t)
	var scope []string
	for _, n := range []string{"c", "a", "b"} {
		scope = append(scope, ingestText(t, s, n+".txt", "indemnity indemnity").ID)
	}
	hits, err := s.Search(t.Context(), scope, "indemnity", 2, 0)
	if err != nil || len(hits) != 2 || hits[0].DocumentID != scope[0] || hits[1].DocumentID != scope[1] {
		t.Fatalf("k=2: %v %v, want ingest order (chunk id) on a tie", hitIDs(hits), err)
	}
	page2, _ := s.Search(t.Context(), scope, "indemnity", 2, 2)
	if len(page2) != 1 || page2[0].DocumentID != scope[2] {
		t.Errorf("offset 2: %v", hitIDs(page2))
	}
	for range 20 {
		again, _ := s.Search(t.Context(), scope, "indemnity", 3, 0)
		if !slices.Equal(hitIDs(again), hitIDs(append(hits, page2...))) {
			t.Fatalf("non-deterministic: %v", hitIDs(again))
		}
	}
	for _, bad := range []struct {
		q      string
		k, off int
	}{{"x", 0, 0}, {"x", MaxK + 1, 0}, {"x", 1, -1}, {strings.Repeat("x", MaxQuery+1), 1, 0}, {"a\x00b", 1, 0}, {"\xff", 1, 0}} {
		if _, err := s.Search(t.Context(), scope, bad.q, bad.k, bad.off); !errors.Is(err, ErrBadInput) {
			t.Errorf("search(%.10q, %d, %d): %v, want ErrBadInput", bad.q, bad.k, bad.off, err)
		}
	}
	if hits, err := s.Search(t.Context(), scope, "the and of", 8, 0); err != nil || len(hits) != 0 {
		t.Errorf("a stop-word query: %v %v, want no hits", hitIDs(hits), err)
	}
	if hits, err := s.Search(t.Context(), scope, `"indemnity' & | ! ( :* <->`, 8, 0); err != nil || len(hits) != 3 {
		t.Errorf("tsquery operators in a query are plain text: %v %v", hitIDs(hits), err)
	}
}

// TestSearchExactIdentifier: identifiers are found ahead of documents that only share their
// parts: s.9.4, C-13 and FS-26-01234 (PLAN), and identifiers whose letter is an English stop word
// (Schedule A, Annex I), which only the simple column keeps.
func TestSearchExactIdentifier(t *testing.T) {
	t.Parallel()
	s := newService(t)
	texts := map[string]string{
		"s.9.4":       "Under s.9.4 the warranty runs for ninety days.",
		"C-13":        "Change C-13 deletes the cap on liability.",
		"FS-26-01234": "Court File No. FS-26-01234, Ontario Superior Court of Justice.",
		"Schedule A":  "Schedule A lists the parties.",
		"Annex I":     "Annex I sets out the services.",
	}
	noise := []string{"Section 9 and section 4 and 9.5 cover the warranty.", "Change C-12 and change 13 are cosmetic.",
		"File FS-26-01235 and 01234 rows in schedule 26.", "Schedule B lists the fees.", "Schedule C lists the sites.", "Annex II sets out the prices."}
	var scope []string
	want := map[string]string{}
	for id, text := range texts {
		d := ingestText(t, s, strings.ReplaceAll(id, " ", "-")+".txt", text)
		scope, want[id] = append(scope, d.ID), d.ID
	}
	for i, text := range noise {
		scope = append(scope, ingestText(t, s, fmt.Sprintf("noise%d.txt", i), text).ID)
	}
	for id := range texts {
		hits, err := s.Search(t.Context(), scope, id, 8, 0)
		if err != nil || len(hits) == 0 || hits[0].DocumentID != want[id] || len(hits) > 1 && hits[0].Rank <= hits[1].Rank {
			t.Errorf("search %q: %v %v", id, hitIDs(hits), err)
		}
	}
}

// TestOutlineHasNumbersTitlesTokens: the outline lists every section in order with its id, number,
// title, level and the tokens Section returns for it.
func TestOutlineHasNumbersTitlesTokens(t *testing.T) {
	t.Parallel()
	s := newService(t)
	text := "SERVICES AGREEMENT\n\n1. Definitions\n\"Fees\" means the fees.\n1.1 Services\nThe services.\n2. Term\nTwo years.\n"
	d := ingestText(t, s, "msa.txt", text)
	out, err := s.Outline(t.Context(), []string{d.ID}, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []OutlineEntry{{"s0", "", "", 0, 0}, {"s1", "1", "Definitions", 2, 0}, {"s2", "1.1", "Services", 3, 0}, {"s3", "2", "Term", 2, 0}}
	if len(out) != len(want) {
		t.Fatalf("outline %+v", out)
	}
	secs := Parse(text)
	for i, e := range out {
		w := want[i]
		w.Tokens = tokens(text[secs[i].Start:secs[i].End])
		if e != w || e.Tokens == 0 {
			t.Errorf("entry %d = %+v, want %+v", i, e, w)
		}
		p, err := s.Section(t.Context(), []string{d.ID}, d.ID, e.SectionID, 0, 0)
		if err != nil || tokens(p.Text) != e.Tokens || p.Truncated || p.Number != e.Number || p.Title != e.Title {
			t.Errorf("section %s: %+v %v; outline tokens %d", e.SectionID, p, err, e.Tokens)
		}
	}
	if out[1].Tokens <= out[2].Tokens {
		t.Errorf("section 1 includes 1.1, so it has more tokens: %+v", out)
	}
	for _, id := range []string{"s4", "s-1", "s01", "x1", "", "s", "s2147483648", "s99999999999"} {
		if _, err := s.Section(t.Context(), []string{d.ID}, d.ID, id, 0, 0); !errors.Is(err, ErrNotFound) {
			t.Errorf("section %q: %v", id, err)
		}
	}
}

// TestSectionPaginates (trap): a section longer than limit comes back truncated with next; the
// pages join to the section verbatim, nothing lost or repeated; a page never ends mid-word when
// white space is near.
func TestSectionPaginates(t *testing.T) {
	t.Parallel()
	s := newService(t)
	var b strings.Builder
	b.WriteString("1. Long clause\n")
	for i := range 400 {
		fmt.Fprintf(&b, "Sentence %d of the long clause names the party Ångström-%d. ", i, i)
	}
	text := b.String()
	d := ingestText(t, s, "long.txt", text)
	var got strings.Builder
	offset, pages := 0, 0
	for {
		p, err := s.Section(t.Context(), []string{d.ID}, d.ID, "s0", offset, 300)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if tokens(p.Text) > 300 || p.Text == "" {
			t.Fatalf("page %d: %d tokens", pages, tokens(p.Text))
		}
		got.WriteString(p.Text)
		if !p.Truncated {
			if p.Next != 0 {
				t.Errorf("last page has next %d", p.Next)
			}
			break
		}
		if p.Next <= offset || !strings.HasSuffix(p.Text, " ") {
			t.Fatalf("page %d: next %d after offset %d, text ends %q", pages, p.Next, offset, p.Text[len(p.Text)-10:])
		}
		offset = p.Next
	}
	if got.String() != text || pages < 10 {
		t.Fatalf("%d pages did not join to the section (%d of %d bytes)", pages, got.Len(), len(text))
	}
	one, _ := s.Section(t.Context(), []string{d.ID}, d.ID, "s0", 0, 0)
	if !one.Truncated || tokens(one.Text) > DefaultSectionTokens {
		t.Errorf("the default page is not truncated at %d tokens: %d", DefaultSectionTokens, tokens(one.Text))
	}
	for _, bad := range [][2]int{{-1, 10}, {0, -1}, {0, MaxSectionTokens + 1}, {len([]rune(text)) + 1, 10}, {1 << 31, 10}} {
		if _, err := s.Section(t.Context(), []string{d.ID}, d.ID, "s0", bad[0], bad[1]); !errors.Is(err, ErrBadInput) {
			t.Errorf("section offset %d limit %d: %v", bad[0], bad[1], err)
		}
	}
}

// TestLexicalRecallProofRuns (gate E4, lexical leg): e4Measure with no embedder. The gate query is
// the fact's answer-free question (answer_key.json "question", written without its figures or
// conclusion: r1 auditor A3); the title, which states the answer, is an upper bound. Lexical
// search does not meet the gate (60/68 = 0.882): this test fails only on a regression below that;
// TestHybridRecallProofRuns is the gate.
func TestLexicalRecallProofRuns(t *testing.T) {
	t.Parallel()
	r := e4Measure(t, newService(t))
	if float64(r.question.found) < 0.95*float64(r.question.facts) {
		t.Logf("E4 not met by lexical search alone: %d/%d < 0.95 (the gate is TestHybridRecallProofRuns)", r.question.found, r.question.facts)
	}
	if r.question.found < 60 || float64(r.title.found) < 0.95*float64(r.title.facts) {
		t.Errorf("E4 regressed: %d/%d by question (floor 60), %d/%d by title (floor 0.95)", r.question.found, r.question.facts, r.title.found, r.title.facts)
	}
}

// TestSearchCost (r2 reproducer N2): a search costs scope × distinct terms, so terms are capped at
// MaxTerms and the statement at searchTimeout. On a 4 MiB scope of short clauses this prints the
// times README § Documents quotes; 33 distinct words and a statement past the timeout are refused.
func TestSearchCost(t *testing.T) {
	if testing.Short() {
		t.Skip("ingests 4 MiB")
	}
	s := newService(t)
	var b strings.Builder
	for i := 0; b.Len() < 4<<20; i++ {
		fmt.Fprintf(&b, "(%c) Clause %d: the Supplier shall pay the fee of %d euros within 30 days of the invoice.\n", 'a'+i%26, i, i*7)
	}
	d := ingestText(t, s, "clauses.txt", b.String())
	scope := []string{d.ID}
	words := func(n int, f string) string {
		var w []string
		for i := range n {
			w = append(w, fmt.Sprintf(f, i))
		}
		return strings.Join(w, " ")
	}
	for name, q := range map[string]string{"2 common words": "supplier invoice", "32 absent words": words(MaxTerms, "absentword%d"),
		"32 words, 6 common": "supplier shall pay fee euros invoice " + words(MaxTerms-6, "absentword%d")} {
		start := time.Now()
		hits, err := s.Search(t.Context(), scope, q, 8, 0)
		t.Logf("%s on %d chunks (%d MiB): %d hits in %s", name, d.Chunks, b.Len()>>20, len(hits), time.Since(start))
		if err != nil || time.Since(start) > searchTimeout {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Search(t.Context(), scope, words(MaxTerms+1, "w%d"), 8, 0); !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "33 distinct words") {
		t.Errorf("33 distinct words: %v", err)
	}
	// Hybrid: the same scope embedded at 512 dimensions (the E4 tape's size); the vector leg ranks
	// every embedded chunk in scope exactly, so it costs scope × dimensions.
	f := model.NewFake(nil)
	f.Vectors = func(text string) ([]float32, error) { return hashVector(text, 512), nil }
	s.UseEmbedder(f, "hash-512")
	start := time.Now()
	if n, _, err := s.Backfill(t.Context()); n != 1 || err != nil {
		t.Fatalf("embedding the scope: %d %v", n, err)
	}
	t.Logf("embedding %d chunks at 512 dimensions (fake model, %d calls): %s", d.Chunks, len(f.EmbedCalls()), time.Since(start))
	for _, q := range []string{"supplier invoice", "absentword1 absentword2", "supplier shall pay fee euros invoice " + words(MaxTerms-6, "absentword%d")} {
		start := time.Now()
		hits, err := s.Search(t.Context(), scope, q, 8, 0)
		t.Logf("hybrid %q on %d chunks: %d hits in %s", q, d.Chunks, len(hits), time.Since(start))
		if err != nil || len(hits) != 8 || time.Since(start) > searchTimeout {
			t.Errorf("hybrid %q: %d hits, %v", q, len(hits), err)
		}
	}
	old := searchTimeout
	searchTimeout = time.Millisecond
	_, err := s.Search(t.Context(), scope, "supplier invoice", 8, 0)
	searchTimeout = old
	if !errors.Is(err, ErrBadInput) || !strings.Contains(err.Error(), "took longer") {
		t.Errorf("a search past its timeout: %v", err)
	}
}

// TestTablePagedByRows (bench: a spreadsheet exhibit read as one 2,000-token page of dense rows):
// a sheet's pages are whole rows, at most TablePageRows each, that join to the section verbatim; a
// page after the first names the sheet's header row by reference (Header, HeaderAt), never inside
// its Text; past headerWindow the header is still the sheet's own (the window doubles back to it).
func TestTablePagedByRows(t *testing.T) {
	t.Parallel()
	s := newService(t)
	for _, rows := range []int{60, 2200} { // 2,200 rows of ~40 characters: pages past the 64 KiB window
		var b strings.Builder
		b.WriteString("## Sheet: Trades\n")
		head := "Date\tAccount\tShares\tProfit"
		b.WriteString(head + "\n")
		for i := range rows {
			fmt.Fprintf(&b, "2023-%02d-%02d\tOceanic-%d\t%d\t$%d.00\n", 1+i%12, 1+i%28, i, 100+i, 7*i)
		}
		text := b.String()
		d := ingestText(t, s, fmt.Sprintf("trades-%d.txt", rows), text)
		var got strings.Builder
		offset, pages := 0, 0
		for {
			p, err := s.Section(t.Context(), []string{d.ID}, d.ID, "s0", offset, 600)
			if err != nil {
				t.Fatal(err)
			}
			pages++
			n := strings.Count(p.Text, "\t") / 3
			if n > TablePageRows || !strings.HasSuffix(p.Text, "\n") || tokens(p.Text) > 600 {
				t.Fatalf("%d rows, page %d: %d rows, %d tokens, ends %q", rows, pages, n, tokens(p.Text), p.Text[max(0, len(p.Text)-12):])
			}
			if offset > 0 && (p.Header != head || p.HeaderAt != len("## Sheet: Trades\n") || strings.Contains(p.Text, head)) {
				t.Fatalf("%d rows, page %d at %d: header %q at %d", rows, pages, offset, p.Header, p.HeaderAt)
			}
			got.WriteString(p.Text)
			if !p.Truncated {
				break
			}
			offset = p.Next
		}
		if got.String() != text || pages < (rows+1)/TablePageRows {
			t.Fatalf("%d rows: %d pages did not join to the sheet (%d of %d bytes)", rows, pages, got.Len(), len(text))
		}
	}
}

// TestTableHeaderIsThePagesOwn (lab-improve r2 verifier M3): a page's Header is the header row of
// the table the page is in, also when that table starts more than a window back after prose.
func TestTableHeaderIsThePagesOwn(t *testing.T) {
	t.Parallel()
	s := newService(t)
	table := func(b *strings.Builder, head string, rows int, tag string) {
		b.WriteString(head + "\n")
		for i := range rows {
			fmt.Fprintf(b, "2023-%02d-%02d\t%s-%d\t%d\t$%d.00\n", 1+i%12, 1+i%28, tag, i, 100+i, 7*i)
		}
	}
	cases := map[string]func() (string, []string){
		"two sheets": func() (string, []string) {
			var b strings.Builder
			b.WriteString("## Sheet: Trades\n")
			table(&b, "Date\tAccount\tShares\tProfit", 2200, "Oceanic")
			b.WriteString("\n## Sheet: Payments\n")
			table(&b, "Paid\tPayee\tInvoice\tAmount", 2200, "Vendor")
			return b.String(), []string{"Date\tAccount\tShares\tProfit", "Paid\tPayee\tInvoice\tAmount"}
		},
		"prose then table": func() (string, []string) {
			var b strings.Builder
			b.WriteString("Schedule A\n")
			for i := range 1200 {
				fmt.Fprintf(&b, "Paragraph %d of the narrative describes the allocation process in some detail.\n", i)
			}
			table(&b, "Paid\tPayee\tInvoice\tAmount", 2200, "Vendor")
			return b.String(), []string{"Paid\tPayee\tInvoice\tAmount"}
		},
	}
	for name, mk := range cases {
		text, heads := mk()
		d := ingestText(t, s, strings.ReplaceAll(name, " ", "-")+".txt", text)
		o, err := s.Outline(t.Context(), []string{d.ID}, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		bad := 0
		for _, sec := range o {
			for offset := 0; ; {
				p, err := s.Section(t.Context(), []string{d.ID}, d.ID, sec.SectionID, offset, 600)
				if err != nil {
					t.Fatal(err)
				}
				// which table is this page in? the last header that starts before the page
				want := ""
				at := strings.Index(text, p.Text)
				for _, h := range heads {
					if i := strings.Index(text, h+"\n"); i >= 0 && i < at {
						want = h
					}
				}
				if p.Header == "" && offset > 0 && want != "" && strings.Contains(p.Text, "\t") && !strings.Contains(p.Text, want) {
					if bad++; bad <= 3 {
						t.Errorf("%s: %s page at %d names no header, want %q", name, sec.SectionID, offset, want)
					}
				}
				if p.Header != "" && p.Header != want {
					if bad++; bad <= 3 {
						t.Errorf("%s: %s page at %d: header %q, want %q", name, sec.SectionID, offset, p.Header, want)
					}
				}
				if !p.Truncated {
					break
				}
				offset = p.Next
			}
		}
		if bad > 0 {
			t.Errorf("%s: %d pages carry another table's or a data row as header (sections %d)", name, bad, len(o))
		}
	}
}

// TestTableHeaderOfASecondTable (lab-improve r2 verifier M3): two tables of one section separated
// by a blank line (a .docx's empty paragraph): the second table's pages name its own header.
func TestTableHeaderOfASecondTable(t *testing.T) {
	t.Parallel()
	s := newService(t)
	var b strings.Builder
	b.WriteString("Schedule 2. Accounts\n")
	b.WriteString("Account\tOwner\tOpened\tBalance\n")
	for i := range 5 {
		fmt.Fprintf(&b, "A-%d\tOwner %d\t2020-01-0%d\t$%d.00\n", i, i, 1+i, 1000*i)
	}
	b.WriteString("\n")
	b.WriteString("Trade date\tTicker\tShares\tPrice\n")
	for i := range 300 {
		fmt.Fprintf(&b, "2023-%02d-%02d\tXYZ\t%d\t$%d.25\n", 1+i%12, 1+i%28, 100+i, 10+i)
	}
	text := b.String()
	d := ingestText(t, s, "two-tables.txt", text)
	o, _ := s.Outline(t.Context(), []string{d.ID}, d.ID)
	t.Logf("sections: %d", len(o))
	for offset := 0; ; {
		p, err := s.Section(t.Context(), []string{d.ID}, d.ID, o[0].SectionID, offset, 600)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.Text, "\tXYZ\t") && !strings.Contains(p.Text, "Trade date") && p.Header != "Trade date\tTicker\tShares\tPrice" {
			t.Fatalf("a page of the trades table at %d names header %q", offset, p.Header)
		}
		if !p.Truncated {
			break
		}
		offset = p.Next
	}
}
