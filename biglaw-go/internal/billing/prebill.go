// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// PreBillStore — pre-bill lifecycle: draft → reviewed → approved → invoiced.

package billing

import (
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// PreBillStatus is the lifecycle state of a pre-bill.
type PreBillStatus string

const (
	PreBillDraft    PreBillStatus = "draft"
	PreBillReviewed PreBillStatus = "reviewed"
	PreBillApproved PreBillStatus = "approved"
	PreBillInvoiced PreBillStatus = "invoiced"
)

// PreBillEntry is a single line item in a pre-bill.
type PreBillEntry struct {
	EntryID            string   `json:"entryId"`
	Description        string   `json:"description"`
	BillingUnits       int      `json:"billingUnits"`
	BillingRate        *float64 `json:"billingRate,omitempty"`
	BillingAmountUsd   *float64 `json:"billingAmountUsd,omitempty"`
	UTBMSTaskCode      string   `json:"utbmsTaskCode,omitempty"`
	UTBMSActivityCode  string   `json:"utbmsActivityCode,omitempty"`
	ProfileName        string   `json:"profileName,omitempty"`
	AgentName          string   `json:"agentName,omitempty"`
	StartedAt          string   `json:"startedAt"`
	EndedAt            string   `json:"endedAt,omitempty"`
	OcgSuggestionCount int      `json:"ocgSuggestionCount"`
}

// PreBill is a draft invoice grouping time entries for a matter.
type PreBill struct {
	ID                 string         `json:"id"`
	MatterNumber       string         `json:"matterNumber"`
	ClientNumber       string         `json:"clientNumber,omitempty"`
	Status             PreBillStatus  `json:"status"`
	CreatedByProfileID string         `json:"createdByProfileId"`
	CreatedAt          string         `json:"createdAt"`
	ReviewedAt         string         `json:"reviewedAt,omitempty"`
	ApprovedAt         string         `json:"approvedAt,omitempty"`
	InvoicedAt         string         `json:"invoicedAt,omitempty"`
	Entries            []PreBillEntry `json:"entries"`
	TotalBillingUnits  int            `json:"totalBillingUnits"`
	TotalAmountUsd     float64        `json:"totalAmountUsd"`
	Notes              string         `json:"notes,omitempty"`
}

// PreBillStore persists pre-bills to a JSON file.
type PreBillStore struct {
	mu        sync.RWMutex
	persistMu sync.Mutex // serialises concurrent fire-and-forget persists
	bills     []PreBill
	path      string
}

// NewPreBillStore creates a PreBillStore backed by path.
func NewPreBillStore(path string) *PreBillStore { return &PreBillStore{path: path} }

// Init loads persisted bills from disk.
func (s *PreBillStore) Init() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(data, &s.bills)
}

// Create builds a new pre-bill from time entries.
func (s *PreBillStore) Create(matterNumber, clientNumber, createdByProfileID string, entries []types.TimeEntry) *PreBill {
	pbEntries := make([]PreBillEntry, len(entries))
	totalUnits := 0
	totalUsd := 0.0
	for i, e := range entries {
		pbe := PreBillEntry{
			EntryID:           e.ID,
			Description:       e.Description,
			BillingUnits:      e.BillingUnits,
			BillingRate:       e.BillingRate,
			BillingAmountUsd:  e.BillingAmountUsd,
			UTBMSTaskCode:     e.UTBMSTaskCode,
			UTBMSActivityCode: e.UTBMSActivityCode,
			ProfileName:       e.ProfileName,
			AgentName:         e.AgentName,
			StartedAt:         e.StartedAt.Format(time.RFC3339),
		}
		if e.EndedAt != nil {
			pbe.EndedAt = e.EndedAt.Format(time.RFC3339)
		}
		totalUnits += e.BillingUnits
		if e.BillingAmountUsd != nil {
			totalUsd += *e.BillingAmountUsd
		}
		pbEntries[i] = pbe
	}

	bill := PreBill{
		ID:                 uuid.New().String(),
		MatterNumber:       matterNumber,
		ClientNumber:       clientNumber,
		Status:             PreBillDraft,
		CreatedByProfileID: createdByProfileID,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339),
		Entries:            pbEntries,
		TotalBillingUnits:  totalUnits,
		TotalAmountUsd:     roundCents(totalUsd),
	}

	s.mu.Lock()
	s.bills = append(s.bills, bill)
	s.mu.Unlock()
	s.persist()
	return &bill
}

