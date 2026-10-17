// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/lpm"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

// registeredPrefixes builds the full production router (including the
// optional LPM group) and returns the first path segment of every route.
func registeredPrefixes(t *testing.T) map[string]bool {
	t.Helper()
	cfg := &config.Config{}
	cfg.Model.PrimaryURL = "http://127.0.0.1:1" // mountBots resolves a provider at registration
	s := &Server{cfg: cfg, provReg: providers.NewRegistry(cfg)}
	s.router = s.newRouter()
	s.AttachLPM(&lpm.Service{})

	out := map[string]bool{}
	for _, rt := range s.router.Routes() {
		seg, _, _ := strings.Cut(strings.TrimPrefix(rt.Path, "/"), "/")
		out["/"+seg] = true
	}
	return out
}

// The workbench dev server (ui/vite.config.ts) proxies a fixed list of path
// prefixes to the API; anything else is answered with index.html, so a
// forgotten prefix surfaces in the browser as "Unexpected token '<'" (that is
// how /reviews broke). Keep the list and the router in lock-step.
func TestViteProxyCoversAllRoutes(t *testing.T) {
	path := filepath.Join("..", "..", "..", "ui", "backend-routes.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("ui/backend-routes.json not present (%v) — biglaw-go checked out without the UI", err)
	}
	var listed []string
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	proxied := map[string]bool{}
	for _, p := range listed {
		proxied[p] = true
	}
	routes := registeredPrefixes(t)

	var missing, stale []string
	for p := range routes {
		if !proxied[p] {
			missing = append(missing, p)
		}
	}
	for p := range proxied {
		if !routes[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("API route prefixes not proxied by the UI dev server — add to ui/backend-routes.json: %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("ui/backend-routes.json proxies prefixes the API no longer serves: %v", stale)
	}
}
