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

// TestProbeSectionQuery shows, for the cherry-picking SECTION-TITLE query (what the
// writer actually passes to extract_specifics), at what RANK each required figure
// surfaces — to explain why top_k=6 misses the rate/count/account rows. RAG_LIVE=1.
func TestProbeSectionQuery(t *testing.T) {
	if os.Getenv("RAG_LIVE") == "" {
		t.Skip("RAG_LIVE=1 + BENCH_DB + LOCAL_INFERENCE_* to run")
	}
	db, err := sql.Open("sqlite", os.Getenv("BENCH_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := New(NewMemStore(), embeddings.NewClient(config.Load()), nil)
	rows, _ := db.Query("select id,title,content from documents")
	for rows.Next() {
		var id, ti, ct string
		rows.Scan(&id, &ti, &ct)
		svc.IngestDoc(id, ti, ct)
	}
	rows.Close()

	facts := []string{"7,800,000", "438,000", "81.6", "7823", "4,217", "4,312", "73", "312"}
	for _, q := range []string{"Cherry-Picking Trade Allocations", "Cherry-Picking Trade Allocations Cherry-Picking Trade Allocations"} {
		hits := svc.Search(q, 30) // figure rows only
		var fig []Chunk
		for _, h := range hits {
			for _, r := range h.Text {
				if r >= '0' && r <= '9' {
					fig = append(fig, h)
					break
				}
			}
		}
		t.Logf("QUERY %q -> %d figure-rows in top-30", q, len(fig))
		for _, f := range facts {
			rank := -1
			for i, h := range fig {
				if strings.Contains(h.Text, f) {
					rank = i + 1
					break
				}
			}
			where := "NOT in top-30"
			if rank > 0 {
				where = "figure-rank " + itoa(rank)
				if rank <= 6 {
					where += " (within top_k=6 ✓)"
				} else {
					where += " (BEYOND top_k=6 ✗)"
				}
			}
			t.Logf("   %-10s %s", f, where)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
