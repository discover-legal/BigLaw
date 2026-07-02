// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package tokenizer implements a pure-Go, byte-level Byte-Pair-Encoding (BPE)
// tokenizer in the GPT-2 / Qwen2.5 family. It exists so BigLaw can count tokens
// accurately against a small local model's context window instead of relying on
// a chars-per-token heuristic.
//
// The implementation is standard-library only (no cgo, no third-party deps). It
// reproduces three pieces of the reference HuggingFace tokenizer:
//
//  1. The reversible byte -> unicode mapping (gpt2 "bytes_to_unicode"), which
//     lifts the 256 raw bytes into a contiguous range of printable runes so the
//     merge table can be expressed as text.
//  2. Pretokenization: the input is first split into coarse word-like chunks by
//     the GPT-2 regex. Go's regexp engine is RE2 and has NO lookahead, so the
//     reference pattern's `\s+(?!\S)` clause (match a run of whitespace only when
//     it is NOT followed by a non-space, i.e. trailing whitespace) is implemented
//     manually after an RE2-compatible approximation. See pretokenize.go.
//  3. The BPE merge loop, driven by a rank table built from merges.txt: within a
//     pretoken, repeatedly merge the adjacent symbol pair with the lowest rank
//     until no known merge remains, then map the resulting symbols to vocab ids.
//
// Unknown symbols fall back to their per-byte tokens (every single mapped byte is
// guaranteed to be in a complete Qwen/GPT-2 vocab); if even a single byte is
// absent from the loaded vocab (only possible with a partial fixture) it is
// skipped from the id stream but still counted so CountTokens never under-counts.
package tokenizer

import (
	"sort"
	"strings"
	"sync"
)

// Tokenizer is the public contract. CountTokens returns how many tokens the
// string encodes to; Encode returns the concrete token ids.
type Tokenizer interface {
	// CountTokens returns the number of tokens s encodes to. Empty string is 0.
	CountTokens(s string) int
	// Encode returns the token ids for s, in order. Empty string is nil.
	Encode(s string) []int
}

// BPE is the concrete byte-level BPE tokenizer.
type BPE struct {
	// encoder maps a token string (in the byte->unicode alphabet) to its id.
	encoder map[string]int
	// decoder is the inverse of encoder, for round-trip / Decode.
	decoder map[int]string
	// ranks maps a merge pair "A B" (space-joined, in the byte->unicode
	// alphabet) to its priority; lower binds tighter / earlier.
	ranks map[mergeKey]int

	// byteToRune / runeToByte are the reversible gpt2 byte<->unicode mapping.
	byteToRune [256]rune
	runeToByte map[rune]byte

	// cache memoizes BPE results per pretoken; the merge loop is the hot path.
	mu    sync.RWMutex
	cache map[string][]string
}

// mergeKey identifies an ordered adjacent symbol pair.
type mergeKey struct {
	left, right string
}

var _ Tokenizer = (*BPE)(nil)

// newBPE constructs a BPE from an already-parsed vocab and ordered merge list.
// vocab maps token string -> id. merges is the ordered list of "A B" merge rules
// (rank 0 is the highest priority, matching merges.txt line order).
func newBPE(vocab map[string]int, merges []string) *BPE {
	b := &BPE{
		encoder:    vocab,
		decoder:    make(map[int]string, len(vocab)),
		ranks:      make(map[mergeKey]int, len(merges)),
		runeToByte: make(map[rune]byte, 256),
		cache:      make(map[string][]string),
	}
	for tok, id := range vocab {
		b.decoder[id] = tok
	}
	for rank, m := range merges {
		l, r, ok := splitMerge(m)
		if !ok {
			continue
		}
		b.ranks[mergeKey{l, r}] = rank
	}
	b.initByteMap()
	return b
}

