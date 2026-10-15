// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	var onDisk map[string]map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
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

// Files from older versions held a full snapshot; they still load, and the
// next save keeps only the values that differ from the environment.
func TestLegacyFullSnapshotFileMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := `{"presentation":{"mode":"","firmName":""},"dytopo":{"maxRounds":5},
		"docuseal":{"enabled":false,"url":"http://localhost:3000","apiKey":""},"models":{"figureModel":"","available":["a","b"]}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	st, cfg := newStore(t, path, nil)
	if cfg.DyTopo.MaxRounds != 5 {
		t.Fatalf("legacy file not applied: %d", cfg.DyTopo.MaxRounds)
	}
	if _, err := st.Update(patchFrom(t, `{"presentation":{"firmName":"Acme LLP"}}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "docuseal") || !strings.Contains(string(raw), `"maxRounds": 5`) ||
		!strings.Contains(string(raw), "Acme LLP") {
		t.Errorf("migrated file = %s", raw)
	}
}
