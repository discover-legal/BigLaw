// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package rag

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
)

// TestLiveTableRetrieval ingests the white-collar task's actual documents (read
// from the bench SQLite store) into a fresh RAG and checks that the granular facts
// the rubric demands — which the OLD chunking missed — are now retrievable. Skipped
// unless RAG_LIVE=1; needs LOCAL_INFERENCE_* env (for nomic-embed) and SQLITE
// pointing at a populated bench.db.
//
//	RAG_LIVE=1 BENCH_DB=...\data-qwenfix\bench.db LOCAL_INFERENCE_* go test ./internal/rag -run TestLiveTableRetrieval -v
func TestLiveTableRetrieval(t *testing.T) {
	if os.Getenv("RAG_LIVE") == "" {
		t.Skip("set RAG_LIVE=1 (+ BENCH_DB, LOCAL_INFERENCE_*) to run live table-retrieval check")
	}
	dbPath := os.Getenv("BENCH_DB")
	if dbPath == "" {
		t.Fatal("BENCH_DB required")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("select id, title, content from documents")
	if err != nil {
		t.Fatal(err)
	}
	svc := New(NewMemStore(), embeddings.NewClient(config.Load()), nil) // nil gen → dense+BM25 only
	n := 0
	for rows.Next() {
		var id, title, content string
		if err := rows.Scan(&id, &title, &content); err != nil {
			t.Fatal(err)
		}
		svc.IngestDoc(id, title, content)
		n++
	}
	rows.Close()
	t.Logf("ingested %d docs", n)

	// Each: a query a drafter would plausibly issue, and the fact its top results must surface.
	cases := []struct{ query, fact string }{
		{"excess profits from cherry-picking allocated to Oceanic Fund", "7,800,000"},
		{"Chao personal account profitable allocation rate", "81.6"},
		{"Chao personal brokerage account number ending", "7823"},
		{"omnibus trades percentage of total equity volume", "73"},
		{"excess profits allocated to Chao personal account", "438"},
	}
	for _, c := range cases {
		hits := svc.Search(c.query, 8)
		found := false
		for _, h := range hits {
			if strings.Contains(h.Text, c.fact) || strings.Contains(h.EmbedText, c.fact) {
				found = true
				break
			}
		}
		status := "FOUND"
		if !found {
			status = "MISSING"
		}
		t.Logf("[%s] %q -> fact %q (top-%d)", status, c.query, c.fact, len(hits))
		if !found {
			t.Errorf("fact %q not retrievable for query %q", c.fact, c.query)
		}
	}
}
