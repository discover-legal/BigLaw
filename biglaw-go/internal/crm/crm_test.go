// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// The bidirectional-consent lifecycle: proposals await the counterparty,
// approvals fire the symbolic rules (supersedence, conflict watch, advocacy
// sync), and the semantic query degrades to lexical scoring without an
// embeddings client.

package crm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/clientvoice"
	"github.com/discover-legal/biglaw-go/internal/store"
)

func newTestService(t *testing.T) (*Service, *clients.ClientStore, *clientvoice.Store) {
	t.Helper()
	dir := t.TempDir()
	cs := clients.NewClientStore()
	if err := cs.Init(filepath.Join(dir, "clients.json")); err != nil {
		t.Fatalf("clients init: %v", err)
	}
	cv := clientvoice.New(filepath.Join(dir, "voice.json"))
	if err := cv.Init(); err != nil {
		t.Fatalf("clientvoice init: %v", err)
	}
	svc := New(store.NewMemoryRepo(), cs, nil) // nil embeddings → lexical fallback
	svc.SetClientVoice(cv)
	return svc, cs, cv
}

func sysCtx() context.Context { return store.WithSystem(context.Background()) }

func TestEnsureProfileCreatesRosterClientAndIsIdempotent(t *testing.T) {
	svc, cs, _ := newTestService(t)
	ctx := sysCtx()

	p1, err := svc.EnsureProfile(ctx, "auth0|abc", "dana@example.com", "Dana Client")
	if err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	if p1.ClientID == "" {
		t.Fatal("profile has no roster client")
	}
	rc := cs.Get(p1.ClientID)
	if rc == nil || rc.Name != "Dana Client" || rc.ClientNumber != "AM-1" {
		t.Fatalf("roster client wrong: %+v", rc)
	}

	p2, err := svc.EnsureProfile(ctx, "auth0|abc", "", "")
	if err != nil {
		t.Fatalf("EnsureProfile (repeat): %v", err)
	}
	if p2.ID != p1.ID {
		t.Fatalf("EnsureProfile not idempotent: %s vs %s", p2.ID, p1.ID)
	}
}

func TestEnsureProfileAttachesToExistingRosterClientByName(t *testing.T) {
	svc, cs, _ := newTestService(t)
	if _, err := cs.Create("Acme, Inc.", "C-100", nil, ""); err != nil {
		t.Fatalf("roster create: %v", err)
	}
	p, err := svc.EnsureProfile(sysCtx(), "auth0|acme", "ops@acme.com", "acme inc")
	if err != nil {
		t.Fatalf("EnsureProfile: %v", err)
	}
	rc := cs.Get(p.ClientID)
	if rc == nil || rc.ClientNumber != "C-100" {
		t.Fatalf("expected attach to existing roster client, got %+v", rc)
	}
}

func TestConsentLifecycleClientProposesLawyerDecides(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := sysCtx()
	p, _ := svc.EnsureProfile(ctx, "auth0|abc", "dana@example.com", "Dana Client")

	props, err := svc.Propose(ctx, p.ID, Actor{Role: RoleClient, ID: "auth0|abc"}, "intake",
		[]FactInput{{Category: "goal", Predicate: "custody", Value: "Primary custody of both children"}})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	f := props[0]
	if f.ApproverRole != RoleLawyer || f.Status != StatusPending {
		t.Fatalf("client proposal must await a lawyer: %+v", f)
	}

	// The client cannot approve their own proposal.
	if _, err := svc.Decide(ctx, f.ID, Actor{Role: RoleClient, ID: "auth0|abc"}, true, ""); err == nil {
		t.Fatal("client approved a lawyer-gated proposal")
	}

	got, err := svc.Decide(ctx, f.ID, Actor{Role: RoleLawyer, ID: "lawyer-1"}, true, "confirmed on call")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Status != StatusApproved || got.DecidedBy != "lawyer:lawyer-1" {
		t.Fatalf("approval state wrong: %+v", got)
	}

	// Re-deciding a settled fact conflicts.
	if _, err := svc.Decide(ctx, f.ID, Actor{Role: RoleLawyer, ID: "lawyer-1"}, false, ""); err == nil {
		t.Fatal("re-decided a settled fact")
	}

	view, err := svc.View(ctx, p.ID)
	if err != nil || view == nil {
		t.Fatalf("View: %v", err)
	}
	if len(view.Facts) != 1 || len(view.PendingLawyerApproval) != 0 {
		t.Fatalf("view wrong: %+v", view)
	}
}

func TestConsentLifecycleLawyerProposesClientDecides(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := sysCtx()
	p, _ := svc.EnsureProfile(ctx, "auth0|abc", "dana@example.com", "Dana Client")

	props, err := svc.Propose(ctx, p.ID, Actor{Role: RoleLawyer, ID: "lawyer-1"}, "lawyer",
		[]FactInput{{Category: "contact", Predicate: "phone", Value: "+1 555 0100"}})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	f := props[0]
	if f.ApproverRole != RoleClient {
		t.Fatalf("lawyer proposal must await the client: %+v", f)
	}

	// Another client cannot decide it.
	if _, err := svc.Decide(ctx, f.ID, Actor{Role: RoleClient, ID: "auth0|other"}, true, ""); err == nil {
		t.Fatal("foreign client decided a proposal")
	}
	// A lawyer cannot short-circuit client consent.
	if _, err := svc.Decide(ctx, f.ID, Actor{Role: RoleLawyer, ID: "lawyer-2"}, true, ""); err == nil {
		t.Fatal("lawyer bypassed client consent")
	}

	got, err := svc.Decide(ctx, f.ID, Actor{Role: RoleClient, ID: "auth0|abc"}, false, "wrong number")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Status != StatusRejected || got.DecisionNote != "wrong number" {
		t.Fatalf("rejection state wrong: %+v", got)
	}
}

