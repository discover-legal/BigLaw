// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Tabular review — a document × column extraction matrix. Each column poses a
// question/field; every document × column cell is answered by one extraction-tier
// model call that returns a cited value, a RAG flag (green/grey/yellow/red), and
// reasoning. Completed matrices persist in an in-memory store keyed by reviewId
// so read_table_cells can slice them later in the same run.

package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

const (
	// maxReviewDocs caps the number of rows (documents) per review.
	maxReviewDocs = 50
	// maxReviewColumns caps the number of extraction columns per review.
	maxReviewColumns = 30
	// maxReviewDocChars caps how much of each document is sent per cell call.
	maxReviewDocChars = 120_000
	// reviewCellMaxTokens bounds each cell's model response (summary + reasoning).
	reviewCellMaxTokens = 1200
)

// reviewFlagLegend defines the four RAG flags. It doubles as the validity set
// for parsed flags and as the legend returned with every matrix.
var reviewFlagLegend = map[string]string{
	"green":  "clearly addressed; favourable or unproblematic",
	"grey":   "not addressed / not found in the document",
	"yellow": "present but qualified, unusual, or needs review",
	"red":    "problematic, onerous, or non-market",
}

// tabularExtractionPrompt is the per-cell system prompt. Wording authored fresh
// for this implementation (clean-room spec §5.1).
const tabularExtractionPrompt = `You extract one specific field from one legal document for a tabular review.

Respond with a single JSON object and nothing else — no prose before or after it, no markdown code fences:
{"summary": "...", "flag": "...", "reasoning": "..."}

"flag" must be exactly one of:
  "green"  — the field is clearly addressed and the position is favourable or unproblematic
  "grey"   — the document does not address the field / nothing relevant was found
  "yellow" — the field is present but qualified, unusual, or warrants reviewer attention
  "red"    — the field is problematic, onerous, or off-market

"summary" carries ONLY the extracted value — no explanation, no commentary. Immediately after every factual claim in the summary, attach a citation of the form [[page:N||quote:...]] where N is the page number and the quote is a short verbatim excerpt copied exactly from the document — at most 25 words, scoped narrowly to that one claim. Give each claim its own tight quote; never stretch one long quote across several claims.

All explanation, caveats, and the justification for your flag belong in "reasoning", never in "summary".

If the document does not contain the requested information, set "summary" to "Not Found" and "flag" to "grey".`

// ─── In-memory review store ─────────────────────────────────────────────────

type reviewCell struct {
	Column    string `json:"column"`
	Summary   string `json:"summary"`
	Flag      string `json:"flag"`
	Reasoning string `json:"reasoning"`
}

type reviewRow struct {
	DocumentID string       `json:"documentId"`
	Document   string       `json:"document"`
	Cells      []reviewCell `json:"cells"`
}

type storedReview struct {
	Columns []string
	Rows    []reviewRow
}

var (
	reviewStoreMu sync.Mutex
	reviewStore   = map[string]*storedReview{}
)

// ─── Registration ───────────────────────────────────────────────────────────

func (r *Registry) registerTabularTools() {
	r.Register(r.tabularReviewTool())
	r.Register(r.readTableCellsTool())
}

// ─── tabular_review ─────────────────────────────────────────────────────────

type reviewColumn struct {
	Name   string
	Prompt string
}

