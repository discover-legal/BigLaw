// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/services"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// failingProvider fails every Chat call with whatever err currently holds.
type failingProvider struct {
	mu  sync.Mutex
	err error
}

func (f *failingProvider) set(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }
func (f *failingProvider) Chat(providers.ChatParams) (*providers.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil, f.err
}

func TestClassifyModelError(t *testing.T) {
	cases := []struct {
		err    error
		status int
		typ    string
		retry  int
	}{
		{providers.ModelFailure("s", errors.New("HTTP 500")), http.StatusBadGateway, errorTypeModelUnavailable, 0},
		{providers.ModelFailure("s", errors.Join(providers.ErrNoProvider)), http.StatusServiceUnavailable, errorTypeModelNotConfigured, 0},
		{providers.ModelFailure("s", &providers.RateLimitError{RetryAfter: 1500 * time.Millisecond, Err: errors.New("429")}), http.StatusServiceUnavailable, errorTypeModelRateLimited, 2},
		{providers.ModelFailure("s", fmt.Errorf("x: %w", providers.ErrModelTimeout)), http.StatusGatewayTimeout, errorTypeModelTimeout, 0},
		{providers.NoContent("s", "no clauses"), http.StatusUnprocessableEntity, errorTypeNoContent, 0},
		{providers.ModelFailure("s", context.Canceled), statusClientClosedRequest, "", 0},
		{errors.New("disk full"), http.StatusInternalServerError, "", 0},
	}
	for _, tc := range cases {
		info := classifyModelError(tc.err, "")
		if info.status != tc.status || info.errorType != tc.typ || info.retryAfter != tc.retry || info.message == "" {
			t.Errorf("%v: got %+v, want status %d type %q retry %d", tc.err, info, tc.status, tc.typ, tc.retry)
		}
		if strings.Contains(info.message, "disk full") || strings.Contains(info.message, "HTTP 500") {
			t.Errorf("%v: message leaks internals: %q", tc.err, info.message)
		}
	}
}

func partnerMW(c *gin.Context) {
	c.Set(ctxUserKey, &types.SessionUser{ProfileID: "p-partner", Role: types.RolePartner})
	c.Next()
}

// newEngineRouter wires the engine routes onto a Server whose per-request
// model resolution is scripted: resolveErr != nil simulates "no provider";
// otherwise every call goes to fp.
func newEngineRouter(t *testing.T, fp providers.Provider, resolveErr error) (*gin.Engine, *Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	orig := engineModelsFor
	t.Cleanup(func() { engineModelsFor = orig })
	engineModelsFor = func(*Server) (engineModels, error) {
		if resolveErr != nil {
			return engineModels{}, resolveErr
		}
		return engineModels{provider: fp, mid: "mid", light: "light", heavy: "heavy"}, nil
	}
	playbookStoreMu.Lock()
	playbookStoreShared = nil // fresh store per test
	playbookStoreMu.Unlock()
	t.Cleanup(func() { playbookStoreMu.Lock(); playbookStoreShared = nil; playbookStoreMu.Unlock() })

	// Knowledge store with an unreachable local embedder: search falls back
	// to lexical without touching the network.
	kcfg := &config.Config{}
	kcfg.Local.LocalEmbeddings = true
	kcfg.Local.OllamaURL = "http://127.0.0.1:1"
	cfg := &config.Config{}
	cfg.Persistence.PlaybooksFile = filepath.Join(t.TempDir(), "playbooks.json")
	s := &Server{cfg: cfg, knowledge: knowledge.NewStore(embeddings.NewClient(kcfg))}
	r := gin.New()
	r.Use(partnerMW)
	r.GET("/playbooks", s.handleListPlaybooks)
	r.POST("/redline", s.handleRedline)
	r.POST("/headnotes/generate", s.handleGenerateHeadnotes)
	r.POST("/precedents/generate", s.handleGeneratePrecedent)
	r.POST("/playbooks/build", s.handleBuildPlaybook)
	return r, s
}

var engineReqs = map[string]string{
	"/redline":             `{"documentText":"1. Governing law. Delaware."}`,
	"/headnotes/generate":  `{"opinionText":"The court held X."}`,
	"/precedents/generate": `{"documentType":"nda"}`,
	"/playbooks/build":     `{"practiceArea":"Corporate & M&A","name":"P","clauseTypes":["Governing law"]}`,
}

