// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/graph"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/gin-gonic/gin"
)

// ─── Clients ──────────────────────────────────────────────────────────────────

func (s *Server) handleListClients(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	list := s.clients.List()
	if list == nil {
		list = []types.Client{}
	}
	c.JSON(http.StatusOK, list)
}

// maxAdversaries bounds a client's adverse-party list. Every listed party is
// conflict-checked, so a longer list is refused rather than half-checked.
const maxAdversaries = 1000

// conflictAck is the override a partner sends after seeing a 409: the IDs of
// exactly the hits they were shown, plus why the conflict is acceptable.
type conflictAck struct {
	// AcknowledgedConflictIDs must equal the current hit set — a new or
	// changed conflict since the 409 is never waived by an older consent.
	AcknowledgedConflictIDs []string `json:"acknowledgedConflictIds"`
	// AcknowledgeConflict is the legacy flag; it is not enough on its own.
	AcknowledgeConflict bool   `json:"acknowledgeConflict"`
	ConflictReason      string `json:"conflictReason"`
}

type createClientBody struct {
	Name         string   `json:"name"`
	ClientNumber string   `json:"clientNumber"`
	Adversaries  []string `json:"adversaries"`
	Notes        string   `json:"notes"`
	conflictAck
}

// conflictHit is one conflict with a stable ID the acknowledgement names.
type conflictHit struct {
	ID string `json:"id"`
	types.ConflictCheckResult
}

// conflictHits converts store hits into ID'd hits, one per (client, party).
func conflictHits(raw []types.ConflictCheckResult) []conflictHit {
	seen := map[string]bool{}
	out := []conflictHit{}
	for _, h := range raw {
		sum := sha256.Sum256([]byte(h.ConflictingClientID + "\x00" + strings.ToLower(strings.TrimSpace(h.MatchedAdversary))))
		id := hex.EncodeToString(sum[:8])
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, conflictHit{ID: id, ConflictCheckResult: h})
	}
	return out
}

// auditHits flattens hits to plain maps so the audit entry reads the same in
// memory and after a reload from the JSONL log.
func auditHits(hits []conflictHit) []interface{} {
	out := make([]interface{}, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]interface{}{
			"id": h.ID, "conflictingClientId": h.ConflictingClientID,
			"conflictingClientName": h.ConflictingClientName, "matchedAdversary": h.MatchedAdversary,
		})
	}
	return out
}

// conflictGate decides whether hits block the write. It returns the trimmed
// override reason when the acknowledgement covers exactly the hits, or
// writes the 409 (and audits the block) and returns ok=false.
func conflictGate(c *gin.Context, subject map[string]interface{}, hits []conflictHit, ack conflictAck) (reason string, ok bool) {
	if len(hits) == 0 {
		return "", true
	}
	reason = strings.TrimSpace(ack.ConflictReason)
	if len(reason) > 1000 {
		reason = strutil.Truncate(reason, 1000)
	}
	want := map[string]bool{}
	for _, h := range hits {
		want[h.ID] = true
	}
	got := map[string]bool{}
	for _, id := range ack.AcknowledgedConflictIDs {
		got[id] = true
	}
	exact := len(got) == len(want)
	for id := range want {
		exact = exact && got[id]
	}
	if exact && reason != "" {
		return reason, true
	}

	data := map[string]interface{}{"conflicts": auditHits(hits), "blocked": true}
	for k, v := range subject {
		data[k] = v
	}
	auditAdmin(c, "client.conflict_detected", data)
	msg := "conflict of interest detected: " + hits[0].ConflictingClientName + " lists \"" + hits[0].MatchedAdversary + "\" as an adverse party"
	if len(hits) > 1 {
		msg += fmt.Sprintf(" (and %d more)", len(hits)-1)
	}
	if len(got) > 0 && !exact {
		msg = "conflict acknowledgement does not match the current conflicts; review them and acknowledge again — " + msg
	}
	c.JSON(http.StatusConflict, gin.H{
		"error":     msg,
		"conflict":  hits[0].ConflictCheckResult,
		"conflicts": hits,
		"resolution": "re-submit with acknowledgedConflictIds set to the id of every conflict listed " +
			"and a non-empty conflictReason (e.g. informed-consent waiver, ethical wall)",
	})
	return "", false
}

