// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package intake is the optional client-intake module: it receives draft
// documents from an external client portal (discover.legal's firm mode) and
// lands them in the firm. Order of operations for every submission:
//
//  1. resolve the portal identity to a CRM profile (a *prospect* — no roster
//     client — on first contact);
//  2. run the conflict check in BOTH directions over everything the
//     IDENTITY has ever supplied — every name it has used and every adverse
//     party it has named, across all its submissions and adverse_party
//     facts: its names against every existing client's adverse parties, and
//     its adverse parties against every existing client;
//  3. on a hit — or while the identity has another submission on hold, or
//     has ever had one declined — hold the submission for a partner (status
//     conflict_hold). The draft is kept only inside the held record —
//     nothing is ingested, no roster client or matter is created, no fact is
//     seeded, no agent runs — and the hold is audited. Holds are never
//     auto-acknowledged; only a partner clears one (ResolveConflict) with a
//     recorded reason, and clearing re-runs the check first;
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
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/clients"
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
	maxAdverseParties  = 50  // per submission / proposal
	maxIdentityParties = 200 // portal-supplied, cumulative per identity
	maxIdentityNames   = 20  // names one identity may supply over time
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
// ConflictCheckResult); Hits lists every hit. The full record is
// firm-confidential (partner or assignee); the portal sees only hasConflict.
type ConflictRecord struct {
	types.ConflictCheckResult
	Hits []types.ConflictCheckResult `json:"hits,omitempty"`
	// Names and AdverseParties are what this document's submissions
	// supplied, cumulative across re-submits.
	Names          []string `json:"names,omitempty"`
	AdverseParties []string `json:"adverseParties,omitempty"`
	// CheckedNames and CheckedParties are the identity-wide unions the check
	// actually ran on: every name and adverse party this portal identity has
	// ever supplied (all its submissions and adverse_party facts, plus its
	// roster entry once admitted).
	CheckedNames   []string `json:"checkedNames,omitempty"`
	CheckedParties []string `json:"checkedParties,omitempty"`
	// OpenHolds are the identity's other submissions on conflict hold when
	// this one was checked; PreviouslyDeclined are its submissions a partner
	// declined on conflict grounds. Either holds the submission: a hold
	// belongs to the person, not to one document.
	OpenHolds          []string `json:"openHolds,omitempty"`
	PreviouslyDeclined []string `json:"previouslyDeclined,omitempty"`
	// ConflictUnchecked says why a clean check is weak: "name_is_email" (the
	// identity never supplied a real name) and/or "no_adverse_parties".
	// Firm-side only; it never holds a submission on its own.
	ConflictUnchecked []string  `json:"conflictUnchecked,omitempty"`
	CheckedAt         time.Time `json:"checkedAt"`
	Acknowledged      bool      `json:"acknowledged,omitempty"`
	AcknowledgedBy    string    `json:"acknowledgedBy,omitempty"`
	Reason            string    `json:"reason,omitempty"`
	AcknowledgedAt    string    `json:"acknowledgedAt,omitempty"`
	// AcknowledgedHits are the hold keys (see holdKeys) a partner has
	// acknowledged; any other key holds again.
	AcknowledgedHits []string `json:"acknowledgedHits,omitempty"`
	// Declined is sticky: once a partner declines this submission on
	// conflict grounds, every later submission from the identity is held.
	Declined   bool   `json:"declined,omitempty"`
	DeclinedBy string `json:"declinedBy,omitempty"`
	DeclinedAt string `json:"declinedAt,omitempty"`
}

// holdKeys identifies every reason this record holds its submission.
func (r ConflictRecord) holdKeys() []string {
	var keys []string
	for _, h := range r.Hits {
		keys = append(keys, hitKey(h))
	}
	for _, id := range r.OpenHolds {
		keys = append(keys, "hold|"+id)
	}
	for _, id := range r.PreviouslyDeclined {
		keys = append(keys, "declined|"+id)
	}
	return keys
}

