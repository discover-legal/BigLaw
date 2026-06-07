// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// RedlineEngine — playbook-driven contract negotiation.
// Step 1 (Haiku): extract clause list from counterparty draft.
// Step 2 (Sonnet, batched): compare each clause against playbook cascade.
// Step 3 (Sonnet): generate executive summary for partner.

package redline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Types ────────────────────────────────────────────────────────────────────

type RedlineAction string

const (
	ActionAccept     RedlineAction = "accept"
	ActionRedline    RedlineAction = "redline"
	ActionEscalate   RedlineAction = "escalate"
	ActionDelete     RedlineAction = "delete"
	ActionNoPosition RedlineAction = "no_position"
)

type RedlineSeverity string

const (
	SeverityCritical RedlineSeverity = "critical"
	SeverityHigh     RedlineSeverity = "high"
	SeverityMedium   RedlineSeverity = "medium"
	SeverityLow      RedlineSeverity = "low"
)

// Issue is a per-clause redline decision.
type Issue struct {
	ClauseType       string          `json:"clauseType"`
	CounterpartyText string          `json:"counterpartyText"`
	FirmPosition     string          `json:"firmPosition"`
	PositionSource   string          `json:"positionSource"`
	Action           RedlineAction   `json:"action"`
	ProposedText     string          `json:"proposedText,omitempty"`
	Rationale        string          `json:"rationale"`
	IsRedLine        bool            `json:"isRedLine"`
	Severity         RedlineSeverity `json:"severity"`
}

// Report is the full redline review output.
type Report struct {
	ID               string  `json:"id"`
	DocumentID       string  `json:"documentId,omitempty"`
	DocumentTitle    string  `json:"documentTitle,omitempty"`
	PracticeArea     string  `json:"practiceArea,omitempty"`
	Jurisdiction     string  `json:"jurisdiction,omitempty"`
	TotalClauses     int     `json:"totalClauses"`
	AcceptCount      int     `json:"acceptCount"`
	RedlineCount     int     `json:"redlineCount"`
	EscalateCount    int     `json:"escalateCount"`
	DeleteCount      int     `json:"deleteCount"`
	CriticalCount    int     `json:"criticalCount"`
	Issues           []Issue `json:"issues"`
	ExecutiveSummary string  `json:"executiveSummary"`
	GeneratedAt      string  `json:"generatedAt"`
}

// RedlineOpts parameterises a redline run.
type RedlineOpts struct {
	PracticeArea  string
	Jurisdiction  string
	MatterNumber  string
	ClientID      string
	ProfileID     string
	DocumentID    string
	DocumentTitle string
	TaskID        string
}

// ─── Engine ───────────────────────────────────────────────────────────────────

// Engine generates playbook-driven redline reports.
type Engine struct {
	provider providers.Provider
	sonnet   string
	haiku    string
}

// New creates a RedlineEngine.
func New(provider providers.Provider, sonnetModel, haikuModel string) *Engine {
	return &Engine{provider: provider, sonnet: sonnetModel, haiku: haikuModel}
}

// Redline generates a redline report for a counterparty draft.
func (e *Engine) Redline(documentText string, store *playbook.Store, opts RedlineOpts) (*Report, error) {
	clauses, err := e.extractClauses(documentText, opts.PracticeArea, opts.TaskID)
	if err != nil || len(clauses) == 0 {
		return e.emptyReport(opts), nil
	}

	var issues []Issue
	const batchSize = 8
	for i := 0; i < len(clauses); i += batchSize {
		end := i + batchSize
		if end > len(clauses) {
			end = len(clauses)
		}
		batch := clauses[i:end]
		bIssues := e.analyseBatch(batch, store, opts)
		issues = append(issues, bIssues...)
	}

	summary := e.generateSummary(issues, opts)

	r := &Report{
		ID:            uuid.New().String(),
		DocumentID:    opts.DocumentID,
		DocumentTitle: opts.DocumentTitle,
		PracticeArea:  opts.PracticeArea,
		Jurisdiction:  opts.Jurisdiction,
		TotalClauses:  len(issues),
		Issues:        issues,
		ExecutiveSummary: summary,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	for _, iss := range issues {
		switch iss.Action {
		case ActionAccept:
			r.AcceptCount++
		case ActionRedline:
			r.RedlineCount++
		case ActionEscalate:
			r.EscalateCount++
		case ActionDelete:
			r.DeleteCount++
		}
		if iss.Severity == SeverityCritical {
			r.CriticalCount++
		}
	}

	slog.Info("Redline report generated", "id", r.ID, "clauses", r.TotalClauses, "redlines", r.RedlineCount, "criticals", r.CriticalCount)
	return r, nil
}

// ─── Step 1: clause extraction ────────────────────────────────────────────────

type rawClause struct {
	ClauseType string `json:"clauseType"`
	Text       string `json:"text"`
}

func (e *Engine) extractClauses(text, practiceArea, taskID string) ([]rawClause, error) {
	start := time.Now()
	area := practiceArea
	if area == "" {
		area = "transactional"
	}

	system := fmt.Sprintf(`You are a contract analysis assistant.
Extract every distinct legal clause from the contract text. For each clause:
- Identify its type (e.g. "MAC/MAE definition", "Indemnification cap", "Non-compete")
- Extract the full verbatim text of the clause

Return a JSON array:
[{"clauseType": "...", "text": "..."}]

Focus on %s clauses. Include up to 40 clauses. Skip boilerplate recitals.`, area)

	resp, err := e.provider.Chat(providers.ChatParams{
		Model:       e.haiku,
		MaxTokens:   3000,
		System:      system,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: "Contract text:\n" + truncate(text, 12000)}},
	})
	if err != nil {
		slog.Warn("RedlineEngine: clause extraction failed", "error", err)
		return nil, err
	}

	dms := time.Since(start).Milliseconds()
	recordCost(e.haiku, resp, dms, taskID)

	raw := textFrom(resp)
	s := strings.Index(raw, "[")
	eIdx := strings.LastIndex(raw, "]")
	if s < 0 || eIdx <= s {
		return nil, nil
	}
	var clauses []rawClause
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &clauses); err != nil {
		return nil, nil
	}
	return clauses, nil
}

