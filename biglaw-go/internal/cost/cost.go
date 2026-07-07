// SPDX-License-Identifier: Apache-2.0
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

// basePricing is the exact-match rate table: model ID → [input, output] USD per
// million tokens. These are public list prices as of early 2026 and are a
// starting point, not gospel — override any of them to your negotiated/contract
// rate with the COST_<FAMILY>_IN/_OUT env vars (see modelClasses below).
//
// Rates we can't stand behind are left at {0, 0} (recorded as $0.00, i.e.
// "recognised but unpriced") rather than fabricated into a billing ledger —
// set them explicitly via the env overrides. This currently applies to the
// gpt-5.x flagship tier and to host-variable open-weight models (Llama, Kimi,
// GLM), whose price depends entirely on which host serves them.
var basePricing = map[string][2]float64{
	// Anthropic
	"claude-haiku-4-5-20251001":  {1.00, 5.00},
	"claude-haiku-4-5":           {1.00, 5.00},
	"claude-sonnet-4-6":          {3.00, 15.00},
	"claude-opus-4-8":            {15.00, 75.00},
	"claude-opus-4-5":            {15.00, 75.00},
	"claude-3-5-haiku-20241022":  {1.00, 5.00},
	"claude-3-5-sonnet-20241022": {3.00, 15.00},
	"claude-3-haiku-20240307":    {0.25, 1.25},
	"claude-3-opus-20240229":     {15.00, 75.00},

	// OpenAI — chat (routed via the OPENAI_MODEL shortcut or LOCAL_INFERENCE_*).
	// gpt-5.5 has no public list price we can cite, so it stays untracked until
	// COST_GPT_IN/_OUT (or a specific entry) is set.
	"gpt-5.5":      {0, 0},
	"gpt-5":        {1.25, 10.00},
	"gpt-5-mini":   {0.25, 2.00},
	"gpt-5-nano":   {0.05, 0.40},
	"gpt-4.1":      {2.00, 8.00},
	"gpt-4.1-mini": {0.40, 1.60},
	"gpt-4.1-nano": {0.10, 0.40},
	"gpt-4o":       {2.50, 10.00},
	"gpt-4o-mini":  {0.15, 0.60},
	"o3":           {2.00, 8.00},
	"o3-mini":      {1.10, 4.40},
	"o4-mini":      {1.10, 4.40},

	// OpenAI — embeddings (input-only; output rate is 0).
	"text-embedding-3-small": {0.02, 0},
	"text-embedding-3-large": {0.13, 0},

	// Google Gemini
	"gemini-2.5-pro":        {1.25, 10.00},
	"gemini-2.5-flash":      {0.30, 2.50},
	"gemini-2.5-flash-lite": {0.10, 0.40},
	"gemini-2.0-flash":      {0.10, 0.40},
	"gemini-2.0-flash-lite": {0.075, 0.30},
	"gemini-1.5-pro":        {1.25, 5.00},
	"gemini-1.5-flash":      {0.075, 0.30},

	// DeepSeek (first-party API; cache-miss input rate)
	"deepseek-chat":     {0.27, 1.10},
	"deepseek-reasoner": {0.55, 2.19},

	// xAI Grok
	"grok-4":      {3.00, 15.00},
	"grok-3":      {3.00, 15.00},
	"grok-3-mini": {0.30, 0.50},
	"grok-2":      {2.00, 10.00},

	// Alibaba Qwen (DashScope international list prices)
	"qwen-max":      {1.60, 6.40},
	"qwen-plus":     {0.40, 1.20},
	"qwen-turbo":    {0.05, 0.20},
	"qwen-vl-max":   {1.60, 6.40},
	"qwen-vl-plus":  {0.21, 0.63},

	// Mistral
	"mistral-large-latest": {2.00, 6.00},
	"mistral-large":        {2.00, 6.00},
	"mistral-medium":       {0.40, 2.00},
	"mistral-small-latest": {0.10, 0.30},
	"mistral-small":        {0.10, 0.30},
	"open-mistral-nemo":    {0.15, 0.15},

	// Cohere
	"command-a":      {2.50, 10.00},
	"command-r-plus": {2.50, 10.00},
	"command-r":      {0.15, 0.60},

	// Amazon Nova
	"nova-pro":   {0.80, 3.20},
	"nova-lite":  {0.06, 0.24},
	"nova-micro": {0.035, 0.14},

	// Moonshot (Kimi) and Zhipu (GLM) — host/plan-variable, untracked by default.
	"kimi-k2":          {0, 0},
	"moonshot-v1-128k": {0, 0},
	"glm-4.6":          {0, 0},
	"glm-4.5":          {0, 0},
}

