// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// bots.go has zero test coverage today (no bots_test.go existed before this
// file), despite carrying the SSRF guard on caller-supplied Teams webhook
// overrides (botsAssertPublicHTTPSURL, bots.go:213-215) and the target-
// resolution precedence used by handleTeamsNotify / handleSlackNotify
// (explicit override > matter link > configured default, bots.go:124-206).
//
// mountBots itself is NOT exercised here: it builds a botFacade that touches
// s.orch, s.provReg, s.knowledge, s.clients, s.time, s.lpm, s.budget, and
// s.dockets — full orchestrator wiring, well beyond a handler unit test. The
// partner-only gate on /bots/teams/notify and /bots/slack/notify is applied
// by the route *group* in mountBots (bots.go:66-71), not inside the handler
// functions below, so it is likewise untested here; it needs either a
// wired-up mountBots or a router that reproduces the same gin.Group + requirePartner
// middleware. Both gaps are worth closing once a lightweight orchestrator
// test harness exists (see billing_test.go / matters_test.go for the same
// structural note elsewhere in this package).
package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/config"
)

// newBotsNotifyRouter wires handleTeamsNotify/handleSlackNotify directly
// (bypassing the partner-gated /bots group in mountBots, and authMiddleware
// entirely) since the handlers themselves have no auth logic — only their
// registration site does.
func newBotsNotifyRouter(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: cfg}
	r := gin.New()
	r.POST("/bots/teams/notify", s.handleTeamsNotify)
	r.POST("/bots/slack/notify", s.handleSlackNotify)
	return r
}

func TestBotsAssertPublicHTTPSURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"public https URL is accepted", "https://outlook.office.com/webhook/abc123", false},
		{"plain http is rejected (https required)", "http://outlook.office.com/webhook/abc123", true},
		{"loopback host is rejected", "https://127.0.0.1/webhook", true},
		{"localhost is rejected", "https://localhost/webhook", true},
		{"private RFC1918 host is rejected", "https://192.168.1.5/webhook", true},
		{"link-local metadata host is rejected", "https://169.254.169.254/latest/meta-data", true},
		{"malformed URL is rejected", "not a url", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := botsAssertPublicHTTPSURL(tt.url, "webhookUrl")
			if (err != nil) != tt.wantErr {
				t.Errorf("botsAssertPublicHTTPSURL(%q) err = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

// TestHandleTeamsNotify_NoTargetConfigured covers the 400 branch when no
// webhookUrl override, no matter link, and no TEAMS_INCOMING_WEBHOOK_URL
// default are present (bots.go:143-149) — the most common misconfiguration.
func TestHandleTeamsNotify_NoTargetConfigured(t *testing.T) {
	r := newBotsNotifyRouter(&config.Config{})
	body := `{"matterNumber":"M-BOTS-1","title":"t","text":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/bots/teams/notify", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
}

// TestHandleTeamsNotify_InvalidJSONBody covers the malformed-body 400 branch
// (bots.go:132-135).
func TestHandleTeamsNotify_InvalidJSONBody(t *testing.T) {
	r := newBotsNotifyRouter(&config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/bots/teams/notify", bytes.NewBufferString(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
}

// TestHandleTeamsNotify_OverrideURLRejectedBySSRFGuard covers the highest-
// value untested branch in bots.go: a caller-supplied webhookUrl pointing at
// a private/loopback host must be rejected with 400 BEFORE any outbound HTTP
// call is attempted (bots.go:153-160). This is the one place in the file
// where a request body directly controls an outbound URL.
func TestHandleTeamsNotify_OverrideURLRejectedBySSRFGuard(t *testing.T) {
	r := newBotsNotifyRouter(&config.Config{})
	body := `{"webhookUrl":"https://127.0.0.1:9999/hook","text":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/bots/teams/notify", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
}

// TestHandleTeamsNotify_MatterLinkResolution documents (does not yet assert
// past the target-resolution step) that a matter-linked webhook takes
// precedence over the configured default. Actually driving this to 200/502
// requires either a live HTTPS endpoint or refactoring integrations.PostToTeamsWebhook
// behind an injectable seam — currently a package function, not a Server
// field — so this is left as a documented skip rather than a flaky
// network-dependent assertion.
func TestHandleTeamsNotify_MatterLinkResolution(t *testing.T) {
	t.Skip("TODO: needs integrations.PostToTeamsWebhook to be injectable (currently a package func) to assert the matter-link-wins-over-default path without a real network call")

	// Sketch:
	// bots.SetTeamsMatterLink(bots.TeamsMatterLink{MatterNumber: "M-BOTS-2", WebhookURL: "https://example.test/hook"})
	// defer bots.DeleteTeamsMatterLink("M-BOTS-2")
	// ... POST {"matterNumber":"M-BOTS-2","text":"hi"} and assert the linked URL wins
	// over cfg.Bots.Teams.IncomingWebhookURL when both are present.
}

// TestHandleSlackNotify_NoTargetConfigured mirrors the Teams case: no
// channelId, no matter link, no SLACK_DEFAULT_CHANNEL default -> 400
// (bots.go:192-198).
func TestHandleSlackNotify_NoTargetConfigured(t *testing.T) {
	r := newBotsNotifyRouter(&config.Config{})
	body := `{"matterNumber":"M-BOTS-3","text":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/bots/slack/notify", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
}
