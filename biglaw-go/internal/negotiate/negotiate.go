// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package negotiate is the counter-redline judgment engine. Opposing counsel
// returns a marked-up draft; each parsed tracked change is classified to a
// clause type (extraction-tier model), resolved against the four-tier
// playbook cascade, and judged (drafting-tier model) to a disposition:
// accept the opposing change, reject it (restore our language), or counter
// it with replacement text. Every decision carries a rationale card grounded
// in the resolved playbook position when one exists. A failed model call
// never aborts the run — that change is marked "review" and the run
// continues.

package negotiate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// ─── Types ────────────────────────────────────────────────────────────────────

// Disposition is the engine's verdict on one opposing change.
type Disposition string

const (
	DispositionAccept  Disposition = "accept"
	DispositionReject  Disposition = "reject"
	DispositionCounter Disposition = "counter"
	// DispositionReview marks a change the engine could not safely decide —
	// a failed classification/judgment or an unactionable verdict. The
	// opposing change is left standing for a human to resolve.
	DispositionReview Disposition = "review"
)

// Decision is the judgment on one opposing tracked change. Decisions are
// index-aligned with the []ooxml.Revision slice they were made from.
type Decision struct {
	Author       string      `json:"author"`
	Kind         string      `json:"kind"`
	DeletedText  string      `json:"deletedText,omitempty"`
	InsertedText string      `json:"insertedText,omitempty"`
	ClauseType   string      `json:"clauseType,omitempty"`
	Disposition  Disposition `json:"disposition"`
	Rationale    string      `json:"rationale"`
	CounterText  string      `json:"counterText,omitempty"`
	PlaybookTier string      `json:"playbookTier,omitempty"`
}

// Opts parameterises a negotiation run. The playbook scoping fields thread
// straight into the cascade (client > matter > personal > firm).
type Opts struct {
	PracticeArea string
	MatterNumber string
	ClientID     string
	ProfileID    string
	// Instructions is optional free-text negotiation guidance from the
	// lawyer ("hold firm on liability, flexible on notice periods").
	Instructions string
	TaskID       string
}

// ─── Engine ───────────────────────────────────────────────────────────────────

// Engine judges opposing tracked changes against the playbook cascade.
type Engine struct {
	provider providers.Provider
	judge    string // drafting/analysis-tier model
	classify string // extraction-tier model
}

// New creates a negotiation engine. judgeModel decides dispositions;
// classifyModel labels clause types.
func New(provider providers.Provider, judgeModel, classifyModel string) *Engine {
	return &Engine{provider: provider, judge: judgeModel, classify: classifyModel}
}

// Decide classifies and judges every opposing revision. The returned slice is
// index-aligned with revs. Errors are absorbed per-change as "review".
func (e *Engine) Decide(revs []ooxml.Revision, store *playbook.Store, opts Opts) []Decision {
	vocab := vocabulary(store)
	decisions := make([]Decision, len(revs))
	for i, rv := range revs {
		d := Decision{
			Author:       rv.Author,
			Kind:         string(rv.Kind),
			DeletedText:  rv.DeletedText,
			InsertedText: rv.InsertedText,
		}

		clauseType, err := e.classifyClause(rv, vocab, opts.TaskID)
		if err != nil {
			d.Disposition = DispositionReview
			d.Rationale = "Classification failed — manual review required: " + err.Error()
			decisions[i] = d
			continue
		}
		d.ClauseType = clauseType

		var resolved *playbook.ResolvedClause
		if store != nil && clauseType != "" {
			resolved = store.Resolve(clauseType, playbook.ResolveOpts{
				PracticeArea: opts.PracticeArea,
				MatterNumber: opts.MatterNumber,
				ClientID:     opts.ClientID,
				ProfileID:    opts.ProfileID,
			})
		}
		if resolved != nil {
			d.PlaybookTier = string(resolved.ResolvedFrom)
		}

		verdict, err := e.judgeRevision(rv, clauseType, resolved, opts)
		if err != nil {
			d.Disposition = DispositionReview
			d.Rationale = "Judgment failed — manual review required: " + err.Error()
			decisions[i] = d
			continue
		}
		d.Disposition = verdict.disposition
		d.Rationale = verdict.rationale
		d.CounterText = verdict.counterText
		if d.Disposition == DispositionCounter && strings.TrimSpace(d.CounterText) == "" {
			// A counter with no replacement text cannot be applied.
			d.Disposition = DispositionReview
			d.CounterText = ""
			d.Rationale = strings.TrimSpace(d.Rationale +
				" (counter proposed without replacement text — manual review required)")
		}
		decisions[i] = d
	}
	return decisions
}