func (r *Registry) tabularReviewTool() *ToolImpl {
	return &ToolImpl{
		Name: "tabular_review",
		Schema: providers.ToolParam{
			Name: "tabular_review",
			Description: "Run a tabular review across one or more knowledge-store documents. " +
				"Each column is a question/field to extract; every document × column cell gets a cited answer, " +
				"a green/grey/yellow/red flag, and reasoning. Suited to due-diligence grids, CP checklists, " +
				"and side-by-side comparisons. Returns the full matrix plus a reviewId for later slicing with read_table_cells.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"documentIds": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Knowledge-store document IDs — one row per document (max 50)",
					},
					"columns": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"name":   map[string]interface{}{"type": "string", "description": "Column name"},
								"prompt": map[string]interface{}{"type": "string", "description": "What to extract for this column"},
							},
							"required": []string{"name", "prompt"},
						},
						"description": "Extraction columns (max 30)",
					},
				},
				"required": []string{"documentIds", "columns"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			fail := func(msg string) (interface{}, error) {
				return map[string]interface{}{"error": msg, "rows": []interface{}{}}, nil
			}
			docIDs := strListInput(input["documentIds"])
			cols := parseReviewColumns(input["columns"])
			if len(docIDs) == 0 {
				return fail("tabular_review requires a non-empty documentIds array of knowledge-store document IDs")
			}
			if len(cols) == 0 {
				return fail("tabular_review requires a non-empty columns array; each column needs a name and a prompt")
			}
			if ctx.KnowledgeStore == nil {
				return fail("knowledge store unavailable — ingest documents before running a tabular review")
			}
			if len(docIDs) > maxReviewDocs {
				docIDs = docIDs[:maxReviewDocs]
			}
			if len(cols) > maxReviewColumns {
				cols = cols[:maxReviewColumns]
			}
			if r.provReg == nil {
				return fail("no model provider configured for tabular review")
			}
			model := routing.SelectModel(r.cfg, routing.SelectParams{TaskType: routing.TaskExtraction})
			prov, err := r.provReg.Get(model)
			if err != nil {
				return fail(fmt.Sprintf("no model provider available: %v", err))
			}

			colNames := make([]string, len(cols))
			for i, c := range cols {
				colNames[i] = c.Name
			}

			tally := map[string]int{}
			for flag := range reviewFlagLegend {
				tally[flag] = 0
			}

			rows := make([]reviewRow, 0, len(docIDs))
			for _, docID := range docIDs {
				row := reviewRow{DocumentID: docID, Document: reviewDocTitle(ctx.KnowledgeStore, docID)}
				text, terr := ctx.KnowledgeStore.GetFullText(docID)
				if terr != nil || strings.TrimSpace(text) == "" {
					// Missing document: a full grey row, never an abort.
					for _, c := range cols {
						row.Cells = append(row.Cells, reviewCell{
							Column:    c.Name,
							Summary:   "Document not found",
							Flag:      "grey",
							Reasoning: fmt.Sprintf("document %q is not in the knowledge store", docID),
						})
					}
				} else {
					text = truncateAtWord(text, maxReviewDocChars)
					// One model call per cell; a row's cells run concurrently.
					cells := make([]reviewCell, len(cols))
					var wg sync.WaitGroup
					for i, c := range cols {
						wg.Add(1)
						go func(i int, c reviewColumn) {
							defer wg.Done()
							cells[i] = r.extractReviewCell(prov, model, docID, text, c, ctx.TaskID)
						}(i, c)
					}
					wg.Wait()
					row.Cells = cells
				}
				for _, cell := range row.Cells {
					tally[cell.Flag]++
				}
				rows = append(rows, row)
			}

			reviewID := uuid.New().String()
			reviewStoreMu.Lock()
			reviewStore[reviewID] = &storedReview{Columns: colNames, Rows: rows}
			reviewStoreMu.Unlock()

			return map[string]interface{}{
				"reviewId":  reviewID,
				"columns":   colNames,
				"rows":      rows,
				"flagTally": tally,
				"legend":    reviewFlagLegend,
			}, nil
		},
	}
}

