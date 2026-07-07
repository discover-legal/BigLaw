// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package tokenizer

import (
	"regexp"
	"unicode"
)

// The reference GPT-2 / Qwen2.5 pretokenization pattern is:
//
//	's|'t|'re|'ve|'m|'ll|'d| ?\p{L}+| ?\p{N}+| ?[^\s\p{L}\p{N}]+|\s+(?!\S)|\s+
//
// Two clauses are problematic for Go's regexp (RE2), which has no lookahead:
//
//	\s+(?!\S)  -- "a run of whitespace NOT followed by a non-space", i.e. a run
//	              of TRAILING whitespace (whitespace at end of input). This is the
//	              only lookahead in the pattern.
//	\s+        -- a fallback run of whitespace.
//
// Together these two clauses mean: a maximal whitespace run is split so that, if
// it is followed by a non-space, the LAST whitespace char is left to attach to
// the following word (via the leading-space " ?" in the word clauses), while any
// preceding whitespace forms its own token; a whitespace run with nothing after
// it (trailing whitespace) is emitted whole.
//
// RE2 cannot express the lookahead, so we compile the lookahead-free portion of
// the pattern (collapsing both whitespace clauses into a single `\s+`) and then
// post-process every matched whitespace run manually in pretokenize: the run's
// last char is carried onto the following word (reproducing the leading-" ?"
// semantics), and a run with nothing after it is emitted whole (reproducing
// `\s+(?!\S)`). This matches the reference behavior exactly without lookahead.

// basePattern is the GPT-2 pattern with the lookahead clause removed. The single
// `\s+` matches every maximal whitespace run; pretokenize then re-slices each run
// via the carry mechanism, restoring the `\s+(?!\S)` vs leading-space semantics.
var basePattern = regexp.MustCompile(
	`'s|'t|'re|'ve|'m|'ll|'d| ?\p{L}+| ?\p{N}+| ?[^\s\p{L}\p{N}]+|\s+`,
)

// pretokenize splits s into the coarse word-like chunks the BPE merge loop then
// operates on. It applies basePattern, then repairs whitespace runs so that the
// behavior matches the reference pattern's lookahead clause exactly.
func pretokenize(s string) []string {
	if s == "" {
		return nil
	}
	matches := basePattern.FindAllString(s, -1)
	out := make([]string, 0, len(matches))
	// carry holds a single whitespace char that the reference pattern's leading
	// " ?" would attach to the FOLLOWING word. basePattern's `\s+` greedily eats
	// it, so we peel it off the whitespace run and prepend it to the next chunk.
	var carry string
	for i, m := range matches {
		if !isAllWhitespace(m) {
			out = append(out, carry+m)
			carry = ""
			continue
		}
		// m is a maximal whitespace run. If it is the last match it is trailing
		// whitespace (the `\s+(?!\S)` clause) and is emitted whole. Otherwise a
		// non-space follows (the run was maximal), so the run's LAST whitespace
		// char must lead that following word: peel it into carry and emit only
		// the preceding spaces (if any) as a standalone whitespace chunk.
		trailing := i == len(matches)-1
		runes := []rune(carry + m)
		if trailing {
			out = append(out, string(runes))
			carry = ""
			continue
		}
		carry = string(runes[len(runes)-1])
		if head := string(runes[:len(runes)-1]); head != "" {
			out = append(out, head)
		}
	}
	// A non-empty carry here means the input ended on a whitespace run that we
	// peeled (only reachable if that run was not classified trailing, which can
	// happen when a prior carry made it non-final). Emit it so we stay lossless.
	if carry != "" {
		out = append(out, carry)
	}
	return out
}

// isAllWhitespace reports whether every rune in s is Unicode whitespace. An empty
// string is not considered an all-whitespace run.
func isAllWhitespace(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
