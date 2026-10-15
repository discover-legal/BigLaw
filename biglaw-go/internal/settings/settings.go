// SPDX-License-Identifier: Apache-2.0
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
	"reflect"
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

// Models is the user-facing model picker: which model performs figure extraction, plus the
// list of model ids offered in the GUI (user-extendable via Available).
type Models struct {
	FigureModel    string   `json:"figureModel"`
	SpineModel     string   `json:"spineModel"`     // BELO conduct/spine pass (e.g. local:qwen2.5:14b)
	SynthesisModel string   `json:"synthesisModel"` // synthesis/drafting the deliverable (the judged memo)
	Available      []string `json:"available"`
}

// Drafting toggles collaborative DyTopo section writing and its huddle size/rounds.
type Drafting struct {
	DyTopo           bool `json:"dytopo"`
	AgentsPerSection int  `json:"agentsPerSection"`
	Rounds           int  `json:"rounds"`
}

// PublicSettings is the GET /settings (and PUT /settings) response.
type PublicSettings struct {
	Presentation Presentation        `json:"presentation"`
	DyTopo       DyTopo              `json:"dytopo"`
	Debate       Debate              `json:"debate"`
	DocuSeal     DocuSealPublic      `json:"docuseal"`
	ClientVoice  ClientVoiceSettings `json:"clientVoice"`
	Models       Models              `json:"models"`
	Drafting     Drafting            `json:"drafting"`
}

// Patch is a deep-partial update; nil fields are left unchanged. Numeric
// fields are *float64 so any JSON number is accepted, then clamped.
type Patch struct {
	Presentation *struct {
		Mode     *string `json:"mode,omitempty"`
		FirmName *string `json:"firmName,omitempty"`
	} `json:"presentation,omitempty"`
	DyTopo *struct {
		MaxRounds           *float64 `json:"maxRounds,omitempty"`
		MaxAgentsPerRound   *float64 `json:"maxAgentsPerRound,omitempty"`
		SimilarityThreshold *float64 `json:"similarityThreshold,omitempty"`
	} `json:"dytopo,omitempty"`
	Debate *struct {
		VerificationPasses      *float64 `json:"verificationPasses,omitempty"`
		GateConfidenceThreshold *float64 `json:"gateConfidenceThreshold,omitempty"`
		AdversarialEnabled      *bool    `json:"adversarialEnabled,omitempty"`
		CitationRequired        *bool    `json:"citationRequired,omitempty"`
	} `json:"debate,omitempty"`
	DocuSeal *struct {
		Enabled *bool   `json:"enabled,omitempty"`
		URL     *string `json:"url,omitempty"`
		APIKey  *string `json:"apiKey,omitempty"`
	} `json:"docuseal,omitempty"`
	ClientVoice *struct {
		GateNotes           *bool `json:"gateNotes,omitempty"`
		MatterNotifications *bool `json:"matterNotifications,omitempty"`
	} `json:"clientVoice,omitempty"`
	Models *struct {
		FigureModel    *string  `json:"figureModel,omitempty"`
		SpineModel     *string  `json:"spineModel,omitempty"`
		SynthesisModel *string  `json:"synthesisModel,omitempty"`
		Available      []string `json:"available,omitempty"`
	} `json:"models,omitempty"`
	Drafting *struct {
		DyTopo           *bool    `json:"dytopo,omitempty"`
		AgentsPerSection *float64 `json:"agentsPerSection,omitempty"`
		Rounds           *float64 `json:"rounds,omitempty"`
	} `json:"drafting,omitempty"`
}

// ─── Store ────────────────────────────────────────────────────────────────────

