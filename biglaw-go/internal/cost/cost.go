// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package cost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type CostContext string

const (
	ContextTask           CostContext = "task"
	ContextDescriptor     CostContext = "descriptor"
	ContextSynthesis      CostContext = "synthesis"
	ContextTabulate       CostContext = "tabulate"
	ContextRoundGoal      CostContext = "round_goal"
	ContextDebate         CostContext = "protocol_debate"
	ContextVerify         CostContext = "protocol_verify"
	ContextToneAnalysis   CostContext = "tone_analysis"
	ContextClassification CostContext = "classification"
	ContextEntrySummarize CostContext = "entry_summarize"
	ContextClientVoice    CostContext = "client_voice"
)

type CostEntry struct {
	ID               string      `json:"id"`
	TS               string      `json:"ts"`
	Model            string      `json:"model"`
	Provider         string      `json:"provider"`
	InputTokens      int         `json:"inputTokens"`
	OutputTokens     int         `json:"outputTokens"`
	CacheWriteTokens *int        `json:"cacheWriteTokens,omitempty"`
	CacheReadTokens  *int        `json:"cacheReadTokens,omitempty"`
	CostUSD          *float64    `json:"costUsd"`
	EstimatedWh      *float64    `json:"estimatedWh"`
	EstimatedWatts   *int        `json:"estimatedWatts"`
	DurationMs       int64       `json:"durationMs"`
	Context          CostContext `json:"context"`
	TaskID           string      `json:"taskId,omitempty"`
	ProfileID        string      `json:"profileId,omitempty"`
	AgentID          string      `json:"agentId,omitempty"`
}

type CostSummary struct {
	TotalUSD          float64                    `json:"totalUsd"`
	TotalInputTokens  int                        `json:"totalInputTokens"`
	TotalOutputTokens int                        `json:"totalOutputTokens"`
	TotalCacheWrite   int                        `json:"totalCacheWriteTokens"`
	TotalCacheRead    int                        `json:"totalCacheReadTokens"`
	TotalWh           float64                    `json:"totalWh"`
	ByModel           map[string]*ModelSummary   `json:"byModel"`
	ByContext         map[string]*ContextSummary `json:"byContext"`
	EntryCount        int                        `json:"entryCount"`
}

type ModelSummary struct {
	USD              float64 `json:"usd"`
	InputTokens      int     `json:"inputTokens"`
	OutputTokens     int     `json:"outputTokens"`
	CacheWriteTokens int     `json:"cacheWriteTokens"`
	CacheReadTokens  int     `json:"cacheReadTokens"`
	Wh               float64 `json:"wh"`
	Calls            int     `json:"calls"`
}

