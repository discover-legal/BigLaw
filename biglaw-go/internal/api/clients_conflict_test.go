// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	r.PATCH("/clients/:id", s.handleUpdateClient)
	r.POST("/clients/check-conflict", s.handleCheckConflict)
	return r
}

func postJSON(t *testing.T, r *gin.Engine, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	return sendJSON(t, r, http.MethodPost, path, body)
}

func sendJSON(t *testing.T, r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
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
		Conflict  types.ConflictCheckResult `json:"conflict"`
		Conflicts []conflictHit             `json:"conflicts"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &refused)
	if w.Code != http.StatusConflict || !refused.Conflict.HasConflict ||
		refused.Conflict.ConflictingClientID != first.ID || refused.Conflict.MatchedAdversary != "Acme Corp." {
		t.Fatalf("expected 409 conflict with Globex, got %d %s", w.Code, w.Body)
	}

	if len(refused.Conflicts) != 1 || refused.Conflicts[0].ID == "" {
		t.Fatalf("409 must list every hit with an id: %s", w.Body)
	}
	hitID := refused.Conflicts[0].ID

	// Acknowledging without a reason is still refused.
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "ACME Corporation", "clientNumber": "C-101",
		"acknowledgedConflictIds": []string{hitID}, "conflictReason": "   "})
	if w.Code != http.StatusConflict {
		t.Fatalf("ack without reason: %d %s", w.Code, w.Body)
	}
	// The bare legacy flag does not name the hits it waives.
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "ACME Corporation", "clientNumber": "C-101",
		"acknowledgeConflict": true, "conflictReason": "consent"})
	if w.Code != http.StatusConflict {
		t.Fatalf("ack without ids: %d %s", w.Code, w.Body)
	}

	// Acknowledged with a reason → created, override audited.
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "ACME Corporation", "clientNumber": "C-101",
		"acknowledgedConflictIds": []string{hitID}, "conflictReason": "Informed consent from both clients 2026-10-01"})
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
	hits, _ := override.Data["conflicts"].([]interface{})
	if got["client.created"] < 2 || got["client.conflict_detected"] < 3 || override.ID == "" ||
		override.Data["reason"] != "Informed consent from both clients 2026-10-01" || len(hits) != 1 ||
		hits[0].(map[string]interface{})["matchedAdversary"] != "Acme Corp." {
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

func createdID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out.ID
}

func hitIDs(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var out struct {
		Conflicts []conflictHit `json:"conflicts"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	ids := make([]string, 0, len(out.Conflicts))
	for _, h := range out.Conflicts {
		ids = append(ids, h.ID)
	}
	return ids
}

