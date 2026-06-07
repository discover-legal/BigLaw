// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Daily matter status-report generator. Facts (health, task/finding/time deltas)
// are computed deterministically from the gathered inputs; a specialised small
// model writes only the narrative (BLUF, summary, workstreams, risks) over those
// facts, then a lightweight recursive verify pass scores its groundedness. The
// resulting MatterStatusReport is the single source of truth the JSON, Markdown
// and DOCX renderers consume.
package lpm

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

// Generator produces structured daily status reports for a matter.
type Generator struct {
	provider providers.Provider
	model    string
}

// NewGenerator builds a Generator that synthesises with the given (typically
// low-power) model.
func NewGenerator(provider providers.Provider, model string) *Generator {
	return &Generator{provider: provider, model: model}
}

// ReportInput is the gathered raw state the generator turns into a report. The
// caller (the LPM service / worker) assembles this from the live stores so the
// generator stays decoupled and unit-testable.
type ReportInput struct {
	MatterNumber string
	ClientNumber string
	Date         string // YYYY-MM-DD; defaults to today (UTC) when empty
	Health       types.MatterHealthScore
	Tasks        []types.Task
	TimeEntries  []types.TimeEntry
	EmailsRouted int                       // Phase 2 email router populates this
	Prev         *types.MatterStatusReport // previous report, for delta + trend
	Lawyer       *types.LawyerProfile      // optional tone injection
}

// GenOpts tunes generation.
type GenOpts struct {
	Verify bool // run the recursive groundedness check (a small extra model call)
}

// Generate computes deltas, synthesises the narrative, and returns a report.
func (g *Generator) Generate(in ReportInput, opts GenOpts) (*types.MatterStatusReport, error) {
	date := in.Date
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}

	// ── Cutoff: deltas are measured since the previous report, else trailing 24h.
	cutoff := time.Now().Add(-24 * time.Hour)
	if in.Prev != nil {
		if t, err := time.Parse(time.RFC3339, in.Prev.GeneratedAt); err == nil {
			cutoff = t
		}
	}

	deltas := computeDeltas(in, cutoff)

	// ── Build the fact sheet the model writes over (facts only, no invention).
	facts := buildFactSheet(in, deltas)

	report := &types.MatterStatusReport{
		ReportID:     uuid.New().String(),
		MatterNumber: in.MatterNumber,
		ClientNumber: in.ClientNumber,
		Date:         date,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		GeneratedBy:  g.model,
		HealthScore:  in.Health.Score,
		HealthSignal: string(in.Health.Signal),
		HealthTrend:  string(in.Health.Trend),
		Deltas:       deltas,
		Confidence:   0.6, // baseline until the model/verify pass refines it
	}
	if in.Prev != nil {
		report.PrevReportID = in.Prev.ReportID
	}

	narrative, costUSD, err := g.synthesize(in, facts)
	if err != nil {
		// Degrade gracefully: a fact-only report still has value on the box.
		slog.Warn("LPM narrative synthesis failed; emitting fact-only report", "matter", in.MatterNumber, "error", err)
		report.BLUF = fallbackBLUF(in, deltas)
		report.Summary = facts
		report.Sources = []string{"deterministic facts only (model unavailable)"}
		return report, nil
	}

	report.BLUF = strings.TrimSpace(narrative.BLUF)
	report.Summary = strings.TrimSpace(narrative.Summary)
	report.Workstreams = narrative.Workstreams
	report.Risks = narrative.Risks
	report.OpenQuestions = narrative.OpenQuestions
	report.Sources = narrative.Sources
	if narrative.Confidence > 0 {
		report.Confidence = clamp01(narrative.Confidence)
	}
	if report.BLUF == "" {
		report.BLUF = fallbackBLUF(in, deltas)
	}
	report.CostUsd = costUSD

	if opts.Verify {
		if conf, vCost, ok := g.verify(facts, report); ok {
			report.Confidence = clamp01(conf)
			report.CostUsd += vCost
		}
	}

	slog.Info("LPM status report generated", "matter", in.MatterNumber, "date", date,
		"health", report.HealthScore, "confidence", report.Confidence)
	return report, nil
}

// ─── Deterministic deltas ───────────────────────────────────────────────────

