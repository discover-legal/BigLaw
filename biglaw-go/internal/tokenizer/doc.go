// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// This file documents how to swap the SMALL synthetic fixture that ships with
// this package for the REAL Qwen2.5 tokenizer assets at integration time. None of
// the Go code changes — only the two files under assets/ are replaced.
//
// # What ships today
//
//	internal/tokenizer/assets/vocab.json   synthetic fixture (~27 tokens)
//	internal/tokenizer/assets/merges.txt   synthetic fixture (12 merges)
//
// These exist solely to unit-test the BPE algorithm offline (the real Qwen vocab
// is not available in this build environment). They are byte-level-BPE-correct
// but cover only a handful of words. DO NOT count real tokens against them.
//
// # Dropping in the real Qwen2.5 assets
//
// The real vocabulary ships in the Qwen2.5 HuggingFace repository, e.g.
// https://huggingface.co/Qwen/Qwen2.5-14B-Instruct/tree/main . Two files are
// needed and both are already in the exact on-disk format this loader expects:
//
//	vocab.json   a flat JSON object: { "<token>": <id>, ... } where <token> is
//	             already encoded in the GPT-2 byte->unicode alphabet (so a leading
//	             space appears as "Ġ", a newline as "Ċ", etc.). ~151k entries.
//	merges.txt   one "A B" merge rule per line, in priority order, prefixed with a
//	             "#version: 0.2" header line (which parseMerges skips).
//
// Integration steps (paths are repo-relative to biglaw-go):
//
//  1. Obtain the two files. From the HF repo:
//     - download tokenizer's vocab.json and merges.txt directly, OR
//     - if only tokenizer.json (the combined fast-tokenizer file) is present,
//     extract them: the "model.vocab" object is vocab.json, and
//     "model.merges" (a JSON array of "A B" strings) becomes merges.txt with
//     a leading "#version: 0.2" line.
//
//  2. Copy them OVER the fixture, keeping the exact names:
//     cp /path/to/vocab.json  internal/tokenizer/assets/vocab.json
//     cp /path/to/merges.txt  internal/tokenizer/assets/merges.txt
//
//  3. Rebuild. The //go:embed directive in loader.go re-embeds whatever is in
//     assets/, so no source edit is required:
//     go build ./internal/tokenizer/...
//
//  4. Re-point or relax the fixture-specific assertions: the algorithm tests
//     (byte mapping, pretokenize, merge loop, round-trip) stay valid against the
//     real vocab, but tests that assert exact ids for the SYNTHETIC words
//     ("Ġlower" -> 19, etc.) must be moved behind a build tag or deleted, since
//     those ids are fixture-specific. The known-answer tests in this package are
//     grouped in fixtureKnownAnswers (tokenizer_test.go) precisely so they are
//     easy to find and retire at integration.
//
// # Alternative source: extract from an Ollama GGUF
//
// If BigLaw is running Qwen2.5 via Ollama, the same tokenizer data lives in the
// GGUF file's metadata and can be extracted offline without HuggingFace:
//
//	tokenizer.ggml.tokens   -> the ordered token list; the array INDEX is the id,
//	                           so vocab.json is { tokens[i]: i }.
//	tokenizer.ggml.merges   -> the ordered merge list; write each entry as a line
//	                           in merges.txt (prepend "#version: 0.2").
//
// GGUF stores the token strings in the SAME byte->unicode alphabet, so no
// re-encoding is needed. Any GGUF metadata reader (e.g. `gguf` CLIs, llama.cpp's
// gguf-dump, or a 30-line Go reader of the GGUF KV header) can dump these two
// arrays; pipe them into the two files above. The Ollama model blob lives under
// ~/.ollama/models/blobs/sha256-... (the GGUF is the largest blob for the model).
//
// In every case the contract is identical: produce assets/vocab.json (token->id)
// and assets/merges.txt (ordered "A B" rules), drop them in, rebuild. The loader,
// byte mapping, pretokenizer, and merge loop are unchanged.
package tokenizer