// SettingsStore applies persisted overrides onto the live config and writes
// updates back to disk atomically (write-to-tmp then rename).
//
// Precedence: the settings file is an overlay of the fields an admin changed
// in the UI, not a snapshot of the whole config. On every save only the
// fields whose value differs from the environment/defaults (the config as it
// was before any overlay — the baseline) are written, in the same sparse
// shape as a PUT /settings Patch. So:
//
//   - a field the admin changed keeps the admin's value across restarts, even
//     if the environment later changes it (the file wins for that field);
//   - every other field follows the environment: changing e.g. DOCUSEAL_URL
//     and restarting takes effect, even after an unrelated Save;
//   - setting a field back to its environment value removes it from the file.
//
// Files written by older versions (full snapshots) still load; their values
// that match the environment are dropped on the next save.
type SettingsStore struct {
	mu       sync.Mutex
	cfg      *config.Config
	path     string
	baseline Patch // env/default values, captured before any overlay
}

// NewSettingsStore creates a SettingsStore that overlays cfg and persists to
// path. It must be called before anything else mutates cfg's settings fields:
// the values it sees are the environment baseline. Call Init to load any
// previously persisted settings.
func NewSettingsStore(cfg *config.Config, path string) *SettingsStore {
	return &SettingsStore{cfg: cfg, path: path, baseline: snapshot(cfg)}
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
	p = s.dropUnchangedURL(p)
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
		Models:   Models{FigureModel: c.Models.FigureModel, SpineModel: c.Models.SpineModel, SynthesisModel: c.Models.SynthesisModel, Available: c.Models.Available},
		Drafting: Drafting{DyTopo: c.Drafting.DyTopo, AgentsPerSection: c.Drafting.AgentsPerSection, Rounds: c.Drafting.Rounds},
	}
}

// dropUnchangedURL removes a DocuSeal URL from the patch when it equals the
// current effective value. The settings form always round-trips every field,
// so without this the operator-supplied default (DOCUSEAL_URL, which is
// http://localhost:3000 out of the box) was re-validated as if an admin had
// typed it, rejected as private, and no setting could ever be saved.
//
// SSRF protection is unchanged for anything a request can introduce: a URL
// that differs from the current one is still required to be public. Values
// that came from the environment or an earlier validated save are trusted
// exactly as they already were at load time. Gating on "only when enabled"
// instead would let a private URL be stored while disabled and then armed by
// flipping the toggle, so the comparison is against the value, not the flag.
func (s *SettingsStore) dropUnchangedURL(p Patch) Patch {
	if p.DocuSeal == nil || p.DocuSeal.URL == nil ||
		strings.TrimSpace(*p.DocuSeal.URL) != s.cfg.DocuSeal.URL {
		return p
	}
	ds := *p.DocuSeal // copy: never mutate the caller's patch
	ds.URL = nil
	p.DocuSeal = &ds
	return p
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
	if p.Models != nil {
		if v := p.Models.FigureModel; v != nil {
			c.Models.FigureModel = strings.TrimSpace(*v)
		}
		if v := p.Models.SpineModel; v != nil {
			c.Models.SpineModel = strings.TrimSpace(*v)
		}
		if v := p.Models.SynthesisModel; v != nil {
			c.Models.SynthesisModel = strings.TrimSpace(*v)
		}
		if p.Models.Available != nil { // full replace: the GUI sends the edited list
			cleaned := make([]string, 0, len(p.Models.Available))
			seen := map[string]bool{}
			for _, m := range p.Models.Available {
				m = strings.TrimSpace(m)
				if m != "" && !seen[m] {
					seen[m] = true
					cleaned = append(cleaned, m)
				}
			}
			c.Models.Available = cleaned
		}
	}
	if p.Drafting != nil {
		if v := p.Drafting.DyTopo; v != nil {
			c.Drafting.DyTopo = *v
		}
		if v := p.Drafting.AgentsPerSection; v != nil {
			c.Drafting.AgentsPerSection = clampInt(*v, 1, 5, c.Drafting.AgentsPerSection)
		}
		if v := p.Drafting.Rounds; v != nil {
			c.Drafting.Rounds = clampInt(*v, 1, 4, c.Drafting.Rounds)
		}
	}
}

