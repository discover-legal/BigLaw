// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/queue"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// fakeData is a static DataProvider for tests.
type fakeData struct{ in ReportInput }

func (f fakeData) ActiveMatters() []MatterRef {
	return []MatterRef{{MatterNumber: f.in.MatterNumber, ClientNumber: f.in.ClientNumber}}
}
func (f fakeData) TasksForMatter(string) []types.Task             { return f.in.Tasks }
func (f fakeData) TimeEntriesForMatter(string) []types.TimeEntry  { return f.in.TimeEntries }
func (f fakeData) HealthForMatter(string) types.MatterHealthScore { return f.in.Health }
func (f fakeData) BudgetForMatter(string) *types.BudgetBurn       { return f.in.Budget }

func newTestService(t *testing.T, prov *fakeProvider) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.LPMConfig{
		DailyHour:  6,
		Formats:    []string{"json", "docx", "markdown"},
		CorpusFile: filepath.Join(dir, "corpus.jsonl"),
		ReportDir:  filepath.Join(dir, "reports"),
	}
	gen := NewGenerator(prov, "m")
	corpus := NewCorpus(cfg.CorpusFile)
	q := queue.New(filepath.Join(dir, "jobs.json"))
	if err := q.Init(); err != nil {
		t.Fatal(err)
	}
	svc := NewService(cfg, gen, corpus, fakeData{in: sampleInput()}, q, nil)
	return svc, dir
}

func TestServiceGenerateForMatterWritesArtifactsAndCorpus(t *testing.T) {
	prov := &fakeProvider{replies: []string{
		`{"bluf":"b","summary":"s","confidence":0.8}`,
		`{"grounded":true,"confidence":0.85}`,
	}}
	svc, dir := newTestService(t, prov)

	rep, err := svc.GenerateForMatter(MatterRef{MatterNumber: "M-001", ClientNumber: "C-100"}, "2026-06-07")
	if err != nil {
		t.Fatalf("GenerateForMatter: %v", err)
	}
	if rep.MatterNumber != "M-001" {
		t.Errorf("matter not set: %q", rep.MatterNumber)
	}

	// Corpus has the report.
	got, _ := svc.Corpus().Query("M-001", "", "")
	if len(got) != 1 {
		t.Fatalf("corpus should have 1 report, got %d", len(got))
	}

	// All three artifacts written.
	base := filepath.Join(dir, "reports", "M-001", "2026-06-07")
	for _, ext := range []string{".json", ".docx", ".md"} {
		if _, err := os.Stat(base + ext); err != nil {
			t.Errorf("expected artifact %s: %v", base+ext, err)
		}
	}
}

func TestServiceNotifierInvoked(t *testing.T) {
	prov := &fakeProvider{replies: []string{`{"bluf":"b","summary":"s","confidence":0.8}`}}
	svc, _ := newTestService(t, prov)

	var gotMatter, gotDocx string
	svc.notify = func(matter string, _ *types.MatterStatusReport, docxPath string) {
		gotMatter = matter
		gotDocx = docxPath
	}
	if _, err := svc.GenerateForMatter(MatterRef{MatterNumber: "M-001"}, "2026-06-07"); err != nil {
		t.Fatal(err)
	}
	if gotMatter != "M-001" {
		t.Errorf("notifier matter: %q", gotMatter)
	}
	if filepath.Ext(gotDocx) != ".docx" {
		t.Errorf("notifier should receive docx path, got %q", gotDocx)
	}
}

func TestReportChannelPostGuardBlocksLeak(t *testing.T) {
	prov := &fakeProvider{replies: []string{`{"bluf":"SSN 123-45-6789 in the note","summary":"s"}`}}
	svc, _ := newTestService(t, prov)
	svc.cfg.ChannelPost = true
	got := make(chan Draft, 1)
	svc.WithDrafting("off", nil, nil, func(d Draft) error { got <- d; return nil })

	if _, err := svc.GenerateForMatter(MatterRef{MatterNumber: "M-001"}, "2026-06-07"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
		t.Error("a BLUF containing PII must not be posted to the channel")
	case <-time.After(200 * time.Millisecond):
		// good — guard blocked the post
	}
}

func TestReportChannelPostCleanPosts(t *testing.T) {
	prov := &fakeProvider{replies: []string{`{"bluf":"All on track this week.","summary":"s"}`}}
	svc, _ := newTestService(t, prov)
	svc.cfg.ChannelPost = true
	got := make(chan Draft, 1)
	svc.WithDrafting("off", nil, nil, func(d Draft) error { got <- d; return nil })

	if _, err := svc.GenerateForMatter(MatterRef{MatterNumber: "M-001"}, "2026-06-07"); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		if d.MatterNumber != "M-001" {
			t.Errorf("posted wrong matter: %s", d.MatterNumber)
		}
	case <-time.After(2 * time.Second):
		t.Error("a clean BLUF should be posted to the channel")
	}
}

func TestEnqueueDailyEnqueuesPerMatter(t *testing.T) {
	prov := &fakeProvider{replies: []string{`{"bluf":"b","summary":"s"}`}}
	svc, _ := newTestService(t, prov)

	svc.enqueueDaily(time.Now())
	stats := svc.queue.Stats()
	if stats.Pending != 1 {
		t.Errorf("want 1 pending job after daily sweep, got %d", stats.Pending)
	}

	// The worker can drain that job.
	job := svc.queue.Dequeue([]types.JobType{types.JobTypeLPMStatusReport})
	if job == nil {
		t.Fatal("expected a dequeued LPM job")
	}
	svc.processJob(job)
	if svc.queue.Stats().Done != 1 {
		t.Errorf("job should be acked done, stats: %+v", svc.queue.Stats())
	}
	if reports, _ := svc.Corpus().Query("M-001", "", ""); len(reports) != 1 {
		t.Errorf("worker should have produced 1 report, got %d", len(reports))
	}
	// The queue persists asynchronously (go q.persist()); let writes settle before
	// t.TempDir cleanup runs, otherwise RemoveAll races the persist goroutine.
	time.Sleep(100 * time.Millisecond)
}
