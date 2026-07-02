// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Ingest-time integrity scan: obfuscated content still ingests (the scan is
// advisory, never fatal) but carries a compact warning on its metadata;
// clean content is untouched. Embeddings come from a local fake Ollama
// endpoint — no network, no models.

package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"embedding": []float32{0.1, 0.2, 0.3}})
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.Local.LocalEmbeddings = true
	cfg.Local.OllamaURL = srv.URL
	return NewStore(embeddings.NewClient(cfg))
}

func TestIngestAttachesIntegrityWarning(t *testing.T) {
	s := newTestStore(t)
	// Cyrillic а in "Pаyment" — a critical homoglyph finding.
	doc, err := s.Ingest(context.Background(), types.Document{
		Title:   "Shady Side Letter",
		Content: "The Pаyment shall be due immediately.",
	})
	if err != nil {
		t.Fatalf("Ingest must not fail on integrity findings: %v", err)
	}
	warning, _ := doc.Metadata["integrityWarning"].(string)
	if warning == "" {
		t.Fatalf("no integrityWarning on metadata: %+v", doc.Metadata)
	}
	if !strings.Contains(warning, "homoglyph") || !strings.Contains(warning, "critical") {
		t.Errorf("integrityWarning = %q", warning)
	}
	// The stored copy carries it too.
	if stored := s.GetByID(doc.ID); stored == nil || stored.Metadata["integrityWarning"] != warning {
		t.Error("stored document missing the integrity warning")
	}
}

func TestIngestCleanContentNoWarning(t *testing.T) {
	s := newTestStore(t)
	doc, err := s.Ingest(context.Background(), types.Document{
		Title:   "Master Services Agreement",
		Content: "The liability cap is twelve (12) months of fees paid under this Agreement.",
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if _, present := doc.Metadata["integrityWarning"]; present {
		t.Errorf("clean content got an integrity warning: %+v", doc.Metadata)
	}
}
