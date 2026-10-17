// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Intake HMAC hardening: the portal channel accepts only correctly signed,
// fresh requests; the public-route set covers exactly the portal-facing
// routes and never the firm queue. Runs against the module mounted on a bare
// gin router with a fake host — no dependency on the core API package.

package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/crm"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const testIntakeSecret = "test-secret-0123456789abcdef"

// fakeHost authenticates a request as the user in the X-Test-User header
// ("partner" | "lawyer"); no header = anonymous.
type fakeHost struct{}

func (fakeHost) User(c *gin.Context) *types.SessionUser {
	switch c.GetHeader("X-Test-User") {
	case "partner":
		u := auth.LocalPartner
		return &u
	case "lawyer":
		return &types.SessionUser{ProfileID: "lawyer-1", Name: "Lee Lawyer", Role: types.LawyerRole("associate")}
	}
	return nil
}

func (h fakeHost) Identity(c *gin.Context) context.Context {
	u := h.User(c)
	if u == nil {
		return c.Request.Context()
	}
	return store.WithIdentity(c.Request.Context(), u.ProfileID, auth.IsPartner(u))
}

func (fakeHost) ProfileName(string) string { return "" }

type testServer struct {
	router *gin.Engine
	mod    *HTTP
	cs     *clients.ClientStore
}

func (s *testServer) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.router.ServeHTTP(w, r) }

func newIntakeTestServer(t *testing.T) *testServer {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := store.NewMemoryRepo()
	cs := clients.NewClientStore()
	if err := cs.Init(filepath.Join(t.TempDir(), "clients.json")); err != nil {
		t.Fatalf("clients init: %v", err)
	}
	crmSvc := crm.New(repo, cs, nil)
	svc := New(repo, crmSvc, cs, nil) // nil knowledge: auth is under test
	mod := NewHTTP(svc, crmSvc, cs, nil, HTTPConfig{HMACSecret: testIntakeSecret, MaxSkewSec: 300})
	r := gin.New()
	mod.Mount(r, fakeHost{})
	return &testServer{router: r, mod: mod, cs: cs}
}

func signIntake(method, pathWithQuery, ts string, body []byte) string {
	return Sign(testIntakeSecret, method, pathWithQuery, ts, body)
}

func doSigned(s *testServer, method, path string, body []byte, ts, sig string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ts != "" {
		req.Header.Set("X-Intake-Timestamp", ts)
	}
	if sig != "" {
		req.Header.Set("X-Intake-Signature", sig)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
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
	s.mod.secrets = nil
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	w := doSigned(s, http.MethodPost, "/intake/submissions", validBody(), ts, "v1=whatever")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured intake should 503, got %d", w.Code)
	}
}

