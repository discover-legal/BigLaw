// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Portfolio BLUF briefing — the 0600 five-minute digest. It rolls the latest
// per-matter status reports out of the corpus into one partner-facing view,
// worst-health first, with a single bottom-line-up-front a senior partner can
// absorb in seconds. Like the per-matter report, the facts (health counts, the
// worst matters) are deterministic; the model only writes the overall BLUF over
// them.
package lpm

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// PortfolioMatterLine summarises one matter in the portfolio view.
type PortfolioMatterLine struct {
	MatterNumber   string  `json:"matterNumber"`
	ClientNumber   string  `json:"clientNumber,omitempty"`
	HealthScore    float64 `json:"healthScore"`
	HealthSignal   string  `json:"healthSignal"`
	BLUF           string  `json:"bluf,omitempty"`
	TopRisk        string  `json:"topRisk,omitempty"`
	LastReportDate string  `json:"lastReportDate,omitempty"`
}

// PortfolioBriefing is the rolled-up daily digest across a set of matters.
type PortfolioBriefing struct {
	GeneratedAt string                `json:"generatedAt"`
	Date        string                `json:"date"`
	MatterCount int                   `json:"matterCount"`
	Green       int                   `json:"green"`
	Amber       int                   `json:"amber"`
	Red         int                   `json:"red"`
	BLUF        string                `json:"bluf"`
	Matters     []PortfolioMatterLine `json:"matters"`
	CostUsd     float64               `json:"costUsd"`
}

// PortfolioBriefer builds portfolio briefings from the report corpus.
type PortfolioBriefer struct {
	provider providers.Provider
	model    string
}

// NewPortfolioBriefer builds a briefer that writes the overall BLUF with model.
func NewPortfolioBriefer(provider providers.Provider, model string) *PortfolioBriefer {
	return &PortfolioBriefer{provider: provider, model: model}
}

// Generate builds the briefing for the given matters as of date, reading each
// matter's most recent report from the corpus.
func (b *PortfolioBriefer) Generate(matters []MatterRef, corpus *Corpus, date string) (*PortfolioBriefing, error) {
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	br := &PortfolioBriefing{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Date:        date,
		MatterCount: len(matters),
	}

	for _, m := range matters {
		line := PortfolioMatterLine{MatterNumber: m.MatterNumber, ClientNumber: m.ClientNumber}
		if rep, _ := corpus.Latest(m.MatterNumber); rep != nil {
			line.HealthScore = rep.HealthScore
			line.HealthSignal = rep.HealthSignal
			line.BLUF = rep.BLUF
			line.LastReportDate = rep.Date
			line.TopRisk = topRisk(rep.Risks)
		} else {
			line.HealthSignal = "unknown"
		}
		switch line.HealthSignal {
		case "green":
			br.Green++
		case "amber":
			br.Amber++
		case "red":
			br.Red++
		}
		br.Matters = append(br.Matters, line)
	}

	// Worst health first so the partner sees the fires at the top.
	sort.SliceStable(br.Matters, func(i, j int) bool {
		return br.Matters[i].HealthScore < br.Matters[j].HealthScore
	})

	br.BLUF, br.CostUsd = b.overallBLUF(br)
	if br.BLUF == "" {
		br.BLUF = fallbackPortfolioBLUF(br)
	}
	return br, nil
}

