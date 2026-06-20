// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package tokenizer

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"sync"
)

// assetsFS embeds the tokenizer asset files. The package ships with a SMALL
// synthetic fixture (assets/vocab.json + assets/merges.txt) so the BPE algorithm
// is fully unit-tested offline. At integration the real Qwen2.5 files are dropped
// into assets/ under the SAME names and this loader works unchanged. See doc.go
// for the exact drop-in procedure.
//
//go:embed assets/vocab.json assets/merges.txt
var assetsFS embed.FS

const (
	vocabFile  = "assets/vocab.json"
	mergesFile = "assets/merges.txt"
)

var (
	defaultOnce sync.Once
	defaultBPE  *BPE
	defaultErr  error
)

// Default returns the process-wide tokenizer built from the embedded assets. It
// is constructed once and cached. An error is returned only if the embedded
// assets are malformed (which a passing test suite rules out).
func Default() (*BPE, error) {
	defaultOnce.Do(func() {
		defaultBPE, defaultErr = loadFromFS(assetsFS)
	})
	return defaultBPE, defaultErr
}

// MustDefault is Default but panics on error. Suitable for package init in
// callers that treat a malformed embedded asset as a build-time fault.
func MustDefault() *BPE {
	b, err := Default()
	if err != nil {
		panic(fmt.Sprintf("tokenizer: loading embedded assets: %v", err))
	}
	return b
}

// loadFromFS builds a BPE from any fs.FS exposing the two asset files under the
// canonical names. Factored out from Default so tests can load alternate
// fixtures without touching the embedded set.
func loadFromFS(fsys fs.FS) (*BPE, error) {
	vocabBytes, err := fs.ReadFile(fsys, vocabFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", vocabFile, err)
	}
	mergesBytes, err := fs.ReadFile(fsys, mergesFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", mergesFile, err)
	}
	vocab, err := parseVocab(vocabBytes)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", vocabFile, err)
	}
	merges, err := parseMerges(mergesBytes)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", mergesFile, err)
	}
	return newBPE(vocab, merges), nil
}

// parseVocab decodes a HuggingFace vocab.json: a flat JSON object mapping token
// string -> integer id. The token strings are already in the byte->unicode
// alphabet (that is the on-disk format both for the fixture and the real Qwen
// files), so no transformation is applied here.
func parseVocab(data []byte) (map[string]int, error) {
	var raw map[string]int
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("vocab is empty")
	}
	return raw, nil
}

// parseMerges decodes a HuggingFace merges.txt: one "A B" merge rule per line, in
// priority order (first line = highest priority = rank 0). A leading "#version"
// comment line (present in the real Qwen file) and blank lines are skipped.
func parseMerges(data []byte) ([]string, error) {
	var merges []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	// Merge lines are short, but allow generous headroom for safety.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			// The real merges.txt begins with a "#version: 0.2" header.
			if strings.HasPrefix(line, "#") {
				continue
			}
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, _, ok := splitMerge(line); !ok {
			// A line that is not a valid "A B" pair is skipped rather than
			// failing the whole load; this tolerates stray comments.
			continue
		}
		merges = append(merges, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return merges, nil
}
