// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func (o *Orchestrator) tabulate(task *types.Task) (*types.TaskTable, error) {
	safeDesc := adapters.SanitizePromptContent(task.Description)
	filteredFindings := make([]types.Finding, 0, len(task.Findings))
	rejectedIDs := map[string]bool{}
	for _, g := range task.PendingGates {
		if g.Status == "rejected" {
			rejectedIDs[g.FindingID] = true
		}
	}
	for _, f := range task.Findings {
		if !rejectedIDs[f.ID] {
			filteredFindings = append(filteredFindings, f)
		}
	}
	if len(filteredFindings) == 0 {
		return nil, nil
	}

	var sb strings.Builder
	for _, f := range filteredFindings {
		c := f.Content
		if len(c) > 500 {
			c = strutil.Truncate(c, 500)
		}
		sb.WriteString(fmt.Sprintf("id=%s | %s (R%d, conf %.2f): %s\n\n", f.ID, f.AgentName, f.Round, f.Confidence, c))
	}

	prompt := fmt.Sprintf(`TASK: %s

FINDINGS:
%s

Extract these findings into a structured table. Choose 3-6 columns appropriate for this subject matter.
Respond with ONLY valid JSON (no prose, no markdown fences):
{
  "columns": ["Column A", "Column B"],
  "rows": [
    { "Column A": "value", "Column B": "value", "_findingIds": ["<finding id>"] }
  ]
}`, safeDesc, sb.String())

	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &tier, TaskType: routing.TaskSynthesis})
	prov, err := o.provReg.Get(model)
	if err != nil {
		return nil, err
	}
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(model),
		MaxTokens:   4000,
		System:      o.rootAgentDef.SystemPrompt,
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
	})
	if err != nil {
		return nil, err
	}
	o.recordCost(resp, routing.ResolveModelID(model), cost.ContextTabulate, task.ID)

	text := ""
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
			break
		}
	}
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "```json", ""), "```", ""))
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end <= start {
		return nil, fmt.Errorf("no JSON in tabulate response")
	}
	var parsed struct {
		Columns []string                 `json:"columns"`
		Rows    []map[string]interface{} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil {
		return nil, err
	}

	rows := make([]map[string]string, 0, len(parsed.Rows))
	var sourceFindingIDs []string
	for _, row := range parsed.Rows {
		outRow := map[string]string{}
		for _, col := range parsed.Columns {
			if v, ok := row[col]; ok {
				outRow[col] = fmt.Sprintf("%v", v)
			}
		}
		if ids, ok := row["_findingIds"].([]interface{}); ok {
			for _, id := range ids {
				idStr := fmt.Sprintf("%v", id)
				outRow["_findingId"] = idStr
				sourceFindingIDs = append(sourceFindingIDs, idStr)
			}
		}
		rows = append(rows, outRow)
	}

	return &types.TaskTable{
		Columns:          parsed.Columns,
		Rows:             rows,
		SourceFindingIDs: sourceFindingIDs,
		GeneratedAt:      time.Now(),
	}, nil
}
