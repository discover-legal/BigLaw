// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package auth

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

var LocalPartner = types.SessionUser{
	ProfileID: "local-partner",
	Name:      "Local Partner",
	Email:     "local@bigmichael.dev",
	Role:      types.RolePartner,
	Mode:      types.ModeAdmin,
}

func ResolveMode(role types.LawyerRole, stored types.UserMode) types.UserMode {
	if role == types.RolePartner {
		return types.ModeAdmin
	}
	if stored == types.ModeLite {
		return types.ModeLite
	}
	return types.ModeFullFlavour
}

func IsPartner(u *types.SessionUser) bool {
	return u != nil && u.Role == types.RolePartner
}

func CanViewTask(u *types.SessionUser, assignedIDs []string) bool {
	if u == nil {
		return false
	}
	if u.Role == types.RolePartner {
		return true
	}
	for _, id := range assignedIDs {
		if id == u.ProfileID {
			return true
		}
	}
	return false
}

// ─── ProfileStore ─────────────────────────────────────────────────────────────

type ProfileStore struct {
	mu        sync.RWMutex
	persistMu sync.Mutex // serialises concurrent fire-and-forget persists
	profiles  []types.LawyerProfile
	path      string
	cfg       *config.Config
}

func NewProfileStore(cfg *config.Config) *ProfileStore {
	return &ProfileStore{path: cfg.Persistence.ProfilesFile, cfg: cfg}
}

func (s *ProfileStore) Init() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.profiles = nil
		} else {
			return err
		}
	} else {
		s.mu.Lock()
		json.Unmarshal(data, &s.profiles)
		s.mu.Unlock()
	}

	// When auth is off, ensure the local partner profile exists.
	if !s.cfg.Auth.Enabled {
		if s.Get(LocalPartner.ProfileID) == nil {
			p := types.LawyerProfile{
				ID:        LocalPartner.ProfileID,
				Name:      LocalPartner.Name,
				Email:     LocalPartner.Email,
				Role:      types.RolePartner,
				Mode:      types.ModeAdmin,
				Title:     "Local development",
				Color:     "#E6B450",
				CreatedAt: time.Now(),
			}
			s.mu.Lock()
			s.profiles = append([]types.LawyerProfile{p}, s.profiles...)
			s.mu.Unlock()
			s.persist()
		}
	}
	return nil
}

func (s *ProfileStore) List() []types.LawyerProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.LawyerProfile, len(s.profiles))
	copy(out, s.profiles)
	return out
}

func (s *ProfileStore) Get(id string) *types.LawyerProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, p := range s.profiles {
		if p.ID == id {
			cp := s.profiles[i]
			return &cp
		}
	}
	return nil
}

func (s *ProfileStore) GetByEmail(email string) *types.LawyerProfile {
	lower := strings.ToLower(email)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, p := range s.profiles {
		if strings.ToLower(p.Email) == lower {
			cp := s.profiles[i]
			return &cp
		}
	}
	return nil
}

type CreateProfileInput struct {
	Name          string
	Email         string
	Role          string
	Title         string
	Color         string
	PracticeAreas []string
	Bio           string
	Mode          string
}

func (s *ProfileStore) Create(input CreateProfileInput) (*types.LawyerProfile, error) {
	name := strings.TrimSpace(input.Name)
	email := strings.TrimSpace(input.Email)
	if name == "" || email == "" {
		return nil, fmt.Errorf("name and email are required")
	}
	if s.GetByEmail(email) != nil {
		return nil, fmt.Errorf("profile with email %s already exists", email)
	}
	role := types.RoleLawyer
	if input.Role == "partner" {
		role = types.RolePartner
	}
	mode := ResolveMode(role, types.UserMode(input.Mode))

	id, _ := generateID()
	p := types.LawyerProfile{
		ID:            id,
		Name:          strutil.Truncate(name, 200),
		Email:         strutil.Truncate(email, 254),
		Role:          role,
		Mode:          mode,
		Title:         trunc(input.Title, 200),
		Color:         colorOrPick(input.Color, name),
		PracticeAreas: input.PracticeAreas,
		Bio:           trunc(input.Bio, 2000),
		CreatedAt:     time.Now(),
	}
	s.mu.Lock()
	s.profiles = append(s.profiles, p)
	s.mu.Unlock()
	s.persist()
	return &p, nil
}

func (s *ProfileStore) Update(id string, patch map[string]interface{}) (*types.LawyerProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.profiles {
		if s.profiles[i].ID != id {
			continue
		}
		p := &s.profiles[i]
		if v, ok := patch["name"].(string); ok && v != "" {
			p.Name = trunc(strings.TrimSpace(v), 200)
		}
		if v, ok := patch["title"].(string); ok {
			p.Title = trunc(strings.TrimSpace(v), 200)
		}
		if v, ok := patch["bio"].(string); ok {
			p.Bio = trunc(strings.TrimSpace(v), 2000)
		}
		if v, ok := patch["color"].(string); ok && profileColorRE.MatchString(strings.TrimSpace(v)) {
			p.Color = strings.ToUpper(strings.TrimSpace(v))
		}
		if v, ok := patch["role"].(string); ok {
			if v == "partner" {
				p.Role = types.RolePartner
			} else {
				p.Role = types.RoleLawyer
			}
		}
		if v, ok := patch["mode"].(string); ok {
			p.Mode = ResolveMode(p.Role, types.UserMode(v))
		} else {
			p.Mode = ResolveMode(p.Role, p.Mode)
		}
		cp := *p
		go s.persist()
		return &cp, nil
	}
	return nil, fmt.Errorf("profile not found")
}

func (s *ProfileStore) UpdateTone(id string, tone *types.ToneProfile) (*types.LawyerProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			s.profiles[i].ToneProfile = tone
			cp := s.profiles[i]
			go s.persist()
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("profile not found")
}

func (s *ProfileStore) Remove(id string) (bool, error) {
	if id == LocalPartner.ProfileID {
		return false, fmt.Errorf("cannot delete local development profile")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := len(s.profiles)
	filtered := s.profiles[:0]
	for _, p := range s.profiles {
		if p.ID != id {
			filtered = append(filtered, p)
		}
	}
	s.profiles = filtered
	if len(s.profiles) == before {
		return false, nil
	}
	go s.persist()
	return true, nil
}

// persist writes the roster atomically. 0600: profiles carry emails, bios,
// and tone fingerprints.
func (s *ProfileStore) persist() {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.RLock()
	data, _ := json.MarshalIndent(s.profiles, "", "  ")
	s.mu.RUnlock()
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		slog.Error("profiles: persist write failed", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Error("profiles: persist rename failed", "path", s.path, "err", err)
	}
}

// ─── Utilities ────────────────────────────────────────────────────────────────

var palette = []string{"#E6B450", "#84A9CC", "#7FB069", "#DA6A60", "#E0913C", "#B08BD6", "#5FB0B7"}
var profileColorRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func colorOrPick(color, seed string) string {
	if profileColorRE.MatchString(strings.TrimSpace(color)) {
		return strings.ToUpper(strings.TrimSpace(color))
	}
	h := 0
	for _, c := range seed {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return palette[h%len(palette)]
}

func trunc(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return strutil.Truncate(s, max)
	}
	return s
}

func generateID() (string, error) {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