func computeDeltas(in ReportInput, cutoff time.Time) types.LPMDeltas {
	d := types.LPMDeltas{
		Since:         cutoff.UTC().Format(time.RFC3339),
		EmailsRouted:  in.EmailsRouted,
		BudgetBurnPct: in.Health.Dimensions.BudgetHealth, // dimension proxy until budget wired
	}
	for _, t := range in.Tasks {
		if t.CreatedAt.After(cutoff) {
			d.NewTasks++
		}
		if t.CompletedAt != nil && t.CompletedAt.After(cutoff) {
			d.ClosedTasks++
		}
		for _, f := range t.Findings {
			if f.Timestamp.After(cutoff) {
				d.NewFindings++
			}
		}
	}
	for _, e := range in.TimeEntries {
		if e.EndedAt != nil && e.EndedAt.After(cutoff) {
			d.HoursLogged += float64(e.BillingUnits) * 0.1 // 6-minute billing units
			if e.BillingAmountUsd != nil {
				d.BilledUsd += *e.BillingAmountUsd
			}
		}
	}
	d.HoursLogged = round1(d.HoursLogged)
	d.BilledUsd = round2(d.BilledUsd)
	return d
}

func buildFactSheet(in ReportInput, d types.LPMDeltas) string {
	var b strings.Builder
	fmt.Fprintf(&b, "MATTER: %s\n", orDash(in.MatterNumber))
	if in.ClientNumber != "" {
		fmt.Fprintf(&b, "CLIENT: %s\n", in.ClientNumber)
	}
	fmt.Fprintf(&b, "HEALTH: %.0f/100 (%s, trend %s)\n", in.Health.Score, orDash(string(in.Health.Signal)), orDash(string(in.Health.Trend)))
	fmt.Fprintf(&b, "SINCE: %s\n", d.Since)
	fmt.Fprintf(&b, "DELTAS: %d new tasks, %d closed, %d new findings, %d emails routed; %.1fh logged ($%.2f), budget health %.0f%%\n",
		d.NewTasks, d.ClosedTasks, d.NewFindings, d.EmailsRouted, d.HoursLogged, d.BilledUsd, d.BudgetBurnPct*100)

	if len(in.Health.RiskFactors) > 0 {
		b.WriteString("RISK FACTORS:\n")
		for _, rf := range in.Health.RiskFactors {
			fmt.Fprintf(&b, "  - [%s] %s%s\n", rf.Severity, rf.Message, suffixIf(" → ", rf.SuggestedAction))
		}
	}

	open := openTasks(in.Tasks)
	if len(open) > 0 {
		b.WriteString("OPEN TASKS:\n")
		for i, t := range open {
			if i >= 8 {
				fmt.Fprintf(&b, "  …and %d more\n", len(open)-8)
				break
			}
			fmt.Fprintf(&b, "  - (%s) %s\n", t.Status, truncate(t.Description, 140))
		}
	}
	return b.String()
}

// ─── Model synthesis ────────────────────────────────────────────────────────

type narrative struct {
	BLUF          string                `json:"bluf"`
	Summary       string                `json:"summary"`
	Workstreams   []types.LPMWorkstream `json:"workstreams"`
	Risks         []types.LPMRisk       `json:"risks"`
	OpenQuestions []string              `json:"openQuestions"`
	Sources       []string              `json:"sources"`
	Confidence    float64               `json:"confidence"`
}

func (g *Generator) synthesize(in ReportInput, facts string) (*narrative, float64, error) {
	system := strings.Join([]string{
		"You are a legal project manager writing a concise daily matter status report.",
		"You are given a FACT SHEET. Write ONLY over those facts — never invent matters, numbers, deadlines or names.",
		"Lead with a BLUF (bottom-line-up-front) a senior partner can digest in seconds.",
		"Do NOT reveal internal agent names, tool names, or system architecture.",
		toneSnippet(in.Lawyer),
		"Respond with ONE JSON object and nothing else, shaped exactly:",
		`{"bluf":string,"summary":string,"workstreams":[{"name":string,"status":string,"owner":string,"nextStep":string,"dueDate":string}],"risks":[{"severity":"low|medium|high","description":string,"recommendedAction":string}],"openQuestions":[string],"sources":[string],"confidence":number}`,
	}, "\n")

	resp, err := g.provider.Chat(providers.ChatParams{
		Model:       g.model,
		MaxTokens:   1500,
		System:      system,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: "FACT SHEET:\n" + facts}},
	})
	if err != nil {
		return nil, 0, err
	}

	raw := firstText(resp)
	n, perr := parseNarrative(raw)
	if perr != nil {
		return nil, g.record(in, resp), perr
	}
	return n, g.record(in, resp), nil
}