// ClassifyChange labels the clause one change touches — the same
// classification step Decide runs per revision, exported for Redtime, which
// builds synthetic Revisions from plain-text diff hunks and buckets timeline
// events in the firm's own clause vocabulary.
func (e *Engine) ClassifyChange(rv ooxml.Revision, vocab []string, taskID string) (string, error) {
	return e.classifyClause(rv, vocab, taskID)
}

// Vocabulary exposes the playbook clause-type labels for external
// classification callers (Redtime).
func Vocabulary(store *playbook.Store) []string { return vocabulary(store) }

// ParseJSONObject exposes the lenient JSON-object extractor for other
// model-facing packages (Redtime's drift judgment) — small local models emit
// fences and trailing commas everywhere, and there should be exactly one
// repair implementation.
func ParseJSONObject(raw string, v interface{}) error { return parseJSONObject(raw, v) }

// vocabulary returns the clause-type labels known to any playbook so
// classification can label changes in the firm's own vocabulary — without it,
// "confidentiality" vs "Confidential Information" never match the cascade.
func vocabulary(store *playbook.Store) []string {
	if store == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, pb := range store.List("", "", "") {
		for _, en := range pb.Entries {
			if !seen[en.ClauseType] {
				seen[en.ClauseType] = true
				out = append(out, en.ClauseType)
			}
			if len(out) >= 60 {
				return out
			}
		}
	}
	return out
}

// ─── Step 1: clause classification ────────────────────────────────────────────

func (e *Engine) classifyClause(rv ooxml.Revision, vocab []string, taskID string) (string, error) {
	vocabHint := ""
	if len(vocab) > 0 {
		vocabHint = fmt.Sprintf("\n\nThe firm's playbook uses these clause-type labels — when the change matches one of these concepts, use that exact label: %s.",
			strings.Join(vocab, ", "))
	}
	system := `You label the contract clause a tracked change touches.

Given one revision (its deleted and/or inserted text plus surrounding context), return a short clause-type label taken from the clause's subject matter (e.g. "Limitation of liability", "Governing law", "Indemnification cap").` + vocabHint + `

Return a JSON object: {"clauseType": "..."}
If the change is purely stylistic or the clause cannot be identified, return {"clauseType": ""}.`

	start := time.Now()
	resp, err := e.provider.Chat(providers.ChatParams{
		Model:       e.classify,
		MaxTokens:   200,
		System:      system,
		CacheSystem: true,
		JSONMode:    true,
		Messages:    []providers.Message{{Role: "user", Content: describeRevision(rv)}},
	})
	if err != nil {
		return "", err
	}
	e.recordCost(e.classify, resp, time.Since(start).Milliseconds(), taskID)

	var parsed struct {
		ClauseType string `json:"clauseType"`
	}
	if err := parseJSONObject(textFrom(resp), &parsed); err != nil {
		return "", fmt.Errorf("classification returned no usable JSON: %w", err)
	}
	return strings.TrimSpace(parsed.ClauseType), nil
}

// ─── Step 2: judgment ─────────────────────────────────────────────────────────

type verdict struct {
	disposition Disposition
	rationale   string
	counterText string
}