func (b *PortfolioBriefer) overallBLUF(br *PortfolioBriefing) (string, float64) {
	if b.provider == nil {
		return "", 0
	}
	var facts strings.Builder
	fmt.Fprintf(&facts, "Portfolio of %d matters: %d red, %d amber, %d green.\n", br.MatterCount, br.Red, br.Amber, br.Green)
	facts.WriteString("Worst matters:\n")
	for i, m := range br.Matters {
		if i >= 6 {
			break
		}
		fmt.Fprintf(&facts, "- %s (%.0f, %s): %s\n", m.MatterNumber, m.HealthScore, orDash(m.HealthSignal), truncate(m.BLUF, 160))
	}

	system := strings.Join([]string{
		"You are a legal project manager writing a partner's 0600 portfolio briefing.",
		"Write ONE bottom-line-up-front paragraph (<=4 sentences) over the FACTS only.",
		"Lead with what needs the partner's attention today. Never invent matters or numbers.",
		"Respond with plain text only — no preamble, no headings.",
	}, "\n")
	resp, err := b.provider.Chat(providers.ChatParams{
		Model: b.model, MaxTokens: 300, System: system, CacheSystem: true,
		Messages: []providers.Message{{Role: "user", Content: "FACTS:\n" + facts.String()}},
	})
	if err != nil {
		return "", 0
	}
	v := costOf(b.model, resp)
	cost.Default.Record(cost.RecordRequest{
		Model: b.model, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CostUSD: &v, DurationMs: resp.DurationMs, Context: "lpm_portfolio_briefing",
	})
	return strings.TrimSpace(firstText(resp)), v
}

// topRisk returns the description of the highest-severity risk, if any.
func topRisk(risks []types.LPMRisk) string {
	rank := map[string]int{"high": 3, "medium": 2, "low": 1}
	best := ""
	bestRank := 0
	for _, r := range risks {
		if rk := rank[strings.ToLower(r.Severity)]; rk > bestRank {
			bestRank = rk
			best = r.Description
		}
	}
	return best
}

func fallbackPortfolioBLUF(br *PortfolioBriefing) string {
	worst := "—"
	if len(br.Matters) > 0 {
		worst = br.Matters[0].MatterNumber
	}
	return fmt.Sprintf("%d matters in view: %d red, %d amber, %d green. Worst: %s.",
		br.MatterCount, br.Red, br.Amber, br.Green, worst)
}

// ─── Renderers ──────────────────────────────────────────────────────────────

// RenderPortfolioMarkdown renders the briefing as Markdown.
func RenderPortfolioMarkdown(br *PortfolioBriefing) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Portfolio Briefing — %s\n\n", br.Date)
	fmt.Fprintf(&b, "*%d matters · %d red / %d amber / %d green*\n\n", br.MatterCount, br.Red, br.Amber, br.Green)
	fmt.Fprintf(&b, "**Bottom line:** %s\n\n", br.BLUF)
	b.WriteString("## Matters (worst first)\n\n")
	for _, m := range br.Matters {
		fmt.Fprintf(&b, "- **%s** (%.0f, %s)", m.MatterNumber, m.HealthScore, orDash(m.HealthSignal))
		if m.BLUF != "" {
			fmt.Fprintf(&b, " — %s", m.BLUF)
		}
		if m.TopRisk != "" {
			fmt.Fprintf(&b, " _(risk: %s)_", m.TopRisk)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n---\n*Generated by Big Michael · CONFIDENTIAL — ATTORNEY-CLIENT PRIVILEGE*\n")
	return b.String()
}

// RenderPortfolioDOCX renders the briefing as a .docx.
func RenderPortfolioDOCX(br *PortfolioBriefing) ([]byte, error) {
	d := &docxBuilder{}
	d.Heading(1, "Portfolio Briefing — "+br.Date)
	d.Para(fmt.Sprintf("%d matters  ·  %d red / %d amber / %d green", br.MatterCount, br.Red, br.Amber, br.Green))
	d.Heading(2, "Bottom line")
	d.Para(br.BLUF)
	d.Heading(2, "Matters (worst first)")
	for _, m := range br.Matters {
		line := fmt.Sprintf("%s (%.0f, %s)", m.MatterNumber, m.HealthScore, orDash(m.HealthSignal))
		if m.BLUF != "" {
			line += " — " + m.BLUF
		}
		d.Bullet(line)
	}
	d.Para("")
	d.Para("Generated by Big Michael · CONFIDENTIAL — ATTORNEY-CLIENT PRIVILEGE")
	return d.Bytes()
}
