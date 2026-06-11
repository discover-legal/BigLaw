// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// StatusReport generator — Opus + lawyer tone injection.
// markdownToHtml converts basic Markdown to printable HTML.

package reports

import (
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Generator creates client-ready status reports.
type Generator struct {
	provider providers.Provider
	opus     string
}

// New creates a Generator.
func New(provider providers.Provider, opusModel string) *Generator {
	return &Generator{provider: provider, opus: opusModel}
}

// Opts controls report generation.
type Opts struct {
	Format             string // "html" or "markdown"
	IncludeTimeEntries bool
	IncludeBudgetBurn  bool
	CustomNote         string
}

// Generate produces a status report for a task.
func (g *Generator) Generate(
	task types.Task,
	timeEntries []types.TimeEntry,
	budgetBurn *types.BudgetBurn,
	opts Opts,
	lawyer *types.LawyerProfile,
) (*types.StatusReport, error) {
	if opts.Format == "" {
		opts.Format = "markdown"
	}

	// Sort findings by confidence desc, take top 5
	topFindings := make([]types.Finding, len(task.Findings))
	copy(topFindings, task.Findings)
	sort.Slice(topFindings, func(i, j int) bool {
		return topFindings[i].Confidence > topFindings[j].Confidence
	})
	if len(topFindings) > 5 {
		topFindings = topFindings[:5]
	}

	var findingsLines []string
	for i, f := range topFindings {
		content := f.Content
		if len(content) > 400 {
			content = strutil.Truncate(content, 400)
		}
		findingsLines = append(findingsLines, fmt.Sprintf("[%d] (%s, conf %.2f) %s", i+1, f.AgentName, f.Confidence, content))
	}
	findingsBlock := "(none yet)"
	if len(findingsLines) > 0 {
		findingsBlock = strings.Join(findingsLines, "\n")
	}

	var timeBlock, budgetBlock string
	if opts.IncludeTimeEntries {
		totalHours := 0.0
		totalUsd := 0.0
		for _, e := range timeEntries {
			if e.EndedAt != nil {
				totalHours += float64(e.BillingUnits) * 0.1
				if e.BillingAmountUsd != nil {
					totalUsd += *e.BillingAmountUsd
				}
			}
		}
		timeBlock = fmt.Sprintf("TIME SPEND: %.1fh billed ($%.2f USD)", totalHours, totalUsd)
	}
	if opts.IncludeBudgetBurn && budgetBurn != nil {
		budgetBlock = fmt.Sprintf("BUDGET BURN: %.1f%% of $%.0f budget ($%.2f spent, $%.2f remaining)",
			budgetBurn.BurnPct*100, budgetBurn.BudgetUsd, budgetBurn.BurnUsd, budgetBurn.Remaining)
	}

	synthesis := task.Output
	if synthesis == "" {
		synthesis = "(analysis in progress)"
	}
	if len(synthesis) > 3000 {
		synthesis = strutil.Truncate(synthesis, 3000)
	}

	matterNum := task.MatterNumber
	if matterNum == "" {
		matterNum = "—"
	}
	jur := task.Jurisdiction
	if jur == "" {
		jur = "Not specified"
	}

	var contextLines []string
	contextLines = append(contextLines,
		fmt.Sprintf("MATTER: %s — %s", matterNum, task.Description),
		fmt.Sprintf("JURISDICTION: %s", jur),
		fmt.Sprintf("STATUS: %s | Phase: %s", task.Status, task.CurrentPhase),
		"",
		fmt.Sprintf("FINDINGS (%d of %d shown, by confidence):", len(topFindings), len(task.Findings)),
		findingsBlock,
		"",
		"SYNTHESIS:",
		synthesis,
	)
	if timeBlock != "" {
		contextLines = append(contextLines, "", timeBlock)
	}
	if budgetBlock != "" {
		contextLines = append(contextLines, budgetBlock)
	}

	toneInjection := ""
	if lawyer != nil && lawyer.ToneProfile != nil {
		snippet := sanitizePromptContent(lawyer.ToneProfile.InjectionSnippet)
		if snippet != "" {
			toneInjection = "\n" + snippet + "\n"
		}
	}

	noteBlock := ""
	if opts.CustomNote != "" {
		note := sanitizePromptContent(opts.CustomNote)
		if len(note) > 1000 {
			note = strutil.Truncate(note, 1000)
		}
		noteBlock = "PARTNER NOTE (include this verbatim near the top of the report):\n" + note + "\n\n"
	}

	systemPrompt := strings.Join([]string{
		"You are a senior lawyer drafting a client status update.",
		fmt.Sprintf("Write a professional, concise status report in %s format.", opts.Format),
		"Address the client directly. Summarise progress, key findings, next steps.",
		"Use clear headings. Keep it under 500 words unless the matter is complex.",
		"Do NOT reveal internal agent names, tool names, or system architecture details.",
		toneInjection,
	}, "\n")

	userPrompt := noteBlock + strings.Join(contextLines, "\n")

	start := time.Now()
	resp, err := g.provider.Chat(providers.ChatParams{
		Model:       g.opus,
		MaxTokens:   2000,
		System:      systemPrompt,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: userPrompt}},
	})
	if err != nil {
		return nil, fmt.Errorf("status report Opus call failed: %w", err)
	}
	dms := time.Since(start).Milliseconds()

	var cw, cr int
	costUSD := cost.CalcCostUSD(g.opus, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	profileID := ""
	if lawyer != nil {
		profileID = lawyer.ID
	}
	cost.Default.Record(cost.RecordRequest{
		Model: g.opus, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "synthesis", TaskID: task.ID, ProfileID: profileID,
	})

	rawMarkdown := ""
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			rawMarkdown = blk.Text
			break
		}
	}

	generatedAt := time.Now().UTC().Format(time.RFC3339)
	var content string
	if opts.Format == "html" {
		htmlBody := MarkdownToHTML(rawMarkdown)
		humanDate := time.Now().Format("2 January 2006")
		content = wrapHTML(htmlBody, task.MatterNumber, humanDate)
	} else {
		content = rawMarkdown
	}

	wordCount := len(strings.Fields(rawMarkdown))

	costVal := 0.0
	if costUSD != nil {
		costVal = *costUSD
	}

	slog.Info("Status report generated", "taskId", task.ID, "format", opts.Format, "words", wordCount)

	return &types.StatusReport{
		TaskID:       task.ID,
		MatterNumber: task.MatterNumber,
		ClientNumber: task.ClientNumber,
		GeneratedAt:  generatedAt,
		Format:       opts.Format,
		Content:      content,
		WordCount:    wordCount,
		CostUsd:      costVal,
	}, nil
}

