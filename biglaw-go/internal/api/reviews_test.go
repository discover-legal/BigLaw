// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/tools"
)

// newReviewJSONRouter wires only the review-JSON route onto a minimal Server —
// the handler touches nothing beyond the review repository.
func newReviewJSONRouter(repo store.ReviewRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{}, reviews: repo}
	r := gin.New()
	s.registerReviewRoutes(r)
	return r
}

func TestGetReviewEndpoint(t *testing.T) {
	repo := store.NewMemoryRepo()

	rec := tools.ReviewRecord{
		ReviewID:  "rev-json-1",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Columns:   []string{"Governing Law"},
		Rows: []tools.ReviewRow{
			{
				DocumentID: "doc-a",
				Document:   "Master Services Agreement",
				Cells: []tools.ReviewCell{
					{
						Column:    "Governing Law",
						Summary:   "New York law [[page:4||quote:governed by the laws of the State of New York]]",
						Flag:      "green",
						Reasoning: "standard",
						Citations: []tools.Citation{
							{Page: 4, Quote: "governed by the laws of the State of New York", Verified: true, Method: "exact_match", Confidence: 1.0},
						},
						CitationsVerified: 1,
						CitationsTotal:    1,
					},
				},
			},
		},
		FlagTally:     map[string]int{"green": 1, "grey": 0, "yellow": 0, "red": 0},
		CitationTally: &tools.CitationTally{Total: 1, Verified: 1, ByMethod: map[string]int{"exact_match": 1}},
		Legend:        map[string]string{"green": "ok", "grey": "n/a", "yellow": "review", "red": "bad"},
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PutReview(context.Background(), rec.ReviewID, time.Now(), payload); err != nil {
		t.Fatal(err)
	}

	router := newReviewJSONRouter(repo)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reviews/rev-json-1", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got tools.ReviewRecord
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a ReviewRecord: %v", err)
	}
	if got.ReviewID != "rev-json-1" || len(got.Columns) != 1 || len(got.Rows) != 1 {
		t.Errorf("record = %+v, want the 1×1 matrix", got)
	}
	cell := got.Rows[0].Cells[0]
	if cell.Flag != "green" || cell.CitationsVerified != 1 || len(cell.Citations) != 1 {
		t.Errorf("cell = %+v, want the verified green cell", cell)
	}
	if c := cell.Citations[0]; !c.Verified || c.Method != "exact_match" || c.Page != 4 {
		t.Errorf("citation = %+v, want verified exact_match on page 4", c)
	}
	if got.CitationTally == nil || got.CitationTally.Verified != 1 {
		t.Errorf("citationTally = %+v, want 1 verified", got.CitationTally)
	}

	// Unknown id → JSON 404.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reviews/no-such-review", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown id status = %d, want 404", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf("unknown id should return a JSON error body, got %q", w.Body.String())
	}
}
