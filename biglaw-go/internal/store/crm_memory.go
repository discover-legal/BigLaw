// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ─── CRMRepository (memory) ──────────────────────────────────────────────────

func (m *MemoryRepo) PutCRMProfile(ctx context.Context, p CRMProfile) error {
	if !CanAccessFirm(ctx) {
		return fmt.Errorf("store: firm identity required")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.crmProfiles[p.ID] = p
	return nil
}

func (m *MemoryRepo) GetCRMProfile(ctx context.Context, id string) (*CRMProfile, bool, error) {
	return m.findCRMProfile(ctx, func(p CRMProfile) bool { return p.ID == id })
}

func (m *MemoryRepo) FindCRMProfileByExternalRef(ctx context.Context, ref string) (*CRMProfile, bool, error) {
	if ref == "" {
		return nil, false, nil
	}
	return m.findCRMProfile(ctx, func(p CRMProfile) bool { return p.ExternalRef == ref })
}

func (m *MemoryRepo) FindCRMProfileByClientID(ctx context.Context, clientID string) (*CRMProfile, bool, error) {
	if clientID == "" {
		return nil, false, nil
	}
	return m.findCRMProfile(ctx, func(p CRMProfile) bool { return p.ClientID == clientID })
}

func (m *MemoryRepo) findCRMProfile(ctx context.Context, match func(CRMProfile) bool) (*CRMProfile, bool, error) {
	if !CanAccessFirm(ctx) {
		return nil, false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.crmProfiles {
		if match(p) {
			cp := p
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryRepo) ListCRMProfiles(ctx context.Context) ([]CRMProfile, error) {
	if !CanAccessFirm(ctx) {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]CRMProfile, 0, len(m.crmProfiles))
	for _, p := range m.crmProfiles {
		out = append(out, p)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, nil
}

func (m *MemoryRepo) PutCRMFact(ctx context.Context, f CRMFact) error {
	if !CanAccessFirm(ctx) {
		return fmt.Errorf("store: firm identity required")
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.crmFacts[f.ID] = f
	return nil
}

func (m *MemoryRepo) GetCRMFact(ctx context.Context, id string) (*CRMFact, bool, error) {
	if !CanAccessFirm(ctx) {
		return nil, false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.crmFacts[id]
	if !ok {
		return nil, false, nil
	}
	cp := f
	return &cp, true, nil
}

func (m *MemoryRepo) ListCRMFacts(ctx context.Context, profileID, status string) ([]CRMFact, error) {
	return m.filterCRMFacts(ctx, func(f CRMFact) bool {
		return f.ProfileID == profileID && (status == "" || f.Status == status)
	})
}

func (m *MemoryRepo) ListPendingCRMFacts(ctx context.Context, approverRole string) ([]CRMFact, error) {
	return m.filterCRMFacts(ctx, func(f CRMFact) bool {
		return f.Status == "pending" && f.ApproverRole == approverRole
	})
}

func (m *MemoryRepo) filterCRMFacts(ctx context.Context, match func(CRMFact) bool) ([]CRMFact, error) {
	if !CanAccessFirm(ctx) {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []CRMFact
	for _, f := range m.crmFacts {
		if match(f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, nil
}

// ─── IntakeRepository (memory) ───────────────────────────────────────────────

func (m *MemoryRepo) PutIntakeSubmission(ctx context.Context, s IntakeSubmission) error {
	if !CanAccessFirm(ctx) {
		return fmt.Errorf("store: firm identity required")
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now()
	}
	s.ConflictJSON = append([]byte(nil), s.ConflictJSON...)
	s.MetadataJSON = append([]byte(nil), s.MetadataJSON...)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.intakes[s.ID] = s
	return nil
}

func (m *MemoryRepo) GetIntakeSubmission(ctx context.Context, id string) (*IntakeSubmission, bool, error) {
	if !CanAccessFirm(ctx) {
		return nil, false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.intakes[id]
	if !ok {
		return nil, false, nil
	}
	cp := s
	return &cp, true, nil
}

func (m *MemoryRepo) FindIntakeSubmissionByExternalID(ctx context.Context, profileID, externalID string) (*IntakeSubmission, bool, error) {
	if !CanAccessFirm(ctx) || externalID == "" {
		return nil, false, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.intakes {
		if s.ProfileID == profileID && s.ExternalID == externalID {
			cp := s
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryRepo) ListIntakeSubmissionsByProfile(ctx context.Context, profileID string) ([]IntakeSubmission, error) {
	return m.filterIntakes(ctx, func(s IntakeSubmission) bool { return s.ProfileID == profileID })
}

func (m *MemoryRepo) ListIntakeSubmissions(ctx context.Context) ([]IntakeSubmission, error) {
	return m.filterIntakes(ctx, func(IntakeSubmission) bool { return true })
}

func (m *MemoryRepo) filterIntakes(ctx context.Context, match func(IntakeSubmission) bool) ([]IntakeSubmission, error) {
	if !CanAccessFirm(ctx) {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []IntakeSubmission
	for _, s := range m.intakes {
		if match(s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, nil
}
