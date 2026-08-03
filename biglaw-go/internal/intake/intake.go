// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package intake receives draft documents from the client portal
// (affidavit-maker) and lands them in the firm: find-or-create the roster
// client + CRM profile, run the conflict check, open a matter, ingest the
// draft into the knowledge store, seed CRM proposals from the client's
// stated facts, and queue the submission for a lawyer. The wire contract is
// docs/integration/affidavit-intake.md.
package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/crm"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Submission statuses (the wire vocabulary — see the contract doc).
const (
	StatusReceived     = "received"
	StatusConflictHold = "conflict_hold"
	StatusInReview     = "in_review"
	StatusReady        = "ready"
	StatusRejected     = "rejected"

	maxContentLen = 1 << 20 // 1 MiB of draft text
	maxTitleLen   = 300
	maxSummaryLen = 4000
)

var validStatuses = map[string]bool{
	StatusReceived: true, StatusConflictHold: true, StatusInReview: true,
	StatusReady: true, StatusRejected: true,
}

// ClientRef identifies the portal user submitting.
type ClientRef struct {
	ExternalID string `json:"externalId"`
	Email      string `json:"email"`
	Name       string `json:"name"`
}

// SubmissionRequest is the parsed POST /intake/submissions body.
type SubmissionRequest struct {
	ExternalID   string                 `json:"externalId"`
	Client       ClientRef              `json:"client"`
	Title        string                 `json:"title"`
	DocumentType string                 `json:"documentType"`
	MatterType   string                 `json:"matterType"`
	Jurisdiction string                 `json:"jurisdiction"`
	Summary      string                 `json:"summary"`
	Content      string                 `json:"content"`
	Facts        []crm.FactInput        `json:"facts"`
	Metadata     map[string]interface{} `json:"metadata"`
}

// Service lands portal submissions in the firm. Safe for concurrent use.
type Service struct {
	repo      store.IntakeRepository
	crm       *crm.Service
	clients   *clients.ClientStore
	knowledge *knowledge.Store
}

// New builds the service; all dependencies are required except knowledge
// (nil skips document ingestion — submissions still land).
func New(repo store.IntakeRepository, crmSvc *crm.Service, clientStore *clients.ClientStore, ks *knowledge.Store) *Service {
	return &Service{repo: repo, crm: crmSvc, clients: clientStore, knowledge: ks}
}

