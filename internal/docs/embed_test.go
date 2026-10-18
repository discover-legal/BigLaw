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
	if n := len(f.EmbedCalls()) - calls; n > 1 { // only the near-miss uuid is well-formed enough to embed for
		t.Errorf("%d query embeddings for scopes with no valid document", n)
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
	s, _ := hybridService(t, func(text string) ([]float32, error) {
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
	// Found lexically, ranked by the lexical leg alone (1/61), never by its unembedded vectors.
	ok := ingestText(t, s, "ok.txt", "Zebra herds graze.")
	hits, err := s.Search(t.Context(), []string{d.ID, ok.ID}, "supplier warrants", 8, 0)
	if err != nil || len(hits) != 2 || hits[0].DocumentID != d.ID || hits[0].Rank != 1.0/61 {
		t.Fatalf("lexical-only document in a hybrid search: %v %v", hits, err)
	}
	// The query's own embedding failing is an error, typed for the API (502).
	fail.Store(true)
	if _, err := s.Search(t.Context(), []string{d.ID}, "supplier", 8, 0); !errors.Is(err, model.ErrUnavailable) || model.HTTPStatus(err) != 502 {
		t.Fatalf("failed query embedding: %v", err)
	}
	// The backfill embeds it later and removes the note.
	fail.Store(false)
	if n, err := s.Backfill(t.Context()); err != nil || n != 1 {
		t.Fatalf("backfill: %d, %v", n, err)
	}
	if got, _ := s.Get(t.Context(), []string{d.ID}, d.ID); got.EmbedModel != "test-embed" || slices.Contains(got.Notes, EmbedNote) {
		t.Errorf("after backfill: %q %v", got.EmbedModel, got.Notes)
	}
}

// TestBackfillEmbedsOldDocuments: documents stored before the embed role was set, or embedded by
// another model, are embedded by Backfill one at a time; documents stored after it starts are left
// to their own ingest; it stops after 3 failures in a row, and a second run has nothing to do.
func TestBackfillEmbedsOldDocuments(t *testing.T) {
	t.Parallel()
	s := newService(t) // no embedder: lexical only
	var ids []string
	for i := range 5 {
		ids = append(ids, ingestText(t, s, fmt.Sprintf("old-%d.txt", i), fmt.Sprintf("Clause %d. Zebra stripes, item %d.", i, i)).ID)
	}
	empty := ingestText(t, s, "empty.txt", " \n")
	if n, err := s.Backfill(t.Context()); n != 0 || err != nil {
		t.Fatalf("backfill without an embedder: %d %v", n, err)
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
	if n, err := s.Backfill(t.Context()); n != 0 || err == nil || !strings.Contains(err.Error(), "3 documents in a row") || len(failing.EmbedCalls()) != 3 {
		t.Fatalf("failing backfill: %d, %v, %d calls", n, err, len(failing.EmbedCalls()))
	}
	f := model.NewFake(nil)
	f.Vectors = axes
	s.UseEmbedder(f, "new-model")
	if n, err := s.Backfill(t.Context()); n != 4 || err != nil {
		t.Fatalf("backfill: %d documents, %v; want 4 (ids 0-3: one of another model, three never embedded)", n, err)
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
	if n, err := s.Backfill(t.Context()); n != 0 || err != nil {
		t.Errorf("second backfill: %d %v", n, err)
	}
	// A cancelled backfill stops at once.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.db.Exec(`UPDATE documents SET embed_model = NULL`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if n, err := s.Backfill(ctx); n != 0 || err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("cancelled backfill: %d %v", n, err)
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
	start = time.Now()
	if _, err := s.Search(t.Context(), []string{d.ID}, "supplier", 8, 0); !errors.Is(err, model.ErrTimeout) || model.HTTPStatus(err) != 504 || time.Since(start) > 10*time.Second {
		t.Fatalf("search against a hung endpoint: %v after %s", err, time.Since(start))
	}
}
