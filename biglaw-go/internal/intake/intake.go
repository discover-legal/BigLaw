// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package intake is the optional client-intake module: it receives draft
// documents from an external client portal (discover.legal's firm mode) and
// lands them in the firm. Order of operations for every submission:
//
//  1. resolve the portal identity to a CRM profile (a *prospect* — no roster
//     client — on first contact);
//  2. run the conflict check in BOTH directions: the client's name against
//     every existing client's adverse parties, and the submitted adverse
//     parties against every existing client;
//  3. on a hit: hold the submission for a lawyer (status conflict_hold). The
//     draft is kept only inside the held record — nothing is ingested, no
//     roster client or matter is created, no fact is seeded, no agent runs —
//     and the hold is audited. Holds are never auto-acknowledged; only a
//     partner clears one (ResolveConflict) with a recorded reason;
//  4. otherwise (or once acknowledged): admit the prospect to the roster,
//     open a matter classified by BigLaw's own vocabulary (practiceArea /
//     NOSLEGAL — never the portal's matter-pack code), ingest the draft into
//     the knowledge store, and seed the client's stated facts as
//     consent-gated CRM proposals.
//
// The module boundary: intake talks to the platform only through the small
// interfaces below (client roster, knowledge ingest) plus the crm module and
// the audit log; nothing outside cmd/biglaw imports this package. The wire
// contract is docs/integration/affidavit-intake.md, and its JSON Schemas in
// docs/integration/schemas/ are the integration surface.
package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/crm"
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

	maxContentLen      = 1 << 20 // 1 MiB of draft text
	maxTitleLen        = 300
	maxSummaryLen      = 4000
	maxAdverseParties  = 50
	maxPartyNameLen    = 200
	maxClassifierLen   = 100
	maxConflictReason  = 1000
	maxMetadataJSONLen = 16 * 1024
)

var validStatuses = map[string]bool{
	StatusReceived: true, StatusConflictHold: true, StatusInReview: true,
	StatusReady: true, StatusRejected: true,
}

// Roster is the slice of the client roster (internal/clients) intake uses.
type Roster interface {
	Get(id string) *types.Client
	GetByClientNumber(num string) *types.Client
	List() []types.Client
	Create(name, clientNumber string, adversaries []string, notes string) (*types.Client, error)
	Update(id string, patch map[string]interface{}) (*types.Client, error)
	AddMatter(clientID, matterNumber, description, practiceArea string) (*types.ClientMatter, error)
	ConflictHits(name string, adversaries []string, excludeClientID string) []types.ConflictCheckResult
}

// Ingester is the knowledge-store ingest seam.
type Ingester interface {
	Ingest(ctx context.Context, doc types.Document) (*types.Document, error)
}

// ClientRef identifies the portal user submitting.
type ClientRef struct {
	ExternalID string `json:"externalId"`
	Email      string `json:"email"`
	Name       string `json:"name"`
}

// PartyRef is one adverse party named by the client.
type PartyRef struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"` // informational (e.g. "spouse", "employer")
}

// NosLegal carries NOSLEGAL v4 taxonomy tags supplied by the portal.
type NosLegal struct {
	AreaOfLaw string `json:"areaOfLaw,omitempty"`
	WorkType  string `json:"workType,omitempty"`
	Sector    string `json:"sector,omitempty"`
	AssetType string `json:"assetType,omitempty"`
}

// SubmissionRequest is the parsed POST /intake/submissions body. Tolerant
// reader: unknown fields are ignored; matterType is informational metadata
// and never drives behaviour.
type SubmissionRequest struct {
	ExternalID     string                 `json:"externalId"`
	Client         ClientRef              `json:"client"`
	Title          string                 `json:"title"`
	DocumentType   string                 `json:"documentType"`
	MatterType     string                 `json:"matterType"`
	Jurisdiction   string                 `json:"jurisdiction"`
	PracticeArea   string                 `json:"practiceArea"`
	NosLegal       *NosLegal              `json:"noslegal"`
	AdverseParties []PartyRef             `json:"adverseParties"`
	Summary        string                 `json:"summary"`
	Content        string                 `json:"content"`
	Facts          []crm.FactInput        `json:"facts"`
	Metadata       map[string]interface{} `json:"metadata"`
}

