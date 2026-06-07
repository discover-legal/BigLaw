// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// ToneAnalyzer — chunked recursive MapReduce to extract a lawyer's writing style
// profile from LinkedIn posts or other writing samples.
// O(log n) Haiku call depth; full parallelism at every level.

package services

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	postChunkSize = 8
	noteChunkSize = 6
	maxPosts      = 500
)

// ToneAnalyzer builds ToneProfiles from writing samples.
type ToneAnalyzer struct {
	provider providers.Provider
	haiku    string
}

// NewToneAnalyzer creates a ToneAnalyzer.
func NewToneAnalyzer(provider providers.Provider, haikuModel string) *ToneAnalyzer {
	return &ToneAnalyzer{provider: provider, haiku: haikuModel}
}

// Analyze runs the MapReduce pipeline on writing samples.
func (a *ToneAnalyzer) Analyze(samples []string, lawyerName, sourceType, profileID string) (*types.ToneProfile, error) {
	safeName := sanitizeTone(lawyerName, 200)

	posts := make([]string, 0, len(samples))
	for _, s := range samples {
		s = sanitizeTone(strings.TrimSpace(s), 0)
		if s != "" {
			posts = append(posts, s)
		}
		if len(posts) >= maxPosts {
			break
		}
	}
	if len(posts) == 0 {
		return nil, fmt.Errorf("no writing samples provided")
	}

	slog.Info("Tone analysis starting", "lawyer", safeName, "posts", len(posts), "sourceType", sourceType)

	finalNote := a.recursiveRollup(posts, safeName, 0, true, profileID)
	profile := a.buildProfile(finalNote, safeName, len(posts), sourceType, profileID)

	slog.Info("Tone analysis complete", "lawyer", safeName, "formality", profile.Formality)
	return profile, nil
}

func (a *ToneAnalyzer) recursiveRollup(items []string, name string, level int, isRaw bool, profileID string) string {
	chunkSize := noteChunkSize
	if isRaw {
		chunkSize = postChunkSize
	}
	chunks := chunkSlice(items, chunkSize)

	// Process all chunks in parallel
	notes := make([]string, len(chunks))
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, c []string) {
			defer wg.Done()
			if isRaw {
				notes[idx] = a.analyzeChunk(c, name, profileID)
			} else {
				notes[idx] = a.rollupNotes(c, name, profileID)
			}
		}(i, chunk)
	}
	wg.Wait()

	if len(notes) == 1 {
		return notes[0]
	}
	return a.recursiveRollup(notes, name, level+1, false, profileID)
}

func (a *ToneAnalyzer) analyzeChunk(posts []string, name, profileID string) string {
	var sb strings.Builder
	for i, p := range posts {
		fmt.Fprintf(&sb, "---POST %d---\n%s\n\n", i+1, p)
	}
	prompt := fmt.Sprintf(
		"Analyse the writing style of %s from these %d posts. "+
			"Write a single dense paragraph (3–5 sentences) capturing: formality level, sentence structure, vocabulary register, rhetorical habits, and any distinctive phrases or transitions. "+
			"Be specific — quote actual words or phrases observed. "+
			"Plain prose only. No JSON, no headers, no bullet points.\n\n%s",
		name, len(posts), sb.String())
	return a.haiku3(prompt, 300, profileID)
}

func (a *ToneAnalyzer) rollupNotes(notes []string, name, profileID string) string {
	var sb strings.Builder
	for i, n := range notes {
		fmt.Fprintf(&sb, "[Observation %d]\n%s\n\n", i+1, n)
	}
	prompt := fmt.Sprintf(
		"Synthesise these %d writing style observations for %s into one coherent paragraph. "+
			"Preserve specific phrases and concrete patterns. Where observations conflict, note the variation briefly. "+
			"Plain prose only. No JSON, no headers, no bullet points.\n\n%s",
		len(notes), name, sb.String())
	return a.haiku3(prompt, 300, profileID)
}