// persist writes the admin overlay — only fields that differ from the
// baseline — to disk atomically: marshal to path+".tmp" then rename over path.
func (s *SettingsStore) persist() error {
	overlay := diffPatch(snapshot(s.cfg), s.baseline)
	data, err := json.MarshalIndent(overlay, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// snapshot renders every settings field of c as a fully populated Patch
// (the API key included — the file is 0600 and never served).
func snapshot(c *config.Config) Patch {
	var p Patch
	f := func(v float64) *float64 { return &v }
	p.Presentation = &struct {
		Mode     *string `json:"mode,omitempty"`
		FirmName *string `json:"firmName,omitempty"`
	}{ptr(c.Presentation.Mode), ptr(c.Presentation.FirmName)}
	p.DyTopo = &struct {
		MaxRounds           *float64 `json:"maxRounds,omitempty"`
		MaxAgentsPerRound   *float64 `json:"maxAgentsPerRound,omitempty"`
		SimilarityThreshold *float64 `json:"similarityThreshold,omitempty"`
	}{f(float64(c.DyTopo.MaxRounds)), f(float64(c.DyTopo.MaxAgentsPerRound)), f(c.DyTopo.SimilarityThreshold)}
	p.Debate = &struct {
		VerificationPasses      *float64 `json:"verificationPasses,omitempty"`
		GateConfidenceThreshold *float64 `json:"gateConfidenceThreshold,omitempty"`
		AdversarialEnabled      *bool    `json:"adversarialEnabled,omitempty"`
		CitationRequired        *bool    `json:"citationRequired,omitempty"`
	}{f(float64(c.Debate.VerificationPasses)), f(c.Debate.GateConfidenceThreshold), ptr(c.Debate.AdversarialEnabled), ptr(c.Debate.CitationRequired)}
	p.DocuSeal = &struct {
		Enabled *bool   `json:"enabled,omitempty"`
		URL     *string `json:"url,omitempty"`
		APIKey  *string `json:"apiKey,omitempty"`
	}{ptr(c.DocuSeal.Enabled), ptr(c.DocuSeal.URL), ptr(c.DocuSeal.APIKey)}
	p.ClientVoice = &struct {
		GateNotes           *bool `json:"gateNotes,omitempty"`
		MatterNotifications *bool `json:"matterNotifications,omitempty"`
	}{ptr(c.ClientVoice.GateNotes), ptr(c.ClientVoice.MatterNotifications)}
	p.Models = &struct {
		FigureModel    *string  `json:"figureModel,omitempty"`
		SpineModel     *string  `json:"spineModel,omitempty"`
		SynthesisModel *string  `json:"synthesisModel,omitempty"`
		Available      []string `json:"available,omitempty"`
	}{ptr(c.Models.FigureModel), ptr(c.Models.SpineModel), ptr(c.Models.SynthesisModel), append([]string{}, c.Models.Available...)}
	p.Drafting = &struct {
		DyTopo           *bool    `json:"dytopo,omitempty"`
		AgentsPerSection *float64 `json:"agentsPerSection,omitempty"`
		Rounds           *float64 `json:"rounds,omitempty"`
	}{ptr(c.Drafting.DyTopo), f(float64(c.Drafting.AgentsPerSection)), f(float64(c.Drafting.Rounds))}
	return p
}

func ptr[T any](v T) *T { return &v }

// diffPatch returns cur with every field equal to base cleared (nil), and
// every section left with no fields cleared to nil, so the result holds only
// what differs. Both arguments are full snapshots.
func diffPatch(cur, base Patch) Patch {
	cv := reflect.ValueOf(&cur).Elem()
	bv := reflect.ValueOf(base)
	for i := range cv.NumField() {
		cs, bs := cv.Field(i), bv.Field(i) // *struct sections
		if cs.IsNil() {
			continue
		}
		if bs.IsNil() {
			continue // nothing to compare against: keep the section whole
		}
		kept := 0
		for j := range cs.Elem().NumField() {
			cf, bf := cs.Elem().Field(j), bs.Elem().Field(j)
			if reflect.DeepEqual(cf.Interface(), bf.Interface()) {
				cf.Set(reflect.Zero(cf.Type()))
			} else {
				kept++
			}
		}
		if kept == 0 {
			cs.Set(reflect.Zero(cs.Type()))
		}
	}
	return cur
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