// verify runs a small recursive groundedness check: does the BLUF/summary follow
// from the facts? Returns an adjusted confidence in [0,1]. Failures are
// non-fatal — the caller keeps the model's own confidence.
func (g *Generator) verify(facts string, r *types.MatterStatusReport) (float64, float64, bool) {
	system := "You are a verifier. Given a FACT SHEET and a DRAFT status report, decide whether every claim in the draft is supported by the facts. " +
		"Respond with ONE JSON object: {\"grounded\":boolean,\"confidence\":number} where confidence in [0,1] reflects how well-supported the draft is."
	user := fmt.Sprintf("FACT SHEET:\n%s\n\nDRAFT BLUF: %s\nDRAFT SUMMARY: %s", facts, r.BLUF, r.Summary)
	resp, err := g.provider.Chat(providers.ChatParams{
		Model:     g.model,
		MaxTokens: 200,
		System:    system,
		Messages:  []providers.Message{{Role: "user", Content: user}},
	})
	if err != nil {
		return 0, 0, false
	}
	var v struct {
		Grounded   bool    `json:"grounded"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(extractJSON(firstText(resp))), &v); err != nil {
		return 0, 0, false
	}
	conf := v.Confidence
	if !v.Grounded && conf > 0.5 {
		conf = 0.5 // cap confidence when the verifier flags unsupported claims
	}
	return conf, costOf(g.model, resp), true
}

func (g *Generator) record(in ReportInput, resp *providers.ChatResponse) float64 {
	c := costOf(g.model, resp)
	profileID := ""
	if in.Lawyer != nil {
		profileID = in.Lawyer.ID
	}
	cw, cr := 0, 0
	costPtr := cost.CalcCostUSD(g.model, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	val := 0.0
	if costPtr != nil {
		val = *costPtr
	}
	cost.Default.Record(cost.RecordRequest{
		Model: g.model, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CostUSD: &val, DurationMs: resp.DurationMs,
		Context: "lpm_status_report", ProfileID: profileID,
	})
	return c
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func parseNarrative(raw string) (*narrative, error) {
	js := extractJSON(raw)
	if js == "" {
		return nil, fmt.Errorf("no JSON object in model output")
	}
	var n narrative
	if err := json.Unmarshal([]byte(js), &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// extractJSON returns the substring from the first '{' to the last '}'.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func firstText(resp *providers.ChatResponse) string {
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			return blk.Text
		}
	}
	return ""
}

func costOf(model string, resp *providers.ChatResponse) float64 {
	p := cost.CalcCostUSD(model, resp.Usage.InputTokens, resp.Usage.OutputTokens, 0, 0)
	if p == nil {
		return 0
	}
	return *p
}

func fallbackBLUF(in ReportInput, d types.LPMDeltas) string {
	return fmt.Sprintf("Matter %s health %.0f/100 (%s). Last period: %d new task(s), %d closed, %d new finding(s), %.1fh logged.",
		orDash(in.MatterNumber), in.Health.Score, orDash(string(in.Health.Signal)), d.NewTasks, d.ClosedTasks, d.NewFindings, d.HoursLogged)
}

func openTasks(tasks []types.Task) []types.Task {
	var out []types.Task
	for _, t := range tasks {
		switch t.Status {
		case types.TaskStatusComplete, types.TaskStatusFailed:
			continue
		default:
			out = append(out, t)
		}
	}
	return out
}

func toneSnippet(l *types.LawyerProfile) string {
	if l == nil || l.ToneProfile == nil {
		return ""
	}
	s := strings.TrimSpace(l.ToneProfile.InjectionSnippet)
	if s == "" {
		return ""
	}
	return "ASSIGNED LAWYER TONE PROFILE (match this voice):\n" + s
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func suffixIf(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
