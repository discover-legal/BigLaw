// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// CRM HTTP surface — the firm side of the client profiles. Any authenticated
// firm member reads the profiles of admitted clients and works their consent
// queue; lawyer-proposed facts await the client's approval through the
// intake module's portal routes, completing the bidirectional-consent loop.
// A *prospect* profile (not yet on the roster — e.g. held on a conflict) is
// partner-only: under Rule 1.18 a prospective client's information must not
// reach a lawyer who may be acting for the conflicting client.
package crm

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/modules"
	"github.com/discover-legal/biglaw-go/internal/store"
)

// ModuleName is the registry name of the CRM module.
const ModuleName = "crm"

// HTTP is the CRM module's route set (implements modules.HTTPModule).
type HTTP struct{ svc *Service }

// NewHTTP builds the CRM HTTP module.
func NewHTTP(svc *Service) *HTTP { return &HTTP{svc: svc} }

// ModuleName implements modules.HTTPModule.
func (h *HTTP) ModuleName() string { return ModuleName }

// PublicRoute implements modules.HTTPModule: every CRM route requires a firm
// credential.
func (h *HTTP) PublicRoute(method, path string) bool { return false }

// Mount registers the /crm endpoints.
func (h *HTTP) Mount(r gin.IRouter, host modules.Host) {
	g := r.Group("/crm")

	// partner reports whether the caller sees prospect profiles.
	partner := func(c *gin.Context) bool { return auth.IsPartner(host.User(c)) }
	// profileVisible gates every per-profile route: unknown and (for
	// non-partners) prospect profiles answer 404 alike.
	profileVisible := func(c *gin.Context, id string) bool {
		p, ok, err := h.svc.Get(host.Identity(c), id)
		if err != nil {
			RespondErr(c, err)
			return false
		}
		if !ok || (p.ClientID == "" && !partner(c)) {
			c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
			return false
		}
		return true
	}

	g.GET("/profiles", func(c *gin.Context) {
		profiles, err := h.svc.List(host.Identity(c))
		if err != nil {
			RespondErr(c, err)
			return
		}
		if !partner(c) {
			admitted := make([]store.CRMProfile, 0, len(profiles))
			for _, p := range profiles {
				if p.ClientID != "" {
					admitted = append(admitted, p)
				}
			}
			profiles = admitted
		}
		c.JSON(http.StatusOK, gin.H{"profiles": NonNil(profiles)})
	})

	g.GET("/profiles/:id", func(c *gin.Context) {
		if !profileVisible(c, c.Param("id")) {
			return
		}
		view, err := h.svc.View(host.Identity(c), c.Param("id"))
		if err != nil {
			RespondErr(c, err)
			return
		}
		if view == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
			return
		}
		c.JSON(http.StatusOK, view)
	})

	g.GET("/profiles/:id/facts", func(c *gin.Context) {
		if !profileVisible(c, c.Param("id")) {
			return
		}
		facts, err := h.svc.Facts(host.Identity(c), c.Param("id"), c.Query("status"))
		if err != nil {
			RespondErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"facts": NonNil(facts)})
	})

	// A lawyer proposes facts about the client → the client approves them
	// through the portal.
	g.POST("/profiles/:id/facts", func(c *gin.Context) {
		u := host.User(c)
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		if !profileVisible(c, c.Param("id")) {
			return
		}
		var body struct {
			Facts []FactInput `json:"facts"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		proposals, err := h.svc.Propose(host.Identity(c), c.Param("id"),
			Actor{Role: RoleLawyer, ID: u.ProfileID}, "lawyer", body.Facts)
		if err != nil {
			RespondErr(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"proposals": proposals})
	})

	// Semantic profile query (neural layer).
	g.POST("/profiles/:id/query", func(c *gin.Context) {
		if !profileVisible(c, c.Param("id")) {
			return
		}
		var body struct {
			Query string `json:"query"`
			TopK  int    `json:"topK"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		hits, err := h.svc.Query(host.Identity(c), c.Param("id"), body.Query, body.TopK)
		if err != nil {
			RespondErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"results": hits})
	})

	// Firm-wide pending queue. approver=lawyer (default) lists client
	// proposals awaiting the firm; approver=client lists what's out with
	// clients for approval.
	g.GET("/proposals", func(c *gin.Context) {
		ctx := host.Identity(c)
		facts, err := h.svc.PendingQueue(ctx, c.DefaultQuery("approver", RoleLawyer))
		if err != nil {
			RespondErr(c, err)
			return
		}
		if !partner(c) {
			admitted := map[string]bool{}
			visible := make([]store.CRMFact, 0, len(facts))
			for _, f := range facts {
				ok, seen := admitted[f.ProfileID]
				if !seen {
					p, found, err := h.svc.Get(ctx, f.ProfileID)
					ok = err == nil && found && p.ClientID != ""
					admitted[f.ProfileID] = ok
				}
				if ok {
					visible = append(visible, f)
				}
			}
			facts = visible
		}
		c.JSON(http.StatusOK, gin.H{"proposals": NonNil(facts)})
	})

	// A lawyer decides a client-proposed fact.
	g.POST("/proposals/:id/decision", func(c *gin.Context) {
		u := host.User(c)
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		var body struct {
			Decision string `json:"decision"`
			Note     string `json:"note"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		if body.Decision != "approve" && body.Decision != "reject" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "decision must be approve or reject"})
			return
		}
		if f, ok, err := h.svc.repo.GetCRMFact(host.Identity(c), c.Param("id")); err != nil {
			RespondErr(c, err)
			return
		} else if ok && !profileVisible(c, f.ProfileID) {
			return
		}
		fact, err := h.svc.Decide(host.Identity(c), c.Param("id"),
			Actor{Role: RoleLawyer, ID: u.ProfileID}, body.Decision == "approve", body.Note)
		if err != nil {
			RespondErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"fact": fact})
	})
}

// ErrStatus maps CRM errors to HTTP status codes. Anything that is not a
// known request error is an internal (store) failure.
func ErrStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotPending):
		return http.StatusConflict
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// RespondErr writes err with its mapped status. Internal failures are logged
// and answered generically — store/driver messages (table and index names,
// constraint text) never reach a caller.
func RespondErr(c *gin.Context, err error) {
	status := ErrStatus(err)
	if status >= http.StatusInternalServerError {
		slog.Error("crm: request failed", "path", c.FullPath(), "err", err)
		c.JSON(status, gin.H{"error": "internal error"})
		return
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

// NonNil returns in, or an empty slice for nil (JSON [] not null).
func NonNil[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}
