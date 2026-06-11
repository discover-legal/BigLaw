// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// RegPulseMonitor — watches for new rules/rulings affecting open matters.
// Tavily search + Haiku relevance gate. Optional — disabled when TAVILY_API_KEY unset.

package regulatory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	tavilyURL         = "https://api.tavily.com/search"
	maxTavilyResults  = 5
	maxResponseBytes  = 2 * 1024 * 1024
	requestTimeoutMs  = 30_000
	cooldownPerMatter = time.Hour
)

// AlertHandler is called when a regulation alert is detected.
type AlertHandler func(alert types.RegulationAlert)

// Monitor watches for new regulations affecting open matters.
type Monitor struct {
	mu          sync.Mutex
	lastChecked map[string]time.Time // matter/task key → last check time
	provider    providers.Provider
	haiku       string
	tavilyKey   string
	onAlert     AlertHandler
	ticker      *time.Ticker
	stop        chan struct{}
}

// New creates a RegPulseMonitor.
func New(provider providers.Provider, haikuModel string) *Monitor {
	return &Monitor{
		lastChecked: make(map[string]time.Time),
		provider:    provider,
		haiku:       haikuModel,
		tavilyKey:   os.Getenv("TAVILY_API_KEY"),
	}
}

// SetAlertHandler sets the callback for regulation alerts.
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
		m.onAlert = func(a types.RegulationAlert) { prev(a); h(a) }
	}
	m.mu.Unlock()
}

// IsEnabled returns true when TAVILY_API_KEY is set.
func (m *Monitor) IsEnabled() bool {
	return m.tavilyKey != ""
}

// Start begins background polling with the given interval.
// getTasks is called on each tick to get open tasks.
func (m *Monitor) Start(interval time.Duration, getTasks func() []types.Task) {
	m.mu.Lock()
	if m.ticker != nil {
		m.mu.Unlock()
		return
	}
	m.ticker = time.NewTicker(interval)
	m.stop = make(chan struct{})
	m.mu.Unlock()

	go func() {
		// Run immediately on start
		m.CheckAll(getTasks())
		for {
			select {
			case <-m.ticker.C:
				m.CheckAll(getTasks())
			case <-m.stop:
				return
			}
		}
	}()
	slog.Info("RegPulseMonitor: started", "interval", interval)
}

// Stop halts background polling.
func (m *Monitor) Stop() {
	m.mu.Lock()
	if m.ticker != nil {
		m.ticker.Stop()
		close(m.stop)
		m.ticker = nil
	}
	m.mu.Unlock()
}

// CheckAll checks all open tasks for new regulations.
func (m *Monitor) CheckAll(tasks []types.Task) []types.RegulationAlert {
	var allAlerts []types.RegulationAlert
	for _, t := range tasks {
		if t.Status != "running" && t.Status != "pending" {
			continue
		}
		alerts, err := m.CheckMatter(t)
		if err != nil {
			slog.Warn("RegPulseMonitor: error checking matter", "taskId", t.ID, "error", err)
			continue
		}
		allAlerts = append(allAlerts, alerts...)
	}
	return allAlerts
}

// CheckMatter checks a single task for new regulations.
func (m *Monitor) CheckMatter(task types.Task) ([]types.RegulationAlert, error) {
	if task.NosLegal == nil || task.NosLegal.AreaOfLaw == nil || *task.NosLegal.AreaOfLaw == "" {
		return nil, nil
	}
	if task.Jurisdiction == "" {
		return nil, nil
	}
	practiceArea := *task.NosLegal.AreaOfLaw
	jurisdiction := task.Jurisdiction

	cooldownKey := task.MatterNumber
	if cooldownKey == "" {
		cooldownKey = task.ID
	}

	m.mu.Lock()
	lastCheck := m.lastChecked[cooldownKey]
	if time.Since(lastCheck) < cooldownPerMatter {
		m.mu.Unlock()
		return nil, nil
	}
	m.lastChecked[cooldownKey] = time.Now()
	m.mu.Unlock()

	query := m.BuildQuery(practiceArea, jurisdiction)
	results, err := m.searchTavily(query)
	if err != nil || len(results) == 0 {
		return nil, err
	}

	alerts, err := m.filterRelevant(results, practiceArea, jurisdiction, task.MatterNumber)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	handler := m.onAlert
	m.mu.Unlock()

	for _, a := range alerts {
		if handler != nil {
			handler(a)
		}
	}

	if len(alerts) > 0 {
		slog.Info("RegPulseMonitor: emitted regulation alerts", "taskId", task.ID, "count", len(alerts))
	}
	return alerts, nil
}