// extractReviewCell runs the single model call for one document × column cell.
// Any failure — transport, garbled output, missing payload — degrades to a grey
// "Extraction failed" cell so one bad cell never aborts the matrix.
func (r *Registry) extractReviewCell(prov providers.Provider, modelID, docID, docText string, col reviewColumn, taskID string) reviewCell {
	failed := func(cause string) reviewCell {
		return reviewCell{Column: col.Name, Summary: "Extraction failed", Flag: "grey", Reasoning: cause}
	}
	user := fmt.Sprintf("FIELD: %s\nQUESTION: %s\n\nDOCUMENT (id %s):\n%s", col.Name, col.Prompt, docID, docText)
	temp := 0.0 // deterministic decoding keeps citation quotes verbatim
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(modelID),
		MaxTokens:   reviewCellMaxTokens,
		System:      tabularExtractionPrompt,
		Messages:    []providers.Message{{Role: "user", Content: user}},
		CacheSystem: true,
		JSONMode:    true,
		Temperature: &temp,
	})
	if err != nil {
		return failed(fmt.Sprintf("model call failed: %v", err))
	}
	r.recordTabularCost(resp, modelID, taskID)

	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	payload, perr := parseReviewCellPayload(text)
	if perr != nil {
		return failed(fmt.Sprintf("could not parse model response as {summary, flag, reasoning}: %v", perr))
	}
	flag := strings.ToLower(strings.TrimSpace(payload.Flag))
	if _, ok := reviewFlagLegend[flag]; !ok {
		payload.Reasoning = strings.TrimSpace(payload.Reasoning +
			fmt.Sprintf(" [flag %q is not one of green/grey/yellow/red; recorded as grey]", payload.Flag))
		flag = "grey"
	}
	return reviewCell{Column: col.Name, Summary: payload.Summary, Flag: flag, Reasoning: payload.Reasoning}
}

// reviewCellPayload is the JSON object each cell call must return.
type reviewCellPayload struct {
	Summary   string `json:"summary"`
	Flag      string `json:"flag"`
	Reasoning string `json:"reasoning"`
}

// parseReviewCellPayload parses a cell response, tolerating markdown fences or
// stray prose around the JSON object by unmarshalling the outermost brace span.
func parseReviewCellPayload(raw string) (reviewCellPayload, error) {
	var p reviewCellPayload
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return p, err
	}
	if strings.TrimSpace(p.Summary) == "" && strings.TrimSpace(p.Reasoning) == "" {
		return p, fmt.Errorf("response JSON carried no summary or reasoning")
	}
	return p, nil
}

// recordTabularCost records one per-cell model call against the task under the
// tabulate cost context. Nil-safe: a registry built without a cost store skips it.
func (r *Registry) recordTabularCost(resp *providers.ChatResponse, modelID, taskID string) {
	if r.costs == nil || resp == nil {
		return
	}
	bare := routing.ResolveModelID(modelID)
	isLocal := routing.IsOllamaModel(modelID) || routing.IsLocalModel(modelID)

	var costUSD *float64
	var wh *float64
	var watts *int
	if isLocal {
		if r.cfg != nil {
			w := cost.CalcWattHours(r.cfg.Local.InferenceWatts, resp.DurationMs)
			wh = &w
			iw := r.cfg.Local.InferenceWatts
			watts = &iw
		}
	} else {
		cw, cr := 0, 0
		if resp.Usage.CacheWriteTokens != nil {
			cw = *resp.Usage.CacheWriteTokens
		}
		if resp.Usage.CacheReadTokens != nil {
			cr = *resp.Usage.CacheReadTokens
		}
		costUSD = cost.CalcCostUSD(bare, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	}

	provider := "primary"
	if routing.IsOllamaModel(modelID) {
		provider = "ollama"
	} else if routing.IsLocalModel(modelID) {
		provider = "local"
	}

	r.costs.Record(cost.RecordRequest{
		Model:            bare,
		Provider:         provider,
		InputTokens:      resp.Usage.InputTokens,
		OutputTokens:     resp.Usage.OutputTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens,
		CacheReadTokens:  resp.Usage.CacheReadTokens,
		CostUSD:          costUSD,
		EstimatedWh:      wh,
		EstimatedWatts:   watts,
		DurationMs:       resp.DurationMs,
		Context:          cost.ContextTabulate,
		TaskID:           taskID,
	})
}

// ─── read_table_cells ───────────────────────────────────────────────────────

func (r *Registry) readTableCellsTool() *ToolImpl {
	return &ToolImpl{
		Name: "read_table_cells",
		Schema: providers.ToolParam{
			Name: "read_table_cells",
			Description: "Read extracted cells from a prior tabular_review by its review_id. " +
				"Pass col_indices and/or row_indices (0-based) to read a subset; omit either to read all columns or all rows.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"review_id": map[string]interface{}{"type": "string", "description": "The reviewId returned by tabular_review"},
					"col_indices": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "integer"},
						"description": "0-based column indices to read (omit for all columns)",
					},
					"row_indices": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "integer"},
						"description": "0-based row indices to read (omit for all rows)",
					},
				},
				"required": []string{"review_id"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			id := strInput(input, "review_id")
			if id == "" {
				return map[string]interface{}{"ok": false, "error": "review_id is required"}, nil
			}
			reviewStoreMu.Lock()
			rev := reviewStore[id]
			reviewStoreMu.Unlock()
			if rev == nil {
				return map[string]interface{}{
					"ok":    false,
					"error": fmt.Sprintf("no tabular review with id %q — run tabular_review first", id),
				}, nil
			}

			colIdx := indexListInput(input["col_indices"], len(rev.Columns))
			rowIdx := indexListInput(input["row_indices"], len(rev.Rows))

			cols := make([]string, 0, len(colIdx))
			for _, ci := range colIdx {
				cols = append(cols, rev.Columns[ci])
			}
			rows := make([]reviewRow, 0, len(rowIdx))
			for _, ri := range rowIdx {
				src := rev.Rows[ri]
				nr := reviewRow{DocumentID: src.DocumentID, Document: src.Document, Cells: make([]reviewCell, 0, len(colIdx))}
				for _, ci := range colIdx {
					if ci < len(src.Cells) {
						nr.Cells = append(nr.Cells, src.Cells[ci])
					}
				}
				rows = append(rows, nr)
			}
			return map[string]interface{}{
				"ok":       true,
				"reviewId": id,
				"columns":  cols,
				"rows":     rows,
			}, nil
		},
	}
}

