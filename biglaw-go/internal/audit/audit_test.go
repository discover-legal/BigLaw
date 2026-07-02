// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package audit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeChain builds n hash-chained entries starting from "genesis".
func makeChain(n int) []AuditEntry {
	entries := make([]AuditEntry, n)
	prev := "genesis"
	for i := range entries {
		entries[i] = AuditEntry{
			ID:       "entry-" + string(rune('a'+i)),
			TS:       "2026-06-10T00:00:00Z",
			Event:    "test.event",
			PrevHash: prev,
			ActorID:  ActorSystem,
			Data:     map[string]interface{}{"i": i},
		}
		prev = hashEntry(entries[i])
	}
	return entries
}

func writeJSONL(t *testing.T, entries []AuditEntry) string {
	t.Helper()
	var buf bytes.Buffer
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureSlog routes the default slog output to a buffer for the duration of
// the test and returns it.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestRestoreFromFileVerifiesIntactChain(t *testing.T) {
	logOut := captureSlog(t)
	entries := makeChain(3)
	l := &Logger{maxBuffer: 10_000, lastHash: "genesis", logFile: writeJSONL(t, entries), enabled: true}

	if err := l.RestoreFromFile(); err != nil {
		t.Fatal(err)
	}
	if len(l.buffer) != 3 {
		t.Fatalf("restored %d entries, want 3", len(l.buffer))
	}
	if want := hashEntry(entries[2]); l.lastHash != want {
		t.Errorf("lastHash = %q, want hash of final entry %q", l.lastHash, want)
	}
	if strings.Contains(logOut.String(), "hash-chain break") {
		t.Errorf("intact chain triggered a tamper warning: %s", logOut.String())
	}
}

func TestRestoreFromFileDetectsTamperedChain(t *testing.T) {
	logOut := captureSlog(t)
	entries := makeChain(3)
	// Tamper with the middle entry AFTER the chain hashes were computed:
	// entries[2].PrevHash no longer matches hash(entries[1]).
	entries[1].Event = "test.event.tampered"
	l := &Logger{maxBuffer: 10_000, lastHash: "genesis", logFile: writeJSONL(t, entries), enabled: true}

	if err := l.RestoreFromFile(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logOut.String(), "hash-chain break") {
		t.Errorf("tampered chain did not trigger a tamper warning; log output: %s", logOut.String())
	}
}