// BuildQuery constructs a Tavily search query.
func (m *Monitor) BuildQuery(practiceArea, jurisdiction string) string {
	year := time.Now().Year()
	return fmt.Sprintf(`new regulation OR ruling OR guidance %q %q %d`, practiceArea, jurisdiction, year)
}

type tavilyResult struct {
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

type tavilyResponse struct {
	Results []tavilyResult `json:"results"`
}

func (m *Monitor) searchTavily(query string) ([]tavilyResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"api_key":        m.tavilyKey,
		"query":          query,
		"search_depth":   "basic",
		"max_results":    maxTavilyResults,
		"include_answer": false,
	})
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: time.Duration(requestTimeoutMs) * time.Millisecond}
	resp, err := client.Post(tavilyURL, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("RegPulseMonitor: Tavily request failed", "error", err)
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		slog.Warn("RegPulseMonitor: Tavily non-200", "status", resp.StatusCode)
		return nil, nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxResponseBytes {
		slog.Warn("RegPulseMonitor: Tavily response exceeded 2 MB cap")
		return nil, nil
	}

	var data tavilyResponse
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, nil
	}
	return data.Results, nil
}

func (m *Monitor) filterRelevant(results []tavilyResult, practiceArea, jurisdiction, matterNumber string) ([]types.RegulationAlert, error) {
	var alerts []types.RegulationAlert

	for _, result := range results {
		safeTitle := sanitize(result.Title)
		if len(safeTitle) > 200 {
			safeTitle = strutil.Truncate(safeTitle, 200)
		}
		safeContent := sanitize(result.Content)
		if len(safeContent) > 800 {
			safeContent = strutil.Truncate(safeContent, 800)
		}

		systemPrompt := `You are a legal relevance filter. Reply with JSON only: {"relevant": true/false, "reason": "..."}.`
		userPrompt := fmt.Sprintf("Is this legal news/ruling/regulation materially relevant to a matter involving %q law in %q?\n\nResult: %s\n%s",
			practiceArea, jurisdiction, safeTitle, safeContent)

		start := time.Now()
		resp, err := m.provider.Chat(providers.ChatParams{
			Model:     m.haiku,
			MaxTokens: 200,
			System:    systemPrompt,
			Messages:  []providers.Message{{Role: "user", Content: userPrompt}},
		})
		if err != nil {
			slog.Warn("RegPulseMonitor: Haiku relevance call failed", "error", err)
			continue
		}
		dms := time.Since(start).Milliseconds()
		costUSD := cost.CalcCostUSD(m.haiku, resp.Usage.InputTokens, resp.Usage.OutputTokens, 0, 0)
		cost.Default.Record(cost.RecordRequest{
			Model: m.haiku, Provider: "anthropic",
			InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			CostUSD: costUSD, DurationMs: dms,
			Context: "classification",
		})

		raw := ""
		for _, blk := range resp.Content {
			if blk.Type == providers.BlockText {
				raw = blk.Text
				break
			}
		}
		raw = strings.ReplaceAll(raw, "```json", "")
		raw = strings.ReplaceAll(raw, "```", "")
		raw = strings.TrimSpace(raw)
		s := strings.Index(raw, "{")
		e := strings.LastIndex(raw, "}")
		if s < 0 || e <= s {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(raw[s:e+1]), &parsed); err != nil {
			continue
		}
		relevant, _ := parsed["relevant"].(bool)
		if !relevant {
			continue
		}
		reason, _ := parsed["reason"].(string)
		if reason == "" {
			reason = "Relevant regulatory development detected."
		}

		alerts = append(alerts, types.RegulationAlert{
			ID:           uuid.New().String(),
			MatterNumber: matterNumber,
			PracticeArea: practiceArea,
			Jurisdiction: jurisdiction,
			Headline:     safeTitle,
			URL:          result.URL,
			Summary:      reason,
			DetectedAt:   time.Now().UTC().Format(time.RFC3339),
			Source:       "tavily",
		})
	}
	return alerts, nil
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, "FINDING:", "[FINDING:]")
	s = strings.ReplaceAll(s, "END_FINDING", "[END_FINDING]")
	s = strings.ReplaceAll(s, "NO_FINDINGS", "[NO_FINDINGS]")
	s = strings.ReplaceAll(s, "NO_CHALLENGE", "[NO_CHALLENGE]")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
