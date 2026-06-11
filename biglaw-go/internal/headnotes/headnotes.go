// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// HeadnoteEngine — extracts legal headnotes and holdings from court opinions.
// Step 1 (Sonnet): structured headnote extraction.
// Step 2 (Haiku): synthesise key holding, practice areas, NOSLEGAL tag.

package headnotes

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// GenerateOpts parameterises a headnote generation run.
type GenerateOpts struct {
	CaseName     string
	Citation     string
	Court        string
	DateFiled    string
	Jurisdiction string
	TaskID       string
}

// Engine extracts headnotes from court opinions.
type Engine struct {
	provider providers.Provider
	sonnet   string
	haiku    string
}

// New creates a HeadnoteEngine.
func New(provider providers.Provider, sonnetModel, haikuModel string) *Engine {
	return &Engine{provider: provider, sonnet: sonnetModel, haiku: haikuModel}
}

// Generate extracts headnotes from a court opinion text.
func (e *Engine) Generate(opinionText string, opts GenerateOpts) (*types.HeadnoteReport, error) {
	headnotes, err := e.extractHeadnotes(opinionText, opts)
	if err != nil {
		slog.Warn("HeadnoteEngine: extraction error", "error", err)
	}

	meta := e.synthesiseMeta(opinionText, headnotes, opts)

	caseName := opts.CaseName
	if caseName == "" {
		caseName = meta["caseName"]
	}
	citation := opts.Citation
	if citation == "" {
		citation = meta["citation"]
	}
	court := opts.Court
	if court == "" {
		court = meta["court"]
	}

	ratioCount := 0
	obiterCount := 0
	for _, h := range headnotes {
		switch h.HoldingType {
		case "ratio":
			ratioCount++
		case "obiter":
			obiterCount++
		}
	}

	relatedPrinciples := splitLines(meta["relatedPrinciples"])
	practiceAreas := splitLines(meta["practiceAreas"])

	report := &types.HeadnoteReport{
		ID:                uuid.New().String(),
		CaseName:          caseName,
		Citation:          citation,
		Court:             court,
		DateFiled:         opts.DateFiled,
		Jurisdiction:      opts.Jurisdiction,
		KeyHolding:        meta["keyHolding"],
		Headnotes:         headnotes,
		RelatedPrinciples: relatedPrinciples,
		PracticeAreas:     practiceAreas,
		NosLegalArea:      meta["noslegalArea"],
		TotalHeadnotes:    len(headnotes),
		RatioCount:        ratioCount,
		ObiterCount:       obiterCount,
		GeneratedAt:       time.Now().UTC().Format(time.RFC3339),
	}

	slog.Info("Headnote report generated", "id", report.ID, "case", report.CaseName, "headnotes", report.TotalHeadnotes, "ratio", report.RatioCount)
	return report, nil
}

// ─── Step 1: headnote extraction ─────────────────────────────────────────────