func (e *Engine) judgeRevision(rv ooxml.Revision, clauseType string, resolved *playbook.ResolvedClause, opts Opts) (verdict, error) {
	system := `You are a senior transactional lawyer reviewing ONE tracked change proposed by opposing counsel, deciding how the firm responds.

Decide exactly one disposition:
- "accept"  — the opposing change is acceptable and will be left standing.
- "reject"  — the opposing change is rejected; the original language will be restored.
- "counter" — the opposing change is replaced with the firm's own language; provide counterText.

Rules:
- When a playbook position is provided, ground the decision and rationale in it. Never accept a change that crosses a red line; reject or counter it back inside the firm's position (prefer the standard position, fall back to the fallback position).
- When NO playbook position is provided, judge the change against market-standard reasonableness and say so explicitly in the rationale.
- counterText must be the exact replacement text that will appear in the document in place of the opposing language — not a description of it. Leave it empty unless the disposition is "counter".
- rationale: 1-2 sentences, specific enough to serve as the change card shown to the negotiating lawyer.

Return a JSON object:
{"disposition":"accept|reject|counter","rationale":"...","counterText":"..."}`

	var b strings.Builder
	b.WriteString("OPPOSING CHANGE:\n")
	b.WriteString(describeRevision(rv))
	if clauseType != "" {
		b.WriteString("\nCLAUSE TYPE: " + clauseType + "\n")
	}
	if resolved != nil {
		en := resolved.EffectiveEntry
		b.WriteString(fmt.Sprintf("\nPLAYBOOK POSITION (resolved from the %s tier):\n", resolved.ResolvedFrom))
		b.WriteString("STANDARD POSITION: " + strutil.Truncate(en.StandardPosition, 600) + "\n")
		if en.FallbackPosition != "" {
			b.WriteString("FALLBACK POSITION: " + strutil.Truncate(en.FallbackPosition, 400) + "\n")
		}
		if len(en.RedLines) > 0 {
			b.WriteString("RED LINES: " + strings.Join(en.RedLines, "; ") + "\n")
		}
	} else {
		b.WriteString("\nPLAYBOOK POSITION: none resolved for this clause type — judge on market-standard reasonableness.\n")
	}
	if strings.TrimSpace(opts.Instructions) != "" {
		b.WriteString("\nNEGOTIATION INSTRUCTIONS FROM THE RESPONSIBLE LAWYER:\n" + strutil.Truncate(opts.Instructions, 1200) + "\n")
	}

	start := time.Now()
	resp, err := e.provider.Chat(providers.ChatParams{
		Model:       e.judge,
		MaxTokens:   800,
		System:      system,
		CacheSystem: true,
		JSONMode:    true,
		Messages:    []providers.Message{{Role: "user", Content: b.String()}},
	})
	if err != nil {
		return verdict{}, err
	}
	e.recordCost(e.judge, resp, time.Since(start).Milliseconds(), opts.TaskID)

	var parsed struct {
		Disposition string `json:"disposition"`
		Rationale   string `json:"rationale"`
		CounterText string `json:"counterText"`
	}
	if err := parseJSONObject(textFrom(resp), &parsed); err != nil {
		return verdict{}, fmt.Errorf("judgment returned no usable JSON: %w", err)
	}
	switch Disposition(strings.ToLower(strings.TrimSpace(parsed.Disposition))) {
	case DispositionAccept:
		return verdict{DispositionAccept, parsed.Rationale, ""}, nil
	case DispositionReject:
		return verdict{DispositionReject, parsed.Rationale, ""}, nil
	case DispositionCounter:
		return verdict{DispositionCounter, parsed.Rationale, parsed.CounterText}, nil
	}
	return verdict{}, fmt.Errorf("judgment returned unrecognised disposition %q", parsed.Disposition)
}

// describeRevision renders one revision for a prompt: kind, author, texts,
// and the untouched context around the change.
func describeRevision(rv ooxml.Revision) string {
	var b strings.Builder
	b.WriteString("KIND: " + string(rv.Kind) + "\n")
	if rv.Author != "" {
		b.WriteString("AUTHOR: " + rv.Author + "\n")
	}
	if rv.DeletedText != "" {
		b.WriteString("DELETED TEXT: " + strutil.Truncate(rv.DeletedText, 1500) + "\n")
	}
	if rv.InsertedText != "" {
		b.WriteString("INSERTED TEXT: " + strutil.Truncate(rv.InsertedText, 1500) + "\n")
	}
	b.WriteString("CONTEXT BEFORE: …" + rv.ContextBefore + "\n")
	b.WriteString("CONTEXT AFTER: " + rv.ContextAfter + "…\n")
	return b.String()
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func (e *Engine) recordCost(model string, resp *providers.ChatResponse, dms int64, taskID string) {
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
		Context: "negotiate", TaskID: taskID,
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

var trailingCommaRe = regexp.MustCompile(`,\s*([\]}])`)

// parseJSONObject extracts and unmarshals the first JSON object in raw into v.
// Lenient like the redline engine's array parser: markdown fences and stray
// backticks are stripped and trailing commas repaired, because small local
// models emit both constantly.
func parseJSONObject(raw string, v interface{}) error {
	raw = strings.ReplaceAll(raw, "```json", "")
	raw = strings.ReplaceAll(raw, "```", "")
	raw = strings.ReplaceAll(raw, "`", "")
	s := strings.Index(raw, "{")
	e := strings.LastIndex(raw, "}")
	if s < 0 || e <= s {
		return fmt.Errorf("no JSON object found")
	}
	frag := raw[s : e+1]
	if err := json.Unmarshal([]byte(frag), v); err == nil {
		return nil
	}
	frag = trailingCommaRe.ReplaceAllString(frag, "$1")
	return json.Unmarshal([]byte(frag), v)
}