func tooManyAdversaries(c *gin.Context, n int) bool {
	if n <= maxAdversaries {
		return false
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("too many adverse parties (%d); at most %d", n, maxAdversaries)})
	return true
}

func (s *Server) handleCreateClient(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body createClientBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if tooManyAdversaries(c, len(body.Adversaries)) {
		return
	}

	// Conflict check runs automatically BEFORE the client joins the roster,
	// against the existing clients only, over every adverse party. Any hit
	// blocks creation with 409 until a partner re-submits acknowledging each
	// hit by ID with a reason (waiver, ethical wall, ...); the override is
	// audited with who, why and every hit.
	hits := conflictHits(s.clients.ConflictHits(body.Name, body.Adversaries, ""))
	reason, ok := conflictGate(c, map[string]interface{}{"name": body.Name}, hits, body.conflictAck)
	if !ok {
		return
	}

	client, err := s.clients.Create(body.Name, body.ClientNumber, body.Adversaries, body.Notes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	go s.syncGraph()
	auditAdmin(c, "client.created", map[string]interface{}{
		"clientId": client.ID, "clientNumber": client.ClientNumber, "name": client.Name,
		"adversaryCount": len(client.Adversaries), "conflict": len(hits) > 0,
	})
	resp := createClientResponse{Client: client, Conflicts: hits}
	if len(hits) > 0 {
		resp.Conflict = hits[0].ConflictCheckResult
		auditAdmin(c, "client.conflict_overridden", map[string]interface{}{
			"clientId": client.ID, "name": client.Name, "reason": reason, "conflicts": auditHits(hits),
		})
	}
	c.JSON(http.StatusCreated, resp)
}

// createClientResponse is the created client with the automatic conflict
// check result alongside (UI type: Client & { conflict: ConflictCheckResult });
// conflict.hasConflict is true only for an acknowledged override, and
// conflicts lists every acknowledged hit.
type createClientResponse struct {
	*types.Client
	Conflict  types.ConflictCheckResult `json:"conflict"`
	Conflicts []conflictHit             `json:"conflicts"`
}

// handleUpdateClient applies a PATCH. A change to the name or adverse
// parties goes through the same conflict gate as creation, for the hits the
// change introduces (conflicts the client already had were settled when they
// arose and are not re-litigated by an unrelated edit).
func (s *Server) handleUpdateClient(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var patch map[string]interface{}
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	var ack conflictAck
	if raw, err := json.Marshal(patch); err == nil {
		_ = json.Unmarshal(raw, &ack)
	}
	for _, k := range []string{"acknowledgedConflictIds", "acknowledgeConflict", "conflictReason"} {
		delete(patch, k)
	}

	id := c.Param("id")
	cur := s.clients.Get(id)
	if cur == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "client not found"})
		return
	}
	next := clients.Patched(*cur, patch)
	if tooManyAdversaries(c, len(next.Adversaries)) {
		return
	}
	var newHits []conflictHit
	if next.Name != cur.Name || !slices.Equal(next.Adversaries, cur.Adversaries) {
		before := map[string]bool{}
		for _, h := range conflictHits(s.clients.ConflictHits(cur.Name, cur.Adversaries, id)) {
			before[h.ID] = true
		}
		for _, h := range conflictHits(s.clients.ConflictHits(next.Name, next.Adversaries, id)) {
			if !before[h.ID] {
				newHits = append(newHits, h)
			}
		}
	}
	reason, ok := conflictGate(c, map[string]interface{}{"clientId": id, "name": next.Name}, newHits, ack)
	if !ok {
		return
	}

	client, err := s.clients.Update(id, patch)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	go s.syncGraph()
	auditAdmin(c, "client.updated", map[string]interface{}{"clientId": client.ID, "fields": patchKeys(patch)})
	if len(newHits) > 0 {
		auditAdmin(c, "client.conflict_overridden", map[string]interface{}{
			"clientId": client.ID, "name": client.Name, "reason": reason, "conflicts": auditHits(newHits),
		})
	}
	c.JSON(http.StatusOK, client)
}

