// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Outbound drafter. Turns the email-write-mode knob into behaviour, with the
// confidentiality guard enforced on every path:
//
//	off        nothing leaves; the draft is suppressed
//	channel    the draft is posted to the matter channel for human comment
//	draft      the draft is saved (unsent) into the mailbox for a human to send
//	send_gate  the draft is saved AND flagged pending — it is never auto-sent;
//	           an explicit ApproveSend (human approval) is required to send
//
// The guard runs before any transport or channel call, and again at send time
// (fail closed). Every outcome is written to the audit log.
package lpm

import (
	"fmt"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/audit"
)

// Draft is an outbound message scoped to a matter.
type Draft struct {
	MatterNumber string
	To           []string
	Subject      string
	Body         string
}

// DraftStatus is the result of processing a draft.
type DraftStatus string

const (
	DraftSuppressed  DraftStatus = "suppressed"   // mode off
	DraftBlocked     DraftStatus = "blocked"      // guard refused
	DraftCirculated  DraftStatus = "circulated"   // posted to channel
	DraftSaved       DraftStatus = "drafted"      // saved as an unsent mailbox draft
	DraftPendingGate DraftStatus = "pending_gate" // saved, awaiting human approval to send
	DraftSent        DraftStatus = "sent"         // sent after approval
)

// DraftOutcome reports what happened to a draft.
type DraftOutcome struct {
	Status    DraftStatus   `json:"status"`
	Decision  GuardDecision `json:"decision"`
	Detail    string        `json:"detail,omitempty"`
	PendingID string        `json:"pendingId,omitempty"` // set when a draft is parked for approval
}

// MailTransport creates/sends mail. Implementations live in transport.go.
type MailTransport interface {
	CreateDraft(d Draft) error
	Send(d Draft) error
}

// ChannelPoster posts a draft into a matter's chat channel for comment.
type ChannelPoster func(d Draft) error

// Drafter applies the email-write-mode policy to outbound drafts.
type Drafter struct {
	mode      string
	guard     *Guard
	transport MailTransport
	channel   ChannelPoster
}

// NewDrafter builds a Drafter. guard must be non-nil; transport/channel may be
// nil if the corresponding mode is unused.
func NewDrafter(mode string, guard *Guard, transport MailTransport, channel ChannelPoster) *Drafter {
	if mode == "" {
		mode = "off"
	}
	return &Drafter{mode: mode, guard: guard, transport: transport, channel: channel}
}

// Process applies the configured write mode to a draft.
func (d *Drafter) Process(draft Draft, actorID string) (DraftOutcome, error) {
	if d.mode == "off" {
		audit.Default.Write(audit.WriteRequest{
			Event: "lpm_draft", ActorID: actorID,
			Data: map[string]interface{}{"matterNumber": draft.MatterNumber, "status": DraftSuppressed, "mode": d.mode},
		})
		return DraftOutcome{Status: DraftSuppressed}, nil
	}

	dec := d.guard.Check(draft.MatterNumber, draft.To, draft.Subject+"\n"+draft.Body)
	d.guard.Audit(actorID, draft.MatterNumber, d.mode, dec)
	if !dec.Allowed {
		return DraftOutcome{Status: DraftBlocked, Decision: dec, Detail: strings.Join(dec.Reasons, "; ")}, nil
	}

	var out DraftOutcome
	out.Decision = dec
	switch d.mode {
	case "channel":
		if d.channel == nil {
			return out, fmt.Errorf("channel write-mode but no channel poster configured")
		}
		if err := d.channel(draft); err != nil {
			return out, err
		}
		out.Status = DraftCirculated
	case "draft":
		if d.transport == nil {
			return out, fmt.Errorf("draft write-mode but no mail transport configured")
		}
		if err := d.transport.CreateDraft(draft); err != nil {
			return out, err
		}
		out.Status = DraftSaved
	case "send_gate":
		if d.transport == nil {
			return out, fmt.Errorf("send_gate write-mode but no mail transport configured")
		}
		// Save the draft but never auto-send — a human must call ApproveSend.
		if err := d.transport.CreateDraft(draft); err != nil {
			return out, err
		}
		out.Status = DraftPendingGate
	default:
		return out, fmt.Errorf("unknown email write mode %q", d.mode)
	}

	audit.Default.Write(audit.WriteRequest{
		Event: "lpm_draft", ActorID: actorID,
		Data: map[string]interface{}{"matterNumber": draft.MatterNumber, "status": out.Status, "mode": d.mode, "recipients": len(draft.To)},
	})
	return out, nil
}

// ApproveSend performs the actual send after an explicit human approval. The
// guard is re-run (fail closed) immediately before the transport is touched.
func (d *Drafter) ApproveSend(draft Draft, approverID string) (DraftOutcome, error) {
	if d.transport == nil {
		return DraftOutcome{}, fmt.Errorf("no mail transport configured")
	}
	dec := d.guard.Check(draft.MatterNumber, draft.To, draft.Subject+"\n"+draft.Body)
	d.guard.Audit(approverID, draft.MatterNumber, "send", dec)
	if !dec.Allowed {
		return DraftOutcome{Status: DraftBlocked, Decision: dec, Detail: strings.Join(dec.Reasons, "; ")}, nil
	}
	if err := d.transport.Send(draft); err != nil {
		return DraftOutcome{Decision: dec}, err
	}
	audit.Default.Write(audit.WriteRequest{
		Event: "lpm_draft_sent", ActorID: approverID,
		Data: map[string]interface{}{"matterNumber": draft.MatterNumber, "recipients": len(draft.To)},
	})
	return DraftOutcome{Status: DraftSent, Decision: dec}, nil
}