func (e *Engine) extractHeadnotes(text string, opts GenerateOpts) ([]types.Headnote, error) {
	start := time.Now()

	system := `You are a legal headnote writer working for a law firm.
Your task: extract every distinct legal proposition from the court opinion below.

For each proposition, output a headnote object:
- number: sequential integer (1, 2, 3...)
- proposition: the legal rule in ONE sentence, stated as a general principle
- sourceText: the verbatim passage (up to 200 words) that establishes the proposition
- location: paragraph or page reference if identifiable
- holdingType: "ratio" (binding), "obiter" (non-binding), "procedural" (process only), or "statutory" (interpreting a statute)
- distinguishingFactors: array of specific facts that limit when this holding applies (may be empty)
- areaOfLaw: NOSLEGAL area of law (e.g. "Contract Law", "Tort - Negligence", "Corporate Finance")
- confidence: 0.0–1.0 (1.0 = clearly a legal holding, 0.5 = marginal)

Rules:
- Only include genuine legal propositions (not background facts, procedural history, or summaries)
- Separate ratio from obiter — if the court said something unnecessary to the decision, mark it obiter
- Do NOT invent propositions not in the text
- Up to 20 headnotes per opinion

Return a JSON array: [{"number":1,"proposition":"...","sourceText":"...","location":"...","holdingType":"ratio","distinguishingFactors":[],"areaOfLaw":"...","confidence":0.9}, ...]`

	resp, err := e.provider.Chat(providers.ChatParams{
		Model:       e.sonnet,
		MaxTokens:   6000,
		System:      system,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: "Court opinion:\n" + truncate(text, 15000)}},
	})
	if err != nil {
		return nil, err
	}

	dms := time.Since(start).Milliseconds()
	recordCost(e.sonnet, resp, dms, opts.TaskID)

	raw := textFrom(resp)
	s := strings.Index(raw, "[")
	eIdx := strings.LastIndex(raw, "]")
	if s < 0 || eIdx <= s {
		return nil, nil
	}

	var parsed []map[string]interface{}
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &parsed); err != nil {
		return nil, err
	}

	headnotes := make([]types.Headnote, 0, len(parsed))
	for _, p := range parsed {
		num := 0
		if v, ok := p["number"].(float64); ok {
			num = int(v)
		}
		conf := 0.8
		if v, ok := p["confidence"].(float64); ok {
			conf = v
		}
		headnotes = append(headnotes, types.Headnote{
			Number:                num,
			Proposition:           strVal(p["proposition"]),
			SourceText:            strVal(p["sourceText"]),
			Location:              strVal(p["location"]),
			HoldingType:           strVal(p["holdingType"]),
			DistinguishingFactors: strSlice(p["distinguishingFactors"]),
			AreaOfLaw:             strVal(p["areaOfLaw"]),
			Confidence:            conf,
		})
	}
	return headnotes, nil
}

// ─── Step 2: key holding synthesis ───────────────────────────────────────────

func (e *Engine) synthesiseMeta(opinionText string, headnotes []types.Headnote, opts GenerateOpts) map[string]string {
	start := time.Now()

	ratioLines := make([]string, 0, 5)
	for i, h := range headnotes {
		if i >= 5 {
			break
		}
		if h.HoldingType == "ratio" {
			ratioLines = append(ratioLines, fmt.Sprintf("%d. %s", h.Number, h.Proposition))
		}
	}
	ratioBlock := strings.Join(ratioLines, "\n")
	if ratioBlock == "" {
		ratioBlock = "(none extracted)"
	}

	prompt := fmt.Sprintf(`Given this court opinion excerpt and its ratio decidendi headnotes, produce a JSON object:
{
  "caseName": "...",
  "citation": "...",
  "court": "...",
  "keyHolding": "...",
  "relatedPrinciples": "principle1\nprinciple2\nprinciple3",
  "practiceAreas": "area1\narea2",
  "noslegalArea": "..."
}

Use newline-separated strings (not JSON arrays) for relatedPrinciples and practiceAreas.

Ratio headnotes:
%s

Opinion excerpt:
%s`, ratioBlock, truncate(opinionText, 3000))

	resp, err := e.provider.Chat(providers.ChatParams{
		Model:     e.haiku,
		MaxTokens: 600,
		Messages:  []providers.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return fallbackMeta(opts)
	}

	dms := time.Since(start).Milliseconds()
	recordCost(e.haiku, resp, dms, opts.TaskID)

	raw := textFrom(resp)
	s := strings.Index(raw, "{")
	eIdx := strings.LastIndex(raw, "}")
	if s < 0 || eIdx <= s {
		return fallbackMeta(opts)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &parsed); err != nil {
		return fallbackMeta(opts)
	}
	if parsed["keyHolding"] == "" {
		parsed["keyHolding"] = "Key holding could not be synthesised — see headnotes."
	}
	return parsed
}

func fallbackMeta(opts GenerateOpts) map[string]string {
	return map[string]string{
		"caseName":          opts.CaseName,
		"citation":          opts.Citation,
		"court":             opts.Court,
		"keyHolding":        "Key holding could not be synthesised — see headnotes.",
		"relatedPrinciples": "",
		"practiceAreas":     "",
		"noslegalArea":      "",
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

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
		Context: "headnote_extract", TaskID: taskID,
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

func strSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, strVal(item))
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