// ConflictRecord is what a submission stores about its conflict check
// (ConflictJSON). The embedded result is the first hit (wire-compatible with
// ConflictCheckResult); Hits lists every hit. The full record is firm-only;
// the portal sees only hasConflict.
type ConflictRecord struct {
	types.ConflictCheckResult
	Hits           []types.ConflictCheckResult `json:"hits,omitempty"`
	AdverseParties []string                    `json:"adverseParties,omitempty"`
	CheckedAt      time.Time                   `json:"checkedAt"`
	Acknowledged   bool                        `json:"acknowledged,omitempty"`
	AcknowledgedBy string                      `json:"acknowledgedBy,omitempty"`
	Reason         string                      `json:"reason,omitempty"`
	AcknowledgedAt string                      `json:"acknowledgedAt,omitempty"`
	// AcknowledgedHits are the hit keys (client id + matched party) a
	// partner has acknowledged; a hit outside this set holds again.
	AcknowledgedHits []string `json:"acknowledgedHits,omitempty"`
}

// heldPayload is what a conflict-held submission keeps until a lawyer
// clears or declines the hold (HeldJSON).
type heldPayload struct {
	Content        string          `json:"content"`
	Facts          []crm.FactInput `json:"facts,omitempty"`
	AdverseParties []string        `json:"adverseParties,omitempty"`
	ClientEmail    string          `json:"clientEmail,omitempty"`
	ClientRef      string          `json:"clientRef"`
}

// Service lands portal submissions in the firm. Safe for concurrent use.
type Service struct {
	repo      store.IntakeRepository
	crm       *crm.Service
	clients   Roster
	knowledge Ingester
}

// New builds the service; all dependencies are required except ks (nil
// skips document ingestion — submissions still land).
func New(repo store.IntakeRepository, crmSvc *crm.Service, roster Roster, ks Ingester) *Service {
	return &Service{repo: repo, crm: crmSvc, clients: roster, knowledge: ks}
}

