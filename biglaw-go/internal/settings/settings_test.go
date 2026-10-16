// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
)

func TestAssertPublicHTTPURL(t *testing.T) {
	rejected := []string{
		// Pre-existing blocklist
		"http://localhost:3101",
		"http://127.0.0.1",
		"http://[::1]:8080",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.5",
		"http://172.16.0.1",
		"http://172.31.255.255",
		"http://192.168.1.1",
		"http://[fc00::1]",
		"http://[fe80::1]",
		// Expanded blocklist (port of TS 3428a26 / f9f5bad)
		"http://0.0.0.0",                // IPv4 unspecified
		"http://0.1.2.3",                // 0.0.0.0/8 "this network"
		"http://[::]",                   // IPv6 unspecified
		"http://0x7f000001",             // hex-encoded 127.0.0.1
		"http://0X7F000001",             // hex, upper-case
		"http://2130706433",             // bare-decimal 127.0.0.1
		"http://100.64.0.1",             // 100.64.0.0/10 CGNAT, low edge
		"http://100.127.255.255",        // 100.64.0.0/10 CGNAT, high edge
		"http://[::ffff:127.0.0.1]",     // IPv4-mapped IPv6 loopback
		"http://[::ffff:10.0.0.1]:8080", // IPv4-mapped IPv6 RFC-1918
		// Not http(s) / unparseable
		"ftp://example.com",
		"not a url",
		"",
	}
	for _, raw := range rejected {
		if _, err := assertPublicHTTPURL(raw, "test"); err == nil {
			t.Errorf("assertPublicHTTPURL(%q) = nil error, want rejection", raw)
		}
	}

	accepted := []string{
		"https://example.com",
		"http://example.com:8080/path",
		"https://8.8.8.8",
		"http://100.63.255.255", // just below CGNAT range
		"http://100.128.0.0",    // just above CGNAT range
		"https://api.anthropic.com/v1",
	}
	for _, raw := range accepted {
		if _, err := assertPublicHTTPURL(raw, "test"); err != nil {
			t.Errorf("assertPublicHTTPURL(%q) = %v, want accepted", raw, err)
		}
	}
}

