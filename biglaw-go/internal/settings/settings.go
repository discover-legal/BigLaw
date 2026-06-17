// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package settings backs the admin panel. Port of src/settings/index.ts:
// the orchestration knobs (DyTopo depth, verification passes, gate threshold)
// and the DocuSeal connection live on the shared *config.Config, which read
// sites consult at runtime. This store persists a small JSON file and applies
// it onto the config in place, so admin-panel changes take effect live
// without a restart. Env vars remain the defaults; the file overrides them.
//
// Concurrency: all writes to the config go through the store mutex; read
// sites access the config unsynchronized. This mirrors the TS backend's
// live-override semantics — overridden fields are scalars and a stale read
// is harmless (next round picks up the new value).
package settings

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/urlguard"
)

// ─── Wire types — must match ui/src/types.ts AppSettings ─────────────────────

type Presentation struct {
	Mode     string `json:"mode"`
	FirmName string `json:"firmName"`
}

type DyTopo struct {
	MaxRounds           int     `json:"maxRounds"`
	MaxAgentsPerRound   int     `json:"maxAgentsPerRound"`
	SimilarityThreshold float64 `json:"similarityThreshold"`
}

type Debate struct {
	VerificationPasses      int     `json:"verificationPasses"`
	GateConfidenceThreshold float64 `json:"gateConfidenceThreshold"`
	AdversarialEnabled      bool    `json:"adversarialEnabled"`
	CitationRequired        bool    `json:"citationRequired"`
}

// DocuSealPublic is the client-facing view — the API key is never returned.
type DocuSealPublic struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	APIKeySet bool   `json:"apiKeySet"`
}

// ClientVoiceSettings governs the Remy/CNTXT client-advocate integration.
type ClientVoiceSettings struct {
	GateNotes           bool `json:"gateNotes"`
	MatterNotifications bool `json:"matterNotifications"`
}

// PublicSettings is the GET /settings (and PUT /settings) response.
type PublicSettings struct {
	Presentation Presentation        `json:"presentation"`
	DyTopo       DyTopo              `json:"dytopo"`
	Debate       Debate              `json:"debate"`
	DocuSeal     DocuSealPublic      `json:"docuseal"`
	ClientVoice  ClientVoiceSettings `json:"clientVoice"`
}

// persisted is the on-disk file shape: full settings including the API key.
type persisted struct {
	Presentation Presentation `json:"presentation"`
	DyTopo       DyTopo       `json:"dytopo"`
	Debate       Debate       `json:"debate"`
	DocuSeal     struct {
		Enabled bool   `json:"enabled"`
		URL     string `json:"url"`
		APIKey  string `json:"apiKey"`
	} `json:"docuseal"`
	ClientVoice ClientVoiceSettings `json:"clientVoice"`
}

// Patch is a deep-partial update; nil fields are left unchanged. Numeric
// fields are *float64 so any JSON number is accepted, then clamped.
type Patch struct {
	Presentation *struct {
		Mode     *string `json:"mode"`
		FirmName *string `json:"firmName"`
	} `json:"presentation"`
	DyTopo *struct {
		MaxRounds           *float64 `json:"maxRounds"`
		MaxAgentsPerRound   *float64 `json:"maxAgentsPerRound"`
		SimilarityThreshold *float64 `json:"similarityThreshold"`
	} `json:"dytopo"`
	Debate *struct {
		VerificationPasses      *float64 `json:"verificationPasses"`
		GateConfidenceThreshold *float64 `json:"gateConfidenceThreshold"`
		AdversarialEnabled      *bool    `json:"adversarialEnabled"`
		CitationRequired        *bool    `json:"citationRequired"`
	} `json:"debate"`
	DocuSeal *struct {
		Enabled *bool   `json:"enabled"`
		URL     *string `json:"url"`
		APIKey  *string `json:"apiKey"`
	} `json:"docuseal"`
	ClientVoice *struct {
		GateNotes           *bool `json:"gateNotes"`
		MatterNotifications *bool `json:"matterNotifications"`
	} `json:"clientVoice"`
}