// Sentinel errors the HTTP layer maps to status codes.
var (
	ErrNotFound     = errors.New("intake: not found")
	ErrNotHeld      = errors.New("intake: submission is not on conflict hold")
	ErrConflictHold = errors.New("intake: submission is on conflict hold; resolve the conflict first")
	ErrNoDocument   = errors.New("intake: submission has no ingested document")
)

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

	existing, isUpdate, err := s.repo.FindIntakeSubmissionByExternalID(ctx, profile.ID, req.ExternalID)
	if err != nil {
		return nil, err
	}
	var sub store.IntakeSubmission
	if isUpdate {
		sub = *existing
	} else {
		sub = store.IntakeSubmission{ID: uuid.New().String(), CreatedAt: time.Now()}
	}
	sub.ExternalID = req.ExternalID
	sub.ProfileID = profile.ID
	sub.Title = title
	sub.DocumentType = strutil.Truncate(strings.TrimSpace(req.DocumentType), 100)
	sub.MatterType = strutil.Truncate(strings.TrimSpace(req.MatterType), 100) // informational only
	sub.Jurisdiction = strutil.Truncate(strings.TrimSpace(req.Jurisdiction), 40)
	sub.Summary = strutil.Truncate(strings.TrimSpace(req.Summary), maxSummaryLen)
	sub.PracticeArea = resolvePracticeArea(req.PracticeArea, req.NosLegal)
	sub.NosLegalJSON = nil
	if tags := cleanNosLegal(req.NosLegal); tags != nil {
		sub.NosLegalJSON, _ = json.Marshal(tags)
	}
	if req.Metadata != nil {
		if b, err := json.Marshal(req.Metadata); err == nil && len(b) <= maxMetadataJSONLen {
			sub.MetadataJSON = b
		}
	}
	sub.UpdatedAt = time.Now()

	// Adverse parties are cumulative across re-submits of one document: a
	// party once named stays in the conflict check and on the record even
	// if a later draft drops it.
	prev := decodeConflict(sub.ConflictJSON)
	adverse, _ := mergeParties(prev.AdverseParties, collectAdverseParties(req.AdverseParties, req.Facts))
	payload := heldPayload{
		Content:        req.Content,
		Facts:          req.Facts,
		AdverseParties: adverse,
		ClientEmail:    req.Client.Email,
		ClientRef:      req.Client.ExternalID,
	}

	// Conflict check — every submission, both directions, every hit. A
	// returning client is excluded from its own check.
	var hits []types.ConflictCheckResult
	var rosterClient *types.Client
	if profile.ClientID != "" {
		rosterClient = s.clients.Get(profile.ClientID)
	}
	if rosterClient != nil {
		hits = s.clients.ConflictHits(rosterClient.Name, adverse, rosterClient.ID)
	} else {
		hits = s.clients.ConflictHits(profile.Name, adverse, "")
	}
	rec := ConflictRecord{Hits: hits, AdverseParties: adverse, CheckedAt: time.Now()}
	if len(hits) > 0 {
		rec.ConflictCheckResult = hits[0]
	}
	// A partner's acknowledgement covers exactly the hits it was given for;
	// any other hit (e.g. a newly named adverse party) holds again.
	rec.AcknowledgedHits = prev.AcknowledgedHits
	unacknowledged := 0
	acked := map[string]bool{}
	for _, k := range prev.AcknowledgedHits {
		acked[k] = true
	}
	for _, h := range hits {
		if !acked[hitKey(h)] {
			unacknowledged++
		}
	}
	if len(hits) > 0 && unacknowledged == 0 {
		rec.Acknowledged, rec.AcknowledgedBy, rec.Reason, rec.AcknowledgedAt =
			true, prev.AcknowledgedBy, prev.Reason, prev.AcknowledgedAt
	}
	// Once held, a submission stays held until a partner resolves it — a
	// re-submit that no longer names the conflicting party must not slip
	// past the hold. The original conflict record is kept for the reviewer.
	stillHeld := isUpdate && sub.Status == StatusConflictHold
	if stillHeld && len(hits) == 0 {
		rec = prev
	}
	sub.Conflict = rec.HasConflict
	sub.ConflictJSON, _ = json.Marshal(rec)

	if unacknowledged > 0 || stillHeld {
		return s.hold(ctx, sub, payload, isUpdate)
	}

	if err := s.land(ctx, &sub, profile, payload, isUpdate); err != nil {
		return nil, err
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.submission_received",
		ActorID: "intake-portal",
		Data: map[string]interface{}{
			"submissionId": sub.ID, "externalId": sub.ExternalID,
			"clientNumber": sub.ClientNumber, "status": sub.Status,
			"conflict": sub.Conflict, "update": isUpdate, "practiceArea": sub.PracticeArea,
		},
	})
	return &sub, nil
}

// hold parks a conflicted submission for a lawyer: the payload stays in the
// held record and nothing else happens.
func (s *Service) hold(ctx context.Context, sub store.IntakeSubmission, payload heldPayload, isUpdate bool) (*store.IntakeSubmission, error) {
	sub.Status = StatusConflictHold
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("intake: encode held payload: %w", err)
	}
	sub.HeldJSON = b
	if err := s.repo.PutIntakeSubmission(ctx, sub); err != nil {
		return nil, err
	}
	rec := decodeConflict(sub.ConflictJSON)
	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.conflict_hold",
		ActorID: "intake-portal",
		Data: map[string]interface{}{
			"submissionId": sub.ID, "externalId": sub.ExternalID, "profileId": sub.ProfileID,
			"conflictingClientId":   rec.ConflictingClientID,
			"conflictingClientName": rec.ConflictingClientName,
			"matchedAdversary":      rec.MatchedAdversary,
			"hits":                  len(rec.Hits),
			"update":                isUpdate,
			"ingested":              false,
		},
	})
	return &sub, nil
}