// The round-2 bypasses: renaming into a conflict, adding an adversary that
// is an existing client, and a conflicting party past the old 200 cap.
func TestClientConflictGateHasNoBypass(t *testing.T) {
	r := newClientsRouter(t)
	alpha := createdID(t, postJSON(t, r, "/clients", map[string]interface{}{
		"name": "Alpha Ltd", "clientNumber": "C-1", "adversaries": []string{"Evil Corp"}}))
	benign := createdID(t, postJSON(t, r, "/clients", map[string]interface{}{"name": "Benign Co", "clientNumber": "C-2"}))

	// (a) rename into a party adverse to Alpha.
	w := sendJSON(t, r, http.MethodPatch, "/clients/"+benign, map[string]interface{}{"name": "Evil Corp"})
	if w.Code != http.StatusConflict {
		t.Fatalf("rename into conflict: %d %s", w.Code, w.Body)
	}
	// (b) list an existing client as adverse.
	w = sendJSON(t, r, http.MethodPatch, "/clients/"+benign, map[string]interface{}{"adversaries": []string{"Alpha Ltd"}})
	if w.Code != http.StatusConflict {
		t.Fatalf("adversary = existing client: %d %s", w.Code, w.Body)
	}
	ids := hitIDs(t, w)
	w = sendJSON(t, r, http.MethodPatch, "/clients/"+benign, map[string]interface{}{
		"adversaries": []string{"Alpha Ltd"}, "acknowledgedConflictIds": ids, "conflictReason": "ethical wall"})
	if w.Code != http.StatusOK {
		t.Fatalf("acknowledged patch: %d %s", w.Code, w.Body)
	}
	// An unrelated edit does not re-trigger the settled conflict, and the
	// client never conflicts with itself.
	w = sendJSON(t, r, http.MethodPatch, "/clients/"+benign, map[string]interface{}{
		"notes": "n", "adversaries": []string{"Alpha Ltd", "Nobody Inc"}})
	if w.Code != http.StatusOK {
		t.Fatalf("unrelated edit: %d %s", w.Code, w.Body)
	}
	w = sendJSON(t, r, http.MethodPatch, "/clients/"+alpha, map[string]interface{}{"name": "Alpha Limited"})
	if w.Code != http.StatusOK {
		t.Fatalf("self rename: %d %s", w.Code, w.Body)
	}

	// (c) the conflicting party is the 201st adversary.
	advs := make([]string, 0, 201)
	for i := 0; i < 200; i++ {
		advs = append(advs, fmt.Sprintf("Party %03d Holdings", i))
	}
	advs = append(advs, "Alpha Limited")
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "Gamma", "clientNumber": "C-3", "adversaries": advs})
	if w.Code != http.StatusConflict {
		t.Fatalf("201st adversary: %d %s", w.Code, w.Body)
	}
	tooMany := make([]string, maxAdversaries+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("P%d", i)
	}
	if w := postJSON(t, r, "/clients", map[string]interface{}{"name": "Delta", "clientNumber": "C-4", "adversaries": tooMany}); w.Code != http.StatusBadRequest {
		t.Fatalf("over cap: %d", w.Code)
	}
}

// Every hit is reported and an acknowledgement covers exactly the hits shown.
func TestConflictAckCoversExactlyTheHitsShown(t *testing.T) {
	r := newClientsRouter(t)
	createdID(t, postJSON(t, r, "/clients", map[string]interface{}{"name": "One Co", "clientNumber": "C-1", "adversaries": []string{"Target Group"}}))
	createdID(t, postJSON(t, r, "/clients", map[string]interface{}{"name": "Two Co", "clientNumber": "C-2", "adversaries": []string{"Target Group"}}))

	w := postJSON(t, r, "/clients/check-conflict", map[string]interface{}{"name": "Target Group"})
	if ids := hitIDs(t, w); len(ids) != 2 {
		t.Fatalf("check-conflict must list both hits: %s", w.Body)
	}
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "Target Group", "clientNumber": "C-3"})
	ids := hitIDs(t, w)
	if w.Code != http.StatusConflict || len(ids) != 2 {
		t.Fatalf("both conflicts: %d %s", w.Code, w.Body)
	}
	// Acknowledging one of two waives nothing.
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "Target Group", "clientNumber": "C-3",
		"acknowledgedConflictIds": ids[:1], "conflictReason": "consent"})
	if w.Code != http.StatusConflict {
		t.Fatalf("partial ack: %d %s", w.Code, w.Body)
	}
	// A hit that appeared after the 409 invalidates the older acknowledgement.
	createdID(t, postJSON(t, r, "/clients", map[string]interface{}{"name": "Three Co", "clientNumber": "C-4", "adversaries": []string{"Target Group"}}))
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "Target Group", "clientNumber": "C-3",
		"acknowledgedConflictIds": ids, "conflictReason": "consent"})
	if w.Code != http.StatusConflict || len(hitIDs(t, w)) != 3 {
		t.Fatalf("stale ack: %d %s", w.Code, w.Body)
	}
	w = postJSON(t, r, "/clients", map[string]interface{}{"name": "Target Group", "clientNumber": "C-3",
		"acknowledgedConflictIds": hitIDs(t, w), "conflictReason": "consent"})
	if w.Code != http.StatusCreated {
		t.Fatalf("full ack: %d %s", w.Code, w.Body)
	}
}
