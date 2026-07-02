// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// DocketMonitor — watches CourtListener for new filings on registered dockets.
// Auto-ingests alerts into the knowledge store and fires alert callbacks.

package dockets

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	courtListenerAPI  = "https://www.courtlistener.com/api/rest/v4"
	requestTimeoutMs  = 30_000
	maxResponseBytes  = 1 * 1024 * 1024
	cooldownPerDocket = time.Hour
)

var docketNumberRE = regexp.MustCompile(`^[\w\-:\./]+$`)
var courtSlugRE = regexp.MustCompile(`^[a-z0-9]+$`)

// KnowledgeIngester can ingest a document into the knowledge store.
type KnowledgeIngester interface {
	IngestDoc(title, content, source, docType string) error
}

// AlertHandler is called when new filings are detected.
type AlertHandler func(alert types.DocketAlert)

// Monitor polls CourtListener for new filings on watched dockets.
type Monitor struct {
	mu         sync.Mutex
	persistMu  sync.Mutex                      // serialises concurrent fire-and-forget persists
	watched    map[string]*types.WatchedDocket // key: matterNumber
	path       string
	knowledge  KnowledgeIngester
	onAlert    AlertHandler
	ticker     *time.Ticker
	stop       chan struct{}
	writeChain chan struct{}
}

// New creates a DocketMonitor backed by path.
func New(path string) *Monitor {
	return &Monitor{
		watched:    make(map[string]*types.WatchedDocket),
		path:       path,
		writeChain: make(chan struct{}, 1),
	}
}

// SetKnowledgeIngester attaches a knowledge store for auto-ingesting alerts.
func (m *Monitor) SetKnowledgeIngester(ki KnowledgeIngester) {
	m.mu.Lock()
	m.knowledge = ki
	m.mu.Unlock()
}

// SetAlertHandler sets the callback for new-filing alerts.
func (m *Monitor) SetAlertHandler(h AlertHandler) {
	m.mu.Lock()
	m.onAlert = h
	m.mu.Unlock()
}

// AddAlertHandler registers an additional alert callback alongside any
// existing one — e.g. the firm monitor's channel poster plus the REST SSE
// broadcaster. Handlers run in registration order.
func (m *Monitor) AddAlertHandler(h AlertHandler) {
	m.mu.Lock()
	prev := m.onAlert
	if prev == nil {
		m.onAlert = h
	} else {
		m.onAlert = func(a types.DocketAlert) { prev(a); h(a) }
	}
	m.mu.Unlock()
}

// Init loads persisted watched dockets.
func (m *Monitor) Init() error {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var entries []*types.WatchedDocket
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	m.mu.Lock()
	for _, e := range entries {
		m.watched[e.MatterNumber] = e
	}
	m.mu.Unlock()
	slog.Info("DocketMonitor: loaded watched dockets", "count", len(m.watched))
	return nil
}

// Start begins the background polling loop with the given interval.
func (m *Monitor) Start(interval time.Duration) {
	m.mu.Lock()
	if m.ticker != nil {
		m.mu.Unlock()
		return
	}
	m.ticker = time.NewTicker(interval)
	m.stop = make(chan struct{})
	m.mu.Unlock()

	go func() {
		for {
			select {
			case <-m.ticker.C:
				m.CheckAll()
			case <-m.stop:
				return
			}
		}
	}()
	slog.Info("DocketMonitor: started", "interval", interval)
}

// Stop halts the background polling loop.
func (m *Monitor) Stop() {
	m.mu.Lock()
	if m.ticker != nil {
		m.ticker.Stop()
		close(m.stop)
		m.ticker = nil
	}
	m.mu.Unlock()
	slog.Info("DocketMonitor: stopped")
}

