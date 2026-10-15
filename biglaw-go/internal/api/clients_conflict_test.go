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

	// Acme is Globex's adverse party → 409 with the conflict, nothing created.
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "ACME Corporation", "clientNumber": "C-101"})
	var refused struct {
		Conflict types.ConflictCheckResult `json:"conflict"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &refused)
	if w.Code != http.StatusConflict || !refused.Conflict.HasConflict ||
		refused.Conflict.ConflictingClientID != first.ID || refused.Conflict.MatchedAdversary != "Acme Corp." {
		t.Fatalf("expected 409 conflict with Globex, got %d %s", w.Code, w.Body)
	}

	// Acknowledging without a reason is still refused.
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "ACME Corporation", "clientNumber": "C-101",
		"acknowledgeConflict": true, "conflictReason": "   "})
	if w.Code != http.StatusConflict {
		t.Fatalf("ack without reason: %d %s", w.Code, w.Body)
	}

	// Acknowledged with a reason → created, override audited.
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "ACME Corporation", "clientNumber": "C-101",
		"acknowledgeConflict": true, "conflictReason": "Informed consent from both clients 2026-10-01"})
	if w.Code != http.StatusCreated {
		t.Fatalf("acknowledged create: %d %s", w.Code, w.Body)
	}
	var second struct {
		ID       string                    `json:"id"`
		Conflict types.ConflictCheckResult `json:"conflict"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &second)
	if second.ID == "" || !second.Conflict.HasConflict {
		t.Fatalf("acknowledged response = %s", w.Body)
	}

	got := map[string]int{}
	var override audit.AuditEntry
	for _, e := range audit.Default.ReadFiltered(audit.Filter{ActorID: "partner-1", Limit: 1000}) {
		got[e.Event]++
		if e.Event == "client.conflict_overridden" && e.Data["clientId"] == second.ID {
			override = e
		}
	}
	if got["client.created"] < 2 || got["client.conflict_detected"] < 2 || override.ID == "" ||
		override.Data["reason"] != "Informed consent from both clients 2026-10-01" ||
		override.Data["matchedAdversary"] != "Acme Corp." {
		t.Fatalf("audit events = %v, override = %+v", got, override)
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
