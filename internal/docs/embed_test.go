// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/model"
)

// axes embeds by keyword: a text naming "stripes" or "zebra" points one way, anything else the
// other, so a vector match can be told apart from a word match.
func axes(text string) ([]float32, error) {
	if t := strings.ToLower(text); strings.Contains(t, "stripes") || strings.Contains(t, "zebra") {
		return []float32{1, 0}, nil
	}
	return []float32{0, 1}, nil
}

func hybridService(t *testing.T, vectors func(string) ([]float32, error)) (*Service, *model.Fake) {
	t.Helper()
	s := newService(t)
	f := model.NewFake(nil)
	f.Vectors = vectors
	s.UseEmbedder(f, "test-embed")
	return s, f
}

// TestHybridKeepsScope (G1 with vectors): the vector leg reads only the scope's documents. A
// document outside the scope whose chunk is the query's exact vector is never returned, while the
// same chunk wins once its document is in scope; an empty or malformed scope returns nothing and
// embeds nothing.
func TestHybridKeepsScope(t *testing.T) {
	t.Parallel()
	s, f := hybridService(t, axes)
	in := ingestText(t, s, "supply.txt", "The Supplier shall deliver the goods.\n\nThe Buyer shall pay the price.")
	out := ingestText(t, s, "other-matter.txt", "Equine stripes appear here.")
	if in.EmbedModel != "test-embed" || out.EmbedModel != "test-embed" {
		t.Fatalf("not embedded: %q %q", in.EmbedModel, out.EmbedModel)
	}
	const q = "zebra-like animal" // no word in common with either document
	hits, err := s.Search(t.Context(), []string{in.ID}, q, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || slices.ContainsFunc(hits, func(h Hit) bool { return h.DocumentID != in.ID }) {
		t.Fatalf("scope [in]: %v", hitIDs(hits))
	}
	both, err := s.Search(t.Context(), []string{in.ID, strings.ToUpper(out.ID)}, q, 8, 0)
	if err != nil || len(both) == 0 || both[0].DocumentID != out.ID {
		t.Fatalf("with the other document in scope its chunk must rank first (the vector leg works): %v %v", hitIDs(both), err)
	}
	calls := len(f.EmbedCalls())
	nearMiss := out.ID[:35] + map[bool]string{true: "1", false: "0"}[out.ID[35] == '0'] // differs in the last hex digit, always
	for _, scope := range [][]string{nil, {}, {"not-a-uuid"}, {nearMiss}} {
		if hits, err := s.Search(t.Context(), scope, q, 8, 0); err != nil || slices.ContainsFunc(hits, func(h Hit) bool { return h.DocumentID == out.ID }) {
			t.Errorf("scope %v: %v %v", scope, hitIDs(hits), err)
		}
	}
	if n := len(f.EmbedCalls()) - calls; n != 0 {
		t.Errorf("%d query embeddings for scopes with no document of the scope", n)
	}
	// Without an embedder the same query finds nothing: the hits above came from the vector leg.
	lex := New(s.st)
	if hits, err := lex.Search(t.Context(), []string{in.ID, out.ID}, q, 8, 0); err != nil || len(hits) != 0 {
		t.Errorf("lexical: %v %v", hitIDs(hits), err)
	}
}

// TestHybridFusion: hybrid rank is reciprocal-rank fusion, 1/(60+lexical rank) + 1/(60+vector
// rank), each leg's ties by chunk id, deterministic across calls and pages; only documents the
// current model embedded completely (and with the query's vector length) are in the vector leg.
func TestHybridFusion(t *testing.T) {
	t.Parallel()
	s, _ := hybridService(t, axes)
	a := ingestText(t, s, "a.txt", "Indemnity applies to stripes.")
	b := ingestText(t, s, "b.txt", "Zebra crossings are marked.")
	c := ingestText(t, s, "c.txt", "Indemnity is capped at the price.")
	scope := []string{a.ID, b.ID, c.ID}
	hits, err := s.Search(t.Context(), scope, "indemnity stripes", 8, 0)
	if err != nil || len(hits) != 3 {
		t.Fatalf("%v %v", hitIDs(hits), err)
	}
	near := func(x, y float64) bool { return math.Abs(x-y) < 1e-12 }
	// Every embedded chunk is in the vector leg: a and b share the query's vector (a first, by
	// chunk id), c is third; lexically a is first and c second, b absent.
	for i, want := range []struct {
		id   string
		rank float64
	}{{a.ID, 2.0 / 61}, {c.ID, 1.0/62 + 1.0/63}, {b.ID, 1.0 / 62}} {
		if hits[i].DocumentID != want.id || !near(hits[i].Rank, want.rank) {
			t.Errorf("hit %d: %s %v, want %s %v", i, hits[i].DocumentID, hits[i].Rank, want.id, want.rank)
		}
	}
	for range 3 {
		again, _ := s.Search(t.Context(), scope, "indemnity stripes", 8, 0)
		page, _ := s.Search(t.Context(), scope, "indemnity stripes", 1, 1)
		if !slices.Equal(hitIDs(again), hitIDs(hits)) || len(page) != 1 || page[0].Text != hits[1].Text || !near(page[0].Rank, hits[1].Rank) {
			t.Fatalf("not deterministic: %v / page %v", hitIDs(again), hitIDs(page))
		}
	}
	// Another model's vectors, a half-done document (embed_model NULL) and a different vector
	// length are all left out of the vector leg: b, which only the vector leg finds, disappears.
	for _, sql := range []string{`UPDATE documents SET embed_model = 'old-model' WHERE id = $1`, `UPDATE documents SET embed_model = NULL WHERE id = $1`,
		`UPDATE chunks SET embedding = '[1,0,0]' WHERE document_id = $1`} {
		if _, err := s.db.Exec(`UPDATE documents SET embed_model = 'test-embed' WHERE id = $1`, b.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(sql, b.ID); err != nil {
			t.Fatal(err)
		}
		hits, err := s.Search(t.Context(), scope, "indemnity stripes", 8, 0)
		if err != nil || slices.ContainsFunc(hits, func(h Hit) bool { return h.DocumentID == b.ID }) {
			t.Errorf("after %q: %v %v", sql, hitIDs(hits), err)
		}
	}
}

// TestEmbedFailureLeavesLexical: a failed or cancelled embedding call never fails the ingest: the
// document is stored lexical only with EmbedNote (persisted), found by its words, and absent from
// the vector leg; a failed query embedding is a typed model error, not a silent lexical answer.
func TestEmbedFailureLeavesLexical(t *testing.T) {
	t.Parallel()
	var fail atomic.Bool
	s, f := hybridService(t, func(text string) ([]float32, error) {
		if fail.Load() {
			return nil, &model.Error{Kind: model.ErrUnavailable, Detail: "HTTP 500"}
		}
		return axes(text)
	})
	fail.Store(true)
	d, err := s.IngestText(t.Context(), "failed.txt", "Zebra clause: the Supplier warrants the stripes.")
	if err != nil || d.EmbedModel != "" || !slices.Contains(d.Notes, EmbedNote) {
		t.Fatalf("ingest with a failing embedder: %+v, %v", d, err)
	}
	got, err := s.Get(t.Context(), []string{d.ID}, d.ID)
	if err != nil || got.EmbedModel != "" || !slices.Contains(got.Notes, EmbedNote) {
		t.Fatalf("stored: %+v %v", got, err)
	}
	// A cancelled ingest (the client went away during embedding) keeps the document too.
	ctx, cancel := context.WithCancel(t.Context())
	fail.Store(false)
	cs, _ := hybridService(t, func(text string) ([]float32, error) { cancel(); return nil, context.Canceled })
	cd, err := cs.IngestText(ctx, "cancelled.txt", "Zebra crossing.")
	if err != nil || cd.EmbedModel != "" || !slices.Contains(cd.Notes, EmbedNote) {
		t.Fatalf("cancelled ingest: %+v, %v", cd, err)
	}
	if got, err := cs.Get(t.Context(), []string{cd.ID}, cd.ID); err != nil || !slices.Contains(got.Notes, EmbedNote) {
		t.Fatalf("cancelled ingest's note not stored: %+v %v", got.Notes, err)
	}
	// Found lexically, ranked by its lexical rank counted twice (2/61: no penalty for the missing
	// vector), never by its unembedded vectors.
	ok := ingestText(t, s, "ok.txt", "Zebra herds graze.")
	hits, err := s.Search(t.Context(), []string{d.ID, ok.ID}, "supplier warrants", 8, 0)
	if err != nil || len(hits) != 2 || hits[0].DocumentID != d.ID || hits[0].Rank != 2.0/61 {
		t.Fatalf("lexical-only document in a hybrid search: %v %v", hits, err)
	}
	// The query's own embedding failing is an error, typed for the API (502), where the scope has
	// vectors to compare; a scope with none is searched lexically without embedding the query.
	fail.Store(true)
	if _, err := s.Search(t.Context(), []string{d.ID, ok.ID}, "supplier", 8, 0); !errors.Is(err, model.ErrUnavailable) || model.HTTPStatus(err) != 502 {
		t.Fatalf("failed query embedding: %v", err)
	}
	calls := len(f.EmbedCalls())
	if hits, err := s.Search(t.Context(), []string{d.ID}, "supplier", 8, 0); err != nil || len(hits) != 1 || len(f.EmbedCalls()) != calls {
		t.Fatalf("a lexical-only scope during an embed outage: %v %v, %d calls", hitIDs(hits), err, len(f.EmbedCalls())-calls)
	}
	// The backfill embeds it later and removes the note.
	fail.Store(false)
	if n, failed, err := s.Backfill(t.Context()); err != nil || n != 1 || failed != 0 {
		t.Fatalf("backfill: %d, %d failed, %v", n, failed, err)
	}
	if got, _ := s.Get(t.Context(), []string{d.ID}, d.ID); got.EmbedModel != "test-embed" || slices.Contains(got.Notes, EmbedNote) {
		t.Errorf("after backfill: %q %v", got.EmbedModel, got.Notes)
	}
}

// TestBackfillEmbedsOldDocuments: documents stored before the embed role was set, or embedded by
// another model, are embedded by Backfill one at a time; documents stored after it starts are left
// to their own ingest; each failure is counted and noted on its document; it stops after 3
// failures in a row (not 3 in all), and a second run has nothing to do.
func TestBackfillEmbedsOldDocuments(t *testing.T) {
	t.Parallel()
	s := newService(t) // no embedder: lexical only
	var ids []string
	for i := range 5 {
		ids = append(ids, ingestText(t, s, fmt.Sprintf("old-%d.txt", i), fmt.Sprintf("Clause %d. Zebra stripes, item %d.", i, i)).ID)
	}
	empty := ingestText(t, s, "empty.txt", " \n")
	if n, failed, err := s.Backfill(t.Context()); n != 0 || failed != 0 || err != nil {
		t.Fatalf("backfill without an embedder: %d %d %v", n, failed, err)
	}
	if _, err := s.db.Exec(`UPDATE documents SET embed_model = 'old-model' WHERE id = $1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE documents SET created_at = now() + interval '1 hour' WHERE id = $1`, ids[4]); err != nil { // "stored after the start"
		t.Fatal(err)
	}
	// An endpoint that is down: three documents fail, then the backfill stops.
	failing := model.NewFake(nil)
	failing.Vectors = func(string) ([]float32, error) {
		return nil, &model.Error{Kind: model.ErrUnavailable, Detail: "HTTP 503"}
	}
	s.UseEmbedder(failing, "new-model")
	if n, failed, err := s.Backfill(t.Context()); n != 0 || failed != 3 || err == nil || !strings.Contains(err.Error(), "3 documents in a row") || len(failing.EmbedCalls()) != 3 {
		t.Fatalf("failing backfill: %d, %d failed, %v, %d calls", n, failed, err, len(failing.EmbedCalls()))
	}
	var noted int
	if err := s.db.QueryRow(`SELECT count(*) FROM documents WHERE $1 = ANY(notes)`, EmbedNote).Scan(&noted); err != nil || noted != 3 {
		t.Errorf("%d failed documents carry EmbedNote, want 3 (%v)", noted, err)
	}
	f := model.NewFake(nil)
	f.Vectors = axes
	s.UseEmbedder(f, "new-model")
	if n, failed, err := s.Backfill(t.Context()); n != 4 || failed != 0 || err != nil {
		t.Fatalf("backfill: %d documents, %d failed, %v; want 4 (ids 0-3: one of another model, three never embedded)", n, failed, err)
	}
	for i, id := range ids {
		d, _ := s.Get(t.Context(), []string{id}, id)
		if want := map[bool]string{true: "new-model", false: ""}[i < 4]; d.EmbedModel != want {
			t.Errorf("doc %d: embedded by %q, want %q", i, d.EmbedModel, want)
		}
	}
	if d, _ := s.Get(t.Context(), []string{empty.ID}, empty.ID); d.EmbedModel != "" {
		t.Errorf("a document with no chunks was embedded: %+v", d)
	}
	var missing int
	if err := s.db.QueryRow(`SELECT count(*) FROM chunks c JOIN documents d ON d.id = c.document_id WHERE d.embed_model = 'new-model' AND c.embedding IS NULL`).Scan(&missing); err != nil || missing != 0 {
		t.Errorf("%d chunks of embedded documents have no vector (%v)", missing, err)
	}
	if n, failed, err := s.Backfill(t.Context()); n != 0 || failed != 0 || err != nil {
		t.Errorf("second backfill: %d %d %v", n, failed, err)
	}
	// A cancelled backfill stops at once.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.db.Exec(`UPDATE documents SET embed_model = NULL`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if n, _, err := s.Backfill(ctx); n != 0 || err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("cancelled backfill: %d %v", n, err)
	}
	// Failures not in a row never stop it: fail, embed, fail, fail, embed covers all five.
	s2 := newService(t)
	for i := range 5 {
		ingestText(t, s2, fmt.Sprintf("d%d.txt", i), "Zebra stripes.")
	}
	var call atomic.Int32
	flaky := model.NewFake(nil)
	flaky.Vectors = func(text string) ([]float32, error) {
		if n := call.Add(1); n == 1 || n == 3 || n == 4 {
			return nil, &model.Error{Kind: model.ErrUnavailable, Detail: "HTTP 503"}
		}
		return axes(text)
	}
	s2.UseEmbedder(flaky, "new-model")
	if n, failed, err := s2.Backfill(t.Context()); n != 2 || failed != 3 || err != nil {
		t.Errorf("fail, embed, fail, fail, embed: %d embedded, %d failed, %v", n, failed, err)
	}
}

// TestReembedNeverMixesModels (r1 M-1, L2): a re-embed under a new model that fails part-way, or
// whose batches come back at different lengths, leaves the document lexical only (embed_model
// unset, EmbedNote), so a rollback to the old model re-embeds it completely instead of searching a
// mix of two models' vectors under the old model's name.
func TestReembedNeverMixesModels(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := range embedBatch + 10 {
		fmt.Fprintf(&b, "%d. Clause %d\n\nZebra term %d.\n\n", i+1, i+1, i)
	}
	vecA := func(string) ([]float32, error) { return []float32{1, 0}, nil }
	s, _ := hybridService(t, vecA)
	s.UseEmbedder(s.embedder, "model-a")
	d := ingestText(t, s, "long.txt", b.String())
	if d.EmbedModel != "model-a" || d.Chunks <= embedBatch {
		t.Fatalf("%d chunks embedded by %q", d.Chunks, d.EmbedModel)
	}
	for name, second := range map[string]func(string) ([]float32, error){
		"fails on its second batch": func(string) ([]float32, error) {
			return nil, &model.Error{Kind: model.ErrUnavailable, Detail: "HTTP 400"}
		},
		"second batch longer": func(string) ([]float32, error) { return []float32{0, 1, 0}, nil },
	} {
		var calls atomic.Int32
		f := model.NewFake(nil)
		f.Vectors = func(text string) ([]float32, error) {
			if calls.Add(1) <= embedBatch { // the first batch: model B's vector
				return []float32{0, 1}, nil
			}
			return second(text)
		}
		s.UseEmbedder(f, "model-b")
		if n, failed, err := s.Backfill(t.Context()); n != 0 || failed != 1 || err != nil {
			t.Fatalf("%s: backfill under B: %d, %d failed, %v", name, n, failed, err)
		}
		got, _ := s.Get(t.Context(), []string{d.ID}, d.ID)
		if got.EmbedModel != "" || got.EmbedTokens != 0 || !slices.Contains(got.Notes, EmbedNote) {
			t.Errorf("%s: after a failed re-embed: %q, %d tokens, %v", name, got.EmbedModel, got.EmbedTokens, got.Notes)
		}
		a := model.NewFake(nil)
		a.Vectors = vecA
		s.UseEmbedder(a, "model-a") // the operator rolls back
		if n, failed, err := s.Backfill(t.Context()); n != 1 || failed != 0 || err != nil {
			t.Fatalf("%s: backfill under A again: %d, %d failed, %v", name, n, failed, err)
		}
		var foreign int
		if err := s.db.QueryRow(`SELECT count(*) FROM chunks WHERE document_id = $1 AND embedding::text <> '[1,0]'`, d.ID).Scan(&foreign); err != nil || foreign != 0 {
			t.Errorf("%s: %d chunks searched as model A's hold another vector (%v)", name, foreign, err)
		}
	}
}

// TestLexicalOnlyNotBuried (r1 reproducer M1): in a scope where another document is embedded, a
// lexical-only document's chunk (embedding failed, or a model change not yet backfilled) is ranked
// by its lexical rank alone, so the only chunk naming "Zebracorn Holdings" stays first instead of
// falling out of the top 8 behind embedded chunks that share one common word; embedded documents
// still gain from the vector leg. A query over MaxTerms words is refused before it is embedded.
func TestLexicalOnlyNotBuried(t *testing.T) {
	t.Parallel()
	s := newService(t) // no embedder: old.txt is stored lexical only
	old := ingestText(t, s, "old.txt", "Zebracorn Holdings indemnity cap is ten million.")
	f := model.NewFake(nil)
	f.Vectors = func(text string) ([]float32, error) { return []float32{1, float32(len(text) % 7)}, nil }
	s.UseEmbedder(f, "test-embed")
	var b strings.Builder
	for i := range 30 {
		fmt.Fprintf(&b, "%d. Clause %d\n\nThe indemnity under clause %d is limited.\n\n", i+1, i+1, i)
	}
	fresh := ingestText(t, s, "new.txt", b.String())
	if old.EmbedModel != "" || fresh.EmbedModel != "test-embed" {
		t.Fatalf("setup: %q %q", old.EmbedModel, fresh.EmbedModel)
	}
	hits, err := s.Search(t.Context(), []string{old.ID, fresh.ID}, "Zebracorn Holdings indemnity cap", 8, 0)
	if err != nil || len(hits) != 8 || hits[0].DocumentID != old.ID {
		t.Fatalf("the lexical-only exact match is not first: %v %v", hitIDs(hits), err)
	}
	// An embedded chunk the lexical leg misses entirely is still found by its vector.
	if hits, err := s.Search(t.Context(), []string{old.ID, fresh.ID}, "absentword", 8, 0); err != nil || len(hits) != 8 || hits[0].DocumentID != fresh.ID {
		t.Fatalf("vector leg: %v %v", hitIDs(hits), err)
	}
	long := ""
	for i := range MaxTerms + 1 {
		long += fmt.Sprintf("w%d ", i)
	}
	calls := len(f.EmbedCalls())
	if _, err := s.Search(t.Context(), []string{fresh.ID}, long, 8, 0); !errors.Is(err, ErrBadInput) || len(f.EmbedCalls()) != calls {
		t.Errorf("33 words: %v after %d query embeddings, want ErrBadInput before any", err, len(f.EmbedCalls())-calls)
	}
}

// TestEmbedBatchesAndUsage: chunks are embedded embedBatch at a time, each call's text is the
// chunk's section path and search text, and the document records the calls' tokens and cost
// (unknown, nil, when any call is unpriced).
func TestEmbedBatchesAndUsage(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := range 150 {
		fmt.Fprintf(&b, "%d. Clause %d\n\nThe parties agree to term %d. %s\n\n", i+1, i+1, i, strings.Repeat("Filler words follow here. ", 60))
	}
	s, f := hybridService(t, axes)
	d := ingestText(t, s, "long.txt", b.String())
	calls := f.EmbedCalls()
	n := 0
	for _, c := range calls {
		if len(c.Inputs) > embedBatch || c.Agent != "ingest" {
			t.Fatalf("a call of %d inputs by %q", len(c.Inputs), c.Agent)
		}
		n += len(c.Inputs)
	}
	if n != d.Chunks || len(calls) != (d.Chunks+embedBatch-1)/embedBatch || d.Chunks <= embedBatch {
		t.Fatalf("%d chunks, %d inputs in %d calls", d.Chunks, n, len(calls))
	}
	if first := calls[0].Inputs[0]; !strings.HasPrefix(first, "1 Clause 1") && !strings.Contains(first, "The parties agree to term 0") {
		t.Errorf("first input %.80q: want the section path and the chunk text", first)
	}
	if d.EmbedCostUSD != nil || d.EmbedTokens != 0 { // model.Fake reports no usage and no price
		t.Errorf("fake usage recorded as %d tokens, cost %v", d.EmbedTokens, d.EmbedCostUSD)
	}
}

// TestEmbedWaitsBounded: a hung embeddings endpoint (a real model.Client on an httptest server)
// holds an ingest's embedding at most embedWait (the document is then stored lexical only, with
// EmbedNote) and a search at most queryWait (then the typed timeout, 504).
func TestEmbedWaitsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // read first, so the server sees the client go
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, err := model.New(model.Options{URL: srv.URL + "/v1", Roles: model.Roles{Embed: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	oldEmbed, oldQuery := embedWait, queryWait
	embedWait, queryWait = 200*time.Millisecond, 200*time.Millisecond
	defer func() { embedWait, queryWait = oldEmbed, oldQuery }()
	s := newService(t)
	s.UseEmbedder(c, "m")
	start := time.Now()
	d, err := s.IngestText(t.Context(), "hung.txt", "The Supplier shall deliver.")
	if err != nil || !slices.Contains(d.Notes, EmbedNote) || d.EmbedModel != "" || time.Since(start) > 10*time.Second {
		t.Fatalf("ingest against a hung endpoint: %v, %+v after %s", err, d, time.Since(start))
	}
	// The backfill bounds each document the same way (r1 L-5): it notes the failure and goes on.
	start = time.Now()
	if n, failed, err := s.Backfill(t.Context()); n != 0 || failed != 1 || err != nil || time.Since(start) > 10*time.Second {
		t.Fatalf("backfill against a hung endpoint: %d, %d failed, %v after %s", n, failed, err, time.Since(start))
	}
	if _, err := s.db.Exec(`UPDATE documents SET embed_model = 'm' WHERE id = $1`, d.ID); err != nil { // so search embeds the query
		t.Fatal(err)
	}
	start = time.Now()
	if _, err := s.Search(t.Context(), []string{d.ID}, "supplier", 8, 0); !errors.Is(err, model.ErrTimeout) || model.HTTPStatus(err) != 504 || time.Since(start) > 10*time.Second {
		t.Fatalf("search against a hung endpoint: %v after %s", err, time.Since(start))
	}
}

// TestLexicalOnlyDisplacementMeasured (PR 3b r2 verifier L2, measured here for README Limitations):
// a lexical-only chunk scores 2/(60 + lexical rank), its best case, so a lexical-only document whose
// chunks all share a query word outranks an embedded chunk that is the vector leg's first but
// lexically weak. With 8 and 20 such chunks the embedded match is #5 and #10 (off a page of 8);
// once the backfill embeds them it is #1 again.
func TestLexicalOnlyDisplacementMeasured(t *testing.T) {
	t.Parallel()
	for n, want := range map[int]int{8: 5, 20: 10} {
		s := newService(t)
		var l, e strings.Builder
		for i := range n {
			fmt.Fprintf(&l, "%d. Indemnity %d\n\nThe indemnity covers loss %d and the indemnity is uncapped.\n\n", i+1, i+1, i)
		}
		lexical := ingestText(t, s, "l.txt", l.String())
		f := model.NewFake(nil)
		f.Vectors = func(text string) ([]float32, error) { // l.txt is semantically far from the query, e.txt's filler nearer
			if strings.Contains(text, "uncapped") {
				return []float32{-1, 0.1}, nil
			}
			return axes(text)
		}
		s.UseEmbedder(f, "test-embed")
		for i := range 20 {
			fmt.Fprintf(&e, "%d. Filler %d\n\nThe parties agree to term %d of the schedule.\n\n", i+1, i+1, i)
		}
		e.WriteString("21. Schedule\n\nThe stripes indemnity schedule applies.\n")
		embedded := ingestText(t, s, "e.txt", e.String())
		position := func() int {
			hits, err := s.Search(t.Context(), []string{lexical.ID, embedded.ID}, "zebra indemnity", 50, 0)
			for i, h := range hits {
				if strings.Contains(h.Text, "stripes") {
					return i + 1
				}
			}
			t.Fatalf("no match: %v", err)
			return 0
		}
		if got := position(); got != want {
			t.Errorf("%d lexical-only chunks: the embedded match is #%d, want #%d (README Limitations)", n, got, want)
		}
		if k, err := s.LexicalOnly(t.Context()); k != 1 || err != nil {
			t.Errorf("LexicalOnly before the backfill: %d %v, want 1", k, err)
		}
		if done, _, err := s.Backfill(t.Context()); done != 1 || err != nil {
			t.Fatalf("backfill: %d %v", done, err)
		}
		if k, err := s.LexicalOnly(t.Context()); k != 0 || err != nil {
			t.Errorf("LexicalOnly after the backfill: %d %v, want 0", k, err)
		}
		if got := position(); got != 1 {
			t.Errorf("all embedded (%d chunks): #%d, want #1", n, got)
		}
		t.Logf("%d lexical-only chunks sharing a query word: the vector leg's first match is #%d; all embedded: #1", n, want)
	}
}