// modelClass groups model IDs into a provider family for two jobs:
//
//  1. Fallback pricing — when an exact model ID isn't in basePricing (a version
//     suffix like "deepseek-v3.1" or "gemini-2.5-flash-preview-09"), the first
//     class whose match substring is contained in the (lowercased) ID supplies
//     the rate. Order matters: cheaper variants (…-mini/-nano/-lite/-turbo) are
//     listed before the broad family so a small model isn't priced as a
//     flagship.
//
//  2. Env overrides — COST_<FAMILY>_IN/_OUT (USD per million tokens) retargets
//     every model in the family at once, e.g. COST_DEEPSEEK_IN=0.27. The same
//     keys also override matching exact entries in basePricing. A family may
//     appear on multiple lines (different tiers); one COST_<FAMILY>_* pair
//     overrides them all.
//
// A class with a {0, 0} rate still counts as a recognised model (recorded as
// $0.00), so its spend appears in the ledger the moment a rate is configured.
type modelClass struct {
	family string     // COST_<FAMILY>_IN/_OUT selector (case-insensitive)
	match  []string   // lowercase model-ID substrings in this family
	rate   [2]float64 // default [input, output] USD per million tokens
}

var modelClasses = []modelClass{
	// OpenAI
	{"gpt", []string{"gpt-5-nano", "gpt-4.1-nano"}, [2]float64{0.05, 0.40}},
	{"gpt", []string{"gpt-5-mini", "gpt-4.1-mini", "gpt-4o-mini"}, [2]float64{0.25, 2.00}},
	{"gpt", []string{"gpt"}, [2]float64{0, 0}}, // gpt-5.x flagship: set COST_GPT_*
	{"embed", []string{"text-embedding"}, [2]float64{0.02, 0}},
	// Google Gemini ("flash-lite" before "flash" before the family)
	{"gemini", []string{"flash-lite"}, [2]float64{0.075, 0.30}},
	{"gemini", []string{"flash"}, [2]float64{0.30, 2.50}},
	{"gemini", []string{"gemini"}, [2]float64{1.25, 10.00}},
	// DeepSeek (reasoner before the base chat model)
	{"deepseek", []string{"deepseek-reason", "deepseek-r1"}, [2]float64{0.55, 2.19}},
	{"deepseek", []string{"deepseek"}, [2]float64{0.27, 1.10}},
	// xAI Grok
	{"grok", []string{"grok-3-mini", "grok-4-mini"}, [2]float64{0.30, 0.50}},
	{"grok", []string{"grok"}, [2]float64{3.00, 15.00}},
	// Alibaba Qwen
	{"qwen", []string{"qwen-turbo"}, [2]float64{0.05, 0.20}},
	{"qwen", []string{"qwen-plus"}, [2]float64{0.40, 1.20}},
	{"qwen", []string{"qwen"}, [2]float64{1.60, 6.40}},
	// Mistral (small/edge tier before the large tier)
	{"mistral", []string{"mistral-small", "ministral", "nemo"}, [2]float64{0.10, 0.30}},
	{"mistral", []string{"mistral", "mixtral", "codestral", "magistral", "pixtral"}, [2]float64{2.00, 6.00}},
	// Cohere
	{"cohere", []string{"command-r-plus", "command-a"}, [2]float64{2.50, 10.00}},
	{"cohere", []string{"command"}, [2]float64{0.15, 0.60}},
	// Amazon Nova
	{"nova", []string{"nova-micro"}, [2]float64{0.035, 0.14}},
	{"nova", []string{"nova-lite"}, [2]float64{0.06, 0.24}},
	{"nova", []string{"nova"}, [2]float64{0.80, 3.20}},
	// Host-variable open-weight / less-public families: recognised, untracked
	// until COST_<FAMILY>_* is set.
	{"kimi", []string{"kimi", "moonshot"}, [2]float64{0, 0}},
	{"glm", []string{"glm"}, [2]float64{0, 0}},
	{"llama", []string{"llama"}, [2]float64{0, 0}},
	// Anthropic (exact entries already cover the dated IDs; these catch drift)
	{"haiku", []string{"haiku"}, [2]float64{1.00, 5.00}},
	{"sonnet", []string{"sonnet"}, [2]float64{3.00, 15.00}},
	{"opus", []string{"opus"}, [2]float64{15.00, 75.00}},
}