func TestSupersedenceOnApproval(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := sysCtx()
	p, _ := svc.EnsureProfile(ctx, "auth0|abc", "", "Dana Client")
	lawyer := Actor{Role: RoleLawyer, ID: "lawyer-1"}
	client := Actor{Role: RoleClient, ID: "auth0|abc"}

	first, _ := svc.Propose(ctx, p.ID, client, "client",
		[]FactInput{{Category: "contact", Predicate: "phone", Value: "+1 555 0100"}})
	if _, err := svc.Decide(ctx, first[0].ID, lawyer, true, ""); err != nil {
		t.Fatalf("approve first: %v", err)
	}

	second, _ := svc.Propose(ctx, p.ID, client, "client",
		[]FactInput{{Category: "contact", Predicate: "phone", Value: "+1 555 0199"}})
	if _, err := svc.Decide(ctx, second[0].ID, lawyer, true, ""); err != nil {
		t.Fatalf("approve second: %v", err)
	}

	approved, _ := svc.Facts(ctx, p.ID, StatusApproved)
	if len(approved) != 1 || approved[0].Value != "+1 555 0199" {
		t.Fatalf("supersedence failed; approved: %+v", approved)
	}
	old, _, _ := svc.repo.GetCRMFact(ctx, first[0].ID)
	if old.Status != StatusSuperseded || old.SupersededBy != second[0].ID {
		t.Fatalf("old fact not superseded: %+v", old)
	}
}

func TestAdversePartyConflictFlagsProfile(t *testing.T) {
	svc, cs, _ := newTestService(t)
	ctx := sysCtx()
	if _, err := cs.Create("MegaCorp Industries", "C-200", nil, ""); err != nil {
		t.Fatalf("roster create: %v", err)
	}
	p, _ := svc.EnsureProfile(ctx, "auth0|abc", "", "Dana Client")

	props, _ := svc.Propose(ctx, p.ID, Actor{Role: RoleClient, ID: "auth0|abc"}, "client",
		[]FactInput{{Category: "adverse_party", Predicate: "opposing", Value: "MegaCorp Industries"}})
	if _, err := svc.Decide(ctx, props[0].ID, Actor{Role: RoleLawyer, ID: "lawyer-1"}, true, ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	got, _, _ := svc.Get(ctx, p.ID)
	if !got.ConflictFlag {
		t.Fatal("adverse-party hit against an existing client did not flag the profile")
	}
}

func TestAdvocacySyncCompilesClientVoice(t *testing.T) {
	svc, cs, cv := newTestService(t)
	ctx := sysCtx()
	p, _ := svc.EnsureProfile(ctx, "auth0|abc", "", "Dana Client")
	if _, err := cs.AddMatter(p.ClientID, "AM-1-1", "Divorce", "Family"); err != nil {
		t.Fatalf("AddMatter: %v", err)
	}

	props, _ := svc.Propose(ctx, p.ID, Actor{Role: RoleClient, ID: "auth0|abc"}, "intake",
		[]FactInput{
			{Category: "goal", Value: "Keep the family home"},
			{Category: "concern", Value: "Cost of prolonged litigation"},
		})
	for _, f := range props {
		if _, err := svc.Decide(ctx, f.ID, Actor{Role: RoleLawyer, ID: "lawyer-1"}, true, ""); err != nil {
			t.Fatalf("Decide: %v", err)
		}
	}

	voice := cv.Voice("AM-1-1")
	if voice == nil {
		t.Fatal("no ClientVoice brief compiled for the matter")
	}
	if voice.Source != "crm" || len(voice.Entries) != 2 {
		t.Fatalf("brief wrong: %+v", voice)
	}
}

func TestQueryLexicalFallback(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := sysCtx()
	p, _ := svc.EnsureProfile(ctx, "auth0|abc", "", "Dana Client")
	lawyer := Actor{Role: RoleLawyer, ID: "lawyer-1"}
	client := Actor{Role: RoleClient, ID: "auth0|abc"}

	props, _ := svc.Propose(ctx, p.ID, client, "client", []FactInput{
		{Category: "goal", Predicate: "custody", Value: "Primary custody of both children"},
		{Category: "financial", Predicate: "income", Value: "Annual salary 95000"},
	})
	for _, f := range props {
		if _, err := svc.Decide(ctx, f.ID, lawyer, true, ""); err != nil {
			t.Fatalf("Decide: %v", err)
		}
	}

	hits, err := svc.Query(ctx, p.ID, "custody children", 5)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(hits) == 0 || hits[0].Fact.Predicate != "custody" {
		t.Fatalf("lexical query missed: %+v", hits)
	}
}

func TestUnknownCategoryCoercesToNote(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := sysCtx()
	p, _ := svc.EnsureProfile(ctx, "auth0|abc", "", "Dana Client")
	props, err := svc.Propose(ctx, p.ID, Actor{Role: RoleClient, ID: "auth0|abc"}, "client",
		[]FactInput{{Category: "SELECT * FROM users", Value: "something"}})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if props[0].Category != "note" {
		t.Fatalf("unknown category not coerced: %q", props[0].Category)
	}
}