// ─── Store ────────────────────────────────────────────────────────────────────

// SettingsStore applies persisted overrides onto the live config and writes
// updates back to disk atomically (write-to-tmp then rename).
type SettingsStore struct {
	mu   sync.Mutex
	cfg  *config.Config
	path string
}

// NewSettingsStore creates a SettingsStore that overlays cfg and persists to
// path. Call Init to load any previously persisted settings.
func NewSettingsStore(cfg *config.Config, path string) *SettingsStore {
	return &SettingsStore{cfg: cfg, path: path}
}

// Init loads persisted overrides (if any) and applies them onto the config.
// A missing file is silently ignored; any other error is returned.
func (s *SettingsStore) Init() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var p Patch
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("settings file %s: %w", s.path, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Skip URL validation on load: the persisted URL was validated when set,
	// and a now-invalid value must not abort the rest of the overlay.
	s.apply(p, false)
	return nil
}

// Get returns the current effective settings with the DocuSeal key redacted.
func (s *SettingsStore) Get() PublicSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.public()
}

// Update applies a partial patch onto the config (with validation and
// clamping), persists the full settings to disk, and returns the result.
func (s *SettingsStore) Update(p Patch) (PublicSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(p); err != nil {
		return PublicSettings{}, err
	}
	s.apply(p, true)
	if err := s.persist(); err != nil {
		return PublicSettings{}, fmt.Errorf("persist settings: %w", err)
	}
	return s.public(), nil
}

// ─── Internals (call with s.mu held) ──────────────────────────────────────────

func (s *SettingsStore) public() PublicSettings {
	c := s.cfg
	return PublicSettings{
		Presentation: Presentation{Mode: c.Presentation.Mode, FirmName: c.Presentation.FirmName},
		DyTopo: DyTopo{
			MaxRounds:           c.DyTopo.MaxRounds,
			MaxAgentsPerRound:   c.DyTopo.MaxAgentsPerRound,
			SimilarityThreshold: c.DyTopo.SimilarityThreshold,
		},
		Debate: Debate{
			VerificationPasses:      c.Debate.VerificationPasses,
			GateConfidenceThreshold: c.Debate.GateConfidenceThreshold,
			AdversarialEnabled:      c.Debate.AdversarialEnabled,
			CitationRequired:        c.Debate.CitationRequired,
		},
		DocuSeal: DocuSealPublic{
			Enabled:   c.DocuSeal.Enabled,
			URL:       c.DocuSeal.URL,
			APIKeySet: c.DocuSeal.APIKey != "",
		},
		ClientVoice: ClientVoiceSettings{
			GateNotes:           c.ClientVoice.GateNotes,
			MatterNotifications: c.ClientVoice.MatterNotifications,
		},
	}
}

// validate rejects values that must fail the whole update (currently only
// the DocuSeal URL — clamps handle out-of-range numerics silently, as TS does).
func (s *SettingsStore) validate(p Patch) error {
	if p.DocuSeal != nil && p.DocuSeal.URL != nil {
		if _, err := assertPublicHTTPURL(*p.DocuSeal.URL, "DocuSeal URL"); err != nil {
			return err
		}
	}
	return nil
}

