// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func newReport(matter, date string) *types.MatterStatusReport {
	return &types.MatterStatusReport{
		ReportID:     date + "-" + matter,
		MatterNumber: matter,
		Date:         date,
		GeneratedAt:  date + "T06:00:00Z",
		BLUF:         "ok",
	}
}

func TestCorpusAppendAndQuery(t *testing.T) {
	c := NewCorpus(filepath.Join(t.TempDir(), "reports.jsonl"))

	// Empty corpus reads cleanly.
	if all, err := c.All(); err != nil || len(all) != 0 {
		t.Fatalf("empty corpus: got %d reports, err %v", len(all), err)
	}

	reports := []*types.MatterStatusReport{
		newReport("M-001", "2026-06-01"),
		newReport("M-002", "2026-06-01"),
		newReport("M-001", "2026-06-02"),
		newReport("M-001", "2026-06-03"),
	}
	for _, r := range reports {
		if err := c.Append(r); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// Query by matter, sorted most-recent first.
	got, err := c.Query("M-001", "", "")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("M-001 query: want 3, got %d", len(got))
	}
	if got[0].Date != "2026-06-03" {
		t.Errorf("expected newest first, got %s", got[0].Date)
	}

	// Date range filter.
	ranged, _ := c.Query("M-001", "2026-06-02", "2026-06-02")
	if len(ranged) != 1 || ranged[0].Date != "2026-06-02" {
		t.Errorf("date-range query failed: %+v", ranged)
	}

	// Latest for delta computation.
	latest, _ := c.Latest("M-001")
	if latest == nil || latest.Date != "2026-06-03" {
		t.Errorf("latest M-001: %+v", latest)
	}
	if none, _ := c.Latest("M-999"); none != nil {
		t.Errorf("latest of unknown matter should be nil, got %+v", none)
	}

	// Get by ID.
	one, _ := c.Get("2026-06-02-M-001")
	if one == nil || one.MatterNumber != "M-001" {
		t.Errorf("get by id failed: %+v", one)
	}
}

func TestCorpusSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports.jsonl")
	c := NewCorpus(path)
	if err := c.Append(newReport("M-001", "2026-06-01")); err != nil {
		t.Fatal(err)
	}
	// Corrupt the file with a junk line; reads should skip it, not fail.
	appendRaw(t, path, "this is not json\n")
	if err := c.Append(newReport("M-001", "2026-06-02")); err != nil {
		t.Fatal(err)
	}
	all, err := c.All()
	if err != nil {
		t.Fatalf("read with malformed line: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("want 2 valid reports, got %d", len(all))
	}
}
