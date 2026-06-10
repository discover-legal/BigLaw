// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package timekeeping

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// TimeFilter controls which entries List() / Export*() return.
type TimeFilter struct {
	ProfileID    string
	AgentID      string
	TaskID       string
	MatterNumber string
	ClientNumber string
	From         *time.Time
	To           *time.Time
	// nil = all, true = only agent_work events, false = exclude agent_work events
	AgentOnly *bool
}

// TimeStore holds all time entries in memory and persists them to a JSON file.
type TimeStore struct {
	mu        sync.Mutex
	persistMu sync.Mutex // serialises concurrent fire-and-forget persists
	entries   []types.TimeEntry
	path      string
}

// NewTimeStore creates an uninitialised TimeStore. Call Init before use.
func NewTimeStore() *TimeStore {
	return &TimeStore{}
}

// Init loads entries from the given JSON file path. Missing file is not an error.
func (s *TimeStore) Init(path string) error {
	s.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.entries = nil
			return nil
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(data, &s.entries)
}

// Open records the start of a new time entry. The caller should populate all
// relevant fields (TaskID, MatterNumber, etc.) before passing the entry.
// The ID and StartedAt fields are assigned here.
func (s *TimeStore) Open(entry types.TimeEntry) types.TimeEntry {
	entry.ID = generateID()
	if entry.StartedAt.IsZero() {
		entry.StartedAt = time.Now()
	}
	s.mu.Lock()
	s.entries = append(s.entries, entry)
	s.mu.Unlock()
	s.persist()
	return entry
}

// Close marks the entry with the given ID as finished and calculates billing
// fields. Returns nil if the ID is not found.
func (s *TimeStore) Close(id string) *types.TimeEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		e := &s.entries[i]
		if e.ID != id {
			continue
		}
		now := time.Now()
		e.EndedAt = &now
		e.DurationMs = now.Sub(e.StartedAt).Milliseconds()
		// Billing units: ceil(durationMs / 360_000)  (1 unit = 0.1 h = 6 min)
		e.BillingUnits = int(math.Ceil(float64(e.DurationMs) / 360_000.0))
		if e.BillingRate != nil && *e.BillingRate > 0 {
			amount := float64(e.BillingUnits) * (*e.BillingRate) / 10.0
			e.BillingAmountUsd = &amount
		}
		cp := *e
		go s.persist()
		return &cp
	}
	return nil
}

// GetByID returns a pointer to a copy of the entry with the given ID, or nil.
func (s *TimeStore) GetByID(id string) *types.TimeEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.entries {
		if e.ID == id {
			cp := s.entries[i]
			return &cp
		}
	}
	return nil
}

// UpdateDescription sets the Description field on the entry with the given ID.
func (s *TimeStore) UpdateDescription(id, desc string) {
	s.mu.Lock()
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries[i].Description = desc
			break
		}
	}
	s.mu.Unlock()
	s.persist()
}

// List returns a filtered snapshot of entries. All filter fields are optional;
// a zero value means "no constraint on this field".
func (s *TimeStore) List(filter TimeFilter) []types.TimeEntry {
	s.mu.Lock()
	snapshot := make([]types.TimeEntry, len(s.entries))
	copy(snapshot, s.entries)
	s.mu.Unlock()

	var out []types.TimeEntry
	for _, e := range snapshot {
		if !matchesFilter(e, filter) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// MarkClioSynced records the current UTC time as ClioSyncedAt on the given entry.
func (s *TimeStore) MarkClioSynced(id string) {
	s.mu.Lock()
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries[i].ClioSyncedAt = time.Now().UTC().Format(time.RFC3339)
			break
		}
	}
	s.mu.Unlock()
	s.persist()
}

// ExportJSON returns filtered entries as a slice (suitable for JSON marshalling).
func (s *TimeStore) ExportJSON(filter TimeFilter) []types.TimeEntry {
	return s.List(filter)
}

// ExportCSV returns a CSV string of all filtered entries.
// Headers: id,event,profileId,profileName,agentId,agentName,taskId,
//
//	matterNumber,clientNumber,description,startedAt,endedAt,durationMs,
//	billingUnits,billingRate,billingAmountUsd,utbmsTaskCode,utbmsActivityCode,
//	clioSyncedAt
func (s *TimeStore) ExportCSV(filter TimeFilter) string {
	entries := s.List(filter)

	var sb strings.Builder
	sb.WriteString("id,event,profileId,profileName,agentId,agentName,taskId," +
		"matterNumber,clientNumber,description,startedAt,endedAt,durationMs," +
		"billingUnits,billingRate,billingAmountUsd,utbmsTaskCode,utbmsActivityCode," +
		"clioSyncedAt\n")

	for _, e := range entries {
		endedAt := ""
		if e.EndedAt != nil {
			endedAt = e.EndedAt.UTC().Format(time.RFC3339)
		}
		rate := ""
		if e.BillingRate != nil {
			rate = fmt.Sprintf("%.4f", *e.BillingRate)
		}
		amount := ""
		if e.BillingAmountUsd != nil {
			amount = fmt.Sprintf("%.4f", *e.BillingAmountUsd)
		}
		sb.WriteString(fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%d,%d,%s,%s,%s,%s,%s\n",
			csvEscape(e.ID),
			csvEscape(string(e.Event)),
			csvEscape(e.ProfileID),
			csvEscape(e.ProfileName),
			csvEscape(e.AgentID),
			csvEscape(e.AgentName),
			csvEscape(e.TaskID),
			csvEscape(e.MatterNumber),
			csvEscape(e.ClientNumber),
			csvEscape(e.Description),
			e.StartedAt.UTC().Format(time.RFC3339),
			endedAt,
			e.DurationMs,
			e.BillingUnits,
			rate,
			amount,
			csvEscape(e.UTBMSTaskCode),
			csvEscape(e.UTBMSActivityCode),
			csvEscape(e.ClioSyncedAt),
		))
	}
	return sb.String()
}

// ─── internal helpers ─────────────────────────────────────────────────────────

func matchesFilter(e types.TimeEntry, f TimeFilter) bool {
	if f.ProfileID != "" && e.ProfileID != f.ProfileID {
		return false
	}
	if f.AgentID != "" && e.AgentID != f.AgentID {
		return false
	}
	if f.TaskID != "" && e.TaskID != f.TaskID {
		return false
	}
	if f.MatterNumber != "" && e.MatterNumber != f.MatterNumber {
		return false
	}
	if f.ClientNumber != "" && e.ClientNumber != f.ClientNumber {
		return false
	}
	if f.From != nil && e.StartedAt.Before(*f.From) {
		return false
	}
	if f.To != nil && e.StartedAt.After(*f.To) {
		return false
	}
	if f.AgentOnly != nil {
		isAgentWork := e.Event == types.TimeEventAgentWork
		if *f.AgentOnly && !isAgentWork {
			return false
		}
		if !*f.AgentOnly && isAgentWork {
			return false
		}
	}
	return true
}

// persist writes the entry list atomically: write to <path>.tmp then rename.
// persist writes time entries atomically. 0600: billable time is client data.
func (s *TimeStore) persist() {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	data, _ := json.MarshalIndent(s.entries, "", "  ")
	s.mu.Unlock()
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		slog.Error("timekeeping: persist write failed", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Error("timekeeping: persist rename failed", "path", s.path, "err", err)
	}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// csvEscape wraps a field in double-quotes if it contains a comma, double-quote,
// or newline, escaping any embedded double-quotes by doubling them.
func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}
