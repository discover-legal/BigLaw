// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Intake HTTP surface. Portal-facing routes authenticate exclusively via
// HMAC request signing (contract: docs/integration/affidavit-intake.md);
// firm-facing queue routes use the core's session/bearer auth (lent through
// modules.Host). The HMAC grants nothing outside the portal route set.
package intake

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/crm"
	"github.com/discover-legal/biglaw-go/internal/modules"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ModuleName is the registry name of the intake module.
const ModuleName = "intake"

// maxBodyBytes bounds a signed portal body (the draft itself is capped
// tighter inside the service).
const maxBodyBytes = 4 << 20

// TaskSubmitter is the task-submission seam (satisfied by the orchestrator).
type TaskSubmitter interface {
	SubmitTask(params orchestrator.SubmitParams) (*types.Task, error)
}

// HTTPConfig configures the portal authentication.
type HTTPConfig struct {
	HMACSecret string // shared with the portal; empty = portal routes answer 503
	MaxSkewSec int    // accepted timestamp skew; <= 0 → 300
}

// HTTP is the intake module's route set (implements modules.HTTPModule).
type HTTP struct {
	svc    *Service
	crm    *crm.Service
	tasks  TaskSubmitter
	cfg    HTTPConfig
	roster Roster
}

// NewHTTP builds the intake HTTP module. tasks may be nil (the /task route
// then answers 503).
func NewHTTP(svc *Service, crmSvc *crm.Service, roster Roster, tasks TaskSubmitter, cfg HTTPConfig) *HTTP {
	return &HTTP{svc: svc, crm: crmSvc, roster: roster, tasks: tasks, cfg: cfg}
}

// ModuleName implements modules.HTTPModule.
func (h *HTTP) ModuleName() string { return ModuleName }

// PublicRoute marks the portal-facing routes as public: they self-
// authenticate via HMAC, exactly like the Slack/Teams webhooks. The
// firm-facing queue routes are NOT public.
func (h *HTTP) PublicRoute(method, path string) bool { return IsPortalRoute(method, path) }

// IsPortalRoute reports whether method+path is in the HMAC portal set.
func IsPortalRoute(method, path string) bool {
	switch method {
	case http.MethodPost:
		if path == "/intake/submissions" {
			return true
		}
		if strings.HasPrefix(path, "/intake/proposals/") && strings.HasSuffix(path, "/decision") {
			return true
		}
		if strings.HasPrefix(path, "/intake/clients/") && strings.HasSuffix(path, "/proposals") {
			return true
		}
	case http.MethodGet:
		if rest, ok := strings.CutPrefix(path, "/intake/submissions/"); ok && rest != "" && !strings.Contains(rest, "/") {
			return true
		}
		if strings.HasPrefix(path, "/intake/clients/") &&
			(strings.HasSuffix(path, "/profile") || strings.HasSuffix(path, "/submissions")) {
			return true
		}
	}
	return false
}

// VerifyHMAC authenticates a portal request per the signing contract. It
// consumes and restores the request body. Writes the error response and
// returns false on failure.
func (h *HTTP) VerifyHMAC(c *gin.Context) bool {
	secret := h.cfg.HMACSecret
	if secret == "" || !modules.Default.Enabled(ModuleName) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "intake is not configured"})
		return false
	}
	fail := func() bool {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "intake authentication failed"})
		return false
	}
	tsHeader := strings.TrimSpace(c.GetHeader("X-Intake-Timestamp"))
	sigHeader := strings.TrimSpace(c.GetHeader("X-Intake-Signature"))
	if tsHeader == "" || sigHeader == "" {
		return fail()
	}
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fail()
	}
	skew := h.cfg.MaxSkewSec
	if skew <= 0 {
		skew = 300
	}
	if math.Abs(float64(time.Now().Unix()-ts)) > float64(skew) {
		return fail()
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if !hmac.Equal([]byte(sigHeader), []byte(Sign(secret, c.Request.Method, c.Request.URL.RequestURI(), tsHeader, body))) {
		return fail()
	}
	return true
}