// splitMerge splits a merges.txt line "A B" into its two symbols. A merge symbol
// is in the byte->unicode alphabet and never itself contains a space, so a single
// SplitN on the first space is correct and unambiguous.
func splitMerge(line string) (left, right string, ok bool) {
	parts := strings.SplitN(line, " ", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// initByteMap builds the canonical gpt2 bytes_to_unicode mapping. Printable
// ASCII / Latin-1 ranges map to themselves; the remaining bytes are lifted to
// runes at 256+n so every byte has a distinct, printable rune.
func (b *BPE) initByteMap() {
	var printable []int
	add := func(lo, hi int) {
		for c := lo; c <= hi; c++ {
			printable = append(printable, c)
		}
	}
	add(0x21, 0x7E) // '!'..'~'  (33..126)
	add(0xA1, 0xAC) // '¡'..'¬'  (161..172)
	add(0xAE, 0xFF) // '®'..'ÿ'  (174..255)

	inPrintable := make(map[int]bool, len(printable))
	for _, c := range printable {
		inPrintable[c] = true
	}

	n := 0
	for bb := 0; bb < 256; bb++ {
		if inPrintable[bb] {
			b.byteToRune[bb] = rune(bb)
			continue
		}
		b.byteToRune[bb] = rune(256 + n)
		n++
	}
	for bb := 0; bb < 256; bb++ {
		b.runeToByte[b.byteToRune[bb]] = byte(bb)
	}
}

// byteString lifts a raw UTF-8 string's bytes into the byte->unicode alphabet,
// returning the per-byte runes as a slice of single-rune strings (the initial
// BPE symbol sequence for a pretoken).
func (b *BPE) byteSymbols(s string) []string {
	raw := []byte(s)
	out := make([]string, len(raw))
	for i, bb := range raw {
		out[i] = string(b.byteToRune[bb])
	}
	return out
}

// bpe runs the merge loop over a single pretoken's byte-symbol sequence,
// returning the merged symbol sequence. Result is memoized per pretoken text.
func (b *BPE) bpe(token string) []string {
	b.mu.RLock()
	if cached, ok := b.cache[token]; ok {
		b.mu.RUnlock()
		return cached
	}
	b.mu.RUnlock()

	symbols := b.byteSymbols(token)
	if len(symbols) < 2 {
		b.store(token, symbols)
		return symbols
	}

	for {
		// Find the adjacent pair with the lowest (best) merge rank.
		bestRank := int(^uint(0) >> 1) // max int
		bestIdx := -1
		for i := 0; i+1 < len(symbols); i++ {
			if r, ok := b.ranks[mergeKey{symbols[i], symbols[i+1]}]; ok && r < bestRank {
				bestRank = r
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break // no mergeable pair remains
		}

		// Merge every non-overlapping occurrence of that winning pair in one
		// pass (matches the reference implementation's behavior and keeps the
		// loop O(merges) rather than O(merges^2)).
		l, r := symbols[bestIdx], symbols[bestIdx+1]
		merged := make([]string, 0, len(symbols))
		for i := 0; i < len(symbols); {
			if i+1 < len(symbols) && symbols[i] == l && symbols[i+1] == r {
				merged = append(merged, l+r)
				i += 2
			} else {
				merged = append(merged, symbols[i])
				i++
			}
		}
		symbols = merged
		if len(symbols) < 2 {
			break
		}
	}

	b.store(token, symbols)
	return symbols
}

func (b *BPE) store(token string, symbols []string) {
	b.mu.Lock()
	b.cache[token] = symbols
	b.mu.Unlock()
}

// Encode implements Tokenizer.
func (b *BPE) Encode(s string) []int {
	if s == "" {
		return nil
	}
	var ids []int
	for _, pretok := range pretokenize(s) {
		for _, sym := range b.bpe(pretok) {
			if id, ok := b.encoder[sym]; ok {
				ids = append(ids, id)
				continue
			}
			// Symbol not in vocab: fall back to its constituent byte tokens.
			// A complete vocab resolves every byte; a partial fixture may not,
			// in which case byteFallback yields -1 sentinels that we drop from
			// the id stream (CountTokens still counts them, so the count stays
			// an accurate upper bound — see CountTokens).
			for _, id := range b.byteFallback(sym) {
				if id >= 0 {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// byteFallback maps an out-of-vocab symbol to per-byte token ids. With a complete
// vocab every single mapped byte is present, so this always resolves. With a
// partial fixture vocab a missing byte still contributes 1 to the count via a
// sentinel -1 so CountTokens never under-counts; -1 ids are dropped by Encode's
// caller-visible contract only at the boundary (see CountTokens).
func (b *BPE) byteFallback(sym string) []int {
	out := make([]int, 0, len(sym))
	for _, r := range sym {
		single := string(r)
		if id, ok := b.encoder[single]; ok {
			out = append(out, id)
		} else {
			out = append(out, -1)
		}
	}
	return out
}

// CountTokens implements Tokenizer. It counts every produced token including any
// byte-fallback sentinels, so the count is an accurate upper bound even against a
// partial fixture vocab; against a complete vocab it equals len(Encode(s)).
func (b *BPE) CountTokens(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, pretok := range pretokenize(s) {
		for _, sym := range b.bpe(pretok) {
			if _, ok := b.encoder[sym]; ok {
				n++
				continue
			}
			n += countRunes(sym)
		}
	}
	return n
}

func countRunes(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// Decode reverses Encode: it maps ids back to their token strings (in the
// byte->unicode alphabet) and lowers them back to the original raw bytes. It is
// primarily used by tests to prove byte-level round-trips. Unknown ids (-1 or
// not in the decoder) are skipped.
func (b *BPE) Decode(ids []int) string {
	var lifted strings.Builder
	for _, id := range ids {
		if tok, ok := b.decoder[id]; ok {
			lifted.WriteString(tok)
		}
	}
	// Lower the byte->unicode runes back to raw bytes.
	raw := make([]byte, 0, lifted.Len())
	for _, r := range lifted.String() {
		if bb, ok := b.runeToByte[r]; ok {
			raw = append(raw, bb)
		}
	}
	return string(raw)
}

// Vocab returns a snapshot of the token->id map. Used by tests and callers that
// need to introspect the loaded vocabulary; the returned map is a copy.
func (b *BPE) Vocab() map[string]int {
	out := make(map[string]int, len(b.encoder))
	for k, v := range b.encoder {
		out[k] = v
	}
	return out
}

// sortedMerges is a tiny helper for deterministic debugging output: it returns
// the merge rules ordered by rank.
func (b *BPE) sortedMerges() []mergeKey {
	keys := make([]mergeKey, 0, len(b.ranks))
	for k := range b.ranks {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return b.ranks[keys[i]] < b.ranks[keys[j]]
	})
	return keys
}
