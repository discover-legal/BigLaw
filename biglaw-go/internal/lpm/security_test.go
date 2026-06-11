// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSafeDateRejectsTraversal(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	cases := map[string]string{
		"2026-06-07":      "2026-06-07", // valid passes through
		"":                today,
		"../../etc":       today,
		"2026-06-07/../x": today,
		"2026/06/07":      today,
		"not-a-date":      today,
	}
	for in, want := range cases {
		if got := safeDate(in); got != want {
			t.Errorf("safeDate(%q) = %q, want %q", in, got, want)
		}
	}
}

// A malicious date must not let artifact writes escape the report directory.
func TestGenerateForMatterDateCannotTraverse(t *testing.T) {
	prov := &fakeProvider{replies: []string{`{"bluf":"b","summary":"s"}`}}
	svc, dir := newTestService(t, prov)

	if _, err := svc.GenerateForMatter(MatterRef{MatterNumber: "M-001"}, "../../escape"); err != nil {
		t.Fatal(err)
	}
	// Nothing should be written outside the configured report dir.
	parent := filepath.Dir(dir)
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if strings.Contains(e.Name(), "escape") {
			t.Fatalf("artifact escaped report dir: %s", e.Name())
		}
	}
	// The report should have landed under the report dir with a safe (today) date.
	reports, _ := svc.Corpus().Query("M-001", "", "")
	if len(reports) != 1 || !dateRE.MatchString(reports[0].Date) {
		t.Fatalf("expected one report with a sanitised date, got %+v", reports)
	}
}