// ─── Step 2: batch analysis ───────────────────────────────────────────────────

type posInfo struct {
	clauseType string
	entry      *types.PlaybookEntry
	source     string
}

func (e *Engine) analyseBatch(clauses []rawClause, store *playbook.Store, opts RedlineOpts) []Issue {
	start := time.Now()

	// Resolve playbook for each clause
	positions := make([]posInfo, len(clauses))
	for i, c := range clauses {
		resolved := store.Resolve(c.ClauseType, playbook.ResolveOpts{
			PracticeArea: opts.PracticeArea,
			MatterNumber: opts.MatterNumber,
			ClientID:     opts.ClientID,
			ProfileID:    opts.ProfileID,
		})
		if resolved != nil {
			entry := resolved.EffectiveEntry
			positions[i] = posInfo{
				clauseType: c.ClauseType,
				entry:      &entry,
				source:     string(resolved.ResolvedFrom),
			}
		} else {
			positions[i] = posInfo{clauseType: c.ClauseType, source: "none"}
		}
	}

	// Build analysis prompt
	var blocks []string
	for idx, c := range clauses {
		p := positions[idx]
		firmPos := "No playbook position — use judgment"
		fallback := "N/A"
		redLines := "None recorded"
		if p.entry != nil {
			firmPos = p.entry.StandardPosition
			if p.entry.FallbackPosition != "" {
				fallback = p.entry.FallbackPosition
			}
			if len(p.entry.RedLines) > 0 {
				redLines = strings.Join(p.entry.RedLines, "; ")
			}
		}
		blocks = append(blocks, fmt.Sprintf("--- CLAUSE %d: %s ---\nCOUNTERPARTY TEXT: %s\nFIRM POSITION (%s): %s\nFALLBACK: %s\nRED LINES: %s",
			idx+1, c.ClauseType, truncate(c.Text, 600), p.source, firmPos, fallback, redLines))
	}

	system := `You are a senior transactional lawyer reviewing a counterparty draft against your firm's playbook positions.

For each clause, determine:
- action: "accept", "redline", "escalate", "delete", or "no_position"
- severity: "critical", "high", "medium", or "low"
- proposedText: replacement language for "redline" action
- rationale: 1-2 sentences
- isRedLine: true if a firm red line is crossed

Return a JSON array — one object per clause in input order:
[{"clauseType":"...","action":"...","severity":"...","proposedText":"...","rationale":"...","isRedLine":false}]`

	resp, err := e.provider.Chat(providers.ChatParams{
		Model:       e.sonnet,
		MaxTokens:   4096,
		System:      system,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: strings.Join(blocks, "\n\n")}},
	})
	if err != nil {
		slog.Warn("RedlineEngine: batch analysis failed", "error", err)
		return fallbackIssues(clauses, positions)
	}

	dms := time.Since(start).Milliseconds()
	recordCost(e.sonnet, resp, dms, opts.TaskID)

	raw := textFrom(resp)
	s := strings.Index(raw, "[")
	eIdx := strings.LastIndex(raw, "]")
	if s < 0 || eIdx <= s {
		return fallbackIssues(clauses, positions)
	}
	var parsed []map[string]interface{}
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &parsed); err != nil {
		return fallbackIssues(clauses, positions)
	}

	issues := make([]Issue, 0, len(parsed))
	for idx, p := range parsed {
		clause := clauses[0]
		pos := positions[0]
		if idx < len(clauses) {
			clause = clauses[idx]
			pos = positions[idx]
		}
		firmPosition := "No position recorded"
		if pos.entry != nil {
			firmPosition = pos.entry.StandardPosition
		}
		proposedText := ""
		if v, ok := p["proposedText"].(string); ok {
			proposedText = v
		}
		issues = append(issues, Issue{
			ClauseType:       clause.ClauseType,
			CounterpartyText: truncate(clause.Text, 500),
			FirmPosition:     firmPosition,
			PositionSource:   pos.source,
			Action:           toAction(p["action"]),
			ProposedText:     proposedText,
			Rationale:        strVal(p["rationale"]),
			IsRedLine:        boolVal(p["isRedLine"]),
			Severity:         toSeverity(p["severity"]),
		})
	}
	return issues
}