func patchFrom(t *testing.T, raw string) Patch {
	t.Helper()
	var p Patch
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// A fresh install has DOCUSEAL_URL=http://localhost:3000 (the default) and the
// settings form sends every field back on save. Saving must not trip the SSRF
// guard on a value the admin did not change — but a changed private URL must
// still be rejected, whether or not DocuSeal is enabled.
func TestUpdateDocuSealURLValidatesOnlyChangedValues(t *testing.T) {
	cfg := &config.Config{}
	cfg.DocuSeal.URL = "http://localhost:3000"
	cfg.DyTopo.MaxRounds = 3
	st := NewSettingsStore(cfg, filepath.Join(t.TempDir(), "settings.json"))

	got, err := st.Update(patchFrom(t, `{"dytopo":{"maxRounds":5},
		"docuseal":{"enabled":false,"url":"http://localhost:3000"}}`))
	if err != nil {
		t.Fatalf("unchanged default URL blocked the save: %v", err)
	}
	if got.DyTopo.MaxRounds != 5 || got.DocuSeal.URL != "http://localhost:3000" {
		t.Fatalf("save not applied: %+v", got)
	}
	// Surrounding whitespace is still "unchanged".
	if _, err := st.Update(patchFrom(t, `{"docuseal":{"url":"  http://localhost:3000 "}}`)); err != nil {
		t.Fatalf("whitespace-only difference rejected: %v", err)
	}

	for _, raw := range []string{
		`{"docuseal":{"enabled":false,"url":"http://169.254.169.254/latest"}}`,
		`{"docuseal":{"enabled":true,"url":"http://10.0.0.5:3000"}}`,
		`{"docuseal":{"url":"http://localhost:3001"}}`,
	} {
		if _, err := st.Update(patchFrom(t, raw)); err == nil {
			t.Errorf("changed private URL accepted: %s", raw)
		}
	}
	if cfg.DocuSeal.URL != "http://localhost:3000" {
		t.Fatalf("rejected update leaked into config: %q", cfg.DocuSeal.URL)
	}

	got, err = st.Update(patchFrom(t, `{"docuseal":{"url":"https://sign.example.com"}}`))
	if err != nil || got.DocuSeal.URL != "https://sign.example.com" {
		t.Fatalf("public URL change: %v %+v", err, got.DocuSeal)
	}
}

func newStore(t *testing.T, path string, mutate func(*config.Config)) (*SettingsStore, *config.Config) {
	t.Helper()
	cfg := &config.Config{}
	cfg.DocuSeal.URL = "http://localhost:3000"
	cfg.DyTopo.MaxRounds = 3
	cfg.Models.Available = []string{"a", "b"}
	if mutate != nil {
		mutate(cfg) // simulates the environment at this boot
	}
	cfg.DocuSeal.OperatorURL = cfg.DocuSeal.URL // as config.Load sets it
	st := NewSettingsStore(cfg, path)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	return st, cfg
}

// The settings file holds only what the admin changed; everything else keeps
// following the environment across restarts.
func TestPersistOnlyAdminChangedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	st, _ := newStore(t, path, nil)
	if _, err := st.Update(patchFrom(t, `{"dytopo":{"maxRounds":5},
		"docuseal":{"enabled":false,"url":"http://localhost:3000"},
		"models":{"spineModel":"local:qwen2.5:14b","synthesisModel":"claude-x","available":["a","b"]}}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var file settingsFile
	if err := json.Unmarshal(raw, &file); err != nil || file.V != fileVersion {
		t.Fatalf("file not versioned: %v %s", err, raw)
	}
	var onDisk map[string]map[string]any
	trimmed, _ := json.Marshal(file.Patch) // the overlay without the version
	if err := json.Unmarshal(trimmed, &onDisk); err != nil {
		t.Fatal(err)
	}
	if _, ok := onDisk["docuseal"]; ok {
		t.Errorf("unchanged DocuSeal settings were persisted: %s", raw)
	}
	if onDisk["dytopo"]["maxRounds"] != 5.0 || onDisk["models"]["spineModel"] != "local:qwen2.5:14b" ||
		onDisk["models"]["synthesisModel"] != "claude-x" {
		t.Errorf("admin changes missing from file: %s", raw)
	}
	if _, ok := onDisk["models"]["available"]; ok {
		t.Errorf("unchanged model list persisted: %s", raw)
	}

	// Next boot: the environment changed DOCUSEAL_URL and MaxRounds.
	_, cfg := newStore(t, path, func(c *config.Config) {
		c.DocuSeal.URL = "https://sign.example.com"
		c.DyTopo.MaxRounds = 9
	})
	if cfg.DocuSeal.URL != "https://sign.example.com" {
		t.Errorf("env DOCUSEAL_URL shadowed by settings file: %q", cfg.DocuSeal.URL)
	}
	if cfg.DyTopo.MaxRounds != 5 {
		t.Errorf("admin-changed field lost to env: %d", cfg.DyTopo.MaxRounds)
	}
	if cfg.Models.SpineModel != "local:qwen2.5:14b" || cfg.Models.SynthesisModel != "claude-x" {
		t.Errorf("spine/synthesis model not persisted: %+v", cfg.Models)
	}
}

func TestRevertToEnvValueDropsOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	st, _ := newStore(t, path, nil)
	if _, err := st.Update(patchFrom(t, `{"dytopo":{"maxRounds":5}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update(patchFrom(t, `{"dytopo":{"maxRounds":3}}`)); err != nil {
		t.Fatal(err)
	}
	_, cfg := newStore(t, path, func(c *config.Config) { c.DyTopo.MaxRounds = 7 })
	if cfg.DyTopo.MaxRounds != 7 {
		t.Errorf("reverted field still pinned: %d", cfg.DyTopo.MaxRounds)
	}
}

// v1.0.0 wrote a full snapshot of every field, unversioned. Values equal to
// the built-in defaults can't be admin edits and must not mask the
// environment; real edits survive; the next save writes the versioned
// sparse form.
func TestLegacyFullSnapshotFileMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	d := config.Defaults()
	legacy, _ := json.Marshal(map[string]any{
		"presentation": map[string]any{"mode": "lawyer", "firmName": ""},
		"dytopo":       map[string]any{"maxRounds": 5, "maxAgentsPerRound": d.DyTopo.MaxAgentsPerRound, "similarityThreshold": d.DyTopo.SimilarityThreshold},
		"debate": map[string]any{"verificationPasses": d.Debate.VerificationPasses, "gateConfidenceThreshold": d.Debate.GateConfidenceThreshold,
			"adversarialEnabled": d.Debate.AdversarialEnabled, "citationRequired": d.Debate.CitationRequired},
		"docuseal":    map[string]any{"enabled": false, "url": "http://localhost:3000", "apiKey": ""},
		"clientVoice": map[string]any{"gateNotes": true, "matterNotifications": true},
		"models":      map[string]any{"figureModel": "", "available": d.Models.Available},
	})
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	// This boot's environment: FIRM_NAME, DOCUSEAL_API_KEY (which enables
	// DocuSeal), DOCUSEAL_URL, a lower gate threshold and notifications off.
	st, cfg := newStore(t, path, func(c *config.Config) {
		c.Presentation.FirmName = "Env LLP"
		c.DocuSeal.APIKey, c.DocuSeal.Enabled = "sekrit", true
		c.DocuSeal.URL = "http://docuseal:3000"
		c.Debate.GateConfidenceThreshold = 0.6
		c.ClientVoice.MatterNotifications = false
	})
	if cfg.DyTopo.MaxRounds != 5 {
		t.Fatalf("legacy admin edit lost: %d", cfg.DyTopo.MaxRounds)
	}
	if cfg.Presentation.FirmName != "Env LLP" || cfg.DocuSeal.APIKey != "sekrit" || !cfg.DocuSeal.Enabled ||
		cfg.DocuSeal.URL != "http://docuseal:3000" || cfg.Debate.GateConfidenceThreshold != 0.6 ||
		cfg.ClientVoice.MatterNotifications {
		t.Fatalf("legacy defaults masked the environment: %+v %+v %+v %+v", cfg.Presentation, cfg.DocuSeal, cfg.Debate, cfg.ClientVoice)
	}
	if _, err := st.Update(patchFrom(t, `{"drafting":{"rounds":3}}`)); err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"v": 2.0, "dytopo": map[string]any{"maxRounds": 5.0}, "drafting": map[string]any{"rounds": 3.0}}
	if !reflect.DeepEqual(onDisk, want) {
		t.Errorf("migrated file = %s", raw)
	}
}

// A current (versioned) file is applied as-is, even where a value equals the
// built-in default: there it is a deliberate admin choice.
func TestVersionedFileKeepsDefaultValuedEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"v":2,"presentation":{"firmName":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cfg := newStore(t, path, func(c *config.Config) { c.Presentation.FirmName = "Env LLP" })
	if cfg.Presentation.FirmName != "" {
		t.Fatalf("versioned admin edit dropped: %q", cfg.Presentation.FirmName)
	}
}

// After an admin points DocuSeal elsewhere, the operator's internal
// DOCUSEAL_URL can be restored — typed back in, or by clearing the field.
func TestOperatorDocuSealURLCanBeRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	st, cfg := newStore(t, path, func(c *config.Config) { c.DocuSeal.URL = "http://docuseal:3000" })
	for _, restore := range []string{`"http://docuseal:3000"`, `""`, `"  "`} {
		if _, err := st.Update(patchFrom(t, `{"docuseal":{"url":"https://sign.example.com"}}`)); err != nil {
			t.Fatal(err)
		}
		got, err := st.Update(patchFrom(t, `{"docuseal":{"url":`+restore+`}}`))
		if err != nil || got.DocuSeal.URL != "http://docuseal:3000" {
			t.Fatalf("restore %s: %v %q", restore, err, got.DocuSeal.URL)
		}
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), "docuseal") {
		t.Errorf("restored operator URL persisted as an override: %s", raw)
	}
	// Other private URLs are still rejected.
	if _, err := st.Update(patchFrom(t, `{"docuseal":{"url":"http://docuseal:3001"}}`)); err == nil || cfg.DocuSeal.URL != "http://docuseal:3000" {
		t.Fatalf("private URL accepted: %v %q", err, cfg.DocuSeal.URL)
	}
}
