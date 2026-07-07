// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package lpm implements the Legal Project Management subsystem: the daily
// per-matter status-report spine. Each run emits a structured MatterStatusReport
// rendered as machine-readable JSON (for downstream signal harvesting) and a
// human-facing DOCX/Markdown stakeholder update. Reports accumulate, one per
// matter per day, into an append-only corpus that becomes a mineable time-series
// over the life of a transaction — the data backbone the BLUF portfolio briefing
// and future insights feed on.
package lpm

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// Corpus is an append-only, newline-delimited JSON store of MatterStatusReports.
// One report per line; the file is the durable source of truth and reloads on
// restart. Reads scan the whole file — fine for the report volumes a single firm
// produces (one line per matter per day).
type Corpus struct {
	mu   sync.Mutex
	path string
}

// NewCorpus returns a Corpus backed by the JSONL file at path.
func NewCorpus(path string) *Corpus {
	return &Corpus{path: path}
}

// Append writes one report as a JSON line, creating the file/dir if needed.
func (c *Corpus) Append(r *types.MatterStatusReport) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// All returns every report in the corpus, oldest first. A missing file is not an
// error — it yields an empty slice. Malformed lines are skipped.
func (c *Corpus) All() ([]types.MatterStatusReport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readLocked()
}

func (c *Corpus) readLocked() ([]types.MatterStatusReport, error) {
	f, err := os.Open(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []types.MatterStatusReport
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var r types.MatterStatusReport
		if err := json.Unmarshal(b, &r); err != nil {
			continue // skip malformed lines rather than fail the whole read
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// Query returns reports for a matter (empty matter = all matters) whose Date
// falls within [from, to] (inclusive; empty bound = unbounded), most recent
// first. Dates are compared lexically on the YYYY-MM-DD Date field.
func (c *Corpus) Query(matter, from, to string) ([]types.MatterStatusReport, error) {
	all, err := c.All()
	if err != nil {
		return nil, err
	}
	var out []types.MatterStatusReport
	for _, r := range all {
		if matter != "" && r.MatterNumber != matter {
			continue
		}
		if from != "" && r.Date < from {
			continue
		}
		if to != "" && r.Date > to {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].GeneratedAt > out[j].GeneratedAt
	})
	return out, nil
}

// Latest returns the most recent report for a matter, or nil if none exists.
// Used to compute the delta-since-last-report.
func (c *Corpus) Latest(matter string) (*types.MatterStatusReport, error) {
	reports, err := c.Query(matter, "", "")
	if err != nil || len(reports) == 0 {
		return nil, err
	}
	r := reports[0]
	return &r, nil
}

// LatestByMatter returns the most recent report for every matter in a single
// pass over the corpus — used by the portfolio briefer to avoid an O(matters ×
// file) re-scan.
func (c *Corpus) LatestByMatter() (map[string]types.MatterStatusReport, error) {
	all, err := c.All()
	if err != nil {
		return nil, err
	}
	latest := make(map[string]types.MatterStatusReport, len(all))
	for _, r := range all {
		cur, ok := latest[r.MatterNumber]
		if !ok || r.Date > cur.Date || (r.Date == cur.Date && r.GeneratedAt > cur.GeneratedAt) {
			latest[r.MatterNumber] = r
		}
	}
	return latest, nil
}

// Get returns the report with the given ID, or nil if not found.
func (c *Corpus) Get(id string) (*types.MatterStatusReport, error) {
	all, err := c.All()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ReportID == id {
			return &all[i], nil
		}
	}
	return nil, nil
}