// Watch registers a docket for monitoring.
func (m *Monitor) Watch(matterNumber, docketNumber, court, caseName string) (*types.WatchedDocket, error) {
	if matterNumber == "" {
		return nil, fmt.Errorf("matterNumber is required")
	}
	if !docketNumberRE.MatchString(docketNumber) || len(docketNumber) > 50 {
		return nil, fmt.Errorf("invalid docketNumber: must match [\\w\\-:\\./ ]+, max 50 chars")
	}
	if !courtSlugRE.MatchString(court) || len(court) > 20 {
		return nil, fmt.Errorf("invalid court: must be lowercase alphanumeric CourtListener slug, max 20 chars")
	}

	entry := &types.WatchedDocket{
		MatterNumber: matterNumber,
		DocketNumber: docketNumber,
		Court:        court,
		CaseName:     caseName,
		AddedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	m.mu.Lock()
	m.watched[matterNumber] = entry
	m.mu.Unlock()
	go m.persist()
	slog.Info("DocketMonitor: watching docket", "matterNumber", matterNumber, "docketNumber", docketNumber)
	return entry, nil
}

// Unwatch removes a docket from monitoring.
func (m *Monitor) Unwatch(matterNumber string) bool {
	m.mu.Lock()
	_, had := m.watched[matterNumber]
	delete(m.watched, matterNumber)
	m.mu.Unlock()
	if had {
		go m.persist()
		slog.Info("DocketMonitor: unwatched docket", "matterNumber", matterNumber)
	}
	return had
}

// List returns all watched dockets.
func (m *Monitor) List() []types.WatchedDocket {
	m.mu.Lock()
	out := make([]types.WatchedDocket, 0, len(m.watched))
	for _, w := range m.watched {
		out = append(out, *w)
	}
	m.mu.Unlock()
	return out
}

// CheckAll checks every watched docket.
func (m *Monitor) CheckAll() {
	m.mu.Lock()
	entries := make([]*types.WatchedDocket, 0, len(m.watched))
	for _, w := range m.watched {
		entries = append(entries, w)
	}
	m.mu.Unlock()

	if len(entries) == 0 {
		return
	}
	slog.Info("DocketMonitor: checking all dockets", "count", len(entries))

	var wg sync.WaitGroup
	for _, w := range entries {
		wg.Add(1)
		go func(w *types.WatchedDocket) {
			defer wg.Done()
			if _, err := m.CheckDocket(w); err != nil {
				slog.Warn("DocketMonitor: error checking docket", "matterNumber", w.MatterNumber, "error", err)
			}
		}(w)
	}
	wg.Wait()
}

type clDocket struct {
	ID                 int    `json:"id"`
	CaseName           string `json:"case_name"`
	DateFiled          string `json:"date_filed"`
	DateLastFiling     string `json:"date_last_filing"`
	DocketEntriesCount int    `json:"docket_entries_count"`
}

type clResponse struct {
	Results []clDocket `json:"results"`
}

// CheckDocket checks a single docket against CourtListener.
func (m *Monitor) CheckDocket(w *types.WatchedDocket) (*types.DocketAlert, error) {
	apiKey := os.Getenv("COURT_LISTENER_API_KEY")

	rawURL := fmt.Sprintf("%s/dockets/?docket_number=%s&court=%s&fields=id,case_name,date_filed,date_last_filing,docket_entries_count",
		courtListenerAPI,
		url.QueryEscape(w.DocketNumber),
		w.Court)

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Token "+apiKey)
	}

	client := &http.Client{
		Timeout: time.Duration(requestTimeoutMs) * time.Millisecond,
		// SSRF redirect-bounce defense: never follow redirects (TS used
		// redirect: "manual"). A 3xx surfaces below as a non-200 error.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CourtListener fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("CourtListener returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeded 1 MB cap")
	}

	var data clResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	w.LastCheckedAt = now
	m.mu.Unlock()

	if len(data.Results) == 0 {
		go m.persist()
		return nil, nil
	}

	docket := data.Results[0]
	latestFilingDate := docket.DateLastFiling
	if latestFilingDate == "" {
		latestFilingDate = docket.DateFiled
	}
	totalEntries := docket.DocketEntriesCount
	caseName := docket.CaseName
	if caseName == "" {
		caseName = w.CaseName
	}
	if caseName == "" {
		caseName = w.DocketNumber
	}

	m.mu.Lock()
	hasNewDate := latestFilingDate != "" && (w.LastFilingDate == "" || latestFilingDate > w.LastFilingDate)
	hasMoreEntries := totalEntries > w.TotalFilingsSeen
	m.mu.Unlock()

	if !hasNewDate && !hasMoreEntries {
		go m.persist()
		return nil, nil
	}

	m.mu.Lock()
	newCount := 1
	if hasMoreEntries {
		newCount = totalEntries - w.TotalFilingsSeen
	}
	if latestFilingDate != "" {
		w.LastFilingDate = latestFilingDate
	}
	w.TotalFilingsSeen = totalEntries
	if w.CaseName == "" && docket.CaseName != "" {
		w.CaseName = docket.CaseName
	}
	m.mu.Unlock()
	go m.persist()

	if latestFilingDate == "" {
		latestFilingDate = now
	}
	courtListenerURL := fmt.Sprintf("https://www.courtlistener.com/docket/%d/", docket.ID)

	alert := types.DocketAlert{
		ID:               uuid.New().String(),
		MatterNumber:     w.MatterNumber,
		DocketNumber:     w.DocketNumber,
		Court:            w.Court,
		CaseName:         caseName,
		NewFilingCount:   newCount,
		LatestFilingDate: latestFilingDate,
		CourtListenerURL: courtListenerURL,
		DetectedAt:       now,
	}

	slog.Info("DocketMonitor: new filings detected", "matterNumber", w.MatterNumber, "newFilingCount", newCount)

	// Auto-ingest
	m.mu.Lock()
	ki := m.knowledge
	handler := m.onAlert
	m.mu.Unlock()

	if ki != nil {
		content := fmt.Sprintf("Case: %s\nDocket: %s\nCourt: %s\nNew filings detected: %d\nLatest filing date: %s\nCourtListener URL: %s\nMatter number: %s\nDetected at: %s",
			caseName, w.DocketNumber, w.Court, newCount, latestFilingDate, courtListenerURL, w.MatterNumber, now)
		title := fmt.Sprintf("Docket Alert: %s (%s) — %d new filing(s)", caseName, w.DocketNumber, newCount)
		if err := ki.IngestDoc(title, content, courtListenerURL, "docket_alert"); err != nil {
			slog.Warn("DocketMonitor: failed to auto-ingest", "error", err)
		}
	}

	if handler != nil {
		handler(alert)
	}

	return &alert, nil
}

func (m *Monitor) persist() {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.mu.Lock()
	entries := make([]*types.WatchedDocket, 0, len(m.watched))
	for _, w := range m.watched {
		entries = append(entries, w)
	}
	m.mu.Unlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, m.path)
}
