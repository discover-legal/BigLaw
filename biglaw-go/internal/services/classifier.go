// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Classifier — Haiku-powered practice area, client, and NOSLEGAL tag detection.

package services

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Classifier detects practice area, client, and NOSLEGAL tags from document content.
type Classifier struct {
	provider providers.Provider
	haiku    string
}

// New creates a Classifier.
func New(provider providers.Provider, haikuModel string) *Classifier {
	return &Classifier{provider: provider, haiku: haikuModel}
}

// DetectPracticeArea classifies a document into a canonical practice area.
func (c *Classifier) DetectPracticeArea(title, content string) string {
	safeTitle := sanitizeClassifier(title, 300)
	snippet := sanitizeSnippet(content, 2000)
	prompt := fmt.Sprintf(`You are a legal categorisation assistant. Given a document title and excerpt, identify the single most relevant practice area from the list below. Reply with ONLY the exact practice area name, or "Unknown" if none fits.

Practice areas:
%s

Document title: %s
Document excerpt:
%s`, strings.Join(types.PracticeAreas, "\n"), safeTitle, snippet)

	raw := c.callHaiku(prompt, 64, "")
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "Unknown" {
		return ""
	}
	for _, pa := range types.PracticeAreas {
		if strings.EqualFold(pa, raw) {
			return pa
		}
	}
	return ""
}

// DetectClient identifies which client a document likely relates to.
func (c *Classifier) DetectClient(title, content string, clients []*types.Client) *types.Client {
	if len(clients) == 0 {
		return nil
	}
	safeTitle := sanitizeClassifier(title, 300)
	snippet := sanitizeSnippet(content, 3000)
	lines := make([]string, len(clients))
	for i, cl := range clients {
		lines[i] = fmt.Sprintf("- %s: %s", cl.ClientNumber, cl.Name)
	}
	prompt := fmt.Sprintf(`You are a legal matter assistant. Given a document and a list of clients, identify which client the document most likely relates to. Reply with ONLY the client number (e.g. "C-001"), or "None" if no clear match.

Clients:
%s

Document title: %s
Document excerpt:
%s`, strings.Join(lines, "\n"), safeTitle, snippet)

	raw := strings.TrimSpace(c.callHaiku(prompt, 32, ""))
	if raw == "" || raw == "None" {
		return nil
	}
	for _, cl := range clients {
		if strings.EqualFold(cl.ClientNumber, raw) {
			return cl
		}
	}
	return nil
}

// DetectNosLegal detects NOSLEGAL v4 taxonomy tags from a task description.
// Never panics — returns empty NosLegalTags on any error.
func (c *Classifier) DetectNosLegal(title, content string) types.NosLegalTags {
	safeTitle := sanitizeClassifier(title, 300)
	snippet := sanitizeSnippet(content, 2000)
	prompt := fmt.Sprintf(`You are a legal taxonomy assistant. Given a task title and description, classify it into NOSLEGAL v4 taxonomy facets. Respond with ONLY valid JSON (no prose, no markdown fences) using exactly this shape:
{
  "areaOfLaw": "<string or omit>",
  "workType": "<Advisory|Transactional|Litigious|Regulatory|Other or omit>",
  "sector": "<string or omit>",
  "assetType": "<string or omit>"
}

Common areaOfLaw values: "Corporate Finance", "Employment", "Intellectual Property", "Real Estate", "Competition", "Tax", "Banking & Finance", "Insolvency", "Data Privacy", "Immigration", "Insurance", "Environmental"
Common sector values: "Financial Services", "Technology", "Healthcare", "Real Estate", "Energy", "Retail", "Media & Entertainment", "Transport", "Government", "Manufacturing"
Common assetType values: "Agreement", "Opinion", "Pleading", "Memorandum", "Report", "Correspondence", "Regulation"

Omit a field entirely if it does not clearly apply. Each value must be under 60 characters.

Task title: %s
Task description:
%s`, safeTitle, snippet)

	raw := strings.TrimSpace(c.callHaiku(prompt, 256, ""))
	// Strip markdown fences
	raw = strings.ReplaceAll(raw, "```json", "")
	raw = strings.ReplaceAll(raw, "```", "")
	raw = strings.TrimSpace(raw)

	s := strings.Index(raw, "{")
	eIdx := strings.LastIndex(raw, "}")
	if s < 0 || eIdx <= s {
		return types.NosLegalTags{}
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &parsed); err != nil {
		return types.NosLegalTags{}
	}
	result := types.NosLegalTags{}
	if v := parsed["areaOfLaw"]; v != "" {
		if len(v) > 60 {
			v = strutil.Truncate(v, 60)
		}
		result.AreaOfLaw = &v
	}
	if v := parsed["workType"]; v != "" {
		if len(v) > 60 {
			v = strutil.Truncate(v, 60)
		}
		result.WorkType = &v
	}
	if v := parsed["sector"]; v != "" {
		if len(v) > 60 {
			v = strutil.Truncate(v, 60)
		}
		result.Sector = &v
	}
	if v := parsed["assetType"]; v != "" {
		if len(v) > 60 {
			v = strutil.Truncate(v, 60)
		}
		result.AssetType = &v
	}
	return result
}

func (c *Classifier) callHaiku(prompt string, maxTokens int, profileID string) string {
	start := time.Now()
	resp, err := c.provider.Chat(providers.ChatParams{
		Model:     c.haiku,
		MaxTokens: maxTokens,
		Messages:  []providers.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		slog.Warn("Classifier callHaiku failed", "error", err)
		return ""
	}
	dms := time.Since(start).Milliseconds()
	costUSD := cost.CalcCostUSD(c.haiku, resp.Usage.InputTokens, resp.Usage.OutputTokens, 0, 0)
	cost.Default.Record(cost.RecordRequest{
		Model: c.haiku, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "classification", ProfileID: profileID,
	})
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			return blk.Text
		}
	}
	return ""
}

func sanitizeClassifier(s string, max int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	for _, marker := range []string{"FINDING:", "END_FINDING", "NO_FINDINGS", "NO_CHALLENGE"} {
		s = strings.ReplaceAll(s, marker, "[redacted]")
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// sanitizeSnippet strips protocol markers from a document excerpt while
// preserving newlines, then truncates to max bytes.
func sanitizeSnippet(s string, max int) string {
	for _, marker := range []string{"FINDING:", "END_FINDING", "NO_FINDINGS", "NO_CHALLENGE"} {
		s = strings.ReplaceAll(s, marker, "[redacted]")
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}
