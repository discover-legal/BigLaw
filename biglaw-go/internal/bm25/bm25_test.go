// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package bm25

import (
	"fmt"
	"math"
	"sync"
	"testing"
)

// rankOf returns the 0-based position of id in results, or -1 if absent.
func rankOf(results []Result, id string) int {
	for i, r := range results {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func scoreOf(results []Result, id string) (float64, bool) {
	for _, r := range results {
		if r.ID == id {
			return r.Score, true
		}
	}
	return 0, false
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"lowercases", "Contract LAW", []string{"contract", "law"}},
		{"splits on punctuation", "merger,acquisition;deal", []string{"merger", "acquisition", "deal"}},
		{"drops stopwords", "the merger of the company", []string{"merger", "company"}},
		{"drops short tokens", "a I am ok", []string{"am", "ok"}},
		{"keeps numbers", "section 12 clause 3a", []string{"section", "12", "clause", "3a"}},
		{"unicode letters", "café Müller", []string{"café", "müller"}},
		{"only stopwords/short -> nil", "a the of to", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Tokenize(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("Tokenize(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Tokenize(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestEmptyQueryAndEmptyIndex(t *testing.T) {
	ix := New()

	if got := ix.Search("anything", 10); got != nil {
		t.Fatalf("Search on empty index = %v, want nil", got)
	}

	ix.Add("d1", "the merger agreement governs the transaction")
	if got := ix.Search("", 10); got != nil {
		t.Fatalf("empty query = %v, want nil", got)
	}
	if got := ix.Search("the of to", 10); got != nil {
		t.Fatalf("all-stopword query = %v, want nil", got)
	}
	if got := ix.Search("merger", 0); got != nil {
		t.Fatalf("topK=0 = %v, want nil", got)
	}
	if got := ix.Search("nonexistentterm", 10); got != nil {
		t.Fatalf("query term absent from corpus = %v, want nil", got)
	}
}

// TestDocFrequencyRanking: a term appearing in 2 of 5 docs must rank exactly
// those two docs and only those two.
func TestTermPresenceRanking(t *testing.T) {
	ix := New()
	ix.Add("d1", "merger control review under competition law")
	ix.Add("d2", "employment dispute and wrongful dismissal claim")
	ix.Add("d3", "merger clearance filing before the regulator")
	ix.Add("d4", "intellectual property licensing royalties")
	ix.Add("d5", "data privacy breach notification duties")

	got := ix.Search("merger", 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 docs containing 'merger', got %d: %v", len(got), got)
	}
	if rankOf(got, "d1") < 0 || rankOf(got, "d3") < 0 {
		t.Fatalf("expected d1 and d3 in results, got %v", got)
	}
}

// TestRareTermOutranksCommon: idf must make a rare term outweigh a common one.
// Doc A contains a term present in every doc (common) once; Doc B contains a
// term present in only one doc (rare) once. A query for "common rare" must rank
// the doc holding the rare term above the doc holding only the common term.
func TestRareTermOutranksCommon(t *testing.T) {
	ix := New()
	// "law" appears in all four docs (common, low idf).
	// "antitrust" appears in only d_rare (rare, high idf).
	ix.Add("d_common", "law law law general principles overview")
	ix.Add("d_rare", "antitrust law specialized analysis")
	ix.Add("d_filler1", "law contract drafting practice")
	ix.Add("d_filler2", "law procedure courtroom etiquette")

	got := ix.Search("law antitrust", 10)
	rRare := rankOf(got, "d_rare")
	rCommon := rankOf(got, "d_common")
	if rRare < 0 || rCommon < 0 {
		t.Fatalf("expected both d_rare and d_common in results: %v", got)
	}
	if rRare >= rCommon {
		t.Fatalf("rare-term doc should outrank common-only doc; got ranks rare=%d common=%d (%v)", rRare, rCommon, got)
	}
}

// TestLengthNormalization: two docs each contain the query term exactly once,
// but one is padded with many unrelated tokens. With b>0 the longer doc must
// score lower.
func TestLengthNormalization(t *testing.T) {
	ix := New()
	ix.Add("short", "arbitration clause")
	ix.Add("long", "arbitration clause "+
		"alpha bravo charlie delta echo foxtrot golf hotel india juliet "+
		"kilo lima mike november oscar papa quebec romeo sierra tango")

	got := ix.Search("arbitration", 10)
	sShort, okS := scoreOf(got, "short")
	sLong, okL := scoreOf(got, "long")
	if !okS || !okL {
		t.Fatalf("expected both docs scored: %v", got)
	}
	if !(sShort > sLong) {
		t.Fatalf("length norm: short (%.4f) should outscore long (%.4f)", sShort, sLong)
	}
}

// TestTermFrequencySaturation: more occurrences of the query term should score
// higher, all else equal (same length).
func TestTermFrequencyOrdering(t *testing.T) {
	ix := New()
	// Equal length (6 tokens each post-tokenize); differ only in tf of "patent".
	ix.Add("once", "patent alpha bravo charlie delta echo")
	ix.Add("thrice", "patent patent patent alpha bravo charlie")

	got := ix.Search("patent", 10)
	if rankOf(got, "thrice") >= rankOf(got, "once") {
		t.Fatalf("higher tf should rank first: %v", got)
	}
}

func TestTopKAndTieDeterminism(t *testing.T) {
	ix := New()
	// Three identical docs -> identical scores -> tie broken by id ascending.
	ix.Add("c", "indemnity indemnity")
	ix.Add("a", "indemnity indemnity")
	ix.Add("b", "indemnity indemnity")

	got := ix.Search("indemnity", 2)
	if len(got) != 2 {
		t.Fatalf("topK=2 should cap at 2, got %d", len(got))
	}
	// Equal scores -> sorted by id: a, b come before c.
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("tie-break by id failed: got %v, want [a b]", got)
	}

	// Determinism across repeated calls.
	for i := 0; i < 20; i++ {
		again := ix.Search("indemnity", 3)
		if len(again) != 3 || again[0].ID != "a" || again[1].ID != "b" || again[2].ID != "c" {
			t.Fatalf("non-deterministic ordering on call %d: %v", i, again)
		}
	}
}

// TestAddReplaceAndRemoveConsistency verifies incremental stats stay correct:
// Len, avgDocLen, and df after replace + remove.
func TestAddReplaceAndRemoveConsistency(t *testing.T) {
	ix := New()

	// Lengths post-tokenize: d1=3, d2=2, d3=5.
	ix.Add("d1", "alpha beta gamma")
	ix.Add("d2", "alpha delta")
	ix.Add("d3", "epsilon zeta eta theta iota")

	if ix.Len() != 3 {
		t.Fatalf("Len = %d, want 3", ix.Len())
	}
	if avg := avgDocLen(ix); avg != (3+2+5)/3.0 {
		t.Fatalf("avgDocLen = %v, want %v", avg, (3+2+5)/3.0)
	}
	if df := dfOf(ix, "alpha"); df != 2 {
		t.Fatalf("df(alpha) = %d, want 2", df)
	}

	// Replace d1 with a shorter doc that drops 'alpha' and 'gamma'.
	ix.Add("d1", "kappa") // 1 token
	if ix.Len() != 3 {
		t.Fatalf("Len after replace = %d, want 3", ix.Len())
	}
	if df := dfOf(ix, "alpha"); df != 1 {
		t.Fatalf("df(alpha) after replacing d1 = %d, want 1 (only d2)", df)
	}
	if df := dfOf(ix, "gamma"); df != 0 {
		t.Fatalf("df(gamma) after replacing d1 = %d, want 0 (term gone)", df)
	}
	if avg := avgDocLen(ix); avg != (1+2+5)/3.0 {
		t.Fatalf("avgDocLen after replace = %v, want %v", avg, (1+2+5)/3.0)
	}

	// Remove d3; only d1(1) and d2(2) remain.
	ix.Remove("d3")
	if ix.Len() != 2 {
		t.Fatalf("Len after remove = %d, want 2", ix.Len())
	}
	if avg := avgDocLen(ix); avg != (1+2)/2.0 {
		t.Fatalf("avgDocLen after remove = %v, want %v", avg, (1+2)/2.0)
	}
	if df := dfOf(ix, "epsilon"); df != 0 {
		t.Fatalf("df(epsilon) after removing d3 = %d, want 0", df)
	}

	// Remove unknown id is a no-op.
	ix.Remove("ghost")
	if ix.Len() != 2 {
		t.Fatalf("Len after no-op remove = %d, want 2", ix.Len())
	}

	// Remove everything; index empty + stats zeroed.
	ix.Remove("d1")
	ix.Remove("d2")
	if ix.Len() != 0 {
		t.Fatalf("Len after removing all = %d, want 0", ix.Len())
	}
	if got := ix.Search("alpha", 5); got != nil {
		t.Fatalf("search on emptied index = %v, want nil", got)
	}
	if ix.totalLen != 0 {
		t.Fatalf("totalLen after removing all = %d, want 0", ix.totalLen)
	}
	if len(ix.df) != 0 {
		t.Fatalf("df map not empty after removing all: %v", ix.df)
	}
}

// avgDocLen / dfOf read internal state under lock for assertions (same package).
func avgDocLen(ix *Index) float64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if len(ix.docs) == 0 {
		return 0
	}
	return float64(ix.totalLen) / float64(len(ix.docs))
}

func dfOf(ix *Index, term string) int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.df[term]
}

// TestIDFFormulaExact pins the math: single matching doc in a 2-doc corpus.
func TestIDFFormulaExact(t *testing.T) {
	ix := New()
	ix.Add("d1", "quantum") // length 1, contains the term once
	ix.Add("d2", "classical")

	got := ix.Search("quantum", 5)
	if len(got) != 1 || got[0].ID != "d1" {
		t.Fatalf("expected only d1, got %v", got)
	}

	// Hand-compute expected score.
	n, df := 2.0, 1.0
	idf := math.Log(1 + (n-df+0.5)/(df+0.5))
	avg := (1.0 + 1.0) / 2.0
	dl, tf := 1.0, 1.0
	denom := tf + DefaultK1*(1-DefaultB+DefaultB*dl/avg)
	want := idf * (tf * (DefaultK1 + 1)) / denom

	if math.Abs(got[0].Score-want) > 1e-9 {
		t.Fatalf("score = %.12f, want %.12f", got[0].Score, want)
	}
}

func TestEmptyTextDocStoredButUnmatchable(t *testing.T) {
	ix := New()
	ix.Add("blank", "!!! ??? ,,,") // tokenizes to nothing
	if ix.Len() != 1 {
		t.Fatalf("blank doc should still be stored; Len = %d", ix.Len())
	}
	if got := ix.Search("anything", 5); got != nil {
		t.Fatalf("blank doc must not match: %v", got)
	}
	// And it can be cleanly removed.
	ix.Remove("blank")
	if ix.Len() != 0 {
		t.Fatalf("Len after removing blank = %d, want 0", ix.Len())
	}
}

// TestConcurrentAddSearch exercises the RWMutex under -race.
func TestConcurrentAddSearch(t *testing.T) {
	ix := New()
	const writers, readers, iters = 8, 8, 200

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := fmt.Sprintf("w%d-d%d", w, i)
				ix.Add(id, fmt.Sprintf("merger acquisition deal number %d %d", w, i))
				if i%3 == 0 {
					ix.Remove(id)
				}
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = ix.Search("merger acquisition", 5)
				_ = ix.Len()
			}
		}()
	}
	wg.Wait()

	// Index must still answer queries sanely after the storm.
	ix.Add("final", "merger acquisition closing")
	got := ix.Search("merger", 50)
	if rankOf(got, "final") < 0 {
		t.Fatalf("post-concurrency search lost 'final': %v", got)
	}
}

func TestNewWithParams(t *testing.T) {
	// b=0 disables length normalization: padding must not change the score of a
	// single-occurrence term.
	ix := NewWithParams(DefaultK1, 0)
	ix.Add("short", "tort")
	ix.Add("long", "tort filler filler filler filler filler")

	got := ix.Search("tort", 5)
	sShort, _ := scoreOf(got, "short")
	sLong, _ := scoreOf(got, "long")
	if math.Abs(sShort-sLong) > 1e-9 {
		t.Fatalf("with b=0 scores should be equal: short=%.6f long=%.6f", sShort, sLong)
	}
}