// Submit lands (or re-lands, keyed by ExternalID) a portal draft.
func (s *Service) Submit(ctx context.Context, req SubmissionRequest) (*store.IntakeSubmission, error) {
	req.ExternalID = strings.TrimSpace(req.ExternalID)
	req.Client.ExternalID = strings.TrimSpace(req.Client.ExternalID)
	req.Content = strings.TrimSpace(req.Content)
	if req.ExternalID == "" {
		return nil, fmt.Errorf("intake: externalId is required")
	}
	if req.Client.ExternalID == "" {
		return nil, fmt.Errorf("intake: client.externalId is required")
	}
	if req.Content == "" {
		return nil, fmt.Errorf("intake: content is required")
	}
	if len(req.Content) > maxContentLen {
		return nil, fmt.Errorf("intake: content exceeds %d bytes", maxContentLen)
	}
	title := strutil.Truncate(strings.TrimSpace(req.Title), maxTitleLen)
	if title == "" {
		title = "Untitled draft " + req.ExternalID
	}

	profile, err := s.crm.EnsureProfile(ctx, req.Client.ExternalID, req.Client.Email, req.Client.Name)
	if err != nil {
		return nil, err
	}
	rosterClient := s.clients.Get(profile.ClientID)
	if rosterClient == nil {
		return nil, fmt.Errorf("intake: roster client %s not found", profile.ClientID)
	}

	conflict := s.clients.CheckConflict(rosterClient.Name, nil)
	conflictJSON, _ := json.Marshal(conflict)

	existing, isUpdate, err := s.repo.FindIntakeSubmissionByExternalID(ctx, profile.ID, req.ExternalID)
	if err != nil {
		return nil, err
	}

	var sub store.IntakeSubmission
	if isUpdate {
		sub = *existing
	} else {
		sub = store.IntakeSubmission{
			ID:        uuid.New().String(),
			CreatedAt: time.Now(),
		}
	}
	sub.ExternalID = req.ExternalID
	sub.ProfileID = profile.ID
	sub.ClientID = rosterClient.ID
	sub.ClientNumber = rosterClient.ClientNumber
	sub.Title = title
	sub.DocumentType = strutil.Truncate(strings.TrimSpace(req.DocumentType), 100)
	sub.MatterType = strutil.Truncate(strings.TrimSpace(req.MatterType), 100)
	sub.Jurisdiction = strutil.Truncate(strings.TrimSpace(req.Jurisdiction), 40)
	sub.Summary = strutil.Truncate(strings.TrimSpace(req.Summary), maxSummaryLen)
	sub.Conflict = conflict.HasConflict
	sub.ConflictJSON = conflictJSON
	if req.Metadata != nil {
		if b, err := json.Marshal(req.Metadata); err == nil && len(b) <= 16*1024 {
			sub.MetadataJSON = b
		}
	}
	sub.UpdatedAt = time.Now()

	// Terminal statuses survive a re-submit; everything else resets so the
	// new content re-enters the queue.
	if !isUpdate || (sub.Status != StatusReady && sub.Status != StatusRejected) {
		if conflict.HasConflict {
			sub.Status = StatusConflictHold
		} else {
			sub.Status = StatusReceived
		}
	}

	// Open a matter for a fresh submission so downstream matter machinery
	// (advocacy briefs, health, channels) has a home.
	if sub.MatterNumber == "" {
		sub.MatterNumber = s.openMatter(rosterClient, &sub)
	}

	// Ingest (or re-ingest under the same document id) the draft.
	if s.knowledge != nil {
		doc := types.Document{
			ID:                   sub.DocumentID, // empty on first submit → new id assigned
			Title:                title,
			Content:              req.Content,
			Source:               "affidavit-maker",
			Jurisdiction:         sub.Jurisdiction,
			DocumentType:         sub.DocumentType,
			DetectedClientNumber: rosterClient.ClientNumber,
			Metadata: map[string]interface{}{
				"intakeExternalId": sub.ExternalID,
				"matterType":       sub.MatterType,
			},
		}
		stored, err := s.knowledge.Ingest(store.WithSystem(ctx), doc)
		if err != nil {
			return nil, fmt.Errorf("intake: ingest draft: %w", err)
		}
		sub.DocumentID = stored.ID
	}

	if err := s.repo.PutIntakeSubmission(ctx, sub); err != nil {
		return nil, err
	}

	// Seed CRM proposals from the client's stated facts (client-proposed →
	// lawyer approves). Best-effort: a bad fact list never sinks the intake.
	if len(req.Facts) > 0 {
		actor := crm.Actor{Role: crm.RoleClient, ID: req.Client.ExternalID}
		if _, err := s.crm.Propose(ctx, profile.ID, actor, "intake", req.Facts); err != nil {
			audit.Default.Write(audit.WriteRequest{
				Event:   "intake.facts_skipped",
				ActorID: "intake",
				Data:    map[string]interface{}{"submissionId": sub.ID, "error": err.Error()},
			})
		}
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.submission_received",
		ActorID: "affidavit-maker",
		Data: map[string]interface{}{
			"submissionId": sub.ID, "externalId": sub.ExternalID,
			"clientNumber": sub.ClientNumber, "status": sub.Status,
			"conflict": sub.Conflict, "update": isUpdate,
		},
	})
	return &sub, nil
}

