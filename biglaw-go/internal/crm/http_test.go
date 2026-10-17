// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package crm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/types"
)

type fakeHost struct{}

func (fakeHost) User(c *gin.Context) *types.SessionUser {
	if c.GetHeader("X-Test-User") == "partner" {
		u := auth.LocalPartner
		return &u
	}
	return &types.SessionUser{ProfileID: "lawyer-1", Role: types.LawyerRole("associate")}
}
func (h fakeHost) Identity(c *gin.Context) context.Context {
	return store.WithSystem(c.Request.Context())
}
func (fakeHost) ProfileName(string) string { return "" }

// Prospect profiles (not yet admitted — e.g. held on a conflict) are
// partner-only; admitted clients' profiles are firm-wide.
func TestProspectProfilesArePartnerOnly(t *testing.T) {
	svc, cs, _ := newTestService(t)
	ctx := sysCtx()
	prospect, _ := svc.EnsureProfile(ctx, "auth0|pat", "", "Pat Prospect")
	client, _ := svc.EnsureProfile(ctx, "auth0|dana", "", "Dana Client")
	rc, _ := cs.Create("Dana Client", "C-1", nil, "")
	if _, err := svc.LinkClient(ctx, client.ID, rc.ID); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewHTTP(svc).Mount(r, fakeHost{})
	get := func(user, path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Test-User", user)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if _, body := get("lawyer", "/crm/profiles"); strings.Contains(body, "Pat Prospect") || !strings.Contains(body, "Dana Client") {
		t.Fatalf("lawyer profile list: %s", body)
	}
	if code, _ := get("lawyer", "/crm/profiles/"+prospect.ID); code != http.StatusNotFound {
		t.Fatalf("lawyer read a prospect: %d", code)
	}
	if code, _ := get("lawyer", "/crm/profiles/"+prospect.ID+"/facts"); code != http.StatusNotFound {
		t.Fatalf("lawyer read a prospect's facts: %d", code)
	}
	if _, body := get("partner", "/crm/profiles"); !strings.Contains(body, "Pat Prospect") {
		t.Fatalf("partner profile list: %s", body)
	}
	if code, _ := get("partner", "/crm/profiles/"+prospect.ID); code != http.StatusOK {
		t.Fatalf("partner read a prospect: %d", code)
	}
}

// racingRepo simulates losing the first-contact unique-index race: the
// insert fails because a concurrent request created the row first.
type racingRepo struct {
	*store.MemoryRepo
	raced bool
}

func (r *racingRepo) PutCRMProfile(ctx context.Context, p store.CRMProfile) error {
	if !r.raced {
		r.raced = true
		winner := p
		winner.ID = "winner"
		_ = r.MemoryRepo.PutCRMProfile(ctx, winner)
		return errors.New("UNIQUE constraint failed: crm_profiles.external_ref")
	}
	return r.MemoryRepo.PutCRMProfile(ctx, p)
}

func TestEnsureProfileIdempotentOnUniqueRace(t *testing.T) {
	svc := New(&racingRepo{MemoryRepo: store.NewMemoryRepo()}, nil, nil)
	p, err := svc.EnsureProfile(sysCtx(), "auth0|dbl", "", "Dee Double")
	if err != nil || p.ID != "winner" {
		t.Fatalf("lost race not resolved to the winning row: %v %+v", err, p)
	}
}

// An email-only first contact is upgraded to the real name supplied later;
// a real name is never overwritten.
func TestEnsureProfileUpgradesEmailName(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := sysCtx()
	p, _ := svc.EnsureProfile(ctx, "auth0|al", "al@x.com", "")
	if p.Name != "al@x.com" {
		t.Fatalf("name = %q", p.Name)
	}
	p, _ = svc.EnsureProfile(ctx, "auth0|al", "al@x.com", "Alice Jones")
	if p.Name != "Alice Jones" {
		t.Fatalf("email name not upgraded: %q", p.Name)
	}
	p, _ = svc.EnsureProfile(ctx, "auth0|al", "", "Someone Else")
	if p.Name != "Alice Jones" {
		t.Fatalf("real name overwritten: %q", p.Name)
	}
}