type ContextSummary struct {
	USD          float64 `json:"usd"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	Calls        int     `json:"calls"`
}

// USD per million tokens — Anthropic list pricing mid-2026.
var basePricing = map[string][2]float64{
	"claude-haiku-4-5-20251001":  {1.00, 5.00},
	"claude-haiku-4-5":           {1.00, 5.00},
	"claude-sonnet-4-6":          {3.00, 15.00},
	"claude-opus-4-8":            {15.00, 75.00},
	"claude-opus-4-5":            {15.00, 75.00},
	"claude-3-5-haiku-20241022":  {1.00, 5.00},
	"claude-3-5-sonnet-20241022": {3.00, 15.00},
	"claude-3-haiku-20240307":    {0.25, 1.25},
	"claude-3-opus-20240229":     {15.00, 75.00},
}

// pricingFamilies are the model families whose pricing can be overridden at
// startup via COST_<FAMILY>_IN / COST_<FAMILY>_OUT env vars (USD per million
// tokens), e.g. COST_HAIKU_IN=1.00 COST_HAIKU_OUT=5.00. An override applies
// to every model in basePricing whose ID contains the family substring.
var pricingFamilies = []string{"haiku", "sonnet", "opus"}

func init() {
	applyPricingEnvOverrides(basePricing)
}

// applyPricingEnvOverrides applies the COST_<FAMILY>_IN/_OUT env vars to the
// given pricing table in place. Unset, non-numeric, or negative values are
// ignored (the built-in rate stands).
func applyPricingEnvOverrides(pricing map[string][2]float64) {
	for _, family := range pricingFamilies {
		key := strings.ToUpper(family)
		in, inOK := parsePriceEnv("COST_" + key + "_IN")
		out, outOK := parsePriceEnv("COST_" + key + "_OUT")
		if !inOK && !outOK {
			continue
		}
		for model, p := range pricing {
			if !strings.Contains(model, family) {
				continue
			}
			if inOK {
				p[0] = in
			}
			if outOK {
				p[1] = out
			}
			pricing[model] = p
		}
	}
}

// parsePriceEnv reads an env var as a non-negative USD-per-million-tokens rate.
func parsePriceEnv(name string) (float64, bool) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return f, true
}

func CalcCostUSD(model string, input, output, cacheWrite, cacheRead int) *float64 {
	p, ok := basePricing[model]
	if !ok {
		return nil
	}
	cost := (float64(input)*p[0] +
		float64(output)*p[1] +
		float64(cacheWrite)*p[0]*1.25 +
		float64(cacheRead)*p[0]*0.10) / 1_000_000
	return &cost
}

func CalcWattHours(watts int, durationMs int64) float64 {
	return float64(watts) * float64(durationMs) / 3_600_000
}

type RecordRequest struct {
	Model            string
	Provider         string
	InputTokens      int
	OutputTokens     int
	CacheWriteTokens *int
	CacheReadTokens  *int
	CostUSD          *float64
	EstimatedWh      *float64
	EstimatedWatts   *int
	DurationMs       int64
	Context          CostContext
	TaskID           string
	ProfileID        string
	AgentID          string
}

type Store struct {
	mu      sync.Mutex
	entries []CostEntry
	file    string
	writeCh chan CostEntry

	stopCh    chan struct{} // signals writeLoop to drain and exit
	doneCh    chan struct{} // closed when writeLoop has exited
	started   bool          // writeLoop running (set by Init)
	closeOnce sync.Once
}

var Default = &Store{
	writeCh: make(chan CostEntry, 256),
	stopCh:  make(chan struct{}),
	doneCh:  make(chan struct{}),
}

func (s *Store) Init(file string) error {
	// Re-apply pricing overrides: init() ran before main loaded .env /
	// Infisical, so env vars sourced there are only visible now. Assigning
	// absolute rates is idempotent.
	applyPricingEnvOverrides(basePricing)
	s.file = file
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e CostEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			s.entries = append(s.entries, e)
		}
	}
	s.started = true
	go s.writeLoop()
	return nil
}

func (s *Store) writeLoop() {
	defer close(s.doneCh)
	for {
		select {
		case entry := <-s.writeCh:
			s.appendEntry(entry)
		case <-s.stopCh:
			// Drain whatever Record queued before exiting so a graceful
			// shutdown doesn't drop the tail of the cost ledger.
			for {
				select {
				case entry := <-s.writeCh:
					s.appendEntry(entry)
				default:
					return
				}
			}
		}
	}
}

func (s *Store) appendEntry(entry CostEntry) {
	raw, _ := json.Marshal(entry)
	f, err := os.OpenFile(s.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		fmt.Fprintln(f, string(raw))
		f.Close()
	}
}

// Close flushes queued cost entries to disk and stops the writer. Safe to
// call more than once; a no-op if Init never ran. Record stays safe to call
// afterwards (its channel send is non-blocking), but entries recorded after
// Close are only kept in memory.
func (s *Store) Close() {
	s.closeOnce.Do(func() {
		if !s.started {
			return
		}
		close(s.stopCh)
		<-s.doneCh
	})
}

func (s *Store) Record(req RecordRequest) {
	entry := CostEntry{
		ID:               uuid.New().String(),
		TS:               time.Now().UTC().Format(time.RFC3339Nano),
		Model:            req.Model,
		Provider:         req.Provider,
		InputTokens:      req.InputTokens,
		OutputTokens:     req.OutputTokens,
		CacheWriteTokens: req.CacheWriteTokens,
		CacheReadTokens:  req.CacheReadTokens,
		CostUSD:          req.CostUSD,
		EstimatedWh:      req.EstimatedWh,
		EstimatedWatts:   req.EstimatedWatts,
		DurationMs:       req.DurationMs,
		Context:          req.Context,
		TaskID:           req.TaskID,
		ProfileID:        req.ProfileID,
		AgentID:          req.AgentID,
	}
	s.mu.Lock()
	s.entries = append(s.entries, entry)
	s.mu.Unlock()

	select {
	case s.writeCh <- entry:
	default:
	}
}

func (s *Store) ForTask(taskID string) []CostEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []CostEntry
	for _, e := range s.entries {
		if e.TaskID == taskID {
			out = append(out, e)
		}
	}
	return out
}

func (s *Store) ForProfile(profileID string) []CostEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []CostEntry
	for _, e := range s.entries {
		if e.ProfileID == profileID {
			out = append(out, e)
		}
	}
	return out
}

func (s *Store) Summarise(entries []CostEntry) CostSummary {
	if entries == nil {
		s.mu.Lock()
		cp := make([]CostEntry, len(s.entries))
		copy(cp, s.entries)
		s.mu.Unlock()
		entries = cp
	}
	sum := CostSummary{
		ByModel:   map[string]*ModelSummary{},
		ByContext: map[string]*ContextSummary{},
	}
	for _, e := range entries {
		usd := 0.0
		if e.CostUSD != nil {
			usd = *e.CostUSD
		}
		wh := 0.0
		if e.EstimatedWh != nil {
			wh = *e.EstimatedWh
		}
		cw, cr := 0, 0
		if e.CacheWriteTokens != nil {
			cw = *e.CacheWriteTokens
		}
		if e.CacheReadTokens != nil {
			cr = *e.CacheReadTokens
		}

		sum.TotalUSD += usd
		sum.TotalInputTokens += e.InputTokens
		sum.TotalOutputTokens += e.OutputTokens
		sum.TotalCacheWrite += cw
		sum.TotalCacheRead += cr
		sum.TotalWh += wh
		sum.EntryCount++

		m := sum.ByModel[e.Model]
		if m == nil {
			m = &ModelSummary{}
			sum.ByModel[e.Model] = m
		}
		m.USD += usd
		m.InputTokens += e.InputTokens
		m.OutputTokens += e.OutputTokens
		m.CacheWriteTokens += cw
		m.CacheReadTokens += cr
		m.Wh += wh
		m.Calls++

		ctx := string(e.Context)
		c := sum.ByContext[ctx]
		if c == nil {
			c = &ContextSummary{}
			sum.ByContext[ctx] = c
		}
		c.USD += usd
		c.InputTokens += e.InputTokens
		c.OutputTokens += e.OutputTokens
		c.Calls++
	}
	return sum
}
