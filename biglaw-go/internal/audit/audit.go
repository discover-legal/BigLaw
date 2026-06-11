// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Append-only, hash-chained audit log.
// Each entry carries a SHA-256 over the preceding entry's JSON (tamper-evident).
// Writes go to a JSONL file, an in-memory ring buffer, and registered sinks.

package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	ActorSystem    = "system"
	ActorAnonymous = "anonymous"
)

type AuditEntry struct {
	ID         string                 `json:"id"`
	TS         string                 `json:"ts"`
	Event      string                 `json:"event"`
	PrevHash   string                 `json:"prevHash"`
	ActorID    string                 `json:"actorId"`
	TaskID     string                 `json:"taskId,omitempty"`
	AgentID    string                 `json:"agentId,omitempty"`
	Model      string                 `json:"model,omitempty"`
	DurationMs *int64                 `json:"durationMs,omitempty"`
	Data       map[string]interface{} `json:"data"`
}

type WriteRequest struct {
	Event      string
	ActorID    string
	TaskID     string
	AgentID    string
	Model      string
	DurationMs *int64
	Data       map[string]interface{}
}

type Sink interface {
	Name() string
	Write(entry AuditEntry)
	Flush() error
}

type Logger struct {
	mu        sync.Mutex
	buffer    []AuditEntry
	maxBuffer int
	lastHash  string
	logFile   string
	enabled   bool
	sinks     []Sink
	listeners []chan AuditEntry
}

var Default = &Logger{
	maxBuffer: 10_000,
	lastHash:  "genesis",
}

func Init(logFile string, enabled bool) {
	Default.logFile = logFile
	Default.enabled = enabled
}

func (l *Logger) RegisterSink(s Sink) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sinks = append(l.sinks, s)
}

func (l *Logger) Write(req WriteRequest) {
	entry := AuditEntry{
		ID:         uuid.New().String(),
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		Event:      req.Event,
		ActorID:    req.ActorID,
		TaskID:     req.TaskID,
		AgentID:    req.AgentID,
		Model:      req.Model,
		DurationMs: req.DurationMs,
		Data:       req.Data,
	}
	if entry.Data == nil {
		entry.Data = map[string]interface{}{}
	}

	l.mu.Lock()
	entry.PrevHash = l.lastHash
	raw, _ := json.Marshal(entry)
	h := sha256.Sum256(raw)
	l.lastHash = hex.EncodeToString(h[:])

	l.buffer = append(l.buffer, entry)
	if len(l.buffer) > l.maxBuffer {
		l.buffer = l.buffer[1:]
	}

	// Snapshot sinks and listeners under the lock to avoid holding it during I/O.
	sinks := make([]Sink, len(l.sinks))
	copy(sinks, l.sinks)
	listeners := make([]chan AuditEntry, len(l.listeners))
	copy(listeners, l.listeners)
	logFile := l.logFile
	enabled := l.enabled
	l.mu.Unlock()

	// Async disk write — never blocks the caller.
	if enabled && logFile != "" {
		go func() {
			f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return
			}
			defer f.Close()
			fmt.Fprintln(f, string(raw))
		}()
	}

	// Notify sinks (fire-and-forget).
	for _, s := range sinks {
		go func(sink Sink) {
			defer func() { recover() }()
			sink.Write(entry)
		}(s)
	}

	// Notify SSE listeners (non-blocking).
	for _, ch := range listeners {
		select {
		case ch <- entry:
		default:
		}
	}
}

// Filter selects audit entries; zero values mean no constraint on that field.
type Filter struct {
	TaskID  string
	ActorID string
	Event   string // prefix match, e.g. "task." matches task.submitted
	Limit   int
}

// ReadFiltered returns the most recent entries matching the filter,
// oldest-first (same ordering as ReadRecent).
func (l *Logger) ReadFiltered(f Filter) []AuditEntry {
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	filtered := make([]AuditEntry, 0, 64)
	for _, e := range l.buffer {
		if f.TaskID != "" && e.TaskID != f.TaskID {
			continue
		}
		if f.ActorID != "" && e.ActorID != f.ActorID {
			continue
		}
		if f.Event != "" && !strings.HasPrefix(e.Event, f.Event) {
			continue
		}
		filtered = append(filtered, e)
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	out := make([]AuditEntry, len(filtered))
	copy(out, filtered)
	return out
}

func (l *Logger) ReadRecent(taskID string, limit int) []AuditEntry {
	if limit <= 0 {
		limit = 500
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	src := l.buffer
	if taskID != "" {
		filtered := make([]AuditEntry, 0, 64)
		for _, e := range l.buffer {
			if e.TaskID == taskID {
				filtered = append(filtered, e)
			}
		}
		src = filtered
	}
	if len(src) > limit {
		src = src[len(src)-limit:]
	}
	out := make([]AuditEntry, len(src))
	copy(out, src)
	return out
}

// Subscribe returns a channel that receives live audit events and a cancel func.
func (l *Logger) Subscribe() (<-chan AuditEntry, func()) {
	ch := make(chan AuditEntry, 64)
	l.mu.Lock()
	l.listeners = append(l.listeners, ch)
	l.mu.Unlock()
	cancel := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for i, c := range l.listeners {
			if c == ch {
				l.listeners = append(l.listeners[:i], l.listeners[i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, cancel
}

// RestoreFromFile loads recent entries from the JSONL log on startup.
func (l *Logger) RestoreFromFile() error {
	if !l.enabled || l.logFile == "" {
		return nil
	}
	data, err := os.ReadFile(l.logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	l.mu.Lock()
	defer l.mu.Unlock()
	start := 0
	if len(lines) > l.maxBuffer {
		start = len(lines) - l.maxBuffer
	}
	for _, line := range lines[start:] {
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err == nil {
			l.buffer = append(l.buffer, e)
		}
	}
	// Verify the hash chain across the restored window — the documented
	// tamper-evidence is only meaningful if it is actually checked. We can only
	// validate links within the loaded window (the entry before the first one is
	// not in memory), so start at index 1. A mismatch means the log was edited.
	for i := 1; i < len(l.buffer); i++ {
		if l.buffer[i].PrevHash != hashEntry(l.buffer[i-1]) {
			slog.Warn("Audit hash-chain break detected on restore — log may have been tampered with",
				"atEntryId", l.buffer[i].ID,
				"index", i,
			)
			break
		}
	}
	if last := len(l.buffer); last > 0 {
		l.lastHash = hashEntry(l.buffer[last-1])
	}
	return nil
}

// hashEntry computes the SHA-256 chain hash of an entry's JSON encoding —
// the same scheme used in Write when advancing lastHash.
func hashEntry(e AuditEntry) string {
	raw, _ := json.Marshal(e)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func (l *Logger) FlushSinks() error {
	l.mu.Lock()
	sinks := make([]Sink, len(l.sinks))
	copy(sinks, l.sinks)
	l.mu.Unlock()
	for _, s := range sinks {
		s.Flush()
	}
	return nil
}
