// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Service wires the daily status-report spine together: a once-a-day scheduler
// enqueues one durable job per active matter; a background worker drains the
// queue, generates each report, appends it to the corpus, and renders the
// configured artifacts (JSON/Markdown/DOCX). The queue makes the sweep
// restart-safe and lets it run at low priority on cheap compute.
package lpm

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/queue"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// MatterRef identifies an active matter the spine should report on.
type MatterRef struct {
	MatterNumber string
	ClientNumber string
}

// DataProvider supplies the live state the generator needs. The concrete
// implementation lives in the wiring layer (main) and adapts the real stores;
// keeping it an interface here keeps the package decoupled and unit-testable.
type DataProvider interface {
	ActiveMatters() []MatterRef
	TasksForMatter(matter string) []types.Task
	TimeEntriesForMatter(matter string) []types.TimeEntry
	HealthForMatter(matter string) types.MatterHealthScore
}

// Notifier, when set, is called after each report is produced (e.g. to post a
// summary + DOCX path to the matter's Teams/Slack channel). May be nil.
type Notifier func(matter string, report *types.MatterStatusReport, docxPath string)

// Service is the LPM daily status-report engine.
type Service struct {
	cfg    config.LPMConfig
	gen    *Generator
	corpus *Corpus
	data   DataProvider
	queue  *queue.Queue
	notify Notifier

	sched     *Scheduler
	stop      chan struct{}
	pollEvery time.Duration

	// Optional Phase 2 email intake; nil disables routing-aware deltas.
	intake *Intake
	routed *RoutedStore

	// Optional outbound drafting (email-write-mode). Defaults to "off".
	draftMode      string
	allowedDomains []string
	transport      MailTransport
	channelPoster  ChannelPoster
}

// WithDrafting configures the outbound drafter. A fresh confidentiality guard is
// built per request from the live matter roster, so cross-matter detection always
// reflects the current set of matters.
func (s *Service) WithDrafting(mode string, allowedDomains []string, transport MailTransport, channel ChannelPoster) *Service {
	s.draftMode = mode
	s.allowedDomains = allowedDomains
	s.transport = transport
	s.channelPoster = channel
	return s
}

func (s *Service) knownMatters() []string {
	refs := s.data.ActiveMatters()
	out := make([]string, 0, len(refs))
	for _, m := range refs {
		out = append(out, m.MatterNumber)
	}
	return out
}

func (s *Service) drafter() *Drafter {
	g := NewGuard(GuardConfig{AllowedDomains: s.allowedDomains, KnownMatterNumbers: s.knownMatters()})
	return NewDrafter(s.draftMode, g, s.transport, s.channelPoster)
}

// ProcessDraft applies the configured email-write-mode policy to a draft.
func (s *Service) ProcessDraft(d Draft, actorID string) (DraftOutcome, error) {
	return s.drafter().Process(d, actorID)
}

// ApproveSend sends a draft after explicit human approval (re-running the guard).
func (s *Service) ApproveSend(d Draft, approverID string) (DraftOutcome, error) {
	return s.drafter().ApproveSend(d, approverID)
}

// WithEmailIntake attaches the email router/intake so daily reports include the
// EmailsRouted delta and the poll loop runs alongside the scheduler.
func (s *Service) WithEmailIntake(intake *Intake, routed *RoutedStore) *Service {
	s.intake = intake
	s.routed = routed
	return s
}

// NewService builds the LPM service. queue may be shared with other subsystems.
func NewService(cfg config.LPMConfig, gen *Generator, corpus *Corpus, data DataProvider, q *queue.Queue, notify Notifier) *Service {
	return &Service{
		cfg:       cfg,
		gen:       gen,
		corpus:    corpus,
		data:      data,
		queue:     q,
		notify:    notify,
		stop:      make(chan struct{}),
		pollEvery: 2 * time.Second,
	}
}

// Corpus exposes the underlying report corpus (for REST reads).
func (s *Service) Corpus() *Corpus { return s.corpus }

// Start launches the daily scheduler and the queue worker.
func (s *Service) Start() {
	s.sched = NewScheduler(s.cfg.DailyHour, s.enqueueDaily)
	s.sched.Start()
	go s.worker()
	if s.intake != nil {
		s.intake.Start()
	}
	slog.Info("LPM service started",
		"dailyHour", s.cfg.DailyHour, "formats", s.cfg.Formats,
		"emailWriteMode", s.cfg.EmailWriteMode, "intakeMode", s.cfg.IntakeMode,
		"intake", s.intake != nil)
}

// Stop halts the scheduler and worker.
func (s *Service) Stop() {
	if s.sched != nil {
		s.sched.Stop()
	}
	if s.intake != nil {
		s.intake.Stop()
	}
	close(s.stop)
}