func postJSON(r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestEngineHandlersMapModelFailures(t *testing.T) {
	fp := &failingProvider{}
	for _, mode := range []struct {
		name       string
		resolveErr error
		callErr    error
		status     int
		typ        string
		retryAfter string
	}{
		{"upstream 500", nil, errors.New("ollama: HTTP 500: overloaded at http://10.1.2.3:8000"), http.StatusBadGateway, errorTypeModelUnavailable, ""},
		{"no provider at resolve", errors.Join(providers.ErrNoProvider, errors.New("unset")), nil, http.StatusServiceUnavailable, errorTypeModelNotConfigured, ""},
		{"no key (401)", nil, errors.Join(providers.ErrNoProvider, errors.New("no API key")), http.StatusServiceUnavailable, errorTypeModelNotConfigured, ""},
		{"rate limited", nil, &providers.RateLimitError{RetryAfter: 7 * time.Second, Err: errors.New("429")}, http.StatusServiceUnavailable, errorTypeModelRateLimited, "7"},
		{"timeout", nil, fmt.Errorf("do request: %w", providers.ErrModelTimeout), http.StatusGatewayTimeout, errorTypeModelTimeout, ""},
	} {
		t.Run(mode.name, func(t *testing.T) {
			fp.set(mode.callErr)
			r, _ := newEngineRouter(t, fp, mode.resolveErr)
			for path, body := range engineReqs {
				w := postJSON(r, path, body)
				if w.Code != mode.status {
					t.Fatalf("%s: status %d, want %d; body %s", path, w.Code, mode.status, w.Body.String())
				}
				var got map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatalf("%s: non-JSON body %q", path, w.Body.String())
				}
				if got["errorType"] != mode.typ {
					t.Errorf("%s: errorType %v, want %s", path, got["errorType"], mode.typ)
				}
				if id, _ := got["requestId"].(string); id == "" || w.Header().Get("X-Request-Id") != id {
					t.Errorf("%s: missing/mismatched requestId: body %v header %q", path, got["requestId"], w.Header().Get("X-Request-Id"))
				}
				if w.Header().Get("Retry-After") != mode.retryAfter {
					t.Errorf("%s: Retry-After %q, want %q", path, w.Header().Get("Retry-After"), mode.retryAfter)
				}
				if _, ok := got["detail"]; ok || strings.Contains(w.Body.String(), "10.1.2.3") || strings.Contains(w.Body.String(), "ollama") {
					t.Errorf("%s: body leaks internals: %s", path, w.Body.String())
				}
				for _, k := range []string{"issues", "headnotes", "clauses", "document", "entries", "executiveSummary"} {
					if _, ok := got[k]; ok {
						t.Errorf("%s: error body carries result field %q", path, k)
					}
				}
			}
		})
	}
}

// Playbook reads never need a model provider (they used to 500/503 with the
// engine bundle), and a failed build persists nothing.
func TestPlaybooksWorkWithoutProvider(t *testing.T) {
	r, s := newEngineRouter(t, nil, errors.Join(providers.ErrNoProvider, errors.New("unset")))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/playbooks", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("GET /playbooks without a provider: %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(r, "/playbooks/build", engineReqs["/playbooks/build"]); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("build without provider: %d", w.Code)
	}
	st, _ := s.playbookStore()
	if n := len(st.List("", "", "")); n != 0 {
		t.Fatalf("failed build persisted %d playbooks", n)
	}
}

// A provider configured after startup is picked up on the next request (no
// process-lifetime caching of the "no provider" error).
func TestEngineProviderResolvedPerRequest(t *testing.T) {
	fp := &failingProvider{}
	r, _ := newEngineRouter(t, fp, errors.Join(providers.ErrNoProvider, errors.New("unset")))
	if w := postJSON(r, "/redline", engineReqs["/redline"]); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("first: %d", w.Code)
	}
	engineModelsFor = func(*Server) (engineModels, error) {
		return engineModels{provider: fp, mid: "m", light: "l", heavy: "h"}, nil
	}
	fp.set(errors.New("HTTP 500"))
	if w := postJSON(r, "/redline", engineReqs["/redline"]); w.Code != http.StatusBadGateway {
		t.Fatalf("after configuring: %d (still cached?)", w.Code)
	}
}

// A valid reply with zero clauses answers 422 no_content.
func TestRedlineNoContentIs422(t *testing.T) {
	ok := providerFunc(func(providers.ChatParams) (*providers.ChatResponse, error) {
		return &providers.ChatResponse{Content: []providers.ContentBlock{{Type: providers.BlockText, Text: `{"clauses":[]}`}}}, nil
	})
	r, _ := newEngineRouter(t, ok, nil)
	w := postJSON(r, "/redline", engineReqs["/redline"])
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), errorTypeNoContent) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

type providerFunc func(providers.ChatParams) (*providers.ChatResponse, error)

func (f providerFunc) Chat(p providers.ChatParams) (*providers.ChatResponse, error) { return f(p) }

func TestToneImportModelFailureStoresNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	// Not t.TempDir(): the profile store persists asynchronously, which races
	// TempDir's strict cleanup. Best-effort removal instead.
	dir, err := os.MkdirTemp("", "bl-tone-import-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg.Persistence.ProfilesFile = filepath.Join(dir, "profiles.json")
	profiles := auth.NewProfileStore(cfg)
	if err := profiles.Init(); err != nil {
		t.Fatal(err)
	}
	prof, err := profiles.Create(auth.CreateProfileInput{Name: "Jane Doe", Email: "jane@example.com", Role: "lawyer"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, profiles: profiles}

	fp := &failingProvider{}
	orig := contentToneAnalyzerFor
	t.Cleanup(func() { contentToneAnalyzerFor = orig })

	r := gin.New()
	r.Use(partnerMW)
	r.POST("/profiles/:id/tone/import", s.handleToneImport)

	post := func() *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "samples.txt")
		_, _ = fw.Write([]byte(strings.Repeat("I write tersely and to the point, with short declarative sentences and few hedges. ", 3)))
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/profiles/"+prof.ID+"/tone/import", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	cases := []struct {
		name    string
		factory func(*Server) (*services.ToneAnalyzer, error)
		status  int
	}{
		{"upstream call fails", func(*Server) (*services.ToneAnalyzer, error) {
			fp.set(errors.New("ollama: HTTP 500"))
			return services.NewToneAnalyzer(fp, "light"), nil
		}, http.StatusBadGateway},
		{"no provider configured", func(*Server) (*services.ToneAnalyzer, error) {
			return nil, errors.Join(providers.ErrNoProvider, errors.New("unset"))
		}, http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contentToneAnalyzerFor = tc.factory
			w := post()
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d; body %s", w.Code, tc.status, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "toneProfile") {
				t.Fatalf("error body carries a toneProfile: %s", w.Body.String())
			}
			if got := profiles.Get(prof.ID); got == nil || got.ToneProfile != nil {
				t.Fatalf("a tone profile was stored after a model failure: %+v", got.ToneProfile)
			}
		})
	}
}
