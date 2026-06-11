// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package clients

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// ClientStore holds the client roster in memory and persists it to a JSON file.
type ClientStore struct {
	mu        sync.RWMutex
	persistMu sync.Mutex // serialises concurrent persists
	clients   []types.Client
	path      string
}

// NewClientStore creates an uninitialised ClientStore. Call Init before use.
func NewClientStore() *ClientStore {
	return &ClientStore{}
}

// Init loads clients from the given JSON file path. Missing file is not an error.
func (s *ClientStore) Init(path string) error {
	s.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.clients = nil
			return nil
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(data, &s.clients)
}

// List returns a copy of all clients.
func (s *ClientStore) List() []types.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.Client, len(s.clients))
	copy(out, s.clients)
	return out
}

// Get returns a pointer to a copy of the client with the given ID, or nil.
func (s *ClientStore) Get(id string) *types.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, c := range s.clients {
		if c.ID == id {
			cp := s.clients[i]
			return &cp
		}
	}
	return nil
}

// GetByClientNumber returns the client whose ClientNumber matches num
// (case-insensitive), or nil if not found.
func (s *ClientStore) GetByClientNumber(num string) *types.Client {
	lower := strings.ToLower(num)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, c := range s.clients {
		if strings.ToLower(c.ClientNumber) == lower {
			cp := s.clients[i]
			return &cp
		}
	}
	return nil
}

