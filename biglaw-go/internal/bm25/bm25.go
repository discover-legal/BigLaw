// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package bm25 is a pure-Go, in-memory Okapi BM25 sparse-retrieval index.
//
// It is the lexical half of a hybrid RAG system: callers index text chunks
// keyed by id and rank them against a query by BM25 relevance. The dense
// (vector) half lives elsewhere; the two rankings are fused via Reciprocal
// Rank Fusion outside this package, so the index deliberately exposes only
// indexing and ranking — no fusion logic.
//
// The index is safe for concurrent Search while Add/Remove run: it is guarded
// by a sync.RWMutex. Scoring statistics (corpus size, document frequencies,
// document lengths and the average document length) are maintained
// incrementally so Add and Remove are O(unique terms in the chunk), never a
// full re-scan.
package bm25

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// BM25 tuning defaults. They are exported so callers may construct an Index
// with different values via NewWithParams.
//
//   - K1 controls term-frequency saturation (how quickly extra occurrences of a
//     term stop adding score). 1.2–2.0 is the usual range.
//   - B controls length normalization (0 = none, 1 = full). 0.75 is standard.
const (
	DefaultK1 = 1.5
	DefaultB  = 0.75
)

// Result is a single ranked chunk: its id and its summed BM25 score across the
// query terms.
type Result struct {
	ID    string
	Score float64
}

// doc holds the per-chunk state needed to score it: term frequencies within the
// chunk and the chunk's total token length (post-tokenization).
type doc struct {
	tf     map[string]int
	length int
}

// Index is an in-memory BM25 index. The zero value is not usable; construct
// one with New or NewWithParams.
type Index struct {
	mu sync.RWMutex

	k1 float64
	b  float64

	docs map[string]*doc // id -> document state
	df   map[string]int  // term -> number of docs containing it (document frequency)

	totalLen int // sum of all document lengths, for incremental avgDocLen
}

// New returns an empty Index using the default BM25 parameters (DefaultK1,
// DefaultB).
func New() *Index {
	return NewWithParams(DefaultK1, DefaultB)
}

// NewWithParams returns an empty Index with caller-supplied BM25 parameters.
func NewWithParams(k1, b float64) *Index {
	return &Index{
		k1:   k1,
		b:    b,
		docs: make(map[string]*doc),
		df:   make(map[string]int),
	}
}

// Add indexes text under id, replacing any chunk previously stored under the
// same id. Tokenization (lowercase, unicode-aware split, stopword and
// short-token removal) is applied before term frequencies are recorded. A chunk
// whose text yields no tokens is still stored (with length 0) so that a
// subsequent Add/Remove of the same id behaves predictably; it simply never
// matches a query.
func (ix *Index) Add(id, text string) {
	tokens := Tokenize(text)

	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	// Replace semantics: undo the old document's contribution first.
	ix.removeLocked(id)

	ix.docs[id] = &doc{tf: tf, length: len(tokens)}
	ix.totalLen += len(tokens)
	for term := range tf {
		ix.df[term]++ // df counts documents, so one increment per unique term
	}
}

// Remove drops the chunk stored under id. Removing an unknown id is a no-op.
func (ix *Index) Remove(id string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.removeLocked(id)
}

// removeLocked retracts id's contribution to df, totalLen and the doc map. The
// caller must hold the write lock.
func (ix *Index) removeLocked(id string) {
	d, ok := ix.docs[id]
	if !ok {
		return
	}
	for term := range d.tf {
		if ix.df[term] <= 1 {
			delete(ix.df, term)
		} else {
			ix.df[term]--
		}
	}
	ix.totalLen -= d.length
	delete(ix.docs, id)
}

// Len reports the number of indexed chunks.
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.docs)
}

