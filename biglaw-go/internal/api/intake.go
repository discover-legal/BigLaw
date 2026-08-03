// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Intake routes — the affidavit-maker channel. Portal-facing routes
// authenticate exclusively via HMAC request signing (contract:
// docs/integration/affidavit-intake.md); firm-facing queue routes use the
// normal session/bearer auth. The HMAC grants nothing outside /intake/*.
package api

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
	"github.com/discover-legal/biglaw-go/internal/intake"
	"github.com/discover-legal/biglaw-go/internal/modules"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// maxIntakeBodyBytes bounds a signed portal body (the draft itself is capped
// tighter inside the intake service).
const maxIntakeBodyBytes = 4 << 20

// isPortalIntakeRoute marks the portal-facing subset of /intake/* as public:
// these self-authenticate via HMAC, exactly like the Slack/Teams webhooks.
// The firm-facing queue routes (/intake/queue, claim/patch/task) are NOT
// public and require a session or bearer credential.
func isPortalIntakeRoute(method, path string) bool {
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

// verifyIntakeHMAC authenticates a portal request per the signing contract.
// It consumes and restores the request body. Writes the error response and
// returns false on failure.
func (s *Server) verifyIntakeHMAC(c *gin.Context) bool {
	secret := s.cfg.Intake.HMACSecret
	if secret == "" || !modules.Default.Enabled("intake") {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "intake is not configured"})
		return false
	}

	tsHeader := strings.TrimSpace(c.GetHeader("X-Intake-Timestamp"))
	sigHeader := strings.TrimSpace(c.GetHeader("X-Intake-Signature"))
	if tsHeader == "" || sigHeader == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "intake authentication failed"})
		return false
	}
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "intake authentication failed"})
		return false
	}
	skew := s.cfg.Intake.MaxSkewSec
	if skew <= 0 {
		skew = 300
	}
	if math.Abs(float64(time.Now().Unix()-ts)) > float64(skew) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "intake authentication failed"})
		return false
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxIntakeBodyBytes+1))
	if err != nil || len(body) > maxIntakeBodyBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	bodyHash := sha256.Sum256(body)
	canonical := strings.ToUpper(c.Request.Method) + "\n" +
		c.Request.URL.RequestURI() + "\n" +
		tsHeader + "\n" +
		hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	expected := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sigHeader), []byte(expected)) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "intake authentication failed"})
		return false
	}
	return true
}

// intakeSubmissionWire is the contract shape: the embedded record plus a
// conflict *object* (shadowing the bool) and the assignee's display name.
type intakeSubmissionWire struct {
	store.IntakeSubmission
	ConflictDetail json.RawMessage `json:"conflict"`
	AssignedToName string          `json:"assignedToName,omitempty"`
}

func (s *Server) intakeWire(sub store.IntakeSubmission) intakeSubmissionWire {
	detail := json.RawMessage(sub.ConflictJSON)
	if len(detail) == 0 {
		detail = json.RawMessage(`{"hasConflict":false}`)
	}
	w := intakeSubmissionWire{IntakeSubmission: sub, ConflictDetail: detail}
	if sub.AssignedTo != "" && s.profiles != nil {
		if p := s.profiles.Get(sub.AssignedTo); p != nil {
			w.AssignedToName = p.Name
		}
	}
	return w
}

func (s *Server) intakeWireList(subs []store.IntakeSubmission) []intakeSubmissionWire {
	out := make([]intakeSubmissionWire, 0, len(subs))
	for _, sub := range subs {
		out = append(out, s.intakeWire(sub))
	}
	return out
}