// Create adds a new client. name and clientNumber must be non-empty and
// clientNumber must not already exist (case-insensitive).
func (s *ClientStore) Create(name, clientNumber string, adversaries []string, notes string) (*types.Client, error) {
	name = strings.TrimSpace(name)
	clientNumber = strings.TrimSpace(clientNumber)
	if name == "" {
		return nil, fmt.Errorf("client name is required")
	}
	if clientNumber == "" {
		return nil, fmt.Errorf("client number is required")
	}
	if s.GetByClientNumber(clientNumber) != nil {
		return nil, fmt.Errorf("client number %s already exists", clientNumber)
	}
	if adversaries == nil {
		adversaries = []string{}
	}
	now := time.Now()
	c := types.Client{
		ID:           generateID(),
		Name:         name,
		ClientNumber: clientNumber,
		Matters:      []types.ClientMatter{},
		Adversaries:  adversaries,
		Notes:        notes,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.mu.Lock()
	s.clients = append(s.clients, c)
	s.mu.Unlock()
	s.persist()
	return &c, nil
}

// Update applies a partial patch to the client with the given ID.
// Recognised patch keys: "name", "adversaries", "notes".
func (s *ClientStore) Update(id string, patch map[string]interface{}) (*types.Client, error) {
	s.mu.Lock()
	var updated *types.Client
	for i := range s.clients {
		if s.clients[i].ID != id {
			continue
		}
		c := &s.clients[i]
		if v, ok := patch["name"].(string); ok && strings.TrimSpace(v) != "" {
			c.Name = strings.TrimSpace(v)
		}
		if v, ok := patch["adversaries"].([]string); ok {
			c.Adversaries = v
		} else if raw, ok := patch["adversaries"].([]interface{}); ok {
			adv := make([]string, 0, len(raw))
			for _, item := range raw {
				if s, ok := item.(string); ok {
					adv = append(adv, s)
				}
			}
			c.Adversaries = adv
		}
		if v, ok := patch["notes"].(string); ok {
			c.Notes = v
		}
		c.UpdatedAt = time.Now()
		cp := *c
		updated = &cp
		break
	}
	s.mu.Unlock()
	if updated == nil {
		return nil, fmt.Errorf("client not found")
	}
	s.persist()
	return updated, nil
}

// AddMatter appends a new matter to the client with the given ID.
// Returns an error if the matterNumber already exists on this client.
func (s *ClientStore) AddMatter(clientID string, matterNumber, description, practiceArea string) (*types.ClientMatter, error) {
	matterNumber = strings.TrimSpace(matterNumber)
	s.mu.Lock()
	var added *types.ClientMatter
	var addErr error
	found := false
	for i := range s.clients {
		if s.clients[i].ID != clientID {
			continue
		}
		found = true
		c := &s.clients[i]
		for _, m := range c.Matters {
			if strings.EqualFold(m.MatterNumber, matterNumber) {
				addErr = fmt.Errorf("matter number %s already exists on this client", matterNumber)
				break
			}
		}
		if addErr != nil {
			break
		}
		matter := types.ClientMatter{
			MatterNumber: matterNumber,
			Description:  description,
			PracticeArea: practiceArea,
			OpenedAt:     time.Now(),
		}
		c.Matters = append(c.Matters, matter)
		c.UpdatedAt = time.Now()
		cp := matter
		added = &cp
		break
	}
	s.mu.Unlock()
	if addErr != nil {
		return nil, addErr
	}
	if !found {
		return nil, fmt.Errorf("client not found")
	}
	s.persist()
	return added, nil
}

// RemoveMatter removes the matter with the given matterNumber from the client.
// Returns (true, nil) if removed, (false, nil) if the matter was not found,
// or an error if the client was not found.
func (s *ClientStore) RemoveMatter(clientID, matterNumber string) (bool, error) {
	s.mu.Lock()
	found := false
	removed := false
	for i := range s.clients {
		if s.clients[i].ID != clientID {
			continue
		}
		found = true
		c := &s.clients[i]
		before := len(c.Matters)
		filtered := c.Matters[:0]
		for _, m := range c.Matters {
			if !strings.EqualFold(m.MatterNumber, matterNumber) {
				filtered = append(filtered, m)
			}
		}
		c.Matters = filtered
		if len(c.Matters) < before {
			c.UpdatedAt = time.Now()
			removed = true
		}
		break
	}
	s.mu.Unlock()
	if !found {
		return false, fmt.Errorf("client not found")
	}
	if removed {
		s.persist()
	}
	return removed, nil
}

// Remove deletes the client with the given ID.
// Returns (true, nil) if deleted, (false, nil) if the ID was not found.
func (s *ClientStore) Remove(id string) (bool, error) {
	s.mu.Lock()
	before := len(s.clients)
	filtered := s.clients[:0]
	for _, c := range s.clients {
		if c.ID != id {
			filtered = append(filtered, c)
		}
	}
	s.clients = filtered
	removed := len(s.clients) < before
	s.mu.Unlock()
	if removed {
		s.persist()
	}
	return removed, nil
}

// CheckConflict performs a case-insensitive substring match between newClientName
// and every adversary string listed on every existing client.
func (s *ClientStore) CheckConflict(newClientName string) types.ConflictCheckResult {
	lower := strings.ToLower(strings.TrimSpace(newClientName))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.clients {
		for _, adv := range c.Adversaries {
			if strings.Contains(lower, strings.ToLower(adv)) ||
				strings.Contains(strings.ToLower(adv), lower) {
				return types.ConflictCheckResult{
					HasConflict:           true,
					ConflictingClientID:   c.ID,
					ConflictingClientName: c.Name,
					MatchedAdversary:      adv,
				}
			}
		}
	}
	return types.ConflictCheckResult{HasConflict: false}
}

// SetMatterBudget sets a matter's budget (and optional alert thresholds),
// resetting any previously-fired alert state. Returns false if not found.
func (s *ClientStore) SetMatterBudget(matterNumber string, budgetUsd *float64, thresholds []float64) error {
	s.mu.Lock()
	found := false
	for i := range s.clients {
		for j := range s.clients[i].Matters {
			if s.clients[i].Matters[j].MatterNumber == matterNumber {
				s.clients[i].Matters[j].BudgetUsd = budgetUsd
				if thresholds != nil {
					s.clients[i].Matters[j].BudgetAlertThresholds = thresholds
				}
				s.clients[i].Matters[j].BudgetAlertsTriggered = nil
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf("matter %s not found", matterNumber)
	}
	s.persist()
	return nil
}

// SetMatterBudgetAlerts records which budget thresholds have fired for a matter,
// mutating the live record under lock and persisting. Returns false if the matter
// is not found. This is the correct path for budget-alert dedup state (List
// returns copies, so callers must not mutate those).
func (s *ClientStore) SetMatterBudgetAlerts(matterNumber string, triggered []float64) error {
	s.mu.Lock()
	found := false
	for i := range s.clients {
		for j := range s.clients[i].Matters {
			if s.clients[i].Matters[j].MatterNumber == matterNumber {
				s.clients[i].Matters[j].BudgetAlertsTriggered = triggered
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return fmt.Errorf("matter %s not found", matterNumber)
	}
	s.persist()
	return nil
}

// ─── internal helpers ─────────────────────────────────────────────────────────

// persist writes the client list atomically: write to <path>.tmp then rename.
// 0600: the roster carries adversary lists and matter data.
func (s *ClientStore) persist() {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.RLock()
	data, _ := json.MarshalIndent(s.clients, "", "  ")
	s.mu.RUnlock()
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		slog.Error("clients: persist write failed", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Error("clients: persist rename failed", "path", s.path, "err", err)
	}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