// ─── Input parsing helpers ──────────────────────────────────────────────────

// strListInput coerces a decoded-JSON array value into a []string, skipping
// non-string and blank entries.
func strListInput(v interface{}) []string {
	var out []string
	appendStr := func(s string) {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	switch a := v.(type) {
	case []interface{}:
		for _, item := range a {
			if s, ok := item.(string); ok {
				appendStr(s)
			}
		}
	case []string:
		for _, s := range a {
			appendStr(s)
		}
	}
	return out
}

// parseReviewColumns coerces the columns input into reviewColumn values,
// skipping entries missing a name or a prompt.
func parseReviewColumns(v interface{}) []reviewColumn {
	items, ok := v.([]interface{})
	if !ok {
		return nil
	}
	var out []reviewColumn
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		prompt, _ := m["prompt"].(string)
		if strings.TrimSpace(name) == "" || strings.TrimSpace(prompt) == "" {
			continue
		}
		out = append(out, reviewColumn{Name: name, Prompt: prompt})
	}
	return out
}

// indexListInput coerces a decoded-JSON index array into in-range 0-based ints.
// An absent or empty value selects everything; out-of-range indices are ignored.
func indexListInput(v interface{}, n int) []int {
	raw, ok := v.([]interface{})
	if !ok || len(raw) == 0 {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		return all
	}
	var out []int
	for _, item := range raw {
		idx := -1
		switch x := item.(type) {
		case float64:
			idx = int(x)
		case int:
			idx = x
		}
		if idx >= 0 && idx < n {
			out = append(out, idx)
		}
	}
	return out
}

// reviewDocTitle resolves a display name for a row, falling back to the ID.
func reviewDocTitle(ks agents.KnowledgeStore, docID string) string {
	if d := ks.GetByID(docID); d != nil && strings.TrimSpace(d.Title) != "" {
		return d.Title
	}
	return docID
}

// truncateUTF8 caps s at max bytes without splitting a multi-byte rune.
// Shared helper (also used by documents_extra.go); delegates to strutil.
func truncateUTF8(s string, max int) string {
	return strutil.Truncate(s, max)
}

// truncateAtWord caps s at max bytes, backing the cut up to a whitespace
// boundary so the document never ends mid-word (and never mid-rune).
func truncateAtWord(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := strutil.Truncate(s, max)
	if i := strings.LastIndexAny(cut, " \t\n\r"); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \t\n\r")
}