// List returns all bills, optionally filtered by matter number.
func (s *PreBillStore) List(matterNumber string) []PreBill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if matterNumber == "" {
		cp := make([]PreBill, len(s.bills))
		copy(cp, s.bills)
		return cp
	}
	var out []PreBill
	for _, b := range s.bills {
		if b.MatterNumber == matterNumber {
			out = append(out, b)
		}
	}
	return out
}

// GetByID returns a bill by ID.
func (s *PreBillStore) GetByID(id string) *PreBill {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.bills {
		if s.bills[i].ID == id {
			cp := s.bills[i]
			return &cp
		}
	}
	return nil
}

// UpdateEntryDescription updates a line-item description.
func (s *PreBillStore) UpdateEntryDescription(billID, entryID, description string) *PreBill {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.bills {
		if s.bills[i].ID != billID {
			continue
		}
		b := &s.bills[i]
		if b.Status == PreBillApproved || b.Status == PreBillInvoiced {
			return nil
		}
		for j := range b.Entries {
			if b.Entries[j].EntryID == entryID {
				if len(description) > 500 {
					description = strutil.Truncate(description, 500)
				}
				b.Entries[j].Description = description
				go s.persist()
				cp := *b
				return &cp
			}
		}
	}
	return nil
}

// Transition moves a bill through its lifecycle.
func (s *PreBillStore) Transition(billID string, toStatus PreBillStatus) *PreBill {
	validTransitions := map[PreBillStatus][]PreBillStatus{
		PreBillDraft:    {PreBillReviewed},
		PreBillReviewed: {PreBillApproved, PreBillDraft},
		PreBillApproved: {PreBillInvoiced},
		PreBillInvoiced: {},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.bills {
		if s.bills[i].ID != billID {
			continue
		}
		b := &s.bills[i]
		allowed := validTransitions[b.Status]
		ok := false
		for _, st := range allowed {
			if st == toStatus {
				ok = true
				break
			}
		}
		if !ok {
			return nil
		}
		b.Status = toStatus
		now := time.Now().UTC().Format(time.RFC3339)
		switch toStatus {
		case PreBillReviewed:
			b.ReviewedAt = now
		case PreBillApproved:
			b.ApprovedAt = now
		case PreBillInvoiced:
			b.InvoicedAt = now
		}
		go s.persist()
		cp := *b
		return &cp
	}
	return nil
}

// SetNotes sets the notes on a pre-bill.
func (s *PreBillStore) SetNotes(billID, notes string) *PreBill {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.bills {
		if s.bills[i].ID != billID {
			continue
		}
		if len(notes) > 2000 {
			notes = strutil.Truncate(notes, 2000)
		}
		s.bills[i].Notes = notes
		go s.persist()
		cp := s.bills[i]
		return &cp
	}
	return nil
}

func (s *PreBillStore) persist() {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.RLock()
	data, err := json.MarshalIndent(s.bills, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		slog.Warn("PreBillStore persist failed", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Warn("PreBillStore persist write failed", "error", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Warn("PreBillStore persist rename failed", "error", err)
	}
}

func roundCents(f float64) float64 {
	// math.Round, not int(f*100+0.5): truncation flips the sign of rounding
	// for negative amounts (credits/write-offs) and overflows on large f.
	return math.Round(f*100) / 100
}