// unacknowledged returns the hold keys no partner has acknowledged.
func (r ConflictRecord) unacknowledged() []string {
	acked := map[string]bool{}
	for _, k := range r.AcknowledgedHits {
		acked[k] = true
	}
	var out []string
	for _, k := range r.holdKeys() {
		if !acked[k] {
			out = append(out, k)
		}
	}
	return out
}

// carry copies the decision history (acknowledgements, a decline) from the
// record a fresh check replaces.
func (r *ConflictRecord) carry(prev ConflictRecord) {
	r.AcknowledgedHits = prev.AcknowledgedHits
	r.Acknowledged, r.AcknowledgedBy, r.Reason, r.AcknowledgedAt =
		prev.Acknowledged, prev.AcknowledgedBy, prev.Reason, prev.AcknowledgedAt
	r.Declined, r.DeclinedBy, r.DeclinedAt = prev.Declined, prev.DeclinedBy, prev.DeclinedAt
}

// seal sets the wire-facing summary fields from the hold keys.
func (r *ConflictRecord) seal() {
	r.ConflictCheckResult = types.ConflictCheckResult{}
	if len(r.Hits) > 0 {
		r.ConflictCheckResult = r.Hits[0]
	}
	r.HasConflict = len(r.holdKeys()) > 0
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

// Service lands portal submissions in the firm. Safe for concurrent use:
// every state transition (submit, conflict decision, client fact proposal,
// lawyer updates) runs under one lock, so a conflict check and the admit
// that depends on it are atomic — two simultaneous submissions cannot both
// pass a check that only one of them would pass after the other landed.
type Service struct {
	mu        sync.Mutex
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
	ErrInvalid      = errors.New("intake: invalid request")
	ErrNotFound     = errors.New("intake: not found")
	ErrForbidden    = errors.New("intake: forbidden")
	ErrNotHeld      = errors.New("intake: submission is not on conflict hold")
	ErrConflictHold = errors.New("intake: submission is on conflict hold; resolve the conflict first")
	ErrReheld       = errors.New("intake: the re-run conflict check found new conflicts; the submission stays on hold")
	ErrNoDocument   = errors.New("intake: submission has no ingested document")
	ErrNotAdmitted  = errors.New("intake: not yet a client of the firm")
	ErrTaskRefused  = errors.New("intake: task not started")
)

func invalid(format string, args ...interface{}) error {
	return fmt.Errorf("%w: "+format, append([]interface{}{ErrInvalid}, args...)...)
}

// Viewer is the firm member asking: held submissions and conflict detail
// are visible only to partners and to the submission's assignee (Rule 1.18
// screening — the lawyer for the conflicting client must not see them).
type Viewer struct {
	ProfileID string
	Partner   bool
}

// SeesDetail reports whether v may see sub's conflict detail.
func (v Viewer) SeesDetail(sub store.IntakeSubmission) bool {
	return v.Partner || (v.ProfileID != "" && sub.AssignedTo == v.ProfileID)
}

// authorize gates acting on a submission: partners always; otherwise only
// the assignee. A held submission someone else could act on is reported as
// not found rather than forbidden, so its existence does not leak.
func (v Viewer) authorize(sub *store.IntakeSubmission) error {
	if v.Partner || (v.ProfileID != "" && sub.AssignedTo == v.ProfileID) {
		return nil
	}
	if sub.Status == StatusConflictHold {
		return ErrNotFound
	}
	if sub.AssignedTo == "" {
		return fmt.Errorf("%w: claim the submission first", ErrForbidden)
	}
	return fmt.Errorf("%w: submission is assigned to another lawyer", ErrForbidden)
}

// Submit lands (or re-lands, keyed by ExternalID) a portal draft.
func (s *Service) Submit(ctx context.Context, req SubmissionRequest) (*store.IntakeSubmission, error) {
	req.ExternalID = strings.TrimSpace(req.ExternalID)
	req.Client.ExternalID = strings.TrimSpace(req.Client.ExternalID)
	req.Content = strings.TrimSpace(req.Content)
	if req.ExternalID == "" {
		return nil, invalid("externalId is required")
	}
	if req.Client.ExternalID == "" {
		return nil, invalid("client.externalId is required")
	}
	if req.Content == "" {
		return nil, invalid("content is required")
	}
	if len(req.Content) > maxContentLen {
		return nil, invalid("content exceeds %d bytes", maxContentLen)
	}
	named := collectAdverseParties(req.AdverseParties, req.Facts)
	if len(named) > maxAdverseParties {
		return nil, invalid("at most %d adverse parties per submission", maxAdverseParties)
	}
	title := strutil.Truncate(strings.TrimSpace(req.Title), maxTitleLen)
	if title == "" {
		title = "Untitled draft " + req.ExternalID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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

	// Names and adverse parties are cumulative across re-submits of one
	// document (and, through identity(), across the person's documents): a
	// party once named stays in the conflict check even if a later draft
	// drops it.
	prev := decodeConflict(sub.ConflictJSON)
	rec, err := s.check(ctx, profile, sub.ID,
		unionNames(prev.Names, []string{req.Client.Name}),
		unionNames(prev.AdverseParties, named), true)
	if err != nil {
		return nil, err
	}
	rec.carry(prev)
	// Once held, a submission stays held until a partner resolves it — a
	// re-submit must never slip past the hold.
	stillHeld := isUpdate && sub.Status == StatusConflictHold
	if stillHeld && len(rec.holdKeys()) == 0 {
		rec.Hits = prev.Hits // keep what the reviewer was shown
	}
	rec.seal()
	sub.Conflict = rec.HasConflict
	sub.ConflictJSON, _ = json.Marshal(rec)
	payload := heldPayload{
		Content:        req.Content,
		Facts:          req.Facts,
		AdverseParties: rec.AdverseParties,
		ClientEmail:    req.Client.Email,
		ClientRef:      req.Client.ExternalID,
	}

	if len(rec.unacknowledged()) > 0 || stillHeld {
		return s.hold(ctx, sub, payload, isUpdate)
	}

	if err := s.land(ctx, &sub, profile, payload, rec.CheckedParties, isUpdate); err != nil {
		return nil, err
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.submission_received",
		ActorID: "intake-portal",
		Data: map[string]interface{}{
			"submissionId": sub.ID, "externalId": sub.ExternalID,
			"clientNumber": sub.ClientNumber, "status": sub.Status,
			"conflict": sub.Conflict, "update": isUpdate, "practiceArea": sub.PracticeArea,
			"conflictUnchecked": rec.ConflictUnchecked,
		},
	})
	return &sub, nil
}

// identityState is everything a portal identity has told the firm, across
// all its submissions — the unit a conflict hold is keyed to.
type identityState struct {
	names, parties      []string // portal-supplied (capped)
	rosterParties       []string // the admitted client's roster adversaries
	openHolds, declined []string // submission ids
	acked               []string // hold keys acknowledged on any submission
}

func (s *Service) identity(ctx context.Context, profile *store.CRMProfile) (identityState, error) {
	st := identityState{names: unionNames(nil, []string{profile.Name})}
	subs, err := s.repo.ListIntakeSubmissionsByProfile(ctx, profile.ID)
	if err != nil {
		return st, err
	}
	for _, sub := range subs {
		rec := decodeConflict(sub.ConflictJSON)
		st.names = unionNames(st.names, rec.Names)
		st.parties = unionNames(st.parties, rec.AdverseParties)
		st.acked = append(st.acked, rec.AcknowledgedHits...)
		if sub.Status == StatusConflictHold {
			st.openHolds = append(st.openHolds, sub.ID)
		}
		if rec.Declined {
			st.declined = append(st.declined, sub.ID)
		}
	}
	facts, err := s.crm.Facts(ctx, profile.ID, "")
	if err != nil {
		return st, err
	}
	for _, f := range facts {
		if f.Category == "adverse_party" {
			st.parties = unionNames(st.parties, []string{f.Value})
		}
	}
	if profile.ClientID != "" {
		if rc := s.clients.Get(profile.ClientID); rc != nil {
			st.names = unionNames(st.names, []string{rc.Name})
			st.rosterParties = rc.Adversaries
		}
	}
	return st, nil
}

// check runs the conflict check for one submission of profile's identity
// in both directions over every name and adverse party the identity has
// ever supplied plus this submission's (docNames, docParties), and records
// the identity's other open holds (when withOpenHolds) and declined
// submissions. subID "" checks on behalf of no submission.
func (s *Service) check(ctx context.Context, profile *store.CRMProfile, subID string, docNames, docParties []string, withOpenHolds bool) (ConflictRecord, error) {
	st, err := s.identity(ctx, profile)
	if err != nil {
		return ConflictRecord{}, err
	}
	names := unionNames(st.names, docNames)
	portalParties := unionNames(st.parties, docParties)
	if len(names) > maxIdentityNames {
		return ConflictRecord{}, invalid("this client has supplied more than %d names; contact the firm", maxIdentityNames)
	}
	if len(portalParties) > maxIdentityParties {
		return ConflictRecord{}, invalid("this client has named more than %d adverse parties; contact the firm", maxIdentityParties)
	}
	parties := unionNames(portalParties, st.rosterParties)

	var realNames []string
	for _, n := range names {
		if !crm.LooksLikeEmail(n) {
			realNames = append(realNames, n)
		}
	}
	rec := ConflictRecord{
		Names: docNames, AdverseParties: docParties,
		CheckedNames: realNames, CheckedParties: parties, CheckedAt: time.Now(),
	}
	seen := map[string]bool{}
	add := func(hits []types.ConflictCheckResult) {
		for _, h := range hits {
			if k := hitKey(h); !seen[k] {
				seen[k] = true
				rec.Hits = append(rec.Hits, h)
			}
		}
	}
	// The adverse-party direction runs once (with the first name, or with
	// none when the identity has no usable name); the client-name direction
	// runs for every name.
	if len(realNames) == 0 {
		add(s.clients.ConflictHits("", parties, profile.ClientID))
		rec.ConflictUnchecked = append(rec.ConflictUnchecked, "name_is_email")
	}
	for i, n := range realNames {
		var p []string
		if i == 0 {
			p = parties
		}
		add(s.clients.ConflictHits(n, p, profile.ClientID))
	}
	if len(parties) == 0 {
		rec.ConflictUnchecked = append(rec.ConflictUnchecked, "no_adverse_parties")
	}
	if withOpenHolds {
		for _, id := range st.openHolds {
			if id != subID {
				rec.OpenHolds = append(rec.OpenHolds, id)
			}
		}
	}
	rec.PreviouslyDeclined = st.declined
	return rec, nil
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
			"openHolds":             rec.OpenHolds,
			"previouslyDeclined":    rec.PreviouslyDeclined,
			"unacknowledged":        rec.unacknowledged(),
			"update":                isUpdate,
			"ingested":              false,
		},
	})
	return &sub, nil
}