// openMatter adds an intake matter to the roster client; returns "" (and the
// submission proceeds matterless) if allocation fails.
func (s *Service) openMatter(rosterClient *types.Client, sub *store.IntakeSubmission) string {
	desc := sub.Title
	if sub.MatterType != "" {
		desc = sub.MatterType + " — " + desc
	}
	for i := len(rosterClient.Matters) + 1; i <= len(rosterClient.Matters)+100; i++ {
		num := fmt.Sprintf("%s-%d", rosterClient.ClientNumber, i)
		m, err := s.clients.AddMatter(rosterClient.ID, num, strutil.Truncate(desc, 200), "")
		if err == nil {
			return m.MatterNumber
		}
		if !strings.Contains(err.Error(), "exists") {
			return ""
		}
	}
	return ""
}

// Get returns one submission.
func (s *Service) Get(ctx context.Context, id string) (*store.IntakeSubmission, bool, error) {
	return s.repo.GetIntakeSubmission(ctx, id)
}

// ListForClient returns the submissions of one portal identity, newest-first.
func (s *Service) ListForClient(ctx context.Context, externalRef string) ([]store.IntakeSubmission, error) {
	profile, ok, err := s.crm.GetByExternalRef(ctx, externalRef)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []store.IntakeSubmission{}, nil
	}
	return s.repo.ListIntakeSubmissionsByProfile(ctx, profile.ID)
}

// Queue returns submissions for the firm view. profileID "" = all (partner);
// otherwise unassigned + assigned-to-that-lawyer.
func (s *Service) Queue(ctx context.Context, profileID string) ([]store.IntakeSubmission, error) {
	all, err := s.repo.ListIntakeSubmissions(ctx)
	if err != nil {
		return nil, err
	}
	if profileID == "" {
		return all, nil
	}
	var out []store.IntakeSubmission
	for _, sub := range all {
		if sub.AssignedTo == "" || sub.AssignedTo == profileID {
			out = append(out, sub)
		}
	}
	return out, nil
}

// Claim assigns a submission to a lawyer and moves it into review.
func (s *Service) Claim(ctx context.Context, id, lawyerProfileID string) (*store.IntakeSubmission, error) {
	sub, ok, err := s.repo.GetIntakeSubmission(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	sub.AssignedTo = lawyerProfileID
	if sub.Status == StatusReceived {
		sub.Status = StatusInReview
	}
	sub.UpdatedAt = time.Now()
	if err := s.repo.PutIntakeSubmission(ctx, *sub); err != nil {
		return nil, err
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.submission_claimed",
		ActorID: lawyerProfileID,
		Data:    map[string]interface{}{"submissionId": sub.ID},
	})
	return sub, nil
}

// Update sets status and/or note (lawyer-driven state machine).
func (s *Service) Update(ctx context.Context, id, status, note, actorID string) (*store.IntakeSubmission, error) {
	sub, ok, err := s.repo.GetIntakeSubmission(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	if status != "" {
		if !validStatuses[status] {
			return nil, fmt.Errorf("intake: invalid status %q", status)
		}
		sub.Status = status
	}
	if note != "" {
		sub.Note = strutil.Truncate(note, maxSummaryLen)
	}
	sub.UpdatedAt = time.Now()
	if err := s.repo.PutIntakeSubmission(ctx, *sub); err != nil {
		return nil, err
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.submission_updated",
		ActorID: actorID,
		Data:    map[string]interface{}{"submissionId": sub.ID, "status": sub.Status},
	})
	return sub, nil
}

// AttachTask links an orchestrator task to a submission and moves it into
// review.
func (s *Service) AttachTask(ctx context.Context, id, taskID string) (*store.IntakeSubmission, error) {
	sub, ok, err := s.repo.GetIntakeSubmission(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	sub.TaskID = taskID
	if sub.Status == StatusReceived {
		sub.Status = StatusInReview
	}
	sub.UpdatedAt = time.Now()
	if err := s.repo.PutIntakeSubmission(ctx, *sub); err != nil {
		return nil, err
	}
	return sub, nil
}

// ErrNotFound is returned for unknown submission ids.
var ErrNotFound = fmt.Errorf("intake: not found")
