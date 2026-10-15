// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/headnotes"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/precedent"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/redline"
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

func TestModelErrorResponseMapping(t *testing.T) {
	cases := []struct {
		err      error
		status   int
		typ      string
		fallback string
	}{
		{providers.ModelFailure("s", errors.New("HTTP 500")), http.StatusBadGateway, errorTypeModelUnavailable, ""},
		{providers.ModelFailure("s", errors.Join(providers.ErrNoProvider)), http.StatusServiceUnavailable, errorTypeModelNotConfigured, ""},
		{errors.Join(providers.ErrNoProvider, errors.New("no key")), http.StatusServiceUnavailable, errorTypeModelNotConfigured, ""},
		{errors.New("disk full"), http.StatusInternalServerError, "", "Tone analysis failed."},
	}
	for _, tc := range cases {
		status, body := modelErrorResponse(tc.err, tc.fallback)
		if status != tc.status {
			t.Errorf("%v: status %d, want %d", tc.err, status, tc.status)
		}
		if got, _ := body["errorType"].(string); got != tc.typ {
			t.Errorf("%v: errorType %q, want %q", tc.err, got, tc.typ)
		}
		if msg, _ := body["error"].(string); msg == "" {
			t.Errorf("%v: empty error message", tc.err)
		}
		if tc.fallback != "" && body["error"] != tc.fallback {
			t.Errorf("non-model error should use the fallback message, got %v", body["error"])
		}
	}
}

func partnerMW(c *gin.Context) {
	c.Set(ctxUserKey, &types.SessionUser{ProfileID: "p-partner", Role: types.RolePartner})
	c.Next()
}

// testEnginesProvider backs the package-level engine bundle seeded by
// installTestEngines. The bundle is built once per test binary (sync.Once),
// so the provider must be shared too (go test -count=N).
var testEnginesProvider = &failingProvider{}

// installTestEngines seeds the package-level engine bundle with engines whose
// provider fails as directed. Only this file's tests use the bundle.
func installTestEngines(t *testing.T) *failingProvider {
	t.Helper()
	fp := testEnginesProvider
	enginesOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bl-engines-*")
		if err != nil {
			t.Fatal(err)
		}
		st := playbook.New(filepath.Join(dir, "playbooks.json"))
		if err := st.Init(); err != nil {
			t.Fatal(err)
		}
		enginesShared = &enginesBundle{
			playbooks: st,
			builder:   playbook.NewBuilder(fp, "light"),
			redline:   redline.New(fp, "mid", "light"),
			headnotes: headnotes.New(fp, "mid", "light"),
			precedent: precedent.New(fp, "heavy", "light"),
		}
	})
	return fp
}

func TestEngineHandlersMapModelFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fp := installTestEngines(t)
	// Knowledge store with an unreachable local embedder: search falls back
	// to lexical without touching the network.
	kcfg := &config.Config{}
	kcfg.Local.LocalEmbeddings = true
	kcfg.Local.OllamaURL = "http://127.0.0.1:1"
	s := &Server{cfg: &config.Config{}, knowledge: knowledge.NewStore(embeddings.NewClient(kcfg))}
	r := gin.New()
	r.Use(partnerMW)
	r.POST("/redline", s.handleRedline)
	r.POST("/headnotes/generate", s.handleGenerateHeadnotes)
	r.POST("/precedents/generate", s.handleGeneratePrecedent)
	r.POST("/playbooks/build", s.handleBuildPlaybook)

	reqs := map[string]string{
		"/redline":             `{"documentText":"1. Governing law. Delaware."}`,
		"/headnotes/generate":  `{"opinionText":"The court held X."}`,
		"/precedents/generate": `{"documentType":"nda"}`,
		"/playbooks/build":     `{"practiceArea":"Corporate & M&A","name":"P","clauseTypes":["Governing law"]}`,
	}
	for _, mode := range []struct {
		err    error
		status int
		typ    string
	}{
		{errors.New("ollama: HTTP 500: overloaded"), http.StatusBadGateway, errorTypeModelUnavailable},
		{errors.Join(providers.ErrNoProvider, errors.New("no key")), http.StatusServiceUnavailable, errorTypeModelNotConfigured},
	} {
		fp.set(mode.err)
		for path, body := range reqs {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			if w.Code != mode.status {
				t.Fatalf("%s (%v): status %d, want %d; body %s", path, mode.err, w.Code, mode.status, w.Body.String())
			}
			var got map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("%s: non-JSON body %q", path, w.Body.String())
			}
			if got["errorType"] != mode.typ {
				t.Errorf("%s: errorType %v, want %s", path, got["errorType"], mode.typ)
			}
			for _, k := range []string{"issues", "headnotes", "clauses", "document", "entries", "executiveSummary"} {
				if _, ok := got[k]; ok {
					t.Errorf("%s: error body carries result field %q", path, k)
				}
			}
		}
	}
	if n := len(enginesShared.playbooks.List("", "", "")); n != 0 {
		t.Fatalf("failed builds persisted %d playbooks", n)
	}
}

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