// ─── Step 3: executive summary ────────────────────────────────────────────────

func (e *Engine) generateSummary(issues []Issue, opts RedlineOpts) string {
	start := time.Now()
	var criticals, redlines, accepts []Issue
	for _, iss := range issues {
		switch {
		case iss.Severity == SeverityCritical:
			criticals = append(criticals, iss)
		case iss.Action == ActionRedline:
			redlines = append(redlines, iss)
		case iss.Action == ActionAccept:
			accepts = append(accepts, iss)
		}
	}

	critTypes := make([]string, len(criticals))
	for i, c := range criticals {
		critTypes[i] = c.ClauseType
	}
	redLineTypes := []string{}
	for _, iss := range issues {
		if iss.IsRedLine {
			redLineTypes = append(redLineTypes, iss.ClauseType)
		}
	}
	keyRedlines := make([]string, 0, 5)
	for i, r := range redlines {
		if i >= 5 {
			break
		}
		keyRedlines = append(keyRedlines, r.ClauseType+": "+r.Rationale)
	}

	title := opts.DocumentTitle
	if title == "" {
		title = "counterparty draft"
	}
	area := opts.PracticeArea
	if area == "" {
		area = "transactional"
	}
	esc := 0
	for _, iss := range issues {
		if iss.Action == ActionEscalate {
			esc++
		}
	}

	prompt := fmt.Sprintf(`Write a 3-paragraph executive summary of a contract redline review.

Document: %s
Practice area: %s
Total clauses: %d
Accepted: %d | Redlined: %d | Escalate: %d
Critical issues (%d): %s

Red lines crossed: %s

Key redlines: %s

Write as if briefing a senior partner before a negotiation call. Concise, specific, commercial.`,
		title, area, len(issues), len(accepts), len(redlines), esc,
		len(criticals), joinOrNone(critTypes),
		joinOrNone(redLineTypes),
		joinOrNone(keyRedlines),
	)

	resp, err := e.provider.Chat(providers.ChatParams{
		Model:     e.sonnet,
		MaxTokens: 400,
		Messages:  []providers.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return fmt.Sprintf("Redline review complete. %d critical issue(s), %d clause(s) require amendment.", len(criticals), len(redlines))
	}
	recordCost(e.sonnet, resp, time.Since(start).Milliseconds(), opts.TaskID)
	return textFrom(resp)
}

func (e *Engine) emptyReport(opts RedlineOpts) *Report {
	return &Report{
		ID:               uuid.New().String(),
		DocumentID:       opts.DocumentID,
		DocumentTitle:    opts.DocumentTitle,
		PracticeArea:     opts.PracticeArea,
		Jurisdiction:     opts.Jurisdiction,
		Issues:           []Issue{},
		ExecutiveSummary: "No clauses extracted from the provided document.",
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func fallbackIssues(clauses []rawClause, positions []posInfo) []Issue {
	issues := make([]Issue, len(clauses))
	for i, c := range clauses {
		firmPos := "No position recorded"
		src := "none"
		if i < len(positions) && positions[i].entry != nil {
			firmPos = positions[i].entry.StandardPosition
			src = positions[i].source
		}
		issues[i] = Issue{
			ClauseType:       c.ClauseType,
			CounterpartyText: truncate(c.Text, 500),
			FirmPosition:     firmPos,
			PositionSource:   src,
			Action:           ActionEscalate,
			Rationale:        "Analysis failed — requires manual review",
			Severity:         SeverityMedium,
		}
	}
	return issues
}

func recordCost(model string, resp *providers.ChatResponse, dms int64, taskID string) {
	cw, cr := 0, 0
	if resp.Usage.CacheWriteTokens != nil {
		cw = *resp.Usage.CacheWriteTokens
	}
	if resp.Usage.CacheReadTokens != nil {
		cr = *resp.Usage.CacheReadTokens
	}
	costUSD := cost.CalcCostUSD(model, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	cost.Default.Record(cost.RecordRequest{
		Model: model, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheReadTokens: resp.Usage.CacheReadTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "redline", TaskID: taskID,
	})
}

func textFrom(resp *providers.ChatResponse) string {
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			return blk.Text
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func strVal(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func boolVal(v interface{}) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func toAction(v interface{}) RedlineAction {
	switch strVal(v) {
	case "accept":
		return ActionAccept
	case "redline":
		return ActionRedline
	case "escalate":
		return ActionEscalate
	case "delete":
		return ActionDelete
	case "no_position":
		return ActionNoPosition
	}
	return ActionEscalate
}

func toSeverity(v interface{}) RedlineSeverity {
	switch strVal(v) {
	case "critical":
		return SeverityCritical
	case "high":
		return SeverityHigh
	case "low":
		return SeverityLow
	}
	return SeverityMedium
}

func joinOrNone(ss []string) string {
	if len(ss) == 0 {
		return "none"
	}
	return strings.Join(ss, ", ")
}