// ─── Markdown → HTML ──────────────────────────────────────────────────────────

var h1RE = regexp.MustCompile(`^# (.+)$`)
var h2RE = regexp.MustCompile(`^## (.+)$`)
var h3RE = regexp.MustCompile(`^### (.+)$`)
var hrRE = regexp.MustCompile(`^-{3,}$`)
var ulRE = regexp.MustCompile(`^[-*] (.+)$`)
var olRE = regexp.MustCompile(`^\d+\. (.+)$`)
var boldRE = regexp.MustCompile(`\*\*(.+?)\*\*`)
var italicRE = regexp.MustCompile(`(?:^|[^*])\*(?:[^*])(.+?)(?:[^*])\*(?:[^*]|$)`)
var codeRE = regexp.MustCompile("`([^`]+)`")

// MarkdownToHTML converts simple Markdown to HTML.
func MarkdownToHTML(md string) string {
	lines := strings.Split(md, "\n")
	var parts []string
	inList := false

	for _, line := range lines {
		switch {
		case h1RE.MatchString(line):
			if inList {
				parts = append(parts, "</ul>")
				inList = false
			}
			parts = append(parts, "<h1>"+inlineFormat(line[2:])+"</h1>")
		case h2RE.MatchString(line):
			if inList {
				parts = append(parts, "</ul>")
				inList = false
			}
			parts = append(parts, "<h2>"+inlineFormat(line[3:])+"</h2>")
		case h3RE.MatchString(line):
			if inList {
				parts = append(parts, "</ul>")
				inList = false
			}
			parts = append(parts, "<h3>"+inlineFormat(line[4:])+"</h3>")
		case hrRE.MatchString(strings.TrimSpace(line)):
			if inList {
				parts = append(parts, "</ul>")
				inList = false
			}
			parts = append(parts, "<hr>")
		case ulRE.MatchString(line):
			if !inList {
				parts = append(parts, "<ul>")
				inList = true
			}
			content := line[2:]
			if line[0] == '*' {
				content = line[2:]
			}
			parts = append(parts, "<li>"+inlineFormat(content)+"</li>")
		case olRE.MatchString(line):
			if inList {
				parts = append(parts, "</ul>")
				inList = false
			}
			parts = append(parts, "<p>"+inlineFormat(line)+"</p>")
		case strings.TrimSpace(line) == "":
			if inList {
				parts = append(parts, "</ul>")
				inList = false
			}
		default:
			if inList {
				parts = append(parts, "</ul>")
				inList = false
			}
			parts = append(parts, "<p>"+inlineFormat(line)+"</p>")
		}
	}
	if inList {
		parts = append(parts, "</ul>")
	}
	return strings.Join(parts, "\n")
}

func inlineFormat(text string) string {
	escaped := html.EscapeString(text)
	escaped = boldRE.ReplaceAllString(escaped, "<strong>$1</strong>")
	escaped = codeRE.ReplaceAllString(escaped, "<code>$1</code>")
	return escaped
}

func wrapHTML(content, matterNumber, date string) string {
	title := "Matter Status Update"
	if matterNumber != "" {
		title = "Matter Status Update — " + matterNumber
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>%s</title>
<style>
  body { font-family: Georgia, serif; max-width: 800px; margin: 40px auto; color: #1a1a1a; line-height: 1.6; }
  h1 { font-size: 1.4em; border-bottom: 2px solid #1a1a1a; padding-bottom: 8px; }
  h2 { font-size: 1.1em; margin-top: 24px; }
  .footer { margin-top: 40px; font-size: 0.8em; color: #888; border-top: 1px solid #ddd; padding-top: 8px; }
</style></head>
<body>
%s
<div class="footer">Generated %s · Big Michael · CONFIDENTIAL — ATTORNEY-CLIENT PRIVILEGE</div>
</body></html>`, html.EscapeString(title), content, html.EscapeString(date))
}

// sanitizePromptContent strips injection markers and control characters from user content.
func sanitizePromptContent(s string) string {
	replacers := [][2]string{
		{"FINDING:", "[FINDING:]"},
		{"END_FINDING", "[END_FINDING]"},
		{"NO_FINDINGS", "[NO_FINDINGS]"},
		{"NO_CHALLENGE", "[NO_CHALLENGE]"},
	}
	for _, r := range replacers {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	var b strings.Builder
	for _, r := range s {
		if r < 0x08 || (r >= 0x0b && r <= 0x1f && r != '\n' && r != '\t') || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