// land admits the client to the roster (if a prospect), opens the matter,
// ingests the draft, persists, and seeds fact proposals. It is the only
// path by which intake content reaches the knowledge store or the roster.
// Callers hold s.mu.
func (s *Service) land(ctx context.Context, sub *store.IntakeSubmission, profile *store.CRMProfile, payload heldPayload, parties []string, isUpdate bool) error {
	rosterClient, err := s.admit(ctx, profile, parties)
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
// client) when needed, and records the identity's adverse parties on it so
// future conflict checks see them.
func (s *Service) admit(ctx context.Context, profile *store.CRMProfile, adverse []string) (*types.Client, error) {
	if profile.ClientID != "" {
		rc := s.clients.Get(profile.ClientID)
		if rc == nil {
			return nil, fmt.Errorf("intake: roster client %s not found", profile.ClientID)
		}
		if merged := unionNames(rc.Adversaries, adverse); len(merged) != len(rc.Adversaries) {
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

// ResolveConflict is the partner's decision on a conflict hold.
//
// acknowledge=true requires a reason (informed-consent waiver, ethical
// wall, false positive, ...) and covers exactly the hold reasons the
// partner was shown. The check is then re-run against the roster as it is
// now: anything new (e.g. a spouse admitted since the hold) re-holds the
// submission with the new detail and returns ErrReheld; otherwise the
// submission lands.
//
// acknowledge=false declines it (status rejected), discards the held draft,
// and marks the identity: every later submission from it is held for a
// partner with a "previously declined" reason — never auto-admitted.
func (s *Service) ResolveConflict(ctx context.Context, id string, acknowledge bool, reason, actorID string) (*store.IntakeSubmission, error) {
	reason = strutil.Truncate(strings.TrimSpace(reason), maxConflictReason)
	if acknowledge && reason == "" {
		return nil, invalid("conflictReason is required to acknowledge a conflict")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

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
	now := time.Now().UTC().Format(time.RFC3339)
	sub.UpdatedAt = time.Now()

	if !acknowledge {
		rec.Declined, rec.DeclinedBy, rec.DeclinedAt = true, actorID, now
		if reason != "" {
			rec.Reason = reason
			sub.Note = reason
		}
		sub.ConflictJSON, _ = json.Marshal(rec)
		sub.Status = StatusRejected
		sub.HeldJSON = nil
		if err := s.repo.PutIntakeSubmission(ctx, *sub); err != nil {
			return nil, err
		}
		audit.Default.Write(audit.WriteRequest{
			Event:   "intake.conflict_declined",
			ActorID: actorID,
			Data: map[string]interface{}{
				"submissionId": sub.ID, "profileId": sub.ProfileID, "reason": reason,
				"conflictingClientId": rec.ConflictingClientID, "matchedAdversary": rec.MatchedAdversary,
			},
		})
		return sub, nil
	}

	var payload heldPayload
	if len(sub.HeldJSON) == 0 || json.Unmarshal(sub.HeldJSON, &payload) != nil || payload.Content == "" {
		return nil, fmt.Errorf("%w: held payload missing; ask the client to re-submit", ErrNotHeld)
	}
	profile, ok, err := s.crm.Get(ctx, sub.ProfileID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("intake: profile %s not found", sub.ProfileID)
	}
	// The acknowledgement covers exactly what the partner was shown.
	shown := rec.holdKeys()
	rec.AcknowledgedHits = unionKeys(rec.AcknowledgedHits, shown)
	rec.Acknowledged, rec.AcknowledgedBy, rec.Reason, rec.AcknowledgedAt = true, actorID, reason, now
	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.conflict_acknowledged",
		ActorID: actorID,
		Data: map[string]interface{}{
			"submissionId": sub.ID, "profileId": sub.ProfileID, "reason": reason,
			"acknowledged":          shown,
			"conflictingClientId":   rec.ConflictingClientID,
			"conflictingClientName": rec.ConflictingClientName,
			"matchedAdversary":      rec.MatchedAdversary,
		},
	})

	// Re-run the check against the roster as it is now. The identity's own
	// other holds don't block this one (each is resolved on its own, and
	// their parties are already in the identity-wide check).
	fresh, err := s.check(ctx, profile, sub.ID, rec.Names, rec.AdverseParties, false)
	if err != nil {
		return nil, err
	}
	fresh.carry(rec)
	fresh.seal()
	sub.Conflict = fresh.HasConflict
	sub.ConflictJSON, _ = json.Marshal(fresh)
	if pending := fresh.unacknowledged(); len(pending) > 0 {
		if err := s.repo.PutIntakeSubmission(ctx, *sub); err != nil {
			return nil, err
		}
		audit.Default.Write(audit.WriteRequest{
			Event:   "intake.conflict_reheld",
			ActorID: actorID,
			Data: map[string]interface{}{
				"submissionId": sub.ID, "profileId": sub.ProfileID, "unacknowledged": pending,
			},
		})
		return sub, ErrReheld
	}

	// isUpdate=false semantics for status: a cleared hold enters the queue.
	if err := s.land(ctx, sub, profile, payload, fresh.CheckedParties, false); err != nil {
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

// ProposeFacts records facts a portal client proposes about themselves
// (pending a lawyer's approval). Only an admitted client with no open
// conflict hold may propose: a prospect's information stays inside its held
// submission until a partner clears it. adverse_party facts run through the
// identity-wide conflict check; a hit no partner has acknowledged flags the
// profile for the firm (and holds the client's next submission).
func (s *Service) ProposeFacts(ctx context.Context, externalRef string, facts []crm.FactInput) ([]store.CRMFact, error) {
	named := collectAdverseParties(nil, facts)
	if len(named) > maxAdverseParties {
		return nil, invalid("at most %d adverse parties per proposal", maxAdverseParties)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	profile, ok, err := s.crm.GetByExternalRef(ctx, strings.TrimSpace(externalRef))
	if err != nil {
		return nil, err
	}
	if !ok || profile.ClientID == "" {
		return nil, ErrNotAdmitted
	}
	st, err := s.identity(ctx, profile)
	if err != nil {
		return nil, err
	}
	if len(st.openHolds) > 0 {
		return nil, ErrConflictHold
	}
	if len(named) > 0 {
		rec, err := s.check(ctx, profile, "", nil, named, false)
		if err != nil {
			return nil, err
		}
		var fresh []types.ConflictCheckResult
		acked := map[string]bool{}
		for _, k := range st.acked {
			acked[k] = true
		}
		for _, h := range rec.Hits {
			if !acked[hitKey(h)] {
				fresh = append(fresh, h)
			}
		}
		if len(fresh) > 0 {
			if err := s.crm.FlagConflict(ctx, profile.ID); err != nil {
				return nil, err
			}
			audit.Default.Write(audit.WriteRequest{
				Event:   "intake.conflict_flagged",
				ActorID: "intake-portal",
				Data: map[string]interface{}{
					"profileId": profile.ID, "hits": fresh, "via": "client_fact_proposal",
				},
			})
		}
	}
	return s.crm.Propose(ctx, profile.ID, crm.Actor{Role: crm.RoleClient, ID: profile.ExternalRef}, "client", facts)
}

// Get returns one submission.
func (s *Service) Get(ctx context.Context, id string) (*store.IntakeSubmission, bool, error) {
	return s.repo.GetIntakeSubmission(ctx, id)
}

// GetForClient returns one submission only if it belongs to the portal
// identity externalRef; any mismatch is reported as not found.
func (s *Service) GetForClient(ctx context.Context, id, externalRef string) (*store.IntakeSubmission, error) {
	externalRef = strings.TrimSpace(externalRef)
	if externalRef == "" {
		return nil, invalid("clientExternalId is required")
	}
	sub, ok, err := s.repo.GetIntakeSubmission(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	profile, ok, err := s.crm.Get(ctx, sub.ProfileID)
	if err != nil {
		return nil, err
	}
	if !ok || profile.ExternalRef != externalRef {
		return nil, ErrNotFound
	}
	return sub, nil
}

// GetForViewer returns one submission a firm member may act on.
func (s *Service) GetForViewer(ctx context.Context, id string, v Viewer) (*store.IntakeSubmission, error) {
	sub, ok, err := s.repo.GetIntakeSubmission(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	if err := v.authorize(sub); err != nil {
		return nil, err
	}
	return sub, nil
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

// Queue returns submissions for the firm view. A partner sees everything;
// a lawyer sees unassigned submissions not on conflict hold plus their own.
func (s *Service) Queue(ctx context.Context, v Viewer) ([]store.IntakeSubmission, error) {
	all, err := s.repo.ListIntakeSubmissions(ctx)
	if err != nil {
		return nil, err
	}
	if v.Partner {
		return all, nil
	}
	out := []store.IntakeSubmission{}
	for _, sub := range all {
		mine := v.ProfileID != "" && sub.AssignedTo == v.ProfileID
		if mine || (sub.AssignedTo == "" && sub.Status != StatusConflictHold) {
			out = append(out, sub)
		}
	}
	return out, nil
}

// Claim assigns a submission to the caller and moves it into review. A
// lawyer may claim only an unassigned submission that is not on conflict
// hold; a partner may claim any (a held one stays held).
func (s *Service) Claim(ctx context.Context, id string, v Viewer) (*store.IntakeSubmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok, err := s.repo.GetIntakeSubmission(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	if !v.Partner {
		if sub.Status == StatusConflictHold {
			return nil, ErrNotFound
		}
		if sub.AssignedTo != "" && sub.AssignedTo != v.ProfileID {
			return nil, fmt.Errorf("%w: submission is assigned to another lawyer", ErrForbidden)
		}
	}
	sub.AssignedTo = v.ProfileID
	if sub.Status == StatusReceived {
		sub.Status = StatusInReview
	}
	sub.UpdatedAt = time.Now()
	if err := s.repo.PutIntakeSubmission(ctx, *sub); err != nil {
		return nil, err
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "intake.submission_claimed",
		ActorID: v.ProfileID,
		Data:    map[string]interface{}{"submissionId": sub.ID},
	})
	return sub, nil
}

// Update sets status and/or note (lawyer-driven state machine; partner or
// assignee only). The conflict hold is entered only by the conflict check
// and left only through ResolveConflict, never by a status patch.
func (s *Service) Update(ctx context.Context, id, status, note string, v Viewer) (*store.IntakeSubmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok, err := s.repo.GetIntakeSubmission(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	if err := v.authorize(sub); err != nil {
		return nil, err
	}
	if status != "" {
		if !validStatuses[status] {
			return nil, invalid("invalid status %q", status)
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
		ActorID: v.ProfileID,
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

// StartTask starts agent work on a submission (partner or assignee only):
// under the service lock it re-reads the submission, checks it is not on
// conflict hold and has an ingested draft, calls start (which submits the
// orchestrator task and returns its id), and records the task — so a
// submission that goes on hold concurrently never gets a task.
func (s *Service) StartTask(ctx context.Context, id string, v Viewer, start func(*store.IntakeSubmission) (string, error)) (*store.IntakeSubmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, err := s.GetForViewer(ctx, id, v)
	if err != nil {
		return nil, err
	}
	if err := s.TaskReady(sub); err != nil {
		return nil, err
	}
	taskID, err := start(sub)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTaskRefused, err)
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
	return h.ConflictingClientID + "|" + clients.NormalizeName(h.MatchedAdversary)
}

func unionKeys(list, add []string) []string {
	seen := map[string]bool{}
	out := append([]string(nil), list...)
	for _, k := range list {
		seen[k] = true
	}
	for _, k := range add {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// collectAdverseParties gathers the adverse parties the client named, from
// the explicit list and from adverse_party facts; cleaned and deduplicated.
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
	return unionNames(nil, names)
}

// unionNames appends to base the cleaned names in add that are new under
// conflict-matching normalisation (so "José García" and "Jose Garcia" are
// one party). Names that normalise empty are dropped.
func unionNames(base, add []string) []string {
	seen := map[string]bool{}
	out := append([]string(nil), base...)
	for _, b := range base {
		seen[clients.NormalizeName(b)] = true
	}
	for _, n := range add {
		n = cleanText(n, maxPartyNameLen)
		k := clients.NormalizeName(n)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	return out
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
