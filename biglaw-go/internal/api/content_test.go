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

// newReviewCSVRouter wires only the review-CSV route onto a minimal Server —
// the handler touches nothing beyond the review repository.
func newReviewCSVRouter(repo store.ReviewRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{}, reviews: repo}
	r := gin.New()
	r.GET("/reviews/:id/table.csv", s.handleReviewTableCSV)
	return r
}

func TestReviewTableCSVEndpoint(t *testing.T) {
	repo := store.NewMemoryRepo()

	rec := tools.ReviewRecord{
		ReviewID:  "rev-api-1",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Columns:   []string{"Governing Law", "Term, Renewal"},
		Rows: []tools.ReviewRow{
			{
				DocumentID: "doc-a",
				Document:   "Master Services Agreement",
				Cells: []tools.ReviewCell{
					{Column: "Governing Law", Summary: "New York law", Flag: "green", Reasoning: "standard"},
					{Column: "Term, Renewal", Summary: `3-year term, "evergreen" renewal`, Flag: "yellow", Reasoning: "auto-renews"},
				},
			},
			{
				DocumentID: "doc-b",
				Document:   "", // falls back to the document ID
				Cells: []tools.ReviewCell{
					{Column: "Governing Law", Summary: "Not Found", Flag: "grey", Reasoning: "silent"},
				},
			},
		},
		FlagTally: map[string]int{"green": 1, "yellow": 1, "grey": 1},
		Legend:    map[string]string{"green": "ok", "grey": "n/a", "yellow": "review", "red": "bad"},
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PutReview(context.Background(), rec.ReviewID, time.Now(), payload); err != nil {
		t.Fatal(err)
	}

	router := newReviewCSVRouter(repo)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reviews/rev-api-1/table.csv", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "tabular-review-rev-api-1.csv") {
		t.Errorf("Content-Disposition = %q, want the review filename", cd)
	}

	lines := strings.Split(w.Body.String(), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("CSV has %d lines, want 3 (header + 2 rows): %q", len(lines), w.Body.String())
	}
	// Header row: Document + column names (comma-bearing name gets quoted).
	if lines[0] != `"Document","Governing Law","Term, Renewal"` {
		t.Errorf("header row = %q", lines[0])
	}
	// Cell text is "[flag] summary"; embedded quotes are doubled per RFC 4180.
	if !strings.Contains(lines[1], `"Master Services Agreement"`) ||
		!strings.Contains(lines[1], "[green] New York law") ||
		!strings.Contains(lines[1], `""evergreen""`) {
		t.Errorf("row 1 = %q", lines[1])
	}
	// A row with fewer cells than columns pads with empty cells; the missing
	// document title falls back to the document ID.
	if !strings.Contains(lines[2], `"doc-b"`) || !strings.Contains(lines[2], "[grey] Not Found") {
		t.Errorf("row 2 = %q", lines[2])
	}

	// Unknown id → JSON 404.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reviews/no-such-review/table.csv", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown id status = %d, want 404", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf("unknown id should return a JSON error body, got %q", w.Body.String())
	}
}
