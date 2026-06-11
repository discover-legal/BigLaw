// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package strutil holds small string helpers shared across modules.
package strutil

import "unicode/utf8"

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