// land admits the client to the roster (if a prospect), opens the matter,
// ingests the draft, persists, and seeds fact proposals. It is the only
// path by which intake content reaches the knowledge store or the roster.
func (s *Service) land(ctx context.Context, sub *store.IntakeSubmission, profile *store.CRMProfile, payload heldPayload, isUpdate bool) error {
	rosterClient, err := s.admit(ctx, profile, payload.AdverseParties)
	if err != nil {
		return err
	}
	sub.ClientID = rosterClient.ID
	sub.ClientNumber = rosterClient.ClientNumber
	sub.HeldJSON = nil

	// Terminal statuses survive a re-submit; everything else (including a
	// cleared hold) re-enters the queue.
	if !isUpdate || (sub.Status != StatusReady && sub.Status != StatusRejected) {
		sub.Status = StatusReceived
	}

	if sub.MatterNumber == "" {
		sub.MatterNumber = s.openMatter(rosterClient, sub)
	}

	if s.knowledge != nil {
		doc := types.Document{
			ID:                   sub.DocumentID, // empty on first landing → new id assigned
			Title:                sub.Title,
			Content:              payload.Content,
			Source:               "client-intake",
			Jurisdiction:         sub.Jurisdiction,
			DocumentType:         sub.DocumentType,
			PracticeArea:         sub.PracticeArea,
			DetectedClientNumber: rosterClient.ClientNumber,
			NosLegal:             nosLegalTags(sub.NosLegalJSON),
			Metadata: map[string]interface{}{
				"intakeSubmissionId": sub.ID,
				"intakeExternalId":   sub.ExternalID,
				"matterNumber":       sub.MatterNumber,
				"portalMatterType":   sub.MatterType, // informational only
				"voice":              "client",       // client-voiced draft, not firm work product
			},
		}
		stored, err := s.knowledge.Ingest(store.WithSystem(ctx), doc)
		if err != nil {
			return fmt.Errorf("intake: ingest draft: %w", err)
		}
		sub.DocumentID = stored.ID
	}

	if err := s.repo.PutIntakeSubmission(ctx, *sub); err != nil {
		return err
	}

	// Seed CRM proposals from the client's stated facts (client-proposed →
	// lawyer approves). Best-effort: a bad fact list never sinks the intake.
	if len(payload.Facts) > 0 {
		actor := crm.Actor{Role: crm.RoleClient, ID: payload.ClientRef}
		if _, err := s.crm.Propose(ctx, profile.ID, actor, "intake", payload.Facts); err != nil {
			audit.Default.Write(audit.WriteRequest{
				Event:   "intake.facts_skipped",
				ActorID: "intake",
				Data:    map[string]interface{}{"submissionId": sub.ID, "error": err.Error()},
			})
		}
	}
	return nil
}

// admit returns the profile's roster client, creating it (prospect →
// client) when needed, and records the submitted adverse parties on it so
// future conflict checks see them.
func (s *Service) admit(ctx context.Context, profile *store.CRMProfile, adverse []string) (*types.Client, error) {
	if profile.ClientID != "" {
		rc := s.clients.Get(profile.ClientID)
		if rc == nil {
			return nil, fmt.Errorf("intake: roster client %s not found", profile.ClientID)
		}
		if merged, changed := mergeParties(rc.Adversaries, adverse); changed {
			if updated, err := s.clients.Update(rc.ID, map[string]interface{}{"adversaries": merged}); err == nil {
				rc = updated
			}
		}
		return rc, nil
	}
	num, err := s.nextClientNumber()
	if err != nil {
		return nil, err
	}
	created, err := s.clients.Create(profile.Name, num, adverse, "Created via client intake portal")
	if err != nil {
		return nil, fmt.Errorf("intake: create roster client: %w", err)
	}
	linked, err := s.crm.LinkClient(ctx, profile.ID, created.ID)
	if err != nil {
		return nil, err
	}
	*profile = *linked
	audit.Default.Write(audit.WriteRequest{
		Event:   "client.created",
		ActorID: "intake",
		Data: map[string]interface{}{
			"clientId": created.ID, "clientNumber": created.ClientNumber,
			"adversaryCount": len(created.Adversaries), "via": "intake",
		},
	})
	return created, nil
}

func (s *Service) nextClientNumber() (string, error) {
	n := len(s.clients.List())
	for i := n + 1; i < n+10_000; i++ {
		num := fmt.Sprintf("AM-%d", i)
		if s.clients.GetByClientNumber(num) == nil {
			return num, nil
		}
	}
	return "", fmt.Errorf("intake: could not allocate a client number")
}

