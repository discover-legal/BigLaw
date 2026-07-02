// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// `biglaw demo` — the 60-second end-to-end showcase. Seeds a synthetic credit
// agreement matter, then walks four beats entirely in-process (no Docker, no
// TypeDB, no REST server), driving the document tools through the same
// registry the agents use:
//
//	1. Seed          — ingest the sample agreement + write a demo playbook
//	2. Tabular review — a RAG-flagged extraction grid over the agreement
//	3. CP checklist   — a landscape Word conditions-precedent checklist
//	4. Counter-redline — opposing counsel marks up a clause sheet; BigLaw
//	   judges every tracked change against the playbook and answers with
//	   its own tracked changes + decision cards
//
// Every artifact lands in the configured document output directory with a
// deterministic demo- name, so re-runs overwrite instead of littering. A
// failed model call degrades that beat and the tour continues.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/joho/godotenv"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/secrets"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/tools"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// demoTaskID scopes every model call the demo makes, so the closing cost
// summary covers exactly this run.
const demoTaskID = "biglaw-demo"

// demoRequested reports whether argv asks for the demo subcommand.
func demoRequested(args []string) bool {
	return len(args) > 1 && args[1] == "demo"
}

// runDemo is the argv entry point: environment → config → guided tour.
func runDemo() int {
	_ = godotenv.Load()
	secrets.Load()
	cfg := config.Load()
	if err := config.GuardVendors(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	return runDemoWithConfig(cfg, os.Stdout, demoColorEnabled())
}

// runDemoWithConfig runs the tour against an explicit config — the testable
// seam (tests point cfg at an httptest fake provider and a temp output dir).
func runDemoWithConfig(cfg *config.Config, out io.Writer, color bool) int {
	// ── Fail fast when no model provider is reachable — before seeding. ──
	provReg := providers.NewRegistry(cfg)
	if msg := demoProviderProblem(cfg, provReg); msg != "" {
		fmt.Fprintln(out, msg)
		return 1
	}

	root, err := filepath.Abs(cfg.PDF.OutputDir)
	if err == nil {
		err = os.MkdirAll(root, 0o755)
	}
	if err != nil {
		fmt.Fprintf(out, "biglaw demo: cannot prepare output directory %q: %v\n", cfg.PDF.OutputDir, err)
		return 1
	}

	// The demo NEVER touches a user playbook file: the cascade is pointed at
	// a demo-owned file inside the output directory instead.
	cfg.Persistence.PlaybooksFile = filepath.Join(root, "demo-playbook.json")

	// Fresh, run-scoped cost ledger (truncated so re-runs start at zero). The
	// file is pre-created empty because cost.Store.Init only starts its disk
	// writer when the ledger file already exists (fresh-file early return in
	// internal/cost — worth fixing there; worked around here).
	costFile := filepath.Join(root, "demo-costs.jsonl")
	costs := cost.Default
	if ierr := os.WriteFile(costFile, nil, 0o644); ierr != nil {
		fmt.Fprintf(out, "  (cost ledger unavailable: %v)\n", ierr)
	} else if ierr := costs.Init(costFile); ierr != nil {
		fmt.Fprintf(out, "  (cost ledger unavailable: %v)\n", ierr)
	}
	defer costs.Close()

	bold := demoStyle(color, "1")
	fmt.Fprintf(out, "\n%s\n", bold("BigLaw demo — a sample matter in four beats"))
	fmt.Fprintf(out, "Artifacts: %s\n", root)

	var artifacts []demoArtifact
	addArtifact := func(label, path string) {
		if path != "" {
			artifacts = append(artifacts, demoArtifact{label, path})
		}
	}

	// ── Beat 1: seed the matter ─────────────────────────────────────────────
	demoBeat(out, color, 1, "Seed — a synthetic $45M revolving credit facility")
	ks := knowledge.NewStore(embeddings.NewClient(cfg))
	if _, err := ks.Ingest(context.Background(), types.Document{
		ID:           demoAgreementDocID,
		Title:        demoAgreementTitle,
		Content:      demoAgreementText,
		DocumentType: "credit_agreement",
		Source:       "biglaw-demo",
	}); err != nil {
		fmt.Fprintf(out, "  !! could not seed the sample agreement: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "  Ingested %q\n", demoAgreementTitle)
	fmt.Fprintf(out, "  (%d words: Meridian Data Systems borrows $45,000,000 from First Harbor Bank at SOFR + 2.75%%)\n",
		len(strings.Fields(demoAgreementText)))
	if err := writeDemoPlaybook(cfg.Persistence.PlaybooksFile); err != nil {
		fmt.Fprintf(out, "  !! could not write the demo playbook: %v\n", err)
	} else {
		fmt.Fprintf(out, "  Playbook written: 2 firm positions (indemnification cap, notice period)\n")
		addArtifact("Demo playbook (firm positions)", cfg.Persistence.PlaybooksFile)
	}

	toolReg := tools.NewRegistry(cfg, provReg, costs, nil, nil)
	toolCtx := agents.ToolContext{
		KnowledgeStore: knowledge.NewAdapter(ks),
		TaskID:         demoTaskID,
	}

	// ── Beat 2: tabular review ──────────────────────────────────────────────
	demoBeat(out, color, 2, "Tabular review — one model call per cell, RAG-flagged")
	review, err := execDemoTool(toolReg, "tabular_review", map[string]interface{}{
		"documentIds": []interface{}{demoAgreementDocID},
		"columns":     demoReviewColumns(),
	}, toolCtx)
	switch {
	case err != nil:
		fmt.Fprintf(out, "  !! tabular review failed: %v — continuing\n", err)
	case review["error"] != nil:
		fmt.Fprintf(out, "  !! tabular review failed: %v — continuing\n", review["error"])
	default:
		renderReviewGrid(out, parseReviewGrid(review), color)
		if path, _ := review["outputPath"].(string); path != "" {
			renamed := filepath.Join(filepath.Dir(path), "demo-tabular-review.docx")
			if rerr := os.Rename(path, renamed); rerr == nil {
				path = renamed
			}
			fmt.Fprintf(out, "  Landscape matrix: %s\n", path)
			addArtifact("Tabular review matrix (landscape .docx)", path)
		}
		if id, _ := review["reviewId"].(string); id != "" {
			fmt.Fprintf(out, "  (with the server running, this grid is also GET /reviews/%s/table.csv)\n", id)
		}
	}

	// ── Beat 3: CP checklist ────────────────────────────────────────────────
	demoBeat(out, color, 3, "Document production — conditions-precedent checklist")
	cp, err := execDemoTool(toolReg, "docx_generate", demoCPChecklistInput(), toolCtx)
	if err != nil {
		fmt.Fprintf(out, "  !! docx generation failed: %v — continuing\n", err)
	} else if path, _ := cp["outputPath"].(string); path != "" {
		fmt.Fprintf(out, "  3 CP categories, four-column tracking tables (Index / Clause Number / Clause / Status), landscape\n")
		fmt.Fprintf(out, "  Checklist: %s\n", path)
		addArtifact("CP checklist (landscape .docx)", path)
	}

	// ── Beat 4: the counter-redline loop ────────────────────────────────────
	demoBeat(out, color, 4, "Counter-redline — opposing markup, judged against the playbook")
	demoCounterRedline(out, color, toolReg, toolCtx, addArtifact)

	// ── Summary ─────────────────────────────────────────────────────────────
	fmt.Fprintf(out, "\n%s\n", bold("Done. Open the artifacts in Word:"))
	for _, a := range artifacts {
		fmt.Fprintf(out, "  %-46s %s\n", a.label, a.path)
	}
	demoCostSummary(out, costs)
	fmt.Fprintf(out, "\n%s\n", bold("Next steps"))
	fmt.Fprintln(out, "  go run ./biglaw-go/cmd/biglaw            # full platform — REST API on :3101")
	fmt.Fprintln(out, "  cd ui && npm run dev                     # web workbench on :5173")
	fmt.Fprintln(out, "  Open this folder in Claude Code          # .mcp.json exposes submit_task & friends")
	return 0
}

// demoCounterRedline runs beat 4: base clause sheet → opposing tracked
// changes → playbook-judged response with decision cards → integrity check.
func demoCounterRedline(out io.Writer, color bool, toolReg *tools.Registry, toolCtx agents.ToolContext, addArtifact func(label, path string)) {
	base, err := execDemoTool(toolReg, "docx_generate", demoBaseClausesInput(), toolCtx)
	if err != nil {
		fmt.Fprintf(out, "  !! could not generate the base clause sheet: %v — skipping beat\n", err)
		return
	}
	basePath, _ := base["outputPath"].(string)
	if basePath == "" {
		fmt.Fprintf(out, "  !! base clause sheet has no output path — skipping beat\n")
		return
	}
	fmt.Fprintf(out, "  Base clause sheet: %s\n", basePath)
	addArtifact("Base clause sheet (.docx)", basePath)

	redline, err := execDemoTool(toolReg, "edit_document", demoOpposingEditsInput(basePath), toolCtx)
	if err != nil || redline["ok"] != true {
		fmt.Fprintf(out, "  !! opposing markup failed: %v %v — skipping beat\n", err, redline["error"])
		return
	}
	redlinePath, _ := redline["outputPath"].(string)
	fmt.Fprintf(out, "  \"Opposing Counsel\" applied %v tracked changes (1 market tweak, 2 that cross firm red lines)\n",
		redline["appliedCount"])
	fmt.Fprintf(out, "  Their markup: %s\n", redlinePath)
	addArtifact("Opposing counsel's markup (.docx)", redlinePath)

	resp, err := execDemoTool(toolReg, "respond_to_redline", map[string]interface{}{
		"path":   redlinePath,
		"author": "Big Michael",
	}, toolCtx)
	switch {
	case err != nil:
		fmt.Fprintf(out, "  !! respond_to_redline failed: %v — continuing\n", err)
	case resp["ok"] != true:
		fmt.Fprintf(out, "  !! respond_to_redline failed: %v — continuing\n", resp["error"])
	default:
		fmt.Fprintln(out, "")
		renderDecisionCards(out, resp, color)
		if path, _ := resp["outputPath"].(string); path != "" {
			fmt.Fprintf(out, "  Response redline: %s\n", path)
			addArtifact("BigLaw's counter-redline (.docx)", path)
		}
	}

	// One-line trust beat: verify the inbound markup carries no silent edits
	// or Unicode obfuscation (available whenever the integrity tool is wired).
	if toolReg.Has("check_document_integrity") && redlinePath != "" {
		integ, ierr := execDemoTool(toolReg, "check_document_integrity", map[string]interface{}{
			"path":               redlinePath,
			"prior_version_path": basePath,
		}, toolCtx)
		if ierr == nil && integ["ok"] == true {
			if summary, _ := integ["summary"].(string); summary != "" {
				fmt.Fprintf(out, "  Integrity: %s\n", summary)
			}
		}
	}
}

// ─── Provider preflight ───────────────────────────────────────────────────────

// hostedDefaultDomains are stack endpoints that always require an API key; a
// default URL pointing at one of these with no key means "nothing configured".
var hostedDefaultDomains = []string{"dashscope", "aliyuncs.com", "bigmodel.cn", "moonshot.ai"}

// demoProviderProblem returns a friendly, actionable message when no usable
// model provider is configured, or "" when the demo can proceed.
func demoProviderProblem(cfg *config.Config, provReg *providers.Registry) string {
	const help = `biglaw demo needs a model provider. Set one of these and re-run:

  QWEN_API_KEY=...                    hosted Qwen / DashScope (the default stack)
  MODEL_STACK=glm|kimi + its API key  another hosted stack
  PRIMARY_MODEL_URL=... (+ _KEY)      any OpenAI-compatible endpoint
  LOCAL_INFERENCE_URL=...             LM Studio / vLLM / llama.cpp - free, local
  OLLAMA_ENABLED=true                 Ollama - free, local

Costs a few cents on a hosted stack; free on local models.`

	for _, taskType := range []routing.TaskType{routing.TaskExtraction, routing.TaskDrafting} {
		model := routing.SelectModel(cfg, routing.SelectParams{TaskType: taskType})
		if _, err := provReg.Get(model); err != nil {
			return fmt.Sprintf("biglaw demo: %v\n\n%s", err, help)
		}
		// A hosted default endpoint without a key resolves to a provider but
		// every call would 401 — catch it here, before anything is seeded.
		if !routing.IsLocalModel(model) && !routing.IsOllamaModel(model) && cfg.Model.PrimaryKey == "" {
			for _, dom := range hostedDefaultDomains {
				if strings.Contains(strings.ToLower(cfg.Model.PrimaryURL), dom) {
					return fmt.Sprintf("biglaw demo: the default %s endpoint needs an API key, and none is set.\n\n%s",
						dom, help)
				}
			}
		}
	}
	return ""
}

// ─── Playbook seeding ─────────────────────────────────────────────────────────

// writeDemoPlaybook persists the demo playbook to its own file (the same JSON
// array shape the playbook store reads).
func writeDemoPlaybook(path string) error {
	data, err := json.MarshalIndent(demoPlaybooks(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ─── Tool plumbing ────────────────────────────────────────────────────────────

// execDemoTool drives one tool through the registry and normalises the result
// to a plain map via a JSON round-trip, so the demo depends only on each
// tool's wire shape — never on internal result types that may be evolving.
func execDemoTool(reg *tools.Registry, name string, input map[string]interface{}, ctx agents.ToolContext) (map[string]interface{}, error) {
	raw, err := reg.Execute(name, input, ctx)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%s returned an unmarshalable result: %w", name, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s returned a non-object result: %w", name, err)
	}
	return m, nil
}

// ─── Terminal rendering ───────────────────────────────────────────────────────

type demoArtifact struct{ label, path string }

// demoGridCell / demoGridRow are the terminal-facing view of a review matrix.
type demoGridCell struct{ Column, Flag, Summary string }

type demoGridRow struct {
	Document string
	Cells    []demoGridCell
}

// parseReviewGrid lifts the tabular_review result map into grid rows.
func parseReviewGrid(res map[string]interface{}) []demoGridRow {
	rowsRaw, _ := res["rows"].([]interface{})
	rows := make([]demoGridRow, 0, len(rowsRaw))
	for _, rr := range rowsRaw {
		rm, ok := rr.(map[string]interface{})
		if !ok {
			continue
		}
		row := demoGridRow{Document: str(rm["document"])}
		cellsRaw, _ := rm["cells"].([]interface{})
		for _, cr := range cellsRaw {
			cm, ok := cr.(map[string]interface{})
			if !ok {
				continue
			}
			summary := stripCitations(str(cm["summary"]))
			// A failed cell carries its cause in "reasoning" — surface it so a
			// degraded beat still explains itself on screen.
			if summary == "Extraction failed" {
				if reason := str(cm["reasoning"]); reason != "" {
					summary += " — " + reason
				}
			}
			row.Cells = append(row.Cells, demoGridCell{
				Column:  str(cm["column"]),
				Flag:    str(cm["flag"]),
				Summary: summary,
			})
		}
		rows = append(rows, row)
	}
	return rows
}

// renderReviewGrid prints the matrix, one block per document, each cell as an
// aligned "column [flag] summary" line with the flag colorized.
func renderReviewGrid(out io.Writer, rows []demoGridRow, color bool) {
	for _, row := range rows {
		fmt.Fprintf(out, "  Document: %s\n", strutil.Truncate(row.Document, 90))
		width := 0
		for _, c := range row.Cells {
			if len(c.Column) > width {
				width = len(c.Column)
			}
		}
		for _, c := range row.Cells {
			fmt.Fprintf(out, "    %-*s  %s  %s\n",
				width, c.Column, flagTag(c.Flag, color), oneLine(c.Summary, 110))
		}
	}
}

// renderDecisionCards prints one card per opposing tracked change from a
// respond_to_redline result.
func renderDecisionCards(out io.Writer, resp map[string]interface{}, color bool) {
	decisions, _ := resp["decisions"].([]interface{})
	for i, dr := range decisions {
		d, ok := dr.(map[string]interface{})
		if !ok {
			continue
		}
		change := str(d["insertedText"])
		if del := str(d["deletedText"]); del != "" {
			if change != "" {
				change = fmt.Sprintf("%q -> %q", oneLine(del, 40), oneLine(change, 40))
			} else {
				change = fmt.Sprintf("deleted %q", oneLine(del, 60))
			}
		} else if change != "" {
			change = fmt.Sprintf("inserted %q", oneLine(change, 60))
		}
		fmt.Fprintf(out, "  Change %d by %s  %s\n", i+1, str(d["author"]), change)
		if ct := str(d["clauseType"]); ct != "" {
			tier := str(d["playbookTier"])
			if tier == "" {
				tier = "no playbook position - market standard"
			} else {
				tier += " playbook"
			}
			fmt.Fprintf(out, "    clause: %s (%s)\n", ct, tier)
		}
		fmt.Fprintf(out, "    %s %s\n", dispositionTag(str(d["disposition"]), color), oneLine(str(d["rationale"]), 130))
		if ctext := str(d["counterText"]); ctext != "" {
			fmt.Fprintf(out, "    counter-language: %q\n", oneLine(ctext, 110))
		}
	}
	if counts, ok := resp["counts"].(map[string]interface{}); ok {
		fmt.Fprintf(out, "  Dispositions: %v accepted / %v rejected / %v countered / %v for review\n",
			counts["accepted"], counts["rejected"], counts["countered"], counts["review"])
	}
}

// demoCostSummary prints this run's model spend from the cost ledger.
func demoCostSummary(out io.Writer, costs *cost.Store) {
	entries := costs.ForTask(demoTaskID)
	if len(entries) == 0 {
		fmt.Fprintln(out, "\nModel spend: no billable model calls recorded this run.")
		return
	}
	sum := costs.Summarise(entries)
	line := fmt.Sprintf("\nModel spend: %d calls, %s in / %s out tokens, $%.4f",
		sum.EntryCount, demoThousands(sum.TotalInputTokens), demoThousands(sum.TotalOutputTokens), sum.TotalUSD)
	if sum.TotalWh > 0 {
		line += fmt.Sprintf(" (+%.1f Wh local inference)", sum.TotalWh)
	}
	fmt.Fprintln(out, line)
}

// demoThousands renders 12345 as "12,345".
func demoThousands(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// ─── Small formatting helpers ─────────────────────────────────────────────────

func demoBeat(out io.Writer, color bool, n int, title string) {
	bold := demoStyle(color, "1")
	fmt.Fprintf(out, "\n%s\n", bold(fmt.Sprintf("[%d/4] %s", n, title)))
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}

var citationMarker = regexp.MustCompile(`\s*\[\[[^\]]*\]\]`)

// stripCitations removes [[page:N||quote:...]] markers for terminal display
// (they remain in the .docx artifact, where they belong).
func stripCitations(s string) string {
	return strings.TrimSpace(citationMarker.ReplaceAllString(s, ""))
}

// oneLine collapses whitespace and truncates at a rune boundary for display.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = strings.TrimRight(strutil.Truncate(s, max), " ") + "…"
	}
	return s
}

// demoColorEnabled turns ANSI color on for interactive terminals only.
func demoColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// demoStyle returns a function wrapping text in the given ANSI SGR code, or
// the identity when color is off.
func demoStyle(color bool, code string) func(string) string {
	if !color {
		return func(s string) string { return s }
	}
	return func(s string) string { return "\x1b[" + code + "m" + s + "\x1b[0m" }
}

var flagSGR = map[string]string{"green": "32", "grey": "90", "yellow": "33", "red": "31"}

// flagTag renders a review flag as a fixed-width colored tag, e.g. "[green ]".
func flagTag(flag string, color bool) string {
	tag := fmt.Sprintf("[%-6s]", flag)
	if code, ok := flagSGR[flag]; ok {
		return demoStyle(color, code)(tag)
	}
	return tag
}

var dispositionSGR = map[string]string{"accept": "32", "reject": "31", "counter": "33", "review": "90"}

// dispositionTag renders a negotiation disposition as a colored upper-case tag.
func dispositionTag(d string, color bool) string {
	tag := fmt.Sprintf("%-8s", strings.ToUpper(d))
	if code, ok := dispositionSGR[d]; ok {
		return demoStyle(color, code)(tag)
	}
	return tag
}
