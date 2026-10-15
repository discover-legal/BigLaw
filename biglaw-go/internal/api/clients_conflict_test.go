// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/graph"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func newClientsRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("TYPEDB_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	store := clients.NewClientStore()
	if err := store.Init(filepath.Join(t.TempDir(), "clients.json")); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &config.Config{}, clients: store, graph: graph.New()}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxUserKey, &types.SessionUser{ProfileID: "partner-1", Role: types.RolePartner})
		c.Next()
	})
	r.POST("/clients", s.handleCreateClient)
	r.POST("/clients/:id/matters", s.handleAddMatter)
	return r
}

func postJSON(t *testing.T, r *gin.Engine, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// CLAUDE.md: the conflict check "is called automatically on POST /clients
// and the result is included in the response".
func TestCreateClientRunsConflictCheck(t *testing.T) {
	r := newClientsRouter(t)

	w := postJSON(t, r, "/clients", map[string]interface{}{
		"name": "Globex Corporation", "clientNumber": "C-100", "adversaries": []string{"Acme Corp."},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create globex: %d %s", w.Code, w.Body)
	}
	var first struct {
		ID       string                    `json:"id"`
		Name     string                    `json:"name"`
		Conflict types.ConflictCheckResult `json:"conflict"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &first)
	if first.ID == "" || first.Name != "Globex Corporation" || first.Conflict.HasConflict {
		t.Fatalf("first client response = %s", w.Body)
	}

	// Acme is Globex's adverse party → conflict surfaced, client still created.
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "ACME Corporation", "clientNumber": "C-101"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create acme: %d %s", w.Code, w.Body)
	}
	var second struct {
		ID       string                    `json:"id"`
		Conflict types.ConflictCheckResult `json:"conflict"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &second)
	if !second.Conflict.HasConflict || second.Conflict.ConflictingClientID != first.ID ||
		second.Conflict.MatchedAdversary != "Acme Corp." {
		t.Fatalf("expected conflict with Globex, got %s", w.Body)
	}

	got := map[string]int{}
	for _, e := range audit.Default.ReadFiltered(audit.Filter{ActorID: "partner-1", Limit: 1000}) {
		got[e.Event]++
	}
	if got["client.created"] < 2 || got["client.conflict_detected"] < 1 {
		t.Fatalf("audit events = %v", got)
	}

	w = postJSON(t, r, "/clients/"+first.ID+"/matters", map[string]interface{}{"matterNumber": "M-9"})
	if w.Code != http.StatusCreated {
		t.Fatalf("add matter: %d %s", w.Code, w.Body)
	}
	found := false
	for _, e := range audit.Default.ReadFiltered(audit.Filter{Event: "matter.created", Limit: 100}) {
		if e.Data["matterNumber"] == "M-9" && e.ActorID == "partner-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("matter.created audit event missing")
	}
}
