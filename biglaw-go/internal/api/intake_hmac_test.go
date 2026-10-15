// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Intake HMAC hardening: the portal channel accepts only correctly signed,
// fresh requests; the public-route set covers exactly the portal-facing
// routes and never the firm queue.

package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/crm"
	"github.com/discover-legal/biglaw-go/internal/intake"
	"github.com/discover-legal/biglaw-go/internal/store"
)

const testIntakeSecret = "test-secret-0123456789abcdef"

func newIntakeTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Intake.HMACSecret = testIntakeSecret
	cfg.Intake.MaxSkewSec = 300

	repo := store.NewMemoryRepo()
	cs := clients.NewClientStore()
	if err := cs.Init(filepath.Join(t.TempDir(), "clients.json")); err != nil {
		t.Fatalf("clients init: %v", err)
	}
	crmSvc := crm.New(repo, cs, nil)
	intakeSvc := intake.New(repo, crmSvc, cs, nil) // nil knowledge: auth is under test

	s := &Server{cfg: cfg, clients: cs, router: gin.New()}
	s.AttachIntake(intakeSvc, crmSvc)
	return s
}

func signIntake(method, pathWithQuery, ts string, body []byte) string {
	h := sha256.Sum256(body)
	canonical := method + "\n" + pathWithQuery + "\n" + ts + "\n" + hex.EncodeToString(h[:])
	mac := hmac.New(sha256.New, []byte(testIntakeSecret))
	mac.Write([]byte(canonical))
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func doSigned(s *Server, method, path string, body []byte, ts, sig string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ts != "" {
		req.Header.Set("X-Intake-Timestamp", ts)
	}
	if sig != "" {
		req.Header.Set("X-Intake-Signature", sig)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func validBody() []byte {
	return []byte(`{"externalId":"am-doc-1","client":{"externalId":"auth0|dana","email":"d@example.com","name":"Dana Client"},"title":"Affidavit","content":"I declare..."}`)
}

func TestIntakeAcceptsCorrectSignature(t *testing.T) {
	s := newIntakeTestServer(t)
	body := validBody()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	w := doSigned(s, http.MethodPost, "/intake/submissions", body, ts,
		signIntake("POST", "/intake/submissions", ts, body))
	if w.Code != http.StatusCreated {
		t.Fatalf("valid signature rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestIntakeRejectsBadCredentials(t *testing.T) {
	s := newIntakeTestServer(t)
	body := validBody()
	now := strconv.FormatInt(time.Now().Unix(), 10)
	stale := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)

	cases := []struct {
		name    string
		ts, sig string
		want    int
	}{
		{"missing headers", "", "", http.StatusUnauthorized},
		{"garbage signature", now, "v1=deadbeef", http.StatusUnauthorized},
		{"stale timestamp", stale, signIntake("POST", "/intake/submissions", stale, body), http.StatusUnauthorized},
		{"timestamp not signed-for", now, signIntake("POST", "/intake/submissions", stale, body), http.StatusUnauthorized},
		{"body tampered", now, signIntake("POST", "/intake/submissions", now, []byte(`{}`)), http.StatusUnauthorized},
		{"path not signed-for", now, signIntake("POST", "/intake/other", now, body), http.StatusUnauthorized},
	}
	for _, tc := range cases {
		if w := doSigned(s, http.MethodPost, "/intake/submissions", body, tc.ts, tc.sig); w.Code != tc.want {
			t.Fatalf("%s: got %d, want %d (%s)", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestIntakeGETSignsQueryAndEmptyBody(t *testing.T) {
	s := newIntakeTestServer(t)

	// Land one submission first.
	body := validBody()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if w := doSigned(s, http.MethodPost, "/intake/submissions", body, ts,
		signIntake("POST", "/intake/submissions", ts, body)); w.Code != http.StatusCreated {
		t.Fatalf("seed submission failed: %d", w.Code)
	}

	path := "/intake/clients/auth0%7Cdana/submissions"
	ts2 := strconv.FormatInt(time.Now().Unix(), 10)
	w := doSigned(s, http.MethodGet, path, nil, ts2, signIntake("GET", path, ts2, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("signed GET rejected: %d %s", w.Code, w.Body.String())
	}
	// Signature over a different path must fail.
	ts3 := strconv.FormatInt(time.Now().Unix(), 10)
	w = doSigned(s, http.MethodGet, path, nil, ts3, signIntake("GET", "/intake/clients/other/submissions", ts3, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-path signature accepted: %d", w.Code)
	}
}

func TestIntakeDisabledWithoutSecret(t *testing.T) {
	s := newIntakeTestServer(t)
	s.cfg.Intake.HMACSecret = ""
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	w := doSigned(s, http.MethodPost, "/intake/submissions", validBody(), ts, "v1=whatever")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured intake should 503, got %d", w.Code)
	}
}

func TestPortalRouteSetIsExact(t *testing.T) {
	public := [][2]string{
		{"POST", "/intake/submissions"},
		{"GET", "/intake/submissions/abc-123"},
		{"GET", "/intake/clients/auth0|x/profile"},
		{"GET", "/intake/clients/auth0|x/submissions"},
		{"POST", "/intake/clients/auth0|x/proposals"},
		{"POST", "/intake/proposals/f-1/decision"},
	}
	private := [][2]string{
		{"GET", "/intake/queue"},
		{"POST", "/intake/submissions/abc/claim"},
		{"POST", "/intake/submissions/abc/task"},
		{"PATCH", "/intake/submissions/abc"},
		{"GET", "/intake/submissions/"},
		{"DELETE", "/intake/submissions/abc"},
		{"GET", "/tasks"},
	}
	for _, p := range public {
		if !isPortalIntakeRoute(p[0], p[1]) {
			t.Errorf("expected public: %s %s", p[0], p[1])
		}
		if !isPublicRoute(p[0], p[1]) {
			t.Errorf("isPublicRoute must cover: %s %s", p[0], p[1])
		}
	}
	for _, p := range private {
		if isPortalIntakeRoute(p[0], p[1]) {
			t.Errorf("must NOT be public: %s %s", p[0], p[1])
		}
	}
}

func TestFirmQueueRejectsHMACPrincipal(t *testing.T) {
	// The intake HMAC must grant nothing outside the portal set: /intake/queue
	// goes through normal auth, and an anonymous (no session/bearer) request
	// carries no user, so the handler refuses it.
	s := newIntakeTestServer(t)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	w := doSigned(s, http.MethodGet, "/intake/queue", nil, ts, signIntake("GET", "/intake/queue", ts, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("HMAC-signed request reached the firm queue: %d %s", w.Code, w.Body.String())
	}
}

func TestClientProfileRoundTripThroughPortal(t *testing.T) {
	s := newIntakeTestServer(t)

	// Submission seeds a profile + a lawyer-gated proposal.
	body := []byte(`{"externalId":"am-doc-9","client":{"externalId":"auth0|dana","email":"d@example.com","name":"Dana Client"},"title":"Affidavit","content":"I declare...","facts":[{"category":"goal","predicate":"custody","value":"Primary custody"}]}`)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if w := doSigned(s, http.MethodPost, "/intake/submissions", body, ts,
		signIntake("POST", "/intake/submissions", ts, body)); w.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}

	path := "/intake/clients/auth0%7Cdana/profile"
	ts2 := strconv.FormatInt(time.Now().Unix(), 10)
	w := doSigned(s, http.MethodGet, path, nil, ts2, signIntake("GET", path, ts2, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("profile: %d %s", w.Code, w.Body.String())
	}
	out := w.Body.String()
	for _, want := range []string{`"clientNumber":"AM-1"`, `"pendingLawyerApproval"`, `"Primary custody"`} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Fatalf("profile response missing %s: %s", want, out)
		}
	}

	// Unknown client → 404.
	other := "/intake/clients/auth0%7Cnobody/profile"
	ts3 := strconv.FormatInt(time.Now().Unix(), 10)
	if w := doSigned(s, http.MethodGet, other, nil, ts3, signIntake("GET", other, ts3, nil)); w.Code != http.StatusNotFound {
		t.Fatalf("unknown profile: %d", w.Code)
	}
}
