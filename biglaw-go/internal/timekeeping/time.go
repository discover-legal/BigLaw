// SPDX-License-Identifier: Apache-2.0
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

	"github.com/discover-legal/biglaw-go/internal/csvutil"
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

// SetSuggestions replaces the OCG suggestions on an entry and stamps
// OcgCheckedAt. A missing ID is a no-op (mirrors the TS store).
func (s *TimeStore) SetSuggestions(entryID string, suggestions []types.OcgSuggestion) {
	s.mu.Lock()
	for i := range s.entries {
		if s.entries[i].ID == entryID {
			s.entries[i].OcgSuggestions = suggestions
			s.entries[i].OcgCheckedAt = time.Now().UTC().Format(time.RFC3339)
			break
		}
	}
	s.mu.Unlock()
	s.persist()
}

// AcceptSuggestion rewrites the entry description from the suggestion and
// marks it accepted. Returns the updated entry, or nil if entry or
// suggestion is not found.
func (s *TimeStore) AcceptSuggestion(entryID, ruleID string) *types.TimeEntry {
	s.mu.Lock()
	var updated *types.TimeEntry
	for i := range s.entries {
		if s.entries[i].ID != entryID {
			continue
		}
		for j := range s.entries[i].OcgSuggestions {
			if s.entries[i].OcgSuggestions[j].RuleID == ruleID {
				s.entries[i].Description = s.entries[i].OcgSuggestions[j].SuggestedDescription
				s.entries[i].OcgSuggestions[j].Status = "accepted"
				cp := s.entries[i]
				updated = &cp
				break
			}
		}
		break
	}
	s.mu.Unlock()
	if updated != nil {
		s.persist()
	}
	return updated
}

// DismissSuggestion marks a suggestion dismissed without changing the
// description. Returns the updated entry, or nil if not found.
func (s *TimeStore) DismissSuggestion(entryID, ruleID string) *types.TimeEntry {
	s.mu.Lock()
	var updated *types.TimeEntry
	for i := range s.entries {
		if s.entries[i].ID != entryID {
			continue
		}
		for j := range s.entries[i].OcgSuggestions {
			if s.entries[i].OcgSuggestions[j].RuleID == ruleID {
				s.entries[i].OcgSuggestions[j].Status = "dismissed"
				cp := s.entries[i]
				updated = &cp
				break
			}
		}
		break
	}
	s.mu.Unlock()
	if updated != nil {
		s.persist()
	}
	return updated
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

// SplitClioUnsynced partitions billable entries for a Clio sync run: entries
// with a positive duration that have never been synced go to toSync; already
// synced ones are counted as skipped. Open entries (durationMs <= 0) are
// excluded from both, mirroring the TS /time-entries/sync-to-clio filter.
func SplitClioUnsynced(entries []types.TimeEntry) (toSync []types.TimeEntry, skipped int) {
	for _, e := range entries {
		if e.DurationMs <= 0 {
			continue
		}
		if e.ClioSyncedAt != "" {
			skipped++
			continue
		}
		toSync = append(toSync, e)
	}
	return toSync, skipped
}

// ClioDurationHours converts an entry to decimal hours for a Clio activity:
// the larger of the 6-minute billing-unit total and the raw elapsed time,
// rounded to two decimal places (TS: max(billingUnits*0.1, durationMs/3.6e6)).
func ClioDurationHours(e types.TimeEntry) float64 {
	h := math.Max(float64(e.BillingUnits)*0.1, float64(e.DurationMs)/3_600_000.0)
	return math.Round(h*100) / 100
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

// csvEscape quotes a field (RFC 4180, embedded quotes doubled) and
// neutralizes spreadsheet formula injection via the shared helper — string
// fields like description and names carry LLM-/user-supplied content.
func csvEscape(s string) string {
	return csvutil.Escape(s)
}