func init() {
	applyPricingEnvOverrides(basePricing)
	applyClassPricingOverrides()
}

// familySubstrings returns each override family mapped to the union of its
// match substrings, derived from modelClasses so the family list never drifts
// from the price table.
func familySubstrings() map[string][]string {
	out := map[string][]string{}
	for _, c := range modelClasses {
		out[c.family] = append(out[c.family], c.match...)
	}
	return out
}

// applyPricingEnvOverrides applies the COST_<FAMILY>_IN/_OUT env vars to the
// given exact-match pricing table in place. An override hits every entry whose
// (lowercased) ID contains any of the family's match substrings. Unset,
// non-numeric, or negative values are ignored (the built-in rate stands).
func applyPricingEnvOverrides(pricing map[string][2]float64) {
	for family, subs := range familySubstrings() {
		in, inOK := parsePriceEnv("COST_" + strings.ToUpper(family) + "_IN")
		out, outOK := parsePriceEnv("COST_" + strings.ToUpper(family) + "_OUT")
		if !inOK && !outOK {
			continue
		}
		for model, p := range pricing {
			if !containsAny(strings.ToLower(model), subs) {
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

// applyClassPricingOverrides applies the COST_<FAMILY>_IN/_OUT env vars to the
// modelClasses fallback table in place, so an override reaches version-drift
// IDs that only resolve through the substring fallback.
func applyClassPricingOverrides() {
	for i := range modelClasses {
		fam := strings.ToUpper(modelClasses[i].family)
		if in, ok := parsePriceEnv("COST_" + fam + "_IN"); ok {
			modelClasses[i].rate[0] = in
		}
		if out, ok := parsePriceEnv("COST_" + fam + "_OUT"); ok {
			modelClasses[i].rate[1] = out
		}
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
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

// lookupPricing resolves a model ID to its [input, output] rate: an exact
// basePricing hit first, then the first modelClasses substring match. The bool
// is false only for a genuinely unrecognised model, which records no cost
// (nil) rather than a misleading $0.00.
func lookupPricing(model string) ([2]float64, bool) {
	if p, ok := basePricing[model]; ok {
		return p, true
	}
	lm := strings.ToLower(model)
	for _, c := range modelClasses {
		if containsAny(lm, c.match) {
			return c.rate, true
		}
	}
	return [2]float64{}, false
}

func CalcCostUSD(model string, input, output, cacheWrite, cacheRead int) *float64 {
	p, ok := lookupPricing(model)
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
	applyClassPricingOverrides()
	s.file = file
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// A missing ledger just means a fresh install — the write loop must
	// still start, or no cost entry ever reaches disk.
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