// openMatter adds an intake matter to the roster client, classified by the
// submission's practice area; returns "" (and the submission proceeds
// matterless) if allocation fails.
func (s *Service) openMatter(rosterClient *types.Client, sub *store.IntakeSubmission) string {
	desc := sub.Title
	if sub.DocumentType != "" {
		desc = sub.DocumentType + " — " + desc
	}
	for i := len(rosterClient.Matters) + 1; i <= len(rosterClient.Matters)+100; i++ {
		num := fmt.Sprintf("%s-%d", rosterClient.ClientNumber, i)
		m, err := s.clients.AddMatter(rosterClient.ID, num, strutil.Truncate(desc, 200), sub.PracticeArea)
		if err == nil {
			return m.MatterNumber
		}
		if !strings.Contains(err.Error(), "exists") {
			return ""
		}
	}
	return ""
}

// ResolveConflict is the lawyer's decision on a conflict hold.
// acknowledge=true requires a reason (informed-consent waiver, ethical
// wall, false positive, ...) and lands the held submission; false declines
// it (status rejected) and discards the held draft. Both are audited.
func (s *Service) ResolveConflict(ctx context.Context, id string, acknowledge bool, reason, actorID string) (*store.IntakeSubmission, error) {
	reason = strutil.Truncate(strings.TrimSpace(reason), maxConflictReason)
	if acknowledge && reason == "" {
		return nil, fmt.Errorf("intake: conflictReason is required to acknowledge a conflict")
	}
	sub, ok, err := s.repo.GetIntakeSubmission(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	if sub.Status != StatusConflictHold {
		return nil, ErrNotHeld
	}
	rec := decodeConflict(sub.ConflictJSON)
	sub.UpdatedAt = time.Now()

	if !acknowledge {
		sub.Status = StatusRejected
		sub.HeldJSON = nil
		if reason != "" {
			sub.Note = reason
		}
		if err := s.repo.PutIntakeSubmission(ctx, *sub); err != nil {
			return nil, err
		}
		audit.Default.Write(audit.WriteRequest{
			Event:   "intake.conflict_declined",
			ActorID: actorID,
			Data: map[string]interface{}{
				"submissionId": sub.ID, "reason": reason,
				"conflictingClientId": rec.ConflictingClientID, "matchedAdversary": rec.MatchedAdversary,
			},
		})
		return sub, nil
	}

	var payload heldPayload
	if len(sub.HeldJSON) == 0 || json.Unmarshal(sub.HeldJSON, &payload) != nil || payload.Content == "" {
		return nil, fmt.Errorf("intake: held payload missing; ask the client to re-submit")
	}
	profile, ok, err := s.crm.Get(ctx, sub.ProfileID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("intake: profile %s not found", sub.ProfileID)
	}
	rec.Acknowledged = true
	for _, h := range append(rec.Hits, rec.ConflictCheckResult) {
		if h.HasConflict {
			rec.AcknowledgedHits = appendUnique(rec.AcknowledgedHits, hitKey(h))
		}
	}
	rec.AcknowledgedBy = actorID
	rec.Reason = reason
	rec.AcknowledgedAt = time.Now().UTC().Format(time.RFC3339)
	sub.ConflictJSON, _ = json.Marshal(rec)
	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.conflict_acknowledged",
		ActorID: actorID,
		Data: map[string]interface{}{
			"submissionId": sub.ID, "profileId": sub.ProfileID, "reason": reason,
			"conflictingClientId":   rec.ConflictingClientID,
			"conflictingClientName": rec.ConflictingClientName,
			"matchedAdversary":      rec.MatchedAdversary,
		},
	})
	// isUpdate=false semantics for status: a cleared hold enters the queue.
	if err := s.land(ctx, sub, profile, payload, false); err != nil {
		return nil, err
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.submission_received",
		ActorID: actorID,
		Data: map[string]interface{}{
			"submissionId": sub.ID, "externalId": sub.ExternalID,
			"clientNumber": sub.ClientNumber, "status": sub.Status,
			"conflict": true, "acknowledged": true,
		},
	})
	return sub, nil
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

// Claim assigns a submission to a lawyer and moves it into review. A held
// submission can be claimed (someone owns resolving it) but stays held.
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

