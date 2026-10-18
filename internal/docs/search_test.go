// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

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
	for _, sc := range [][]string{scope, nil, {}, {"not-a-uuid"}, {out.ID[:35] + "0"}, {"' OR true --"}} {
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
	for _, id := range []string{"s4", "s-1", "s01", "x1", "", "s"} {
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
	for _, bad := range [][2]int{{-1, 10}, {0, -1}, {0, MaxSectionTokens + 1}, {len([]rune(text)) + 1, 10}} {
		if _, err := s.Section(t.Context(), []string{d.ID}, d.ID, "s0", bad[0], bad[1]); !errors.Is(err, ErrBadInput) {
			t.Errorf("section offset %d limit %d: %v", bad[0], bad[1], err)
		}
	}
}

// TestLexicalRecallProofRuns (gate E4): for each planted proof-run fact, a search with the fact's
// title, restricted to its matter's documents, returns a chunk holding one of its evidence strings
// in the top 8, for at least 95% of the facts. Per-matter recall is printed.
func TestLexicalRecallProofRuns(t *testing.T) {
	t.Parallel()
	s := newService(t)
	keys, _ := filepath.Glob("../../eval/tasks/*/*/answer_key.json")
	if len(keys) != 5 {
		t.Fatalf("%d proof-run matters, want 5", len(keys))
	}
	ws := regexp.MustCompile(`\s+`)
	norm := func(x string) string { return strings.ToLower(ws.ReplaceAllString(strings.TrimSpace(x), " ")) }
	total, found, allTotal, allFound := 0, 0, 0, 0
	for _, key := range keys {
		var ak struct {
			Facts []struct {
				ID, Title string
				Evidence  []string
			}
		}
		raw, err := os.ReadFile(key)
		if err != nil || json.Unmarshal(raw, &ak) != nil {
			t.Fatalf("%s: %v", key, err)
		}
		docsDir := filepath.Join(filepath.Dir(key), "documents")
		files, _ := os.ReadDir(docsDir)
		var scope []string
		for _, f := range files {
			b, err := os.ReadFile(filepath.Join(docsDir, f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			scope = append(scope, ingestText(t, s, f.Name(), string(b)).ID)
		}
		n, hit := 0, 0
		for _, f := range ak.Facts {
			hits, err := s.Search(t.Context(), scope, f.Title, 8, 0)
			if err != nil {
				t.Fatal(err)
			}
			any := false
			for _, ev := range f.Evidence {
				in := slices.ContainsFunc(hits, func(h Hit) bool { return strings.Contains(norm(h.Text), norm(ev)) })
				allTotal++
				if in {
					allFound++
					any = true
				}
			}
			if n++; any {
				hit++
			} else {
				t.Logf("  miss %s: %s", f.ID, f.Title)
			}
		}
		t.Logf("%s: recall@8 %d/%d", filepath.Base(filepath.Dir(key)), hit, n)
		total, found = total+n, found+hit
	}
	t.Logf("E4 recall@8: %d/%d facts = %.3f (every evidence string: %d/%d = %.3f)", found, total, float64(found)/float64(total),
		allFound, allTotal, float64(allFound)/float64(allTotal))
	if total != 68 {
		t.Errorf("%d planted facts, want 68", total)
	}
	if float64(found) < 0.95*float64(total) {
		t.Errorf("E4 failed: recall@8 %d/%d < 0.95", found, total)
	}
}