// enqueueDaily is the scheduler callback: enqueue one status-report job per
// active matter so the worker processes them durably and at low priority.
func (s *Service) enqueueDaily(now time.Time) {
	matters := s.data.ActiveMatters()
	for _, m := range matters {
		s.queue.Enqueue(types.JobTypeLPMStatusReport, map[string]interface{}{
			"matterNumber": m.MatterNumber,
			"clientNumber": m.ClientNumber,
			"date":         now.UTC().Format("2006-01-02"),
		}, s.cfg.PollIntervalM /*unused as retries; kept for parity*/)
	}
	slog.Info("LPM daily sweep enqueued", "matters", len(matters))
}

// worker drains LPM status-report jobs from the shared queue.
func (s *Service) worker() {
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		job := s.queue.Dequeue([]types.JobType{types.JobTypeLPMStatusReport})
		if job == nil {
			select {
			case <-s.stop:
				return
			case <-time.After(s.pollEvery):
			}
			continue
		}
		s.processJob(job)
	}
}

func (s *Service) processJob(job *types.Job) {
	ref := MatterRef{
		MatterNumber: stringField(job.Payload, "matterNumber"),
		ClientNumber: stringField(job.Payload, "clientNumber"),
	}
	if ref.MatterNumber == "" {
		s.queue.Fail(job.ID, "missing matterNumber in payload")
		return
	}
	if _, err := s.GenerateForMatter(ref, stringField(job.Payload, "date")); err != nil {
		s.queue.Fail(job.ID, err.Error())
		return
	}
	s.queue.Ack(job.ID)
}

// GenerateForMatter produces, persists and renders a report for one matter. It
// is used both by the worker and by the on-demand REST/command path.
func (s *Service) GenerateForMatter(ref MatterRef, date string) (*types.MatterStatusReport, error) {
	prev, _ := s.corpus.Latest(ref.MatterNumber)
	in := ReportInput{
		MatterNumber: ref.MatterNumber,
		ClientNumber: ref.ClientNumber,
		Date:         date,
		Health:       s.data.HealthForMatter(ref.MatterNumber),
		Tasks:        s.data.TasksForMatter(ref.MatterNumber),
		TimeEntries:  s.data.TimeEntriesForMatter(ref.MatterNumber),
		Prev:         prev,
		EmailsRouted: s.emailsRoutedSince(ref.MatterNumber, prev),
	}
	report, err := s.gen.Generate(in, GenOpts{Verify: true})
	if err != nil {
		return nil, err
	}
	if err := s.corpus.Append(report); err != nil {
		return nil, fmt.Errorf("append corpus: %w", err)
	}

	docxPath, err := s.writeArtifacts(report)
	if err != nil {
		slog.Warn("LPM artifact write failed", "matter", ref.MatterNumber, "error", err)
	}
	if s.notify != nil {
		s.notify(ref.MatterNumber, report, docxPath)
	}
	return report, nil
}

// writeArtifacts renders the configured formats to ReportDir/<matter>/<date>.*.
// Returns the DOCX path (if written) for the notifier.
func (s *Service) writeArtifacts(r *types.MatterStatusReport) (string, error) {
	dir := filepath.Join(s.cfg.ReportDir, safeName(r.MatterNumber))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(dir, r.Date)
	docxPath := ""
	for _, f := range s.cfg.Formats {
		switch f {
		case "json":
			b, err := RenderJSON(r)
			if err != nil {
				return docxPath, err
			}
			if err := os.WriteFile(base+".json", b, 0o644); err != nil {
				return docxPath, err
			}
		case "markdown", "md":
			if err := os.WriteFile(base+".md", []byte(RenderMarkdown(r)), 0o644); err != nil {
				return docxPath, err
			}
		case "docx":
			b, err := RenderDOCX(r)
			if err != nil {
				return docxPath, err
			}
			docxPath = base + ".docx"
			if err := os.WriteFile(docxPath, b, 0o644); err != nil {
				return "", err
			}
		}
	}
	return docxPath, nil
}

// emailsRoutedSince counts emails routed to the matter since the previous
// report (or the trailing 24h when there is none).
func (s *Service) emailsRoutedSince(matter string, prev *types.MatterStatusReport) int {
	if s.routed == nil {
		return 0
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	if prev != nil {
		if t, err := time.Parse(time.RFC3339, prev.GeneratedAt); err == nil {
			cutoff = t
		}
	}
	return s.routed.CountForMatter(matter, cutoff)
}

func stringField(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// safeName makes a matter number safe to use as a path segment.
func safeName(s string) string {
	if s == "" {
		return "_unassigned"
	}
	repl := func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ':
			return '_'
		}
		return r
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		out = append(out, repl(r))
	}
	return string(out)
}