func (s *Server) handleDeleteClient(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	deleted, err := s.clients.Remove(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !deleted {
		c.JSON(http.StatusNotFound, gin.H{"error": "client not found"})
		return
	}
	go s.syncGraph()
	auditAdmin(c, "client.deleted", map[string]interface{}{"clientId": c.Param("id")})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type addMatterBody struct {
	MatterNumber string `json:"matterNumber"`
	Description  string `json:"description"`
	PracticeArea string `json:"practiceArea"`
}

func (s *Server) handleAddMatter(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body addMatterBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.MatterNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber is required"})
		return
	}

	matter, err := s.clients.AddMatter(c.Param("id"), body.MatterNumber, body.Description, body.PracticeArea)
	if err != nil {
		if err.Error() == "client not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	go s.syncGraph()
	auditAdmin(c, "matter.created", map[string]interface{}{
		"clientId": c.Param("id"), "matterNumber": matter.MatterNumber, "practiceArea": matter.PracticeArea,
	})
	c.JSON(http.StatusCreated, matter)
}

func (s *Server) handleRemoveMatter(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	removed, err := s.clients.RemoveMatter(c.Param("id"), c.Param("num"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if !removed {
		c.JSON(http.StatusNotFound, gin.H{"error": "matter not found"})
		return
	}
	go s.syncGraph()
	auditAdmin(c, "matter.deleted", map[string]interface{}{"clientId": c.Param("id"), "matterNumber": c.Param("num")})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type checkConflictBody struct {
	Name        string   `json:"name"`
	Adversaries []string `json:"adversaries"`
}

func (s *Server) handleCheckConflict(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body checkConflictBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if len(name) > 500 {
		name = strutil.Truncate(name, 500)
	}
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if tooManyAdversaries(c, len(body.Adversaries)) {
		return
	}
	hits := conflictHits(s.clients.ConflictHits(name, body.Adversaries, ""))
	// The first hit stays at the top level (the legacy single-result shape);
	// conflicts lists every hit with the ID an acknowledgement must name.
	resp := gin.H{"hasConflict": false, "conflicts": hits}
	if len(hits) > 0 {
		h := hits[0]
		resp = gin.H{
			"hasConflict": true, "conflictingClientId": h.ConflictingClientID,
			"conflictingClientName": h.ConflictingClientName, "matchedAdversary": h.MatchedAdversary,
			"conflicts": hits,
		}
	}
	c.JSON(http.StatusOK, resp)
}

// handleClientGraphConflicts returns inference-derived conflicts for an
// existing client from the TypeDB sidecar (direct adversity, subsidiary
// chains). 503 if the sidecar or TypeDB is down — never silently empty.
func (s *Server) handleClientGraphConflicts(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	client := s.clients.Get(c.Param("id"))
	if client == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "client not found"})
		return
	}
	reports, err := s.graph.CheckClient(client.ID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conflict graph unavailable: " + err.Error()})
		return
	}
	if reports == nil {
		reports = []types.ConflictReport{}
	}
	c.JSON(http.StatusOK, gin.H{"clientId": client.ID, "conflicts": reports})
}

// syncGraph pushes the full client/matter roster to the TypeDB sidecar.
// Best-effort: failure is logged, the substring conflict check still works.
// The sidecar creates its socket asynchronously at container start, so the
// initial ping retries with backoff instead of giving up on the first miss.
func (s *Server) syncGraph() {
	var err error
	for attempt, wait := 0, time.Second; attempt < 6; attempt, wait = attempt+1, wait*2 {
		if err = s.graph.Ping(); err == nil {
			break
		}
		time.Sleep(wait)
	}
	if err != nil {
		slog.Warn("conflict graph: sidecar unreachable, graph conflicts disabled", "err", err)
		return
	}
	roster := s.clients.List()
	input := graph.SyncInput{}
	for _, cl := range roster {
		sc := graph.SyncClient{
			ID:          cl.ID,
			Name:        cl.Name,
			Adversaries: cl.Adversaries,
		}
		for _, m := range cl.Matters {
			sc.Matters = append(sc.Matters, graph.MatterRef{
				MatterNumber: m.MatterNumber,
				PracticeArea: m.PracticeArea,
			})
			input.Matters = append(input.Matters, graph.SyncMatter{
				MatterNumber: m.MatterNumber,
				PracticeArea: m.PracticeArea,
				Status:       "active",
			})
		}
		input.Clients = append(input.Clients, sc)
	}
	if err := s.graph.Sync(input); err != nil {
		slog.Warn("conflict graph: sync failed", "err", err)
		return
	}
	slog.Info("conflict graph: roster synced", "clients", len(input.Clients), "matters", len(input.Matters))
}
