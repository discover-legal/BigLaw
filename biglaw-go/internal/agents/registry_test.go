// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agents

import (
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// fakeEmbedServer stands up an Ollama-style embeddings endpoint that returns a
// deterministic, prompt-dependent unit-ish vector so distinct agents get distinct
// (non-zero) embeddings — enough for cosine search to be meaningful.
func fakeEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h := fnv.New32a()
		_, _ = h.Write([]byte(body.Prompt))
		seed := h.Sum32()
		vec := make([]float32, 8)
		for i := range vec {
			vec[i] = float32((seed>>(i*3))&0x7) + 1 // always > 0
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"embedding": vec})
	}))
}

func embedTestClient(t *testing.T, url string) *embeddings.Client {
	cfg := &config.Config{}
	cfg.Local.LocalEmbeddings = true
	cfg.Local.OllamaURL = url
	cfg.Local.LocalEmbeddingModel = "nomic-embed-text"
	return embeddings.NewClient(cfg)
}

func regTestDefs() []types.AgentDefinition {
	tier := types.AgentTier(2)
	return []types.AgentDefinition{
		{ID: "a1", Name: "Trust Specialist", Tier: tier, Description: "estate planning, trusts, and residuary distribution"},
		{ID: "a2", Name: "Antitrust Analyst", Tier: tier, Description: "merger review, HSR notification, and competition law"},
		{ID: "a3", Name: "Employment Counsel", Tier: tier, Description: "settlement agreements, non-competes, and wrongful dismissal"},
	}
}

// TestRegistryReembedsOnReload is the restart-bricks-recruitment regression: agent
// embeddings are not persisted (types.AgentDefinition.Embedding is json:"-"), so a
// reloaded registry has zero vectors and searchByEmbedding would skip every agent —
// permanently returning no recruits after a restart. Init() must re-embed on load so
// a reloaded registry recruits IDENTICALLY to a fresh one.
func TestRegistryReembedsOnReload(t *testing.T) {
	srv := fakeEmbedServer(t)
	defer srv.Close()
	embedC := embedTestClient(t, srv.URL)
	dir := t.TempDir()
	defs := regTestDefs()

	// Fresh registry: seed + embed + persist.
	fresh := NewRegistry(embedC, dir)
	if err := fresh.RegisterAll(defs); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	if err := fresh.Persist(); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	// Confirm the persisted agents.json indeed dropped the vectors (the root cause).
	reloadedRaw := NewRegistry(embedC, dir)
	if err := jsonReload(t, dir, reloadedRaw); err == nil {
		for _, a := range reloadedRaw.ListAll() {
			if len(a.Embedding) != 0 {
				t.Fatalf("expected persisted agents.json to carry NO embeddings (json:\"-\"), agent %s had %d", a.ID, len(a.Embedding))
			}
		}
	}

	// Restart path: a brand-new registry loads from disk via Init(), which must
	// re-embed the zero-vector agents.
	restarted := NewRegistry(embedC, dir)
	if err := restarted.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, a := range restarted.ListAll() {
		if len(a.Embedding) == 0 {
			t.Fatalf("agent %s still has no embedding after Init — recruitment would be bricked", a.ID)
		}
	}

	// The reloaded registry must recruit: searchByEmbedding (via Search) returns agents.
	got, err := restarted.Search("trusts and residuary estate distribution", SearchOpts{TopK: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("reloaded registry returned zero recruits — the restart-bricks-recruitment bug")
	}

	// And it must recruit IDENTICALLY to a fresh registry for the same query.
	freshGot, err := fresh.Search("trusts and residuary estate distribution", SearchOpts{TopK: 3})
	if err != nil {
		t.Fatalf("fresh Search: %v", err)
	}
	if len(freshGot) != len(got) || freshGot[0].ID != got[0].ID {
		t.Errorf("reloaded top recruit %q != fresh %q — restart must recruit identically", got[0].ID, freshGot[0].ID)
	}
}

// jsonReload loads agents.json into r without re-embedding, to inspect what was
// actually persisted (mirrors Init's unmarshal step, minus ensureEmbeddings).
func jsonReload(t *testing.T, dir string, r *Registry) error {
	t.Helper()
	data, err := os.ReadFile(dir + "/agents.json")
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &r.agents)
}
