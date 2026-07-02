// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/redtime"
	"github.com/discover-legal/biglaw-go/internal/store"
)

// newTimelineRouter wires only the timeline route onto a minimal Server —
// the handler touches nothing beyond the version repository and config.
func newTimelineRouter(repo store.ReviewRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{}, reviews: repo}
	r := gin.New()
	s.registerRedtimeRoutes(r)
	return r
}

func TestDocumentTimelineEndpoint(t *testing.T) {
	ctx := context.Background()
	repo := store.NewMemoryRepo()

	v1, err := redtime.RegisterVersion(ctx, repo, redtime.RegisterOpts{
		Text:   "1. LIMITATION OF LIABILITY\nThe liability cap is twelve (12) months of fees.",
		Source: "ours", Author: "Big Michael",
	})
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}
	v2, err := redtime.RegisterVersion(ctx, repo, redtime.RegisterOpts{
		Text:   "1. LIMITATION OF LIABILITY\nThe liability cap is thirty-six (36) months of fees.",
		Source: "theirs", ParentID: v1.ID,
	})
	if err != nil {
		t.Fatalf("register v2: %v", err)
	}

	router := newTimelineRouter(repo)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/documents/"+v1.LineageID+"/timeline", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var tl redtime.Timeline
	if err := json.Unmarshal(w.Body.Bytes(), &tl); err != nil {
		t.Fatalf("body is not a Timeline: %v", err)
	}
	if tl.LineageID != v1.LineageID || tl.Rounds != 2 || len(tl.Versions) != 2 {
		t.Errorf("timeline = %+v, want the 2-round lineage", tl)
	}
	if len(tl.Clauses) != 1 || len(tl.Clauses[0].Events) != 1 {
		t.Fatalf("clauses = %+v, want one clause with one event", tl.Clauses)
	}
	ev := tl.Clauses[0].Events[0]
	if ev.Round != 2 || ev.Actor != "theirs" || ev.Kind != "substitution" || ev.ViaTrackedChange {
		t.Errorf("event = %+v, want a round-2 silent substitution by theirs", ev)
	}
	// Provider-less server → drift unknown.
	if tl.Clauses[0].Drift == nil || tl.Clauses[0].Drift.Status != redtime.DriftUnknown {
		t.Errorf("drift = %+v, want unknown", tl.Clauses[0].Drift)
	}

	// Forgiving: a VERSION id resolves to its lineage.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/documents/"+v2.ID+"/timeline", nil))
	if w.Code != http.StatusOK {
		t.Errorf("by version id: status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}

	// Unknown id → JSON 404.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/documents/no-such-lineage/timeline", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown id: status = %d, want 404", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf("unknown id should return a JSON error body, got %q", w.Body.String())
	}
}

// TestDocumentTimelineUnavailable: a store without version support answers
// 503, not a panic.
func TestDocumentTimelineUnavailable(t *testing.T) {
	router := newTimelineRouter(nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/documents/x/timeline", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}