// Sign computes the v1 request signature (exported for tests and tools).
func Sign(secret, method, pathWithQuery, ts string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	canonical := strings.ToUpper(method) + "\n" + pathWithQuery + "\n" + ts + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// submissionWire is the contract shape: the record plus a conflict *object*
// and the assignee's display name. Portal responses carry only
// {"hasConflict": bool}; who the conflict is with is firm-confidential.
type submissionWire struct {
	store.IntakeSubmission
	ConflictDetail json.RawMessage `json:"conflict"`
	AssignedToName string          `json:"assignedToName,omitempty"`
	NosLegal       json.RawMessage `json:"noslegal,omitempty"`
}

func (h *HTTP) wire(host modules.Host, sub store.IntakeSubmission, firm bool) submissionWire {
	var detail json.RawMessage
	if firm && len(sub.ConflictJSON) > 0 {
		detail = json.RawMessage(sub.ConflictJSON)
	} else if sub.Conflict {
		detail = json.RawMessage(`{"hasConflict":true}`)
	} else {
		detail = json.RawMessage(`{"hasConflict":false}`)
	}
	w := submissionWire{IntakeSubmission: sub, ConflictDetail: detail}
	if len(sub.NosLegalJSON) > 0 {
		w.NosLegal = json.RawMessage(sub.NosLegalJSON)
	}
	if host != nil {
		w.AssignedToName = host.ProfileName(sub.AssignedTo)
	}
	return w
}

func (h *HTTP) wireList(host modules.Host, subs []store.IntakeSubmission, firm bool) []submissionWire {
	out := make([]submissionWire, 0, len(subs))
	for _, sub := range subs {
		out = append(out, h.wire(host, sub, firm))
	}
	return out
}

// Mount registers the intake endpoints (modules.HTTPModule).
func (h *HTTP) Mount(r gin.IRouter, host modules.Host) {
	svc, crmSvc := h.svc, h.crm

	// ── Portal-facing (HMAC) ────────────────────────────────────────────────
	// These run as the trusted intake principal after signature verification;
	// scoping to the calling client is enforced per-route via externalRef.

	r.POST("/intake/submissions", func(c *gin.Context) {
		if !h.VerifyHMAC(c) {
			return
		}
		var req SubmissionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		sub, err := svc.Submit(store.WithSystem(c.Request.Context()), req)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"submission": h.wire(host, *sub, false)})
	})

	r.GET("/intake/submissions/:id", func(c *gin.Context) {
		if !h.VerifyHMAC(c) {
			return
		}
		sub, ok, err := svc.Get(store.WithSystem(c.Request.Context()), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "submission not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submission": h.wire(host, *sub, false)})
	})

	r.GET("/intake/clients/:externalId/submissions", func(c *gin.Context) {
		if !h.VerifyHMAC(c) {
			return
		}
		subs, err := svc.ListForClient(store.WithSystem(c.Request.Context()), c.Param("externalId"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submissions": h.wireList(host, subs, false)})
	})

	r.GET("/intake/clients/:externalId/profile", func(c *gin.Context) {
		if !h.VerifyHMAC(c) {
			return
		}
		ctx := store.WithSystem(c.Request.Context())
		profile, ok, err := crmSvc.GetByExternalRef(ctx, c.Param("externalId"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "no profile for this client"})
			return
		}
		view, err := crmSvc.View(ctx, profile.ID)
		if err != nil || view == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "profile view failed"})
			return
		}
		c.JSON(http.StatusOK, h.profileWire(view))
	})

	r.POST("/intake/clients/:externalId/proposals", func(c *gin.Context) {
		if !h.VerifyHMAC(c) {
			return
		}
		var body struct {
			Client struct {
				Email string `json:"email"`
				Name  string `json:"name"`
			} `json:"client"`
			Facts []crm.FactInput `json:"facts"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		ctx := store.WithSystem(c.Request.Context())
		externalID := c.Param("externalId")
		profile, err := crmSvc.EnsureProfile(ctx, externalID, body.Client.Email, body.Client.Name)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		proposals, err := crmSvc.Propose(ctx, profile.ID,
			crm.Actor{Role: crm.RoleClient, ID: externalID}, "client", body.Facts)
		if err != nil {
			c.JSON(crm.ErrStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"proposals": proposals})
	})

	r.POST("/intake/proposals/:id/decision", func(c *gin.Context) {
		if !h.VerifyHMAC(c) {
			return
		}
		var body struct {
			ClientExternalID string `json:"clientExternalId"`
			Decision         string `json:"decision"`
			Note             string `json:"note"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		if body.ClientExternalID == "" || (body.Decision != "approve" && body.Decision != "reject") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "clientExternalId and decision (approve|reject) are required"})
			return
		}
		fact, err := crmSvc.Decide(store.WithSystem(c.Request.Context()), c.Param("id"),
			crm.Actor{Role: crm.RoleClient, ID: body.ClientExternalID}, body.Decision == "approve", body.Note)
		if err != nil {
			c.JSON(crm.ErrStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"fact": fact})
	})

	// ── Firm-facing (session/bearer) ────────────────────────────────────────

	requireUser := func(c *gin.Context) *types.SessionUser {
		u := host.User(c)
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		}
		return u
	}

	r.GET("/intake/queue", func(c *gin.Context) {
		u := requireUser(c)
		if u == nil {
			return
		}
		scope := u.ProfileID
		if auth.IsPartner(u) {
			scope = "" // partners see everything
		}
		subs, err := svc.Queue(host.Identity(c), scope)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submissions": h.wireList(host, subs, true)})
	})

	r.POST("/intake/submissions/:id/claim", func(c *gin.Context) {
		u := requireUser(c)
		if u == nil {
			return
		}
		sub, err := svc.Claim(host.Identity(c), c.Param("id"), u.ProfileID)
		if err != nil {
			c.JSON(errStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submission": h.wire(host, *sub, true)})
	})

	r.PATCH("/intake/submissions/:id", func(c *gin.Context) {
		u := requireUser(c)
		if u == nil {
			return
		}
		var body struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		sub, err := svc.Update(host.Identity(c), c.Param("id"), body.Status, body.Note, u.ProfileID)
		if err != nil {
			c.JSON(errStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submission": h.wire(host, *sub, true)})
	})

	// Conflict-hold decision (partner only — mirrors the roster's
	// acknowledgeConflict + conflictReason override on POST /clients).
	r.POST("/intake/submissions/:id/conflict", func(c *gin.Context) {
		u := requireUser(c)
		if u == nil {
			return
		}
		if !auth.IsPartner(u) {
			c.JSON(http.StatusForbidden, gin.H{"error": "partner access required"})
			return
		}
		var body struct {
			Decision       string `json:"decision"` // acknowledge | decline
			ConflictReason string `json:"conflictReason"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		if body.Decision != "acknowledge" && body.Decision != "decline" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "decision must be acknowledge or decline"})
			return
		}
		sub, err := svc.ResolveConflict(host.Identity(c), c.Param("id"),
			body.Decision == "acknowledge", body.ConflictReason, u.ProfileID)
		if err != nil {
			c.JSON(errStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submission": h.wire(host, *sub, true)})
	})

	// Spin up an orchestrator review task over the ingested draft. Refused
	// while the submission is on conflict hold (no agent work on held
	// submissions).
	r.POST("/intake/submissions/:id/task", func(c *gin.Context) {
		u := requireUser(c)
		if u == nil {
			return
		}
		if h.tasks == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "task submission unavailable"})
			return
		}
		ctx := host.Identity(c)
		sub, ok, err := svc.Get(ctx, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "submission not found"})
			return
		}
		if err := svc.TaskReady(sub); err != nil {
			c.JSON(errStatus(err), gin.H{"error": err.Error()})
			return
		}
		var body struct {
			Description  string `json:"description"`
			WorkflowType string `json:"workflowType"`
		}
		_ = c.ShouldBindJSON(&body) // both fields optional
		desc := strings.TrimSpace(body.Description)
		if desc == "" {
			desc = "Review the client-submitted draft \"" + sub.Title + "\""
			if sub.DocumentType != "" {
				desc += " (" + sub.DocumentType + ")"
			}
			if sub.PracticeArea != "" {
				desc += " — " + sub.PracticeArea
			}
			desc += " for legal sufficiency, jurisdictional compliance, and risk; propose revisions."
		}
		wf := types.WorkflowReview
		if body.WorkflowType != "" {
			wf = types.WorkflowType(body.WorkflowType)
		}
		task, err := h.tasks.SubmitTask(orchestrator.SubmitParams{
			Description:        desc,
			WorkflowType:       wf,
			DocumentIDs:        []string{sub.DocumentID},
			ClientNumber:       sub.ClientNumber,
			MatterNumber:       sub.MatterNumber,
			Jurisdiction:       sub.Jurisdiction,
			CreatedByProfileID: u.ProfileID,
		})
		if err != nil {
			if errors.Is(err, orchestrator.ErrTaskQueueFull) {
				c.Header("Retry-After", "30")
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		updated, err := svc.AttachTask(ctx, sub.ID, task.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"submission": h.wire(host, *updated, true), "task": task})
	})
}

// profileWire shapes the portal profile view per the contract: the profile
// object gains the roster clientNumber ("" while the client is a prospect).
func (h *HTTP) profileWire(view *crm.ProfileView) gin.H {
	clientNumber := ""
	if h.roster != nil && view.Profile.ClientID != "" {
		if rc := h.roster.Get(view.Profile.ClientID); rc != nil {
			clientNumber = rc.ClientNumber
		}
	}
	type profileWire struct {
		store.CRMProfile
		ClientNumber string `json:"clientNumber"`
	}
	return gin.H{
		"profile":               profileWire{CRMProfile: view.Profile, ClientNumber: clientNumber},
		"facts":                 crm.NonNil(view.Facts),
		"pendingYourApproval":   crm.NonNil(view.PendingClientApproval),
		"pendingLawyerApproval": crm.NonNil(view.PendingLawyerApproval),
	}
}

func errStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrNotHeld), errors.Is(err, ErrConflictHold), errors.Is(err, ErrNoDocument):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}
