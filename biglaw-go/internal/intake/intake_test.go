// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Intake end-to-end over the in-memory store: a portal submission creates the
// roster client + CRM profile + matter, ingests the draft, seeds consent-gated
// fact proposals, is idempotent on the portal document id, and walks the
// lawyer state machine (claim → update → terminal statuses survive re-submits).

package intake

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/crm"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/store"
)

func newTestIntake(t *testing.T) (*Service, *crm.Service, *clients.ClientStore, *store.MemoryRepo) {
	t.Helper()
	repo := store.NewMemoryRepo()
	cs := clients.NewClientStore()
	if err := cs.Init(filepath.Join(t.TempDir(), "clients.json")); err != nil {
		t.Fatalf("clients init: %v", err)
	}
	// Fake local embeddings endpoint (no network, no models).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"embedding": []float32{0.1, 0.2, 0.3}})
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.Local.LocalEmbeddings = true
	cfg.Local.OllamaURL = srv.URL
	ks := knowledge.NewStoreWithRepo(embeddings.NewClient(cfg), repo)

	crmSvc := crm.New(repo, cs, nil)
	svc := New(repo, crmSvc, cs, ks)
	return svc, crmSvc, cs, repo
}

func sysCtx() context.Context { return store.WithSystem(context.Background()) }

func baseRequest() SubmissionRequest {
	return SubmissionRequest{
		ExternalID:   "am-doc-42",
		Client:       ClientRef{ExternalID: "auth0|dana", Email: "dana@example.com", Name: "Dana Client"},
		Title:        "Affidavit of Dana Client",
		DocumentType: "affidavit",
		MatterType:   "DIVORCE",
		Jurisdiction: "US-CA",
		Summary:      "Client seeks dissolution",
		Content:      "I, Dana Client, declare under penalty of perjury...",
		Facts: []crm.FactInput{
			{Category: "goal", Predicate: "custody", Value: "Primary custody"},
		},
		Metadata: map[string]interface{}{"portalVersion": "5.0.0"},
	}
}

func TestSubmitLandsEverything(t *testing.T) {
	svc, crmSvc, cs, _ := newTestIntake(t)
	ctx := sysCtx()

	sub, err := svc.Submit(ctx, baseRequest())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if sub.Status != StatusReceived {
		t.Fatalf("status = %q, want received", sub.Status)
	}
	if sub.ClientNumber != "AM-1" || sub.ProfileID == "" || sub.DocumentID == "" {
		t.Fatalf("submission incomplete: %+v", sub)
	}
	if sub.MatterNumber == "" {
		t.Fatal("no matter opened")
	}
	rc := cs.Get(sub.ClientID)
	if rc == nil || len(rc.Matters) != 1 {
		t.Fatalf("roster matter missing: %+v", rc)
	}

	// The client's stated facts became lawyer-gated proposals.
	pending, err := crmSvc.PendingQueue(ctx, crm.RoleLawyer)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending queue: %v %+v", err, pending)
	}
	if pending[0].Source != "intake" {
		t.Fatalf("proposal provenance wrong: %+v", pending[0])
	}
}

func TestSubmitIsIdempotentPerExternalID(t *testing.T) {
	svc, _, _, _ := newTestIntake(t)
	ctx := sysCtx()

	first, err := svc.Submit(ctx, baseRequest())
	if err != nil {
		t.Fatalf("Submit 1: %v", err)
	}
	req := baseRequest()
	req.Content = "Revised draft text."
	req.Facts = nil
	second, err := svc.Submit(ctx, req)
	if err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-submit duplicated: %s vs %s", second.ID, first.ID)
	}
	if second.DocumentID != first.DocumentID {
		t.Fatalf("re-submit should re-ingest under the same document id")
	}
	if second.MatterNumber != first.MatterNumber {
		t.Fatalf("re-submit opened a second matter")
	}

	subs, _ := svc.ListForClient(ctx, "auth0|dana")
	if len(subs) != 1 {
		t.Fatalf("expected 1 submission, got %d", len(subs))
	}
}

func TestConflictHoldOnAdverseClient(t *testing.T) {
	svc, _, cs, _ := newTestIntake(t)
	ctx := sysCtx()
	// An existing client lists Dana as an adversary.
	if _, err := cs.Create("MegaCorp Industries", "C-1", []string{"Dana Client"}, ""); err != nil {
		t.Fatalf("roster create: %v", err)
	}
	sub, err := svc.Submit(ctx, baseRequest())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if sub.Status != StatusConflictHold || !sub.Conflict {
		t.Fatalf("expected conflict_hold, got %+v", sub)
	}
}

func TestLawyerStateMachine(t *testing.T) {
	svc, _, _, _ := newTestIntake(t)
	ctx := sysCtx()
	sub, _ := svc.Submit(ctx, baseRequest())

	claimed, err := svc.Claim(ctx, sub.ID, "lawyer-1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.AssignedTo != "lawyer-1" || claimed.Status != StatusInReview {
		t.Fatalf("claim state wrong: %+v", claimed)
	}

	updated, err := svc.Update(ctx, sub.ID, StatusReady, "Reviewed; call to discuss.", "lawyer-1")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != StatusReady || updated.Note == "" {
		t.Fatalf("update state wrong: %+v", updated)
	}

	// A terminal status survives a portal re-submit.
	resub, err := svc.Submit(ctx, baseRequest())
	if err != nil {
		t.Fatalf("re-submit: %v", err)
	}
	if resub.Status != StatusReady {
		t.Fatalf("terminal status reset on re-submit: %q", resub.Status)
	}

	if _, err := svc.Update(ctx, sub.ID, "bogus", "", "lawyer-1"); err == nil {
		t.Fatal("invalid status accepted")
	}
	if _, err := svc.Claim(ctx, "nope", "lawyer-1"); err == nil {
		t.Fatal("claimed unknown submission")
	}
}

func TestSubmitValidation(t *testing.T) {
	svc, _, _, _ := newTestIntake(t)
	ctx := sysCtx()

	for name, mutate := range map[string]func(*SubmissionRequest){
		"missing externalId":        func(r *SubmissionRequest) { r.ExternalID = "" },
		"missing client externalId": func(r *SubmissionRequest) { r.Client.ExternalID = "" },
		"missing content":           func(r *SubmissionRequest) { r.Content = "  " },
	} {
		req := baseRequest()
		mutate(&req)
		if _, err := svc.Submit(ctx, req); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestQueueScoping(t *testing.T) {
	svc, _, _, _ := newTestIntake(t)
	ctx := sysCtx()
	a, _ := svc.Submit(ctx, baseRequest())
	req := baseRequest()
	req.ExternalID = "am-doc-43"
	b, _ := svc.Submit(ctx, req)

	if _, err := svc.Claim(ctx, a.ID, "lawyer-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// Partner scope ("") sees both.
	all, _ := svc.Queue(ctx, "")
	if len(all) != 2 {
		t.Fatalf("partner queue = %d, want 2", len(all))
	}
	// Another lawyer sees only the unassigned one.
	mine, _ := svc.Queue(ctx, "lawyer-2")
	if len(mine) != 1 || mine[0].ID != b.ID {
		t.Fatalf("lawyer-2 queue wrong: %+v", mine)
	}
	// The assignee sees their own plus unassigned.
	assignee, _ := svc.Queue(ctx, "lawyer-1")
	if len(assignee) != 2 {
		t.Fatalf("assignee queue = %d, want 2", len(assignee))
	}
}