// Update sets status and/or note (lawyer-driven state machine). The
// conflict hold is entered only by the conflict check and left only through
// ResolveConflict, never by a status patch.
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
		if status != sub.Status && (status == StatusConflictHold || sub.Status == StatusConflictHold) {
			return nil, ErrConflictHold
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

// TaskReady checks a submission may have agent work started on it: not on
// conflict hold, and its draft has been ingested.
func (s *Service) TaskReady(sub *store.IntakeSubmission) error {
	if sub.Status == StatusConflictHold {
		return ErrConflictHold
	}
	if sub.DocumentID == "" {
		return ErrNoDocument
	}
	return nil
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

// ─── helpers ─────────────────────────────────────────────────────────────────

// DecodeConflict returns a submission's stored conflict record.
func DecodeConflict(sub store.IntakeSubmission) ConflictRecord {
	return decodeConflict(sub.ConflictJSON)
}

func decodeConflict(b []byte) ConflictRecord {
	var rec ConflictRecord
	if len(b) > 0 {
		_ = json.Unmarshal(b, &rec)
	}
	return rec
}

// hitKey identifies one conflict hit for acknowledgement tracking.
func hitKey(h types.ConflictCheckResult) string {
	return h.ConflictingClientID + "|" + strings.ToLower(strings.TrimSpace(h.MatchedAdversary))
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// collectAdverseParties gathers the adverse parties the client named, from
// the explicit list and from adverse_party facts; deduplicated, capped.
func collectAdverseParties(parties []PartyRef, facts []crm.FactInput) []string {
	var names []string
	for _, p := range parties {
		names = append(names, p.Name)
	}
	for _, f := range facts {
		if strings.EqualFold(strings.TrimSpace(f.Category), "adverse_party") {
			names = append(names, f.Value)
		}
	}
	out, _ := mergeParties(nil, names)
	return out
}

// mergeParties appends cleaned, case-insensitively new names to base.
func mergeParties(base, add []string) ([]string, bool) {
	seen := map[string]bool{}
	out := append([]string(nil), base...)
	for _, b := range base {
		seen[strings.ToLower(strings.TrimSpace(b))] = true
	}
	changed := false
	for _, n := range add {
		n = cleanText(n, maxPartyNameLen)
		k := strings.ToLower(n)
		if n == "" || seen[k] || len(out) >= maxAdverseParties+len(base) {
			continue
		}
		seen[k] = true
		out = append(out, n)
		changed = true
	}
	return out, changed
}

// resolvePracticeArea classifies the submission in BigLaw's own vocabulary:
// a canonical practice area (types.PracticeAreas, case-insensitive) wins;
// then the NOSLEGAL areaOfLaw; then a non-canonical practiceArea as free
// text; else "". The portal's matter-pack code is never consulted.
func resolvePracticeArea(practiceArea string, tags *NosLegal) string {
	pa := cleanText(practiceArea, maxClassifierLen)
	for _, canon := range types.PracticeAreas {
		if pa != "" && strings.EqualFold(pa, canon) {
			return canon
		}
	}
	if tags != nil {
		if area := cleanText(tags.AreaOfLaw, maxClassifierLen); area != "" {
			for _, canon := range types.PracticeAreas {
				if strings.EqualFold(area, canon) {
					return canon
				}
			}
			return area
		}
	}
	return pa
}

func cleanNosLegal(t *NosLegal) *NosLegal {
	if t == nil {
		return nil
	}
	c := NosLegal{
		AreaOfLaw: cleanText(t.AreaOfLaw, maxClassifierLen),
		WorkType:  cleanText(t.WorkType, maxClassifierLen),
		Sector:    cleanText(t.Sector, maxClassifierLen),
		AssetType: cleanText(t.AssetType, maxClassifierLen),
	}
	if c == (NosLegal{}) {
		return nil
	}
	return &c
}

func nosLegalTags(b []byte) *types.NosLegalTags {
	var t NosLegal
	if len(b) == 0 || json.Unmarshal(b, &t) != nil {
		return nil
	}
	ptr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	out := &types.NosLegalTags{AreaOfLaw: ptr(t.AreaOfLaw), WorkType: ptr(t.WorkType), Sector: ptr(t.Sector), AssetType: ptr(t.AssetType)}
	if out.AreaOfLaw == nil && out.WorkType == nil && out.Sector == nil && out.AssetType == nil {
		return nil
	}
	return out
}

// cleanText trims, drops control characters, collapses whitespace, caps.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strutil.Truncate(strings.Join(strings.Fields(s), " "), max)
}
