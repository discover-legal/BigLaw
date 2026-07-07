// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package strutil holds small string helpers shared across modules.
package strutil

import (
	"math"
	"strings"
	"unicode/utf8"
)

// Truncate caps s at max bytes without splitting a UTF-8 sequence: the cut
// backs up to the nearest rune boundary. Limits stay byte-denominated (the
// callers budget prompt and payload sizes in bytes), but the result is
// always valid UTF-8 — naive s[:max] can split a multi-byte rune, which
// JSON-encodes as U+FFFD and corrupts legal text containing §, curly
// quotes, accents, or non-Latin scripts.
func Truncate(s string, max int) string {
	if max < 0 {
		max = 0
	}
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// charsPerToken is a deliberately conservative characters-per-token ratio for
// modern BPE tokenizers (Qwen/GLM/Kimi/OpenAI-family). English averages ~4, but
// legal text fragments lower (citations, section symbols, numbers, casing), so
// 3.5 OVER-counts tokens and UNDER-fills budgets — callers cut early and stay
// under the real context window. Exact counts need the model's own tokenizer,
// which a multi-model stack over Ollama does not expose; this is the
// tokenizer-free stand-in.
const charsPerToken = 3.5

// EstimateTokens returns a conservative (high) estimate of the token count of s.
// It counts runes, not bytes, so multibyte characters are not over-weighted.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return int(math.Ceil(float64(utf8.RuneCountInString(s)) / charsPerToken))
}

// TokenBudgetToChars converts a token budget into a conservative character ceiling
// (a low estimate of how many characters fit). Returns 0 for a non-positive budget.
func TokenBudgetToChars(maxTokens int) int {
	if maxTokens <= 0 {
		return 0
	}
	return int(float64(maxTokens) * charsPerToken)
}

// TruncateToTokens returns the longest prefix of s estimated to fit within
// maxTokens, cut on a WORD boundary (never mid-word) and a rune boundary (never
// mid-character). s is returned unchanged when it already fits or when maxTokens
// is non-positive. The result is always a verbatim prefix of s, so a quote copied
// from it still verifies against the source. A single unbroken word longer than
// the budget (rare: a long URL/hash) has no boundary to cut on and is returned
// truncated at a rune boundary.
func TruncateToTokens(s string, maxTokens int) string {
	if maxTokens <= 0 || EstimateTokens(s) <= maxTokens {
		return s
	}
	limit := TokenBudgetToChars(maxTokens)
	if limit >= len(s) {
		return s
	}
	// Back the cut up to a rune boundary so a multibyte character is never split.
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	cut := s[:limit]
	// Drop a partial trailing word so the cut never lands mid-word. Both are
	// sub-slices, so the result stays a verbatim prefix of s.
	if i := strings.LastIndexAny(cut, " \t\n\r"); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " \t\n\r")
}