// AttachIntake registers the intake endpoints. No-op when svc is nil or the
// intake module is disabled.
func (s *Server) AttachIntake(svc *intake.Service, crmSvc *crm.Service) {
	if svc == nil || !modules.Default.Enabled("intake") {
		return
	}
	s.intake = svc
	r := s.router

	// ── Portal-facing (HMAC) ────────────────────────────────────────────────
	// These run as the trusted intake principal after signature verification;
	// scoping to the calling client is enforced per-route via externalRef.

	r.POST("/intake/submissions", func(c *gin.Context) {
		if !s.verifyIntakeHMAC(c) {
			return
		}
		var req intake.SubmissionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		sub, err := svc.Submit(store.WithSystem(c.Request.Context()), req)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"submission": s.intakeWire(*sub)})
	})

	r.GET("/intake/submissions/:id", func(c *gin.Context) {
		if !s.verifyIntakeHMAC(c) {
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
		c.JSON(http.StatusOK, gin.H{"submission": s.intakeWire(*sub)})
	})

	r.GET("/intake/clients/:externalId/submissions", func(c *gin.Context) {
		if !s.verifyIntakeHMAC(c) {
			return
		}
		subs, err := svc.ListForClient(store.WithSystem(c.Request.Context()), c.Param("externalId"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submissions": s.intakeWireList(subs)})
	})

	r.GET("/intake/clients/:externalId/profile", func(c *gin.Context) {
		if !s.verifyIntakeHMAC(c) {
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
		c.JSON(http.StatusOK, s.crmProfileWire(view))
	})

	r.POST("/intake/clients/:externalId/proposals", func(c *gin.Context) {
		if !s.verifyIntakeHMAC(c) {
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
			c.JSON(crmErrStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"proposals": proposals})
	})

	r.POST("/intake/proposals/:id/decision", func(c *gin.Context) {
		if !s.verifyIntakeHMAC(c) {
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
			c.JSON(crmErrStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"fact": fact})
	})

	// ── Firm-facing (session/bearer) ────────────────────────────────────────

	r.GET("/intake/queue", func(c *gin.Context) {
		u := getUser(c)
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		scope := u.ProfileID
		if auth.IsPartner(u) {
			scope = "" // partners see everything
		}
		subs, err := svc.Queue(reqIdentity(c), scope)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submissions": s.intakeWireList(subs)})
	})

	r.POST("/intake/submissions/:id/claim", func(c *gin.Context) {
		u := getUser(c)
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		sub, err := svc.Claim(reqIdentity(c), c.Param("id"), u.ProfileID)
		if err != nil {
			c.JSON(intakeErrStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submission": s.intakeWire(*sub)})
	})

	r.PATCH("/intake/submissions/:id", func(c *gin.Context) {
		u := getUser(c)
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
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
		sub, err := svc.Update(reqIdentity(c), c.Param("id"), body.Status, body.Note, u.ProfileID)
		if err != nil {
			c.JSON(intakeErrStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"submission": s.intakeWire(*sub)})
	})

	// Spin up an orchestrator review task over the ingested draft.
	r.POST("/intake/submissions/:id/task", func(c *gin.Context) {
		u := getUser(c)
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		ctx := reqIdentity(c)
		sub, ok, err := svc.Get(ctx, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "submission not found"})
			return
		}
		var body struct {
			Description  string `json:"description"`
			WorkflowType string `json:"workflowType"`
		}
		_ = c.ShouldBindJSON(&body) // both fields optional
		desc := strings.TrimSpace(body.Description)
		if desc == "" {
			desc = "Review the client-submitted draft \"" + sub.Title + "\" (" + sub.DocumentType +
				") for legal sufficiency, jurisdictional compliance, and risk; propose revisions."
		}
		wf := types.WorkflowReview
		if body.WorkflowType != "" {
			wf = types.WorkflowType(body.WorkflowType)
		}
		var docIDs []string
		if sub.DocumentID != "" {
			docIDs = []string{sub.DocumentID}
		}
		task, err := s.orch.SubmitTask(orchestrator.SubmitParams{
			Description:        desc,
			WorkflowType:       wf,
			DocumentIDs:        docIDs,
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
		c.JSON(http.StatusAccepted, gin.H{"submission": s.intakeWire(*updated), "task": task})
	})
}

// crmProfileWire shapes the portal profile view per the contract: the profile
// object gains the roster clientNumber.
func (s *Server) crmProfileWire(view *crm.ProfileView) gin.H {
	clientNumber := ""
	if s.clients != nil {
		if rc := s.clients.Get(view.Profile.ClientID); rc != nil {
			clientNumber = rc.ClientNumber
		}
	}
	type profileWire struct {
		store.CRMProfile
		ClientNumber string `json:"clientNumber"`
	}
	return gin.H{
		"profile":               profileWire{CRMProfile: view.Profile, ClientNumber: clientNumber},
		"facts":                 emptyIfNilFacts(view.Facts),
		"pendingYourApproval":   emptyIfNilFacts(view.PendingClientApproval),
		"pendingLawyerApproval": emptyIfNilFacts(view.PendingLawyerApproval),
	}
}

func intakeErrStatus(err error) int {
	if errors.Is(err, intake.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
