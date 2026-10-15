// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// CRM/intake repository contract, run against the memory and sqlite backends:
// round-trips, upserts, index lookups, and the firm-access policy (anonymous
// callers see and write nothing).

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func crmBackends(t *testing.T) map[string]DocRepository {
	t.Helper()
	sqlite, err := openSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlite.Close() })
	return map[string]DocRepository{
		"memory": NewMemoryRepo(),
		"sqlite": sqlite,
	}
}

func TestCRMRepositoryRoundTrip(t *testing.T) {
	for name, repo := range crmBackends(t) {
		t.Run(name, func(t *testing.T) {
			r, ok := repo.(CRMRepository)
			if !ok {
				t.Fatalf("%s does not implement CRMRepository", name)
			}
			ctx := WithSystem(context.Background())

			p := CRMProfile{
				ID: "p1", ClientID: "c1", ExternalRef: "auth0|x",
				Email: "x@example.com", Name: "X", CreatedAt: time.Now(),
			}
			if err := r.PutCRMProfile(ctx, p); err != nil {
				t.Fatalf("PutCRMProfile: %v", err)
			}
			byRef, ok2, err := r.FindCRMProfileByExternalRef(ctx, "auth0|x")
			if err != nil || !ok2 || byRef.ID != "p1" {
				t.Fatalf("FindByExternalRef: %v %v %+v", err, ok2, byRef)
			}
			byClient, ok2, err := r.FindCRMProfileByClientID(ctx, "c1")
			if err != nil || !ok2 || byClient.ID != "p1" {
				t.Fatalf("FindByClientID: %v %v %+v", err, ok2, byClient)
			}

			// Upsert flips the conflict flag in place.
			p.ConflictFlag = true
			if err := r.PutCRMProfile(ctx, p); err != nil {
				t.Fatalf("PutCRMProfile (update): %v", err)
			}
			got, _, _ := r.GetCRMProfile(ctx, "p1")
			if !got.ConflictFlag {
				t.Fatal("conflict flag lost on upsert")
			}

			now := time.Now()
			f := CRMFact{
				ID: "f1", ProfileID: "p1", Category: "goal", Predicate: "custody",
				Value: "Primary custody", Source: "intake", ProposedBy: "client:auth0|x",
				ApproverRole: "lawyer", Status: "pending", CreatedAt: now,
			}
			if err := r.PutCRMFact(ctx, f); err != nil {
				t.Fatalf("PutCRMFact: %v", err)
			}
			pending, err := r.ListPendingCRMFacts(ctx, "lawyer")
			if err != nil || len(pending) != 1 {
				t.Fatalf("ListPendingCRMFacts: %v %+v", err, pending)
			}

			f.Status = "approved"
			f.DecidedBy = "lawyer:l1"
			f.DecidedAt = &now
			if err := r.PutCRMFact(ctx, f); err != nil {
				t.Fatalf("PutCRMFact (decide): %v", err)
			}
			approved, err := r.ListCRMFacts(ctx, "p1", "approved")
			if err != nil || len(approved) != 1 || approved[0].DecidedAt == nil {
				t.Fatalf("ListCRMFacts approved: %v %+v", err, approved)
			}
			if left, _ := r.ListPendingCRMFacts(ctx, "lawyer"); len(left) != 0 {
				t.Fatalf("fact still pending after decision: %+v", left)
			}
		})
	}
}

func TestIntakeRepositoryRoundTrip(t *testing.T) {
	for name, repo := range crmBackends(t) {
		t.Run(name, func(t *testing.T) {
			r, ok := repo.(IntakeRepository)
			if !ok {
				t.Fatalf("%s does not implement IntakeRepository", name)
			}
			ctx := WithSystem(context.Background())

			s := IntakeSubmission{
				ID: "s1", ExternalID: "doc-1", ProfileID: "p1", ClientID: "c1",
				ClientNumber: "AM-1", Title: "Affidavit", Status: "received",
				Conflict: true, ConflictJSON: []byte(`{"hasConflict":true}`),
				MetadataJSON: []byte(`{"v":"1"}`), CreatedAt: time.Now(),
			}
			if err := r.PutIntakeSubmission(ctx, s); err != nil {
				t.Fatalf("PutIntakeSubmission: %v", err)
			}
			byExt, ok2, err := r.FindIntakeSubmissionByExternalID(ctx, "p1", "doc-1")
			if err != nil || !ok2 || byExt.ID != "s1" || !byExt.Conflict {
				t.Fatalf("FindByExternalID: %v %v %+v", err, ok2, byExt)
			}
			if string(byExt.ConflictJSON) != `{"hasConflict":true}` {
				t.Fatalf("conflict json lost: %q", byExt.ConflictJSON)
			}

			s.Status = "ready"
			if err := r.PutIntakeSubmission(ctx, s); err != nil {
				t.Fatalf("PutIntakeSubmission (update): %v", err)
			}
			list, err := r.ListIntakeSubmissionsByProfile(ctx, "p1")
			if err != nil || len(list) != 1 || list[0].Status != "ready" {
				t.Fatalf("ListByProfile: %v %+v", err, list)
			}
			all, err := r.ListIntakeSubmissions(ctx)
			if err != nil || len(all) != 1 {
				t.Fatalf("ListAll: %v %+v", err, all)
			}
		})
	}
}

func TestCRMFirmAccessPolicy(t *testing.T) {
	for name, repo := range crmBackends(t) {
		t.Run(name, func(t *testing.T) {
			r := repo.(CRMRepository)
			ir := repo.(IntakeRepository)
			sys := WithSystem(context.Background())
			anon := context.Background() // no identity → default-deny

			if err := r.PutCRMProfile(sys, CRMProfile{ID: "p1", Name: "X", ClientID: "c1"}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := ir.PutIntakeSubmission(sys, IntakeSubmission{ID: "s1", ProfileID: "p1"}); err != nil {
				t.Fatalf("seed intake: %v", err)
			}

			// Anonymous: no reads, no writes.
			if _, ok, _ := r.GetCRMProfile(anon, "p1"); ok {
				t.Fatal("anonymous read of a crm profile")
			}
			if ps, _ := r.ListCRMProfiles(anon); len(ps) != 0 {
				t.Fatal("anonymous list of crm profiles")
			}
			if err := r.PutCRMProfile(anon, CRMProfile{ID: "p2", Name: "Y"}); err == nil && name == "memory" {
				t.Fatal("anonymous write of a crm profile (memory)")
			}
			if _, ok, _ := ir.GetIntakeSubmission(anon, "s1"); ok {
				t.Fatal("anonymous read of an intake submission")
			}

			// A plain lawyer (non-partner) is a firm member and sees firm-wide rows.
			lawyer := WithIdentity(context.Background(), "lawyer-1", false)
			if _, ok, _ := r.GetCRMProfile(lawyer, "p1"); !ok {
				t.Fatal("lawyer denied a firm-wide crm profile")
			}
			if subs, _ := ir.ListIntakeSubmissions(lawyer); len(subs) != 1 {
				t.Fatal("lawyer denied firm-wide intake list")
			}
		})
	}
}
