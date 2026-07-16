// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Coverage gap this file targets: handleUpdateProfile (server.go) has no
// direct test. It is one of the few handlers with authorization logic
// beyond a flat requirePartner() gate — self-vs-other AND role-stripping
// in the same function (server.go ~908-936) — so a regression could let a
// lawyer either edit someone else's profile or silently escalate their own
// role via a same-profile PATCH.

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// newProfileUpdateRouter wires only PATCH /profiles/:id onto a minimal
// Server and injects `caller` directly into the gin context, bypassing
// authMiddleware — the same shortcut newReviewJSONRouter/newTimelineRouter
// use elsewhere in this package for handlers with no other dependencies.
func newProfileUpdateRouter(t *testing.T, caller *types.SessionUser) (*gin.Engine, *auth.ProfileStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Persistence.ProfilesFile = t.TempDir() + "/profiles.json"
	profiles := auth.NewProfileStore(cfg)
	if err := profiles.Init(); err != nil {
		t.Fatalf("profiles.Init: %v", err)
	}
	s := &Server{cfg: cfg, profiles: profiles}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxUserKey, caller)
		c.Next()
	})
	r.PATCH("/profiles/:id", s.handleUpdateProfile)
	return r, profiles
}

func patchProfile(t *testing.T, r *gin.Engine, id string, patch map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/profiles/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHandleUpdateProfileAccessControl(t *testing.T) {
	t.Run("lawyer editing another profile is forbidden", func(t *testing.T) {
		caller := &types.SessionUser{ProfileID: "caller-id", Role: types.RoleLawyer}
		r, profiles := newProfileUpdateRouter(t, caller)
		target, err := profiles.Create(auth.CreateProfileInput{Name: "Target", Email: "target@example.com", Role: "lawyer"})
		if err != nil {
			t.Fatal(err)
		}

		w := patchProfile(t, r, target.ID, map[string]interface{}{"name": "Hacked"})
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403 (body: %s)", w.Code, w.Body.String())
		}
		if fresh := profiles.Get(target.ID); fresh.Name != "Target" {
			t.Errorf("target profile was mutated despite the 403: name = %q", fresh.Name)
		}
	})

	t.Run("lawyer editing own profile succeeds but role is stripped from the patch", func(t *testing.T) {
		// Seed the profile first so the caller's ProfileID can match it —
		// mirrors how a real session user always corresponds to an existing
		// profile row.
		seedCfg := &config.Config{}
		seedCfg.Persistence.ProfilesFile = t.TempDir() + "/profiles.json"
		seedStore := auth.NewProfileStore(seedCfg)
		if err := seedStore.Init(); err != nil {
			t.Fatal(err)
		}
		me, err := seedStore.Create(auth.CreateProfileInput{Name: "Me", Email: "me@example.com", Role: "lawyer"})
		if err != nil {
			t.Fatal(err)
		}

		caller := &types.SessionUser{ProfileID: me.ID, Role: types.RoleLawyer}
		s := &Server{cfg: seedCfg, profiles: seedStore}
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set(ctxUserKey, caller); c.Next() })
		r.PATCH("/profiles/:id", s.handleUpdateProfile)

		w := patchProfile(t, r, me.ID, map[string]interface{}{"name": "New Name", "role": "partner"})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
		}
		var got types.LawyerProfile
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Name != "New Name" {
			t.Errorf("name = %q, want %q", got.Name, "New Name")
		}
		if got.Role != types.RoleLawyer {
			t.Errorf("role = %q, want unchanged %q — self-service role escalation must be stripped, not honored",
				got.Role, types.RoleLawyer)
		}
	})

	t.Run("partner may change another profile's role", func(t *testing.T) {
		caller := &types.SessionUser{ProfileID: "partner-id", Role: types.RolePartner}
		r, profiles := newProfileUpdateRouter(t, caller)
		target, err := profiles.Create(auth.CreateProfileInput{Name: "Target", Email: "target2@example.com", Role: "lawyer"})
		if err != nil {
			t.Fatal(err)
		}

		w := patchProfile(t, r, target.ID, map[string]interface{}{"role": "partner"})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
		}
		var got types.LawyerProfile
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Role != types.RolePartner {
			t.Errorf("role = %q, want %q — a partner-initiated role change must be honored", got.Role, types.RolePartner)
		}
	})

	t.Run("unknown target profile id is 404, even for a partner caller", func(t *testing.T) {
		caller := &types.SessionUser{ProfileID: "partner-id", Role: types.RolePartner}
		r, _ := newProfileUpdateRouter(t, caller)
		w := patchProfile(t, r, "no-such-profile", map[string]interface{}{"name": "X"})
		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404 (body: %s)", w.Code, w.Body.String())
		}
	})
}
