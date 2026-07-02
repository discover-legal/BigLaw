// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package tokenizer

import (
	"reflect"
	"testing"
)

// mustDefault loads the embedded-fixture tokenizer or fails the test.
func mustDefault(t *testing.T) *BPE {
	t.Helper()
	b, err := Default()
	if err != nil {
		t.Fatalf("Default() loading embedded fixture: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// Byte<->unicode mapping (gpt2 bytes_to_unicode)
// ---------------------------------------------------------------------------

func TestByteUnicodeMapping(t *testing.T) {
	b := mustDefault(t)

	// Printable ASCII maps to itself.
	if got := b.byteToRune['a']; got != 'a' {
		t.Errorf("byteToRune['a'] = %q, want 'a'", got)
	}
	// Space (0x20) is NOT printable in the gpt2 table; it lifts to U+0120 'Ġ'.
	if got := b.byteToRune[' ']; got != 'Ġ' {
		t.Errorf("byteToRune[space] = %q (U+%04X), want 'Ġ'", got, got)
	}
	// Newline (0x0A) lifts to U+010A 'Ċ'.
	if got := b.byteToRune['\n']; got != 'Ċ' {
		t.Errorf("byteToRune[newline] = %q (U+%04X), want 'Ċ'", got, got)
	}

	// The mapping must be a bijection over all 256 bytes.
	seen := make(map[rune]bool, 256)
	for i := 0; i < 256; i++ {
		r := b.byteToRune[i]
		if seen[r] {
			t.Fatalf("byteToRune not injective: rune %q produced twice", r)
		}
		seen[r] = true
		if bb, ok := b.runeToByte[r]; !ok || int(bb) != i {
			t.Errorf("runeToByte inverse broken for byte %d (rune %q): got %d ok=%v", i, r, bb, ok)
		}
	}
	if len(seen) != 256 {
		t.Errorf("byteToRune covered %d distinct runes, want 256", len(seen))
	}
}

// ---------------------------------------------------------------------------
// Pretokenization, including the RE2 trailing-whitespace workaround
// ---------------------------------------------------------------------------

func TestPretokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single word", "hello", []string{"hello"}},
		// Leading space attaches to the following word via " ?\p{L}+".
		{"two words", "hello world", []string{"hello", " world"}},
		{"contraction", "don't", []string{"don", "'t"}},
		{"word and number", "abc123", []string{"abc", "123"}},
		// Trailing whitespace (the \s+(?!\S) clause): the run has nothing after
		// it, so it is emitted whole as its own chunk.
		{"trailing space", "hi ", []string{"hi", " "}},
		{"trailing spaces", "hi   ", []string{"hi", "   "}},
		// Interior run of length>1 before a word: all but the last space form a
		// chunk, the last space leads the word.
		{"interior double space", "a  b", []string{"a", " ", " b"}},
		{"interior triple space", "a   b", []string{"a", "  ", " b"}},
		// Punctuation clause.
		{"punctuation", "a!!", []string{"a", "!!"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pretokenize(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pretokenize(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestPretokenizeRoundTripsBytes guarantees the lookahead-free pretokenizer is
// lossless: concatenating the chunks must reproduce the input exactly, for any
// arrangement of whitespace (the property the RE2 workaround must preserve).
func TestPretokenizeLossless(t *testing.T) {
	inputs := []string{
		"hello", "hello world", "a  b   c", "trailing   ", "  leading",
		"tabs\t\tand\nnewlines\n", "mix 12 .. !! end", "",
	}
	for _, in := range inputs {
		var joined string
		for _, c := range pretokenize(in) {
			joined += c
		}
		if joined != in {
			t.Errorf("pretokenize not lossless for %q: rejoined to %q", in, joined)
		}
	}
}

// ---------------------------------------------------------------------------
// Known-answer BPE encoding against the synthetic fixture
//
// These ids are FIXTURE-SPECIFIC (see doc.go). Retire/relocate this block when
// the real Qwen2.5 assets are dropped in.
// ---------------------------------------------------------------------------

func TestFixtureKnownAnswers(t *testing.T) {
	b := mustDefault(t)

	// Hand-traced against assets/merges.txt + assets/vocab.json:
	//
	//   "low"     -> [l, ow]            (o w merge (rank 0) wins before l o)
	//   " lower"  -> [Ġlower]           (Ġ l, e r, o w, Ġl ow, Ġlow er chain)
	//   "newest"  -> [newest]           (e s, es t, n e, ne w, new est chain)
	cases := []struct {
		in   string
		want []int
	}{
		{"", nil},
		{"low", []int{0, 11}},           // l, ow
		{" lower", []int{19}},           // Ġlower
		{"newest", []int{20}},           // newest
		{"low lower", []int{0, 11, 19}}, // [l, ow] + [Ġlower]
	}
	for _, tc := range cases {
		got := b.Encode(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Encode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	// Exercise a leading-space "newest" explicitly and assert it round-trips
	// rather than guessing its decomposition.
	if got := b.Decode(b.Encode(" newest")); got != " newest" {
		t.Errorf("round-trip of %q failed: got %q", " newest", got)
	}
}

// TestCountTokensMatchesEncodeLen holds whenever every produced symbol is in the
// vocab (the complete-vocab invariant). All fixture words satisfy this.
func TestCountTokensMatchesEncodeLen(t *testing.T) {
	b := mustDefault(t)
	for _, s := range []string{"", "low", " lower", "newest", "low lower newest"} {
		if got, want := b.CountTokens(s), len(b.Encode(s)); got != want {
			t.Errorf("CountTokens(%q) = %d, len(Encode) = %d", s, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Byte-level round-trips
// ---------------------------------------------------------------------------

func TestRoundTrip(t *testing.T) {
	b := mustDefault(t)
	// Every input here uses only symbols/bytes present in the fixture vocab, so
	// Encode -> Decode must reproduce the original bytes exactly.
	inputs := []string{
		"low", " lower", "newest", "lower", "lowest", "new", "owl",
		" lower newest", "test", "tree", "id",
	}
	for _, in := range inputs {
		if got := b.Decode(b.Encode(in)); got != in {
			t.Errorf("round-trip failed for %q: got %q (ids=%v)", in, got, b.Encode(in))
		}
	}
}

// ---------------------------------------------------------------------------
// Degenerate cases: empty string and byte fallback
// ---------------------------------------------------------------------------

func TestEmptyString(t *testing.T) {
	b := mustDefault(t)
	if n := b.CountTokens(""); n != 0 {
		t.Errorf("CountTokens(\"\") = %d, want 0", n)
	}
	if ids := b.Encode(""); ids != nil {
		t.Errorf("Encode(\"\") = %v, want nil", ids)
	}
}

// TestByteFallback exercises a multi-byte UTF-8 character whose bytes are only
// partially in the fixture vocab. "é" is U+00E9 = bytes 0xC3 0xA9. Under the
// byte->unicode map 0xC3 -> 'Ã' (id 22 in the fixture) and 0xA9 -> '©' (absent
// from the fixture). So:
//   - CountTokens counts BOTH bytes (2), never under-counting.
//   - Encode emits only the resolvable byte (id 22), dropping the missing-byte
//     sentinel.
func TestByteFallback(t *testing.T) {
	b := mustDefault(t)
	const s = "é"

	if got := b.CountTokens(s); got != 2 {
		t.Errorf("CountTokens(%q) = %d, want 2 (one per byte)", s, got)
	}
	if got := b.Encode(s); !reflect.DeepEqual(got, []int{22}) {
		t.Errorf("Encode(%q) = %v, want [22] (Ã resolves, © sentinel dropped)", s, got)
	}

	// A character whose BOTH bytes are absent from the fixture still counts each
	// byte and emits no ids. U+20AC '€' = 0xE2 0x82 0xAC -> runes 'â','Ĥ'? none
	// of which are in the fixture vocab.
	const euro = "€"
	if got := b.CountTokens(euro); got != 3 {
		t.Errorf("CountTokens(%q) = %d, want 3 (one per byte)", euro, got)
	}
	if got := b.Encode(euro); got != nil {
		t.Errorf("Encode(%q) = %v, want nil (all bytes absent)", euro, got)
	}
}

// ---------------------------------------------------------------------------
// Loader robustness
// ---------------------------------------------------------------------------

func TestLoaderParsesFixture(t *testing.T) {
	b := mustDefault(t)
	v := b.Vocab()
	if len(v) == 0 {
		t.Fatal("loaded vocab is empty")
	}
	// Spot-check a few known fixture entries.
	for tok, want := range map[string]int{"l": 0, "Ġlower": 19, "newest": 20} {
		if got, ok := v[tok]; !ok || got != want {
			t.Errorf("vocab[%q] = %d ok=%v, want %d", tok, got, ok, want)
		}
	}
	// Merge ranks must be loaded and ordered: "o w" is the first real merge line
	// (rank 0) and must therefore outrank a later one like "new est".
	rOW, ok1 := b.ranks[mergeKey{"o", "w"}]
	rNew, ok2 := b.ranks[mergeKey{"new", "est"}]
	if !ok1 || !ok2 {
		t.Fatalf("merge ranks missing: o w ok=%v, new est ok=%v", ok1, ok2)
	}
	if rOW >= rNew {
		t.Errorf("merge order wrong: rank(o w)=%d should be < rank(new est)=%d", rOW, rNew)
	}
	// sortedMerges must return them in rank order (smoke test of the helper).
	sm := b.sortedMerges()
	if len(sm) == 0 || sm[0] != (mergeKey{"o", "w"}) {
		t.Errorf("sortedMerges()[0] = %+v, want {o w}", sm[0])
	}
}

// TestInterfaceSatisfied is a compile-time-ish guard that *BPE is usable through
// the Tokenizer interface.
func TestInterfaceSatisfied(t *testing.T) {
	var tk Tokenizer = mustDefault(t)
	if tk.CountTokens("newest") != 1 {
		t.Errorf("via interface, CountTokens(newest) = %d, want 1", tk.CountTokens("newest"))
	}
	if ids := tk.Encode("newest"); !reflect.DeepEqual(ids, []int{20}) {
		t.Errorf("via interface, Encode(newest) = %v, want [20]", ids)
	}
}
