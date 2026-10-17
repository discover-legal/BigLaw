// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package tools

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
)

// The operator's DOCUSEAL_URL may point at an internal DocuSeal; a URL an
// admin swapped in at runtime must go through the dial-time SSRF guard, so it
// can't reach loopback/internal hosts (directly or via DNS rebinding).
func TestDocuSealClientTrustsOnlyOperatorURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.DocuSeal.APIKey = "k"
	cfg.DocuSeal.Enabled = true
	cfg.DocuSeal.URL = srv.URL
	cfg.DocuSeal.OperatorURL = srv.URL
	r := &Registry{tools: map[string]*ToolImpl{}, cfg: cfg}
	if _, err := r.docusealFetch(http.MethodGet, "/api/templates", "", nil); err != nil {
		t.Fatalf("operator-configured internal DocuSeal blocked: %v", err)
	}

	cfg.DocuSeal.OperatorURL = "http://localhost:3000" // URL now came from an admin
	if _, err := r.docusealFetch(http.MethodGet, "/api/templates", "", nil); err == nil {
		t.Fatal("admin-supplied URL reached a loopback host")
	}
}
