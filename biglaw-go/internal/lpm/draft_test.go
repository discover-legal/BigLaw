// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package lpm

import "testing"

// fakeTransport records calls without touching the network.
type fakeTransport struct {
	drafts int
	sends  int
	err    error
}

func (f *fakeTransport) CreateDraft(Draft) error { f.drafts++; return f.err }
func (f *fakeTransport) Send(Draft) error        { f.sends++; return f.err }

var cleanGuard = NewGuard(GuardConfig{AllowedDomains: []string{"firm.com"}})
var sampleDraft = Draft{MatterNumber: "M-001", To: []string{"a@firm.com"}, Subject: "Update", Body: "All on track."}

func TestDrafterOffSuppresses(t *testing.T) {
	d := NewDrafter("off", cleanGuard, &fakeTransport{}, nil)
	out, err := d.Process(sampleDraft, "actor")
	if err != nil || out.Status != DraftSuppressed {
		t.Fatalf("off mode: %+v err=%v", out, err)
	}
}

func TestDrafterChannelCirculates(t *testing.T) {
	posted := 0
	d := NewDrafter("channel", cleanGuard, nil, func(Draft) error { posted++; return nil })
	out, err := d.Process(sampleDraft, "actor")
	if err != nil || out.Status != DraftCirculated || posted != 1 {
		t.Fatalf("channel mode: %+v err=%v posted=%d", out, err, posted)
	}
}

func TestDrafterDraftSavesToMailbox(t *testing.T) {
	tr := &fakeTransport{}
	d := NewDrafter("draft", cleanGuard, tr, nil)
	out, err := d.Process(sampleDraft, "actor")
	if err != nil || out.Status != DraftSaved || tr.drafts != 1 || tr.sends != 0 {
		t.Fatalf("draft mode: %+v err=%v tr=%+v", out, err, tr)
	}
}

func TestDrafterSendGateNeverAutoSends(t *testing.T) {
	tr := &fakeTransport{}
	d := NewDrafter("send_gate", cleanGuard, tr, nil)
	out, err := d.Process(sampleDraft, "actor")
	if err != nil || out.Status != DraftPendingGate {
		t.Fatalf("send_gate should be pending: %+v err=%v", out, err)
	}
	if tr.sends != 0 {
		t.Fatal("send_gate must NOT auto-send")
	}
	if tr.drafts != 1 {
		t.Fatal("send_gate should save a draft")
	}

	// Explicit human approval performs the send.
	out2, err := d.ApproveSend(sampleDraft, "approver")
	if err != nil || out2.Status != DraftSent || tr.sends != 1 {
		t.Fatalf("approve send: %+v err=%v sends=%d", out2, err, tr.sends)
	}
}

func TestDrafterGuardBlocksLeakyDraft(t *testing.T) {
	tr := &fakeTransport{}
	d := NewDrafter("draft", cleanGuard, tr, nil)
	leaky := Draft{MatterNumber: "M-001", To: []string{"a@firm.com"}, Subject: "x", Body: "SSN 123-45-6789"}
	out, err := d.Process(leaky, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != DraftBlocked {
		t.Errorf("leaky content must be blocked, got %s", out.Status)
	}
	if tr.drafts != 0 {
		t.Error("blocked draft must not reach the transport")
	}
}

func TestDrafterBlocksBadRecipientBeforeTransport(t *testing.T) {
	tr := &fakeTransport{}
	d := NewDrafter("draft", cleanGuard, tr, nil)
	out, _ := d.Process(Draft{MatterNumber: "M-001", To: []string{"x@evil.com"}, Subject: "s", Body: "hi"}, "actor")
	if out.Status != DraftBlocked || tr.drafts != 0 {
		t.Errorf("disallowed recipient must block before transport: %+v tr=%+v", out, tr)
	}
}

func TestNewTransportSelection(t *testing.T) {
	if NewTransport(false, false, "", "") != nil {
		t.Error("no providers → nil transport")
	}
	if _, ok := NewTransport(true, false, "u@firm.com", "").(*graphTransport); !ok {
		t.Error("graph should be selected when enabled")
	}
	if _, ok := NewTransport(false, true, "", "").(*gmailTransport); !ok {
		t.Error("gmail should be selected when only it is enabled")
	}
}