func (a *ToneAnalyzer) buildProfile(note, name string, sampleCount int, sourceType, profileID string) *types.ToneProfile {
	prompt := fmt.Sprintf(
		"Convert this writing style description for %s into structured JSON. "+
			"Respond with ONLY valid JSON — no prose, no markdown fences.\n\n"+
			"Shape:\n"+
			"{\n"+
			"  \"formality\": \"formal\" | \"semi-formal\" | \"conversational\",\n"+
			"  \"sentenceStyle\": \"long-complex\" | \"mixed\" | \"short-punchy\",\n"+
			"  \"vocabulary\": \"technical-heavy\" | \"balanced\" | \"plain-language\",\n"+
			"  \"rhetoricalStyle\": \"assertive\" | \"collaborative\" | \"hedging\" | \"analytical\",\n"+
			"  \"signaturePatterns\": [\"<specific observation>\", ...],\n"+
			"  \"injectionSnippet\": \"<3–5 sentence instruction for an LLM drafter to mirror this voice>\"\n"+
			"}\n\n"+
			"Style description:\n%s",
		name, note)

	raw := a.haiku3(prompt, 800, profileID)
	raw = strings.ReplaceAll(raw, "```json", "")
	raw = strings.ReplaceAll(raw, "```", "")
	raw = strings.TrimSpace(raw)
	s := strings.Index(raw, "{")
	eIdx := strings.LastIndex(raw, "}")
	if s < 0 || eIdx <= s {
		return fallbackProfile(name, sampleCount, sourceType)
	}
	var p map[string]interface{}
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &p); err != nil {
		return fallbackProfile(name, sampleCount, sourceType)
	}

	profile := &types.ToneProfile{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		SourceType:   sourceType,
		SampleCount:  sampleCount,
		Formality:    pickStr(p["formality"], []string{"formal", "semi-formal", "conversational"}, "semi-formal"),
		SentenceStyle: pickStr(p["sentenceStyle"], []string{"long-complex", "mixed", "short-punchy"}, "mixed"),
		Vocabulary:   pickStr(p["vocabulary"], []string{"technical-heavy", "balanced", "plain-language"}, "balanced"),
		RhetoricalStyle: pickStr(p["rhetoricalStyle"], []string{"assertive", "collaborative", "hedging", "analytical"}, "analytical"),
	}

	if arr, ok := p["signaturePatterns"].([]interface{}); ok {
		for i, item := range arr {
			if i >= 5 {
				break
			}
			if s, ok := item.(string); ok && s != "" {
				if len(s) > 200 {
					s = s[:200]
				}
				profile.SignaturePatterns = append(profile.SignaturePatterns, s)
			}
		}
	}

	if inj, ok := p["injectionSnippet"].(string); ok && inj != "" {
		if len(inj) > 1000 {
			inj = inj[:1000]
		}
		profile.InjectionSnippet = inj
	} else {
		profile.InjectionSnippet = fmt.Sprintf("%s — no distinctive style detected. Write in clear, professional legal English.", name)
	}

	return profile
}

func (a *ToneAnalyzer) haiku3(prompt string, maxTokens int, profileID string) string {
	start := time.Now()
	resp, err := a.provider.Chat(providers.ChatParams{
		Model:     a.haiku,
		MaxTokens: maxTokens,
		Messages:  []providers.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		slog.Warn("ToneAnalyzer Haiku call failed", "error", err)
		return ""
	}
	dms := time.Since(start).Milliseconds()
	costUSD := cost.CalcCostUSD(a.haiku, resp.Usage.InputTokens, resp.Usage.OutputTokens, 0, 0)
	cost.Default.Record(cost.RecordRequest{
		Model: a.haiku, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "tone_analysis", ProfileID: profileID,
	})
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			return blk.Text
		}
	}
	return ""
}

func fallbackProfile(name string, sampleCount int, sourceType string) *types.ToneProfile {
	return &types.ToneProfile{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		SourceType:       sourceType,
		SampleCount:      sampleCount,
		Formality:        "semi-formal",
		SentenceStyle:    "mixed",
		Vocabulary:       "balanced",
		RhetoricalStyle:  "analytical",
		SignaturePatterns: []string{},
		InjectionSnippet: fmt.Sprintf("%s — no distinctive style detected. Write in clear, professional legal English.", name),
	}
}

func chunkSlice(items []string, size int) [][]string {
	if size <= 0 {
		size = 1
	}
	var chunks [][]string
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		chunks = append(chunks, items[i:end])
	}
	return chunks
}

func pickStr(v interface{}, allowed []string, fallback string) string {
	s, ok := v.(string)
	if !ok {
		return fallback
	}
	for _, a := range allowed {
		if a == s {
			return s
		}
	}
	return fallback
}

func sanitizeTone(s string, max int) string {
	// Strip markers that could confuse agent prompts
	replacers := []string{
		"FINDING:", "[FINDING:]",
		"END_FINDING", "[END_FINDING]",
		"NO_FINDINGS", "[NO_FINDINGS]",
		"NO_CHALLENGE", "[NO_CHALLENGE]",
	}
	for i := 0; i < len(replacers); i += 2 {
		s = strings.ReplaceAll(s, replacers[i], replacers[i+1])
	}
	// Strip control chars except tab and newline
	var b strings.Builder
	for _, r := range s {
		if r < 0x08 || (r >= 0x0b && r <= 0x1f && r != '\n' && r != '\t') || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	result := b.String()
	if max > 0 && len(result) > max {
		return result[:max]
	}
	return result
}
