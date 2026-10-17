// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Regression (independent verifier): the restored chain head must equal the
// hash the next entry would have had without a restart, even when Data held
// structs (re-marshalling a decoded entry is not byte-identical).

package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type toneLike struct {
	SourceType  string `json:"sourceType"`
	Formality   string `json:"formality"`
	SampleCount int    `json:"sampleCount"`
}

func TestRestoreHashesRawLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l := &Logger{maxBuffer: 100, lastHash: "genesis", logFile: path, enabled: true}
	l.Write(WriteRequest{Event: "a", ActorID: "x", Data: map[string]interface{}{"k": 1}})
	l.Write(WriteRequest{Event: "profile.tone", ActorID: "x", Data: map[string]interface{}{"toneProfile": &toneLike{"linkedin", "formal", 3}}})
	want := l.lastHash
	time.Sleep(300 * time.Millisecond)
	// independent: hash of raw last line
	f, _ := os.Open(path)
	sc := bufio.NewScanner(f)
	var last string
	for sc.Scan() {
		last = sc.Text()
	}
	f.Close()
	h := sha256.Sum256([]byte(last))
	t.Logf("in-memory lastHash=%s rawLineHash=%s", want, hex.EncodeToString(h[:]))
	l2 := &Logger{maxBuffer: 100, lastHash: "genesis", logFile: path, enabled: true}
	if err := l2.RestoreFromFile(); err != nil {
		t.Fatal(err)
	}
	t.Logf("restored lastHash=%s", l2.lastHash)
	if l2.lastHash != want {
		t.Errorf("restored chain head differs from the hash the next entry would have had without restart")
	}
}
