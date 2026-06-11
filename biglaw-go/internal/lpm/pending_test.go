// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"path/filepath"
	"testing"
)

func newPendingTestService(t *testing.T, mode string) (*Service, *fakeTransport, *PendingStore) {
	t.Helper()
	prov := &fakeProvider{replies: []string{`{"bluf":"b","summary":"s"}`}}
	svc, dir := newTestService(t, prov)
	tr := &fakeTransport{}
	svc.WithDrafting(mode, []string{"firm.com"}, tr, nil)
	ps := NewPendingStore(filepath.Join(dir, "pending.json"))
	if err := ps.Init(); err != nil {
		t.Fatal(err)
	}
	svc.WithPendingDrafts(ps)
	return svc, tr, ps
}

func TestSendGateParksThenApproves(t *testing.T) {
	svc, tr, ps := newPendingTestService(t, "send_gate")
	d := Draft{MatterNumber: "M-001", To: []string{"a@firm.com"}, Subject: "Update", Body: "All good."}

	out, err := svc.ProcessDraft(d, "lawyer")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != DraftPendingGate || out.PendingID == "" {
		t.Fatalf("expected pending with an ID, got %+v", out)
	}
	if tr.sends != 0 {
		t.Fatal("nothing should be sent before approval")
	}
	if len(svc.PendingDrafts()) != 1 {
		t.Fatalf("expected 1 pending draft, got %d", len(svc.PendingDrafts()))
	}

	// Approve by ID → sends and resolves.
	appr, err := svc.ApprovePending(out.PendingID, "partner")
	if err != nil {
		t.Fatal(err)
	}
	if appr.Status != DraftSent || tr.sends != 1 {
		t.Fatalf("approval should send: %+v sends=%d", appr, tr.sends)
	}
	if len(svc.PendingDrafts()) != 0 {
		t.Error("approved draft should no longer be pending")
	}
	if pd, _ := ps.Get(out.PendingID); pd.Status != PendingSent || pd.ResolvedBy != "partner" {
		t.Errorf("pending record not resolved correctly: %+v", pd)
	}

	// Double-approval is rejected.
	if _, err := svc.ApprovePending(out.PendingID, "partner"); err == nil {
		t.Error("re-approving a sent draft should error")
	}
}

func TestSendGateCancel(t *testing.T) {
	svc, tr, _ := newPendingTestService(t, "send_gate")
	out, _ := svc.ProcessDraft(Draft{MatterNumber: "M-001", To: []string{"a@firm.com"}, Subject: "s", Body: "b"}, "lawyer")

	if err := svc.CancelPending(out.PendingID, "partner"); err != nil {
		t.Fatal(err)
	}
	if len(svc.PendingDrafts()) != 0 {
		t.Error("cancelled draft should not be pending")
	}
	if tr.sends != 0 {
		t.Error("cancel must not send")
	}
	if _, err := svc.ApprovePending(out.PendingID, "partner"); err == nil {
		t.Error("approving a cancelled draft should error")
	}
}

func TestPendingResolveUnknownID(t *testing.T) {
	svc, _, _ := newPendingTestService(t, "send_gate")
	if _, err := svc.ApprovePending("nope", "x"); err == nil {
		t.Error("approving an unknown ID should error")
	}
}
