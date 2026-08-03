// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// CRM + intake persistence: the neurosymbolic client-profile records (typed
// facts under bidirectional consent) and the affidavit-maker intake
// submissions. Both are firm-wide artifacts — any authenticated firm member
// may read them (see CanAccessFirm); the API layer applies finer-grained
// rules (who may decide which proposal).
package store

import (
	"context"
	"time"
)

// CRMProfile is the head record of one client's CRM profile. Facts hang off
// it; the roster client (internal/clients) holds matters and adversaries.
type CRMProfile struct {
	ID           string    `json:"id"`
	ClientID     string    `json:"clientId"`
	ExternalRef  string    `json:"externalRef,omitempty"` // portal identity (e.g. Auth0 sub)
	Email        string    `json:"email,omitempty"`
	Name         string    `json:"name"`
	ConflictFlag bool      `json:"conflictFlag,omitempty"` // an approved adverse_party fact hit the roster
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// CRMFact is one typed statement about a client, with full provenance and a
// bidirectional-consent state machine: client-proposed facts await a lawyer,
// lawyer-proposed facts await the client. Only approved facts form the
// profile; superseded facts stay as history.
type CRMFact struct {
	ID           string     `json:"id"`
	ProfileID    string     `json:"profileId"`
	Category     string     `json:"category"`  // see crm.Categories
	Predicate    string     `json:"predicate"` // short machine key, e.g. "phone"
	Value        string     `json:"value"`
	Note         string     `json:"note,omitempty"`
	Source       string     `json:"source"`       // client | lawyer | intake | system
	ProposedBy   string     `json:"proposedBy"`   // "client:<externalRef>" | "lawyer:<profileId>"
	ApproverRole string     `json:"approverRole"` // lawyer | client
	Status       string     `json:"status"`       // pending | approved | rejected | superseded
	DecidedBy    string     `json:"decidedBy,omitempty"`
	DecisionNote string     `json:"decisionNote,omitempty"`
	SupersededBy string     `json:"supersededBy,omitempty"` // fact id that replaced this one
	CreatedAt    time.Time  `json:"createdAt"`
	DecidedAt    *time.Time `json:"decidedAt,omitempty"`
}

// CRMRepository stores profiles and facts durably.
type CRMRepository interface {
	PutCRMProfile(ctx context.Context, p CRMProfile) error
	GetCRMProfile(ctx context.Context, id string) (*CRMProfile, bool, error)
	FindCRMProfileByExternalRef(ctx context.Context, ref string) (*CRMProfile, bool, error)
	FindCRMProfileByClientID(ctx context.Context, clientID string) (*CRMProfile, bool, error)
	ListCRMProfiles(ctx context.Context) ([]CRMProfile, error)

	PutCRMFact(ctx context.Context, f CRMFact) error
	GetCRMFact(ctx context.Context, id string) (*CRMFact, bool, error)
	// ListCRMFacts returns a profile's facts, newest-first. status "" = all.
	ListCRMFacts(ctx context.Context, profileID, status string) ([]CRMFact, error)
	// ListPendingCRMFacts returns the firm-wide pending queue for one approver
	// role ("lawyer" or "client"), newest-first.
	ListPendingCRMFacts(ctx context.Context, approverRole string) ([]CRMFact, error)
}

// IntakeSubmission is one draft document submitted by the client portal
// (affidavit-maker) for firm review.
type IntakeSubmission struct {
	ID           string    `json:"id"`
	ExternalID   string    `json:"externalId"` // portal's document id (idempotency key per profile)
	ProfileID    string    `json:"crmProfileId"`
	ClientID     string    `json:"clientId"`
	ClientNumber string    `json:"clientNumber"`
	MatterNumber string    `json:"matterNumber,omitempty"`
	Title        string    `json:"title"`
	DocumentType string    `json:"documentType,omitempty"`
	MatterType   string    `json:"matterType,omitempty"`
	Jurisdiction string    `json:"jurisdiction,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	DocumentID   string    `json:"documentId,omitempty"` // knowledge-store document
	Status       string    `json:"status"`               // received | conflict_hold | in_review | ready | rejected
	Note         string    `json:"note,omitempty"`
	AssignedTo   string    `json:"assignedTo,omitempty"` // lawyer profile id
	TaskID       string    `json:"taskId,omitempty"`
	Conflict     bool      `json:"conflict"`
	ConflictJSON []byte    `json:"-"` // full ConflictCheckResult
	MetadataJSON []byte    `json:"-"` // portal-supplied metadata, verbatim
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// IntakeRepository stores intake submissions durably.
type IntakeRepository interface {
	PutIntakeSubmission(ctx context.Context, s IntakeSubmission) error
	GetIntakeSubmission(ctx context.Context, id string) (*IntakeSubmission, bool, error)
	// FindIntakeSubmissionByExternalID resolves the idempotency key: one
	// (profile, externalId) pair maps to at most one submission.
	FindIntakeSubmissionByExternalID(ctx context.Context, profileID, externalID string) (*IntakeSubmission, bool, error)
	// ListIntakeSubmissionsByProfile returns a client's submissions, newest-first.
	ListIntakeSubmissionsByProfile(ctx context.Context, profileID string) ([]IntakeSubmission, error)
	// ListIntakeSubmissions returns every submission, newest-first.
	ListIntakeSubmissions(ctx context.Context) ([]IntakeSubmission, error)
}
