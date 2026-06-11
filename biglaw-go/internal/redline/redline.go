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
	"math"
	"regexp"
	"strconv"
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

// MissingClause is a playbook position with no corresponding clause in the
// counterparty draft — protections the firm expects that simply aren't there.
type MissingClause struct {
	ClauseType     string          `json:"clauseType"`
	FirmPosition   string          `json:"firmPosition"`
	PositionSource string          `json:"positionSource"`
	Severity       RedlineSeverity `json:"severity"`
	IsRedLine      bool            `json:"isRedLine"`
	SuggestedText  string          `json:"suggestedText,omitempty"`
	Rationale      string          `json:"rationale"`
}

// Report is the full redline review output.
type Report struct {
	ID               string          `json:"id"`
	DocumentID       string          `json:"documentId,omitempty"`
	DocumentTitle    string          `json:"documentTitle,omitempty"`
	PracticeArea     string          `json:"practiceArea,omitempty"`
	Jurisdiction     string          `json:"jurisdiction,omitempty"`
	TotalClauses     int             `json:"totalClauses"`
	AcceptCount      int             `json:"acceptCount"`
	RedlineCount     int             `json:"redlineCount"`
	EscalateCount    int             `json:"escalateCount"`
	DeleteCount      int             `json:"deleteCount"`
	CriticalCount    int             `json:"criticalCount"`
	MissingCount     int             `json:"missingCount"`
	Issues           []Issue         `json:"issues"`
	MissingClauses   []MissingClause `json:"missingClauses"`
	ExecutiveSummary string          `json:"executiveSummary"`
	GeneratedAt      string          `json:"generatedAt"`
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
	clauses, err := e.extractClauses(documentText, opts.PracticeArea, playbookVocabulary(store, opts), opts.TaskID)
	if err != nil || len(clauses) == 0 {
		slog.Warn("RedlineEngine: no clauses extracted — returning empty report", "error", err, "docChars", len(documentText))
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

	missing := e.detectMissingClauses(clauses, store, opts)
	summary := e.generateSummary(issues, opts)

	r := &Report{
		ID:               uuid.New().String(),
		DocumentID:       opts.DocumentID,
		DocumentTitle:    opts.DocumentTitle,
		PracticeArea:     opts.PracticeArea,
		Jurisdiction:     opts.Jurisdiction,
		TotalClauses:     len(issues),
		Issues:           issues,
		MissingClauses:   missing,
		MissingCount:     len(missing),
		ExecutiveSummary: summary,
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
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

// playbookVocabulary returns the clause-type labels known to the applicable
// playbooks so extraction can label clauses in the firm's own vocabulary —
// without it, "confidentiality" vs "Confidential Information" never match.
func playbookVocabulary(store *playbook.Store, opts RedlineOpts) []string {
	seen := map[string]bool{}
	var out []string
	for _, pb := range store.List("", "", opts.PracticeArea) {
		for _, en := range pb.Entries {
			if !seen[en.ClauseType] {
				seen[en.ClauseType] = true
				out = append(out, en.ClauseType)
			}
		}
	}
	return out
}

func (e *Engine) extractClauses(text, practiceArea string, vocabulary []string, taskID string) ([]rawClause, error) {
	start := time.Now()
	area := practiceArea
	if area == "" {
		area = "transactional"
	}

	vocabHint := ""
	if len(vocabulary) > 0 {
		vocabHint = fmt.Sprintf("\n\nThe firm's playbook uses these clause-type labels — when a clause matches one of these concepts, use that exact label as clauseType: %s.",
			strings.Join(vocabulary, ", "))
	}

	system := fmt.Sprintf(`You are a contract analysis assistant. The contract below relates to %s work.

Extract every distinct legal clause that actually appears in the contract text. For each clause:
- clauseType: a short label for what the clause is, taken from its heading or subject matter (e.g. "Confidentiality", "Limitation of liability", "Governing law")
- text: the verbatim text of the clause from the document

Only report clauses present in the document — never invent clause types that are not there.%s

Return a JSON array:
[{"clauseType": "...", "text": "..."}]

Include up to 40 clauses. Skip boilerplate recitals.`, area, vocabHint)

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
	var clauses []rawClause
	if err := parseJSONArray(raw, &clauses); err != nil {
		slog.Warn("RedlineEngine: clause extraction parse failed", "error", err, "rawPrefix", truncate(raw, 300))
		return nil, nil
	}
	return clauses, nil
}

// ─── Step 1b: missing-clause detection ───────────────────────────────────────
// A playbook review has two halves: what's wrong with the clauses that ARE in
// the draft (Step 2), and which firm-expected protections are simply ABSENT.
// Clause-type naming is free-form on both sides, so the absence judgment is
// made by the model rather than by string matching.

func (e *Engine) detectMissingClauses(found []rawClause, store *playbook.Store, opts RedlineOpts) []MissingClause {
	// Union of clause types across the applicable playbooks, each resolved
	// through the cascade so the effective tier's position is used.
	type expected struct {
		entry  types.PlaybookEntry
		source string
	}
	expectedByType := map[string]expected{}
	for _, pb := range store.List("", "", opts.PracticeArea) {
		for _, en := range pb.Entries {
			if _, seen := expectedByType[en.ClauseType]; seen {
				continue
			}
			resolved := store.Resolve(en.ClauseType, playbook.ResolveOpts{
				PracticeArea: opts.PracticeArea,
				MatterNumber: opts.MatterNumber,
				ClientID:     opts.ClientID,
				ProfileID:    opts.ProfileID,
			})
			if resolved != nil {
				expectedByType[en.ClauseType] = expected{entry: resolved.EffectiveEntry, source: string(resolved.ResolvedFrom)}
			}
		}
	}
	if len(expectedByType) == 0 {
		return nil
	}

	var expBlocks, foundTypes []string
	count := 0
	for ct, ex := range expectedByType {
		if count >= 40 {
			break
		}
		count++
		expBlocks = append(expBlocks, fmt.Sprintf("- %s: %s", ct, truncate(ex.entry.StandardPosition, 300)))
	}
	for _, c := range found {
		foundTypes = append(foundTypes, c.ClauseType)
	}

	system := `You are a senior transactional lawyer. Given (a) the clause types your firm's playbook expects in this kind of agreement, with the firm's position on each, and (b) the clause types actually present in a counterparty draft, identify which expected protections are MISSING from the draft.

Treat differently-worded names for the same concept as present (e.g. "Indemnification cap" covers "indemnity"). Only report genuine absences that matter.

For each missing clause return:
- clauseType: the playbook's clause type, verbatim
- suggestedText: insertable clause language implementing the firm position (2-5 sentences)
- rationale: 1 sentence on the risk of leaving it out
- severity: "critical", "high", "medium", or "low"

Return a JSON array (empty array if nothing material is missing):
[{"clauseType":"...","suggestedText":"...","rationale":"...","severity":"..."}]`

	user := fmt.Sprintf("PLAYBOOK EXPECTS:\n%s\n\nCLAUSES PRESENT IN DRAFT:\n%s",
		strings.Join(expBlocks, "\n"), strings.Join(foundTypes, ", "))

	start := time.Now()
	resp, err := e.provider.Chat(providers.ChatParams{
		Model:       e.sonnet,
		MaxTokens:   2500,
		System:      system,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: user}},
	})
	if err != nil {
		slog.Warn("RedlineEngine: missing-clause detection failed", "error", err)
		return nil
	}
	recordCost(e.sonnet, resp, time.Since(start).Milliseconds(), opts.TaskID)

	raw := textFrom(resp)
	var parsed []struct {
		ClauseType    string `json:"clauseType"`
		SuggestedText string `json:"suggestedText"`
		Rationale     string `json:"rationale"`
		Severity      string `json:"severity"`
	}
	if err := parseJSONArray(raw, &parsed); err != nil {
		slog.Warn("RedlineEngine: missing-clause parse failed", "error", err, "rawPrefix", truncate(raw, 300))
		return nil
	}

	// Map model-returned clause types back to playbook entries with the same
	// normalization Resolve uses — the model may restyle "limitation_of_liability"
	// as "Limitation of Liability".
	normalized := map[string]string{}
	for ct := range expectedByType {
		normalized[playbook.NormalizeClauseType(ct)] = ct
	}

	out := make([]MissingClause, 0, len(parsed))
	for _, m := range parsed {
		key, ok := normalized[playbook.NormalizeClauseType(m.ClauseType)]
		if !ok {
			continue // model invented a clause type — drop it
		}
		ex := expectedByType[key]
		sev := RedlineSeverity(m.Severity)
		switch sev {
		case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		default:
			sev = SeverityMedium
		}
		out = append(out, MissingClause{
			ClauseType:     key,
			FirmPosition:   ex.entry.StandardPosition,
			PositionSource: ex.source,
			Severity:       sev,
			IsRedLine:      len(ex.entry.RedLines) > 0,
			SuggestedText:  m.SuggestedText,
			Rationale:      m.Rationale,
		})
	}
	return out
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

Return a JSON array — one object per clause. ALWAYS include "clauseIndex" set to the
CLAUSE number shown in the input header (1-based) so each verdict is bound to its clause:
[{"clauseIndex":1,"clauseType":"...","action":"...","severity":"...","proposedText":"...","rationale":"...","isRedLine":false}]`

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
	var parsed []map[string]interface{}
	if err := parseJSONArray(raw, &parsed); err != nil {
		slog.Warn("RedlineEngine: batch analysis parse failed", "error", err, "rawPrefix", truncate(raw, 300))
		return fallbackIssues(clauses, positions)
	}

	// Join verdicts to clauses by the model-echoed 1-based clauseIndex, not by
	// array position — the model may drop, merge, or reorder clauses, and a
	// positional zip would then bind the wrong "accept"/"redline" to a clause
	// (e.g. mark a red-line-crossing clause as acceptable). Any clause with no
	// matching verdict is escalated rather than silently mispaired.
	byIndex := make(map[int]map[string]interface{}, len(parsed))
	for _, p := range parsed {
		if ci, ok := clauseIndexOf(p["clauseIndex"]); ok && ci >= 1 && ci <= len(clauses) {
			byIndex[ci] = p
		}
	}

	issues := make([]Issue, 0, len(clauses))
	for idx, clause := range clauses {
		pos := positions[idx]
		firmPosition := "No position recorded"
		if pos.entry != nil {
			firmPosition = pos.entry.StandardPosition
		}
		p, matched := byIndex[idx+1]
		if !matched {
			issues = append(issues, Issue{
				ClauseType:       clause.ClauseType,
				CounterpartyText: truncate(clause.Text, 500),
				FirmPosition:     firmPosition,
				PositionSource:   pos.source,
				Action:           ActionEscalate,
				Rationale:        "No verdict returned for this clause — escalated for manual review.",
				IsRedLine:        false,
				Severity:         SeverityHigh,
			})
			continue
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

// clauseIndexOf coerces a model-echoed clauseIndex into an int. JSON numbers
// arrive as float64; lenient like the TS Number() coercion, it also accepts
// integral numeric strings ("3"). Non-integral or non-numeric values are
// rejected so the clause falls through to escalation.
func clauseIndexOf(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n == math.Trunc(n) && !math.IsInf(n, 0) {
			return int(n), true
		}
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i, true
		}
	case int:
		return n, true
	}
	return 0, false
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

// parseJSONArray extracts and unmarshals the first JSON array in raw into v.
// Lenient: strips trailing commas before ] and } — small local models emit
// them constantly, and a review that silently returns nothing is worse than
// a forgiving parse.
func parseJSONArray(raw string, v interface{}) error {
	s := strings.Index(raw, "[")
	e := strings.LastIndex(raw, "]")
	if s < 0 || e <= s {
		return fmt.Errorf("no JSON array found")
	}
	frag := raw[s : e+1]
	if err := json.Unmarshal([]byte(frag), v); err == nil {
		return nil
	}
	frag = trailingCommaRe.ReplaceAllString(frag, "$1")
	if err := json.Unmarshal([]byte(frag), v); err == nil {
		return nil
	}
	// Quote bare-word values ("severity": Low → "severity": "Low"),
	// leaving true/false/null intact.
	frag = bareWordValueRe.ReplaceAllStringFunc(frag, func(m string) string {
		parts := bareWordValueRe.FindStringSubmatch(m)
		word := parts[2]
		switch strings.ToLower(word) {
		case "true", "false", "null":
			return m
		}
		return parts[1] + `"` + word + `"` + parts[3]
	})
	if err := json.Unmarshal([]byte(frag), v); err == nil {
		return nil
	}
	// Quote bare object keys ({severity: → {"severity":).
	frag = bareKeyRe.ReplaceAllString(frag, `$1"$2"$3`)
	return json.Unmarshal([]byte(frag), v)
}

var (
	trailingCommaRe = regexp.MustCompile(`,\s*([\]}])`)
	bareWordValueRe = regexp.MustCompile(`(:\s*)([A-Za-z][A-Za-z _-]*?)(\s*[,}\]])`)
	bareKeyRe       = regexp.MustCompile(`([{,]\s*)([A-Za-z_][A-Za-z0-9_]*)(\s*:)`)
)

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