func TestPortalRouteSetIsExact(t *testing.T) {
	// The public set is matched on registered route patterns (c.FullPath),
	// so check it against what Mount actually registers: exactly these
	// routes are portal (HMAC) routes, every other intake route needs a
	// firm credential, and no portal entry is stale.
	want := map[[2]string]bool{
		{"POST", "/intake/submissions"}:                    true,
		{"GET", "/intake/submissions/:id"}:                 true,
		{"GET", "/intake/clients/:externalId/profile"}:     true,
		{"GET", "/intake/clients/:externalId/submissions"}: true,
		{"POST", "/intake/clients/:externalId/proposals"}:  true,
		{"POST", "/intake/proposals/:id/decision"}:         true,
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	mod := &HTTP{}
	mod.Mount(r, fakeHost{})
	registered := map[[2]string]bool{}
	for _, rt := range r.Routes() {
		k := [2]string{rt.Method, rt.Path}
		registered[k] = true
		if got := mod.PublicRoute(rt.Method, rt.Path); got != want[k] {
			t.Errorf("%s %s: public=%v, want %v", rt.Method, rt.Path, got, want[k])
		}
	}
	for k := range want {
		if !registered[k] {
			t.Errorf("portal route %s %s is not registered by Mount", k[0], k[1])
		}
	}
	// Request paths are not route patterns: a concrete path never matches,
	// and neither does an unmatched request (empty pattern).
	for _, p := range [][2]string{
		{"GET", "/intake/submissions/abc-123"},
		{"POST", "/intake/proposals/f-1/decision"},
		{"GET", ""},
		{"GET", "/tasks"},
	} {
		if IsPortalRoute(p[0], p[1]) {
			t.Errorf("must NOT be public: %q %q", p[0], p[1])
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

func signedPost(t *testing.T, s *testServer, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	return doSigned(s, http.MethodPost, path, body, ts, signIntake("POST", path, ts, body))
}

// The portal never learns who a conflict is with; the firm queue does.
func TestConflictDetailIsFirmOnly(t *testing.T) {
	s := newIntakeTestServer(t)
	if _, err := s.cs.Create("Robert Smith", "C-7", nil, ""); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"externalId":"d-1","client":{"externalId":"auth0|dana","email":"d@example.com","name":"Dana Client"},"title":"Affidavit","content":"I declare...","adverseParties":[{"name":"Robert Smith","role":"spouse"}],"someFutureField":{"x":1}}`)
	w := signedPost(t, s, "/intake/submissions", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	var portal struct {
		Submission map[string]json.RawMessage `json:"submission"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &portal)
	if string(portal.Submission["status"]) != `"conflict_hold"` {
		t.Fatalf("status = %s", portal.Submission["status"])
	}
	if got := string(portal.Submission["conflict"]); got != `{"hasConflict":true}` {
		t.Fatalf("portal conflict detail leaked: %s", got)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("Robert Smith")) {
		t.Fatalf("portal response names the conflicting client: %s", w.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/intake/queue", nil)
	req.Header.Set("X-Test-User", "partner")
	rw := httptest.NewRecorder()
	s.ServeHTTP(rw, req)
	if rw.Code != http.StatusOK || !bytes.Contains(rw.Body.Bytes(), []byte(`"conflictingClientName":"Robert Smith"`)) {
		t.Fatalf("firm queue lacks conflict detail: %d %s", rw.Code, rw.Body.String())
	}
}

func TestConflictRouteIsPartnerOnly(t *testing.T) {
	s := newIntakeTestServer(t)
	_, _ = s.cs.Create("Robert Smith", "C-7", nil, "")
	body := []byte(`{"externalId":"d-1","client":{"externalId":"auth0|dana","name":"Dana Client"},"content":"x","adverseParties":[{"name":"Robert Smith"}]}`)
	w := signedPost(t, s, "/intake/submissions", body)
	var out struct {
		Submission struct {
			ID string `json:"id"`
		} `json:"submission"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)

	do := func(user, decision, reason string) int {
		b, _ := json.Marshal(map[string]string{"decision": decision, "conflictReason": reason})
		req := httptest.NewRequest(http.MethodPost, "/intake/submissions/"+out.Submission.ID+"/conflict", bytes.NewReader(b))
		if user != "" {
			req.Header.Set("X-Test-User", user)
		}
		rw := httptest.NewRecorder()
		s.ServeHTTP(rw, req)
		return rw.Code
	}
	if code := do("", "acknowledge", "waiver"); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code := do("lawyer", "acknowledge", "waiver"); code != http.StatusForbidden {
		t.Fatalf("non-partner: %d", code)
	}
	if code := do("partner", "acknowledge", ""); code != http.StatusBadRequest {
		t.Fatalf("missing reason: %d", code)
	}
	if code := do("partner", "acknowledge", "informed-consent waiver"); code != http.StatusOK {
		t.Fatalf("partner acknowledge: %d", code)
	}
	if code := do("partner", "acknowledge", "again"); code != http.StatusConflict {
		t.Fatalf("second resolve: %d", code)
	}
}

// A captured signed write cannot be replayed inside the skew window; the
// idempotent submission upsert (double-click) and reads still can.
func TestReplayedWritesRejected(t *testing.T) {
	s := newIntakeTestServer(t)
	if w := signedPost(t, s, "/intake/submissions", validBody()); w.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	body := validBody()
	sig := signIntake("POST", "/intake/submissions", ts, body)
	for i := 0; i < 2; i++ {
		if w := doSigned(s, http.MethodPost, "/intake/submissions", body, ts, sig); w.Code != http.StatusCreated {
			t.Fatalf("idempotent re-send %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	path := "/intake/clients/auth0%7Cdana/proposals"
	pb := []byte(`{"facts":[{"category":"contact","predicate":"phone","value":"555"}]}`)
	sig = signIntake("POST", path, ts, pb)
	if w := doSigned(s, http.MethodPost, path, pb, ts, sig); w.Code != http.StatusCreated {
		t.Fatalf("proposal: %d %s", w.Code, w.Body.String())
	}
	if w := doSigned(s, http.MethodPost, path, pb, ts, sig); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed proposal accepted: %d %s", w.Code, w.Body.String())
	}
}

// INTAKE_HMAC_SECRET may list several secrets (rotation).
func TestSecretRotationList(t *testing.T) {
	s := newIntakeTestServer(t)
	s.mod.secrets = ParseSecrets(" new-secret-0123456789abcdef0123456789 , " + testIntakeSecret)
	for _, secret := range s.mod.secrets {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		body := validBody()
		if w := doSigned(s, http.MethodPost, "/intake/submissions", body, ts,
			Sign(secret, "POST", "/intake/submissions", ts, body)); w.Code != http.StatusCreated {
			t.Fatalf("secret %q rejected: %d", secret[:6], w.Code)
		}
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	if w := doSigned(s, http.MethodPost, "/intake/submissions", validBody(), ts,
		Sign("retired-secret-0123456789abcdef", "POST", "/intake/submissions", ts, validBody())); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown secret accepted: %d", w.Code)
	}
}

// A lawyer's queue hides held submissions; partners see them with detail.
func TestQueueHidesHeldFromLawyers(t *testing.T) {
	s := newIntakeTestServer(t)
	_, _ = s.cs.Create("Robert Smith", "C-7", nil, "")
	held := []byte(`{"externalId":"d-1","client":{"externalId":"auth0|pat","name":"Pat Prospect"},"content":"x","adverseParties":[{"name":"Robert Smith"}]}`)
	if w := signedPost(t, s, "/intake/submissions", held); w.Code != http.StatusCreated {
		t.Fatalf("submit: %d", w.Code)
	}
	get := func(user string) string {
		req := httptest.NewRequest(http.MethodGet, "/intake/queue", nil)
		req.Header.Set("X-Test-User", user)
		rw := httptest.NewRecorder()
		s.ServeHTTP(rw, req)
		return rw.Body.String()
	}
	if out := get("lawyer"); strings.Contains(out, "Pat") || strings.Contains(out, "Robert Smith") {
		t.Fatalf("lawyer sees a held submission: %s", out)
	}
	if out := get("partner"); !strings.Contains(out, `"conflictingClientName":"Robert Smith"`) {
		t.Fatalf("partner lacks detail: %s", out)
	}
}

type fakeTasks struct{ n int }

func (f *fakeTasks) SubmitTask(p orchestrator.SubmitParams) (*types.Task, error) {
	f.n++
	return &types.Task{ID: fmt.Sprintf("task-%d", f.n), Description: p.Description}, nil
}

// /task respects assignment and the hold: no task is ever created for a
// submission the caller may not act on.
func TestTaskRouteRespectsAssignmentAndHold(t *testing.T) {
	svc, crmSvc, cs, _ := newTestIntake(t)
	_, _ = cs.Create("Rex Existing", "C-1", nil, "")
	tasks := &fakeTasks{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewHTTP(svc, crmSvc, cs, tasks, HTTPConfig{HMACSecret: testIntakeSecret}).Mount(r, fakeHost{})
	clean, _ := svc.Submit(sysCtx(), personReq("auth0|dana", "Dana Client", "d-1"))
	held, _ := svc.Submit(sysCtx(), personReq("auth0|pat", "Pat Prospect", "p-1", "Rex Existing"))
	post := func(user, id string) int {
		req := httptest.NewRequest(http.MethodPost, "/intake/submissions/"+id+"/task", nil)
		req.Header.Set("X-Test-User", user)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := post("lawyer", clean.ID); code != http.StatusForbidden {
		t.Fatalf("unassigned lawyer started a task: %d", code)
	}
	if code := post("lawyer", held.ID); code != http.StatusNotFound {
		t.Fatalf("lawyer reached a held submission: %d", code)
	}
	if code := post("partner", held.ID); code != http.StatusConflict {
		t.Fatalf("task on a held submission: %d", code)
	}
	if tasks.n != 0 {
		t.Fatalf("%d tasks created for refused requests", tasks.n)
	}
	if code := post("partner", clean.ID); code != http.StatusAccepted || tasks.n != 1 {
		t.Fatalf("partner task: %d (%d tasks)", code, tasks.n)
	}
}