// Search ranks indexed chunks against query by descending BM25 score and
// returns at most topK results. An empty query, an empty index, or topK <= 0
// returns nil. Ties in score are broken by id so results are deterministic.
//
// Only documents that contain at least one query term receive a score; documents
// matching no term are omitted entirely (a zero-score result carries no signal
// for downstream rank fusion).
func (ix *Index) Search(query string, topK int) []Result {
	if topK <= 0 {
		return nil
	}
	qTerms := Tokenize(query)
	if len(qTerms) == 0 {
		return nil
	}

	// Deduplicate query terms: a term repeated in the query must not multiply a
	// document's score, and it lets us skip redundant idf work.
	seen := make(map[string]struct{}, len(qTerms))
	terms := qTerms[:0:0]
	for _, t := range qTerms {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		terms = append(terms, t)
	}

	ix.mu.RLock()
	defer ix.mu.RUnlock()

	n := len(ix.docs)
	if n == 0 {
		return nil
	}
	avgLen := float64(ix.totalLen) / float64(n)

	// Precompute idf per query term once.
	type termStat struct {
		term string
		idf  float64
	}
	stats := make([]termStat, 0, len(terms))
	for _, t := range terms {
		df := ix.df[t]
		if df == 0 {
			continue // term not in corpus: contributes nothing to any doc
		}
		idf := math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5))
		stats = append(stats, termStat{term: t, idf: idf})
	}
	if len(stats) == 0 {
		return nil
	}

	scores := make(map[string]float64)
	for id, d := range ix.docs {
		var s float64
		dl := float64(d.length)
		denomNorm := ix.k1 * (1 - ix.b + ix.b*dl/avgLen)
		for _, st := range stats {
			tf := d.tf[st.term]
			if tf == 0 {
				continue
			}
			f := float64(tf)
			s += st.idf * (f * (ix.k1 + 1)) / (f + denomNorm)
		}
		if s > 0 {
			scores[id] = s
		}
	}
	if len(scores) == 0 {
		return nil
	}

	results := make([]Result, 0, len(scores))
	for id, s := range scores {
		results = append(results, Result{ID: id, Score: s})
	}
	// Descending by score, ties broken by ascending id for determinism.
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].ID < results[j].ID
	})

	if len(results) > topK {
		results = results[:topK]
	}
	return results
}

// stopwords is a deliberately small set of high-frequency English function
// words. BM25's idf already down-weights ubiquitous terms, so the goal here is
// only to drop the most common closed-class words that add noise and bloat
// postings — not to perform aggressive linguistic filtering. Keep this list
// short; an over-eager stoplist hurts recall on legal text (e.g. "will",
// "shall", "may" are load-bearing in contracts and are intentionally absent).
var stopwords = map[string]struct{}{
	"the": {}, "and": {}, "of": {}, "to": {}, "in": {},
	"is": {}, "that": {}, "it": {}, "for": {}, "on": {},
	"with": {}, "as": {}, "at": {}, "by": {}, "an": {},
	"be": {}, "this": {}, "or": {}, "are": {}, "was": {},
	"but": {}, "not": {}, "from": {}, "has": {}, "have": {},
}

// minTokenLen drops single-character tokens, which are almost never useful query
// terms. The single-letter article "a" and conjunction-noise are removed here
// rather than via the stoplist.
const minTokenLen = 2

// Tokenize converts text into lowercased BM25 terms: it splits on any rune that
// is neither a Unicode letter nor a Unicode number, lowercases each token, drops
// tokens shorter than minTokenLen runes, and drops stopwords. It is exported so
// that callers building a hybrid retriever can tokenize a query identically to
// the way documents were indexed.
func Tokenize(text string) []string {
	if text == "" {
		return nil
	}
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(f)
		// Count runes, not bytes, so multibyte tokens aren't wrongly dropped.
		if utf8RuneCountLessThan(f, minTokenLen) {
			continue
		}
		if _, stop := stopwords[f]; stop {
			continue
		}
		tokens = append(tokens, f)
	}
	if len(tokens) == 0 {
		return nil
	}
	return tokens
}

// utf8RuneCountLessThan reports whether s has fewer than n runes, scanning only
// as far as needed.
func utf8RuneCountLessThan(s string, n int) bool {
	count := 0
	for range s {
		count++
		if count >= n {
			return false
		}
	}
	return count < n
}