func (s *SettingsStore) apply(p Patch, validateURL bool) {
	c := s.cfg
	if p.Presentation != nil {
		if m := p.Presentation.Mode; m != nil && (*m == "lawyer" || *m == "plain") {
			c.Presentation.Mode = *m
		}
		if f := p.Presentation.FirmName; f != nil {
			c.Presentation.FirmName = truncate(*f, 200)
		}
	}
	if p.DyTopo != nil {
		if v := p.DyTopo.MaxRounds; v != nil {
			c.DyTopo.MaxRounds = clampInt(*v, 1, 30, c.DyTopo.MaxRounds)
		}
		if v := p.DyTopo.MaxAgentsPerRound; v != nil {
			c.DyTopo.MaxAgentsPerRound = clampInt(*v, 1, 48, c.DyTopo.MaxAgentsPerRound)
		}
		if v := p.DyTopo.SimilarityThreshold; v != nil {
			c.DyTopo.SimilarityThreshold = clampFloat(*v, 0.1, 0.99, c.DyTopo.SimilarityThreshold)
		}
	}
	if p.Debate != nil {
		if v := p.Debate.VerificationPasses; v != nil {
			c.Debate.VerificationPasses = clampInt(*v, 0, 25, c.Debate.VerificationPasses)
		}
		if v := p.Debate.GateConfidenceThreshold; v != nil {
			c.Debate.GateConfidenceThreshold = clampFloat(*v, 0, 1, c.Debate.GateConfidenceThreshold)
		}
		if v := p.Debate.AdversarialEnabled; v != nil {
			c.Debate.AdversarialEnabled = *v
		}
		if v := p.Debate.CitationRequired; v != nil {
			c.Debate.CitationRequired = *v
		}
	}
	if p.DocuSeal != nil {
		if v := p.DocuSeal.Enabled; v != nil {
			c.DocuSeal.Enabled = *v
		}
		if v := p.DocuSeal.URL; v != nil {
			if validateURL {
				if u, err := assertPublicHTTPURL(*v, "DocuSeal URL"); err == nil {
					c.DocuSeal.URL = u
				}
			} else {
				c.DocuSeal.URL = strings.TrimSpace(*v)
			}
		}
		if v := p.DocuSeal.APIKey; v != nil {
			c.DocuSeal.APIKey = strings.TrimSpace(*v)
		}
	}
	if p.ClientVoice != nil {
		if v := p.ClientVoice.GateNotes; v != nil {
			c.ClientVoice.GateNotes = *v
		}
		if v := p.ClientVoice.MatterNotifications; v != nil {
			c.ClientVoice.MatterNotifications = *v
		}
	}
}

// persist writes the full current settings (including the API key) to disk
// atomically: marshal to path+".tmp" then rename over path.
func (s *SettingsStore) persist() error {
	c := s.cfg
	var p persisted
	p.Presentation = Presentation{Mode: c.Presentation.Mode, FirmName: c.Presentation.FirmName}
	p.DyTopo = DyTopo{
		MaxRounds:           c.DyTopo.MaxRounds,
		MaxAgentsPerRound:   c.DyTopo.MaxAgentsPerRound,
		SimilarityThreshold: c.DyTopo.SimilarityThreshold,
	}
	p.Debate = Debate{
		VerificationPasses:      c.Debate.VerificationPasses,
		GateConfidenceThreshold: c.Debate.GateConfidenceThreshold,
		AdversarialEnabled:      c.Debate.AdversarialEnabled,
		CitationRequired:        c.Debate.CitationRequired,
	}
	p.DocuSeal.Enabled = c.DocuSeal.Enabled
	p.DocuSeal.URL = c.DocuSeal.URL
	p.DocuSeal.APIKey = c.DocuSeal.APIKey
	p.ClientVoice = ClientVoiceSettings{
		GateNotes:           c.ClientVoice.GateNotes,
		MatterNotifications: c.ClientVoice.MatterNotifications,
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// assertPublicHTTPURL validates that raw is a public http/https URL (no SSRF
// via private/loopback addresses). Delegates to the shared urlguard validator.
func assertPublicHTTPURL(raw, label string) (string, error) {
	return urlguard.AssertPublicHTTP(raw, label)
}

func clampInt(v float64, lo, hi int, dflt int) int {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dflt
	}
	n := int(math.Round(v))
	return min(hi, max(lo, n))
}

func clampFloat(v, lo, hi, dflt float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return dflt
	}
	return math.Min(hi, math.Max(lo, v))
}

func truncate(s string, n int) string {
	return strutil.Truncate(s, n)
}
