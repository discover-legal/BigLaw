// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package crm is the neurosymbolic client-profile engine.
//
// Symbolic layer: typed facts (category, predicate, value) with full
// provenance and a bidirectional-consent state machine — a fact proposed by
// the client awaits a lawyer's approval; a fact proposed by a lawyer awaits
// the client's approval. Only approved facts form the profile. Rules fire on
// approval: same-key supersedence, adverse-party conflict checks against the
// roster, and advocacy sync into the per-matter ClientVoice brief so the
// orchestrator surfaces the client's stated goals at human gates.
//
// Neural layer: approved facts are embedded (same embeddings client as the
// knowledge store) into an in-memory cosine index per profile, rebuilt on
// boot — enabling semantic profile queries by lawyers and agents.
package crm

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/clientvoice"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Categories is the closed fact-category vocabulary. Unknown categories
// coerce to "note".
var Categories = []string{
	"identity", "contact", "family", "financial", "employment", "matter",
	"adverse_party", "goal", "concern", "constraint", "preference", "history", "note",
}

// advocacyCategories are compiled into the ClientVoice brief on approval.
var advocacyCategories = map[string]bool{
	"goal": true, "concern": true, "constraint": true, "preference": true,
}

const (
	RoleClient = "client"
	RoleLawyer = "lawyer"

	StatusPending    = "pending"
	StatusApproved   = "approved"
	StatusRejected   = "rejected"
	StatusSuperseded = "superseded"

	maxFactValueLen = 2000
	maxFactNoteLen  = 2000
	maxFactsPerCall = 20
)

// Actor identifies who is proposing or deciding. Role is RoleClient or
// RoleLawyer; ID is the portal externalRef (client) or profile id (lawyer).
type Actor struct {
	Role string
	ID   string
}

// FactInput is one proposed fact, pre-validation.
type FactInput struct {
	Category  string `json:"category"`
	Predicate string `json:"predicate"`
	Value     string `json:"value"`
	Note      string `json:"note,omitempty"`
}

// ScoredFact is a semantic-query hit.
type ScoredFact struct {
	Fact  store.CRMFact `json:"fact"`
	Score float64       `json:"score"`
}

// ProfileView is the full consent-aware read of one profile.
type ProfileView struct {
	Profile               store.CRMProfile `json:"profile"`
	Facts                 []store.CRMFact  `json:"facts"`                 // approved, newest-first
	PendingClientApproval []store.CRMFact  `json:"pendingYourApproval"`   // lawyer-proposed
	PendingLawyerApproval []store.CRMFact  `json:"pendingLawyerApproval"` // client-proposed
}

// Service is the CRM engine. Safe for concurrent use.
type Service struct {
	repo    store.CRMRepository
	clients *clients.ClientStore
	embed   *embeddings.Client

	mu      sync.RWMutex
	vectors map[string][]float32 // factID → embedding (approved facts only)

	voiceMu sync.RWMutex
	voice   *clientvoice.Store // optional; nil disables advocacy sync
}

// New builds the service. repo must not be nil; embed may be nil (semantic
// query then falls back to substring scoring).
func New(repo store.CRMRepository, clientStore *clients.ClientStore, embed *embeddings.Client) *Service {
	return &Service{
		repo:    repo,
		clients: clientStore,
		embed:   embed,
		vectors: map[string][]float32{},
	}
}

// SetClientVoice wires the advocacy brief store; approved advocacy facts are
// compiled into it per matter.
func (s *Service) SetClientVoice(cv *clientvoice.Store) {
	s.voiceMu.Lock()
	s.voice = cv
	s.voiceMu.Unlock()
}

func (s *Service) clientVoice() *clientvoice.Store {
	s.voiceMu.RLock()
	defer s.voiceMu.RUnlock()
	return s.voice
}

// Load rebuilds the neural index from approved facts. Call once at boot;
// embedding failures degrade to the lexical fallback, never error.
func (s *Service) Load() error {
	ctx := store.WithSystem(context.Background())
	profiles, err := s.repo.ListCRMProfiles(ctx)
	if err != nil {
		return fmt.Errorf("crm: load profiles: %w", err)
	}
	var texts []string
	var ids []string
	for _, p := range profiles {
		facts, err := s.repo.ListCRMFacts(ctx, p.ID, StatusApproved)
		if err != nil {
			return fmt.Errorf("crm: load facts for %s: %w", p.ID, err)
		}
		for _, f := range facts {
			ids = append(ids, f.ID)
			texts = append(texts, factText(f))
		}
	}
	if len(texts) == 0 || s.embed == nil {
		return nil
	}
	results, err := s.embed.EmbedBatch(texts)
	if err != nil || len(results) != len(ids) {
		slog.Warn("crm: boot embedding failed; semantic query degrades to lexical", "error", err)
		return nil
	}
	s.mu.Lock()
	for i, id := range ids {
		s.vectors[id] = results[i].Embedding
	}
	s.mu.Unlock()
	return nil
}

// ─── Profiles ────────────────────────────────────────────────────────────────

// EnsureProfile finds or creates the CRM profile (and the roster client) for
// a portal identity. Resolution: profile by externalRef → roster client by
// normalized name (profile attached) → create both.
func (s *Service) EnsureProfile(ctx context.Context, externalRef, email, name string) (*store.CRMProfile, error) {
	externalRef = strings.TrimSpace(externalRef)
	if externalRef == "" {
		return nil, fmt.Errorf("crm: externalRef is required")
	}
	if p, ok, err := s.repo.FindCRMProfileByExternalRef(ctx, externalRef); err != nil {
		return nil, err
	} else if ok {
		return p, nil
	}

	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name == "" {
		name = email
	}
	if name == "" {
		return nil, fmt.Errorf("crm: name or email required to create a profile")
	}

	// Attach to an existing roster client with the same normalized name, or
	// create a fresh one with an auto-assigned AM- client number.
	rosterClient := s.matchRosterByName(name)
	if rosterClient == nil {
		num, err := s.nextClientNumber()
		if err != nil {
			return nil, err
		}
		created, err := s.clients.Create(name, num, nil, "Created via affidavit-maker intake")
		if err != nil {
			return nil, fmt.Errorf("crm: create roster client: %w", err)
		}
		rosterClient = created
	}

	// A roster client carries at most one CRM profile; adopt it if present
	// (e.g. profile created firm-side before the portal identity linked up).
	if existing, ok, err := s.repo.FindCRMProfileByClientID(ctx, rosterClient.ID); err != nil {
		return nil, err
	} else if ok && existing.ExternalRef == "" {
		existing.ExternalRef = externalRef
		if email != "" {
			existing.Email = email
		}
		existing.UpdatedAt = time.Now()
		if err := s.repo.PutCRMProfile(ctx, *existing); err != nil {
			return nil, err
		}
		return existing, nil
	}

	p := store.CRMProfile{
		ID:          uuid.New().String(),
		ClientID:    rosterClient.ID,
		ExternalRef: externalRef,
		Email:       email,
		Name:        rosterClient.Name,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.repo.PutCRMProfile(ctx, p); err != nil {
		return nil, err
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "crm.profile_created",
		ActorID: "intake",
		Data: map[string]interface{}{
			"profileId": p.ID, "clientId": p.ClientID, "clientNumber": rosterClient.ClientNumber,
		},
	})
	return &p, nil
}

func (s *Service) matchRosterByName(name string) *types.Client {
	want := normalizeName(name)
	if want == "" {
		return nil
	}
	for _, c := range s.clients.List() {
		if normalizeName(c.Name) == want {
			cp := c
			return &cp
		}
	}
	return nil
}

func (s *Service) nextClientNumber() (string, error) {
	for i := len(s.clients.List()) + 1; i < len(s.clients.List())+10_000; i++ {
		num := fmt.Sprintf("AM-%d", i)
		if s.clients.GetByClientNumber(num) == nil {
			return num, nil
		}
	}
	return "", fmt.Errorf("crm: could not allocate a client number")
}

// Get returns a profile by id.
func (s *Service) Get(ctx context.Context, profileID string) (*store.CRMProfile, bool, error) {
	return s.repo.GetCRMProfile(ctx, profileID)
}

// GetByExternalRef returns a profile by portal identity.
func (s *Service) GetByExternalRef(ctx context.Context, ref string) (*store.CRMProfile, bool, error) {
	return s.repo.FindCRMProfileByExternalRef(ctx, ref)
}

// List returns all profiles.
func (s *Service) List(ctx context.Context) ([]store.CRMProfile, error) {
	return s.repo.ListCRMProfiles(ctx)
}

// View assembles the consent-aware read of a profile.
func (s *Service) View(ctx context.Context, profileID string) (*ProfileView, error) {
	p, ok, err := s.repo.GetCRMProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	approved, err := s.repo.ListCRMFacts(ctx, profileID, StatusApproved)
	if err != nil {
		return nil, err
	}
	pending, err := s.repo.ListCRMFacts(ctx, profileID, StatusPending)
	if err != nil {
		return nil, err
	}
	view := &ProfileView{Profile: *p, Facts: approved}
	for _, f := range pending {
		if f.ApproverRole == RoleClient {
			view.PendingClientApproval = append(view.PendingClientApproval, f)
		} else {
			view.PendingLawyerApproval = append(view.PendingLawyerApproval, f)
		}
	}
	return view, nil
}

// ─── Proposals & decisions ───────────────────────────────────────────────────

// Propose records new facts as pending, awaiting the counterparty's approval.
// A client actor's facts await a lawyer; a lawyer actor's facts await the
// client. source tags provenance ("client", "lawyer", "intake", "system").
func (s *Service) Propose(ctx context.Context, profileID string, actor Actor, source string, inputs []FactInput) ([]store.CRMFact, error) {
	if actor.Role != RoleClient && actor.Role != RoleLawyer {
		return nil, fmt.Errorf("crm: actor role must be %q or %q", RoleClient, RoleLawyer)
	}
	if _, ok, err := s.repo.GetCRMProfile(ctx, profileID); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("crm: profile %s not found", profileID)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("crm: facts are required")
	}
	if len(inputs) > maxFactsPerCall {
		inputs = inputs[:maxFactsPerCall]
	}
	approver := RoleLawyer
	if actor.Role == RoleLawyer {
		approver = RoleClient
	}
	if source == "" {
		source = actor.Role
	}

	var out []store.CRMFact
	for _, in := range inputs {
		value := strutil.Truncate(strings.TrimSpace(in.Value), maxFactValueLen)
		if value == "" {
			continue
		}
		f := store.CRMFact{
			ID:           uuid.New().String(),
			ProfileID:    profileID,
			Category:     coerceCategory(in.Category),
			Predicate:    strutil.Truncate(strings.TrimSpace(in.Predicate), 64),
			Value:        value,
			Note:         strutil.Truncate(strings.TrimSpace(in.Note), maxFactNoteLen),
			Source:       source,
			ProposedBy:   actor.Role + ":" + actor.ID,
			ApproverRole: approver,
			Status:       StatusPending,
			CreatedAt:    time.Now(),
		}
		if err := s.repo.PutCRMFact(ctx, f); err != nil {
			return out, err
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("crm: no valid facts in proposal")
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "crm.facts_proposed",
		ActorID: actor.Role + ":" + actor.ID,
		Data: map[string]interface{}{
			"profileId": profileID, "count": len(out), "approverRole": approver,
		},
	})
	return out, nil
}

// Decide approves or rejects a pending fact. The decider's role must match
// the fact's ApproverRole; a client decider must own the profile
// (Actor.ID == profile.ExternalRef). Approval fires the symbolic rules.
func (s *Service) Decide(ctx context.Context, factID string, actor Actor, approve bool, note string) (*store.CRMFact, error) {
	f, ok, err := s.repo.GetCRMFact(ctx, factID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	if f.Status != StatusPending {
		return nil, fmt.Errorf("%w: fact is %s", ErrNotPending, f.Status)
	}
	if f.ApproverRole != actor.Role {
		return nil, fmt.Errorf("%w: approver role is %s", ErrForbidden, f.ApproverRole)
	}
	profile, ok, err := s.repo.GetCRMProfile(ctx, f.ProfileID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("crm: profile %s not found", f.ProfileID)
	}
	if actor.Role == RoleClient && profile.ExternalRef != actor.ID {
		return nil, fmt.Errorf("%w: proposal belongs to another client", ErrForbidden)
	}

	now := time.Now()
	f.DecidedBy = actor.Role + ":" + actor.ID
	f.DecisionNote = strutil.Truncate(strings.TrimSpace(note), maxFactNoteLen)
	f.DecidedAt = &now

	if !approve {
		f.Status = StatusRejected
		if err := s.repo.PutCRMFact(ctx, *f); err != nil {
			return nil, err
		}
		audit.Default.Write(audit.WriteRequest{
			Event:   "crm.fact_rejected",
			ActorID: f.DecidedBy,
			Data:    map[string]interface{}{"factId": f.ID, "profileId": f.ProfileID},
		})
		return f, nil
	}

	f.Status = StatusApproved
	if err := s.repo.PutCRMFact(ctx, *f); err != nil {
		return nil, err
	}

	// Rule 1 — supersedence: an approved fact replaces any earlier approved
	// fact with the same (category, predicate) key.
	if f.Predicate != "" {
		approved, err := s.repo.ListCRMFacts(ctx, f.ProfileID, StatusApproved)
		if err == nil {
			for _, old := range approved {
				if old.ID != f.ID && old.Category == f.Category && old.Predicate == f.Predicate {
					old.Status = StatusSuperseded
					old.SupersededBy = f.ID
					if perr := s.repo.PutCRMFact(ctx, old); perr == nil {
						s.mu.Lock()
						delete(s.vectors, old.ID)
						s.mu.Unlock()
					}
				}
			}
		}
	}

	// Rule 2 — conflict watch: an approved adverse-party fact re-runs the
	// roster conflict check (is the adverse party an existing client, or is
	// this client on someone's adversary list?); hits flag the profile.
	if f.Category == "adverse_party" && s.clients != nil {
		res := s.clients.CheckConflict(profile.Name, []string{f.Value})
		if res.HasConflict {
			profile.ConflictFlag = true
			profile.UpdatedAt = time.Now()
			_ = s.repo.PutCRMProfile(ctx, *profile)
			audit.Default.Write(audit.WriteRequest{
				Event:   "crm.conflict_flagged",
				ActorID: f.DecidedBy,
				Data: map[string]interface{}{
					"profileId": f.ProfileID, "factId": f.ID,
					"conflictingClientId": res.ConflictingClientID,
					"matchedAdversary":    res.MatchedAdversary,
				},
			})
		}
	}

	// Rule 3 — advocacy sync: goals/concerns/constraints/preferences flow
	// into the per-matter ClientVoice brief.
	if advocacyCategories[f.Category] {
		s.syncAdvocacy(ctx, profile)
	}

	// Neural layer: index the newly approved fact.
	s.indexFact(*f)

	audit.Default.Write(audit.WriteRequest{
		Event:   "crm.fact_approved",
		ActorID: f.DecidedBy,
		Data: map[string]interface{}{
			"factId": f.ID, "profileId": f.ProfileID, "category": f.Category, "predicate": f.Predicate,
		},
	})
	return f, nil
}

// Facts lists a profile's facts filtered by status ("" = all).
func (s *Service) Facts(ctx context.Context, profileID, status string) ([]store.CRMFact, error) {
	return s.repo.ListCRMFacts(ctx, profileID, status)
}

// PendingQueue is the firm-wide pending list for one approver role.
func (s *Service) PendingQueue(ctx context.Context, approverRole string) ([]store.CRMFact, error) {
	if approverRole != RoleClient && approverRole != RoleLawyer {
		approverRole = RoleLawyer
	}
	return s.repo.ListPendingCRMFacts(ctx, approverRole)
}

// ─── Neural layer ────────────────────────────────────────────────────────────

// Query ranks a profile's approved facts against a natural-language query.
// With an embeddings client it is cosine over the fact index (lazily filling
// missing vectors); otherwise a lexical overlap fallback.
func (s *Service) Query(ctx context.Context, profileID, query string, topK int) ([]ScoredFact, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("crm: query is required")
	}
	if topK <= 0 || topK > 50 {
		topK = 10
	}
	facts, err := s.repo.ListCRMFacts(ctx, profileID, StatusApproved)
	if err != nil {
		return nil, err
	}
	if len(facts) == 0 {
		return []ScoredFact{}, nil
	}

	var qvec []float32
	if s.embed != nil {
		if res, err := s.embed.Embed(query); err == nil && res != nil {
			qvec = res.Embedding
		}
	}

	scored := make([]ScoredFact, 0, len(facts))
	for _, f := range facts {
		var score float64
		if qvec != nil {
			vec := s.factVector(f)
			if vec != nil {
				score = embeddings.CosineSimilarity(qvec, vec)
			}
		}
		if score == 0 {
			score = lexicalScore(query, factText(f))
		}
		scored = append(scored, ScoredFact{Fact: f, Score: score})
	}
	sort.Slice(scored, func(a, b int) bool { return scored[a].Score > scored[b].Score })
	if len(scored) > topK {
		scored = scored[:topK]
	}
	return scored, nil
}

func (s *Service) factVector(f store.CRMFact) []float32 {
	s.mu.RLock()
	vec, ok := s.vectors[f.ID]
	s.mu.RUnlock()
	if ok {
		return vec
	}
	if s.embed == nil {
		return nil
	}
	res, err := s.embed.Embed(factText(f))
	if err != nil || res == nil {
		return nil
	}
	s.mu.Lock()
	s.vectors[f.ID] = res.Embedding
	s.mu.Unlock()
	return res.Embedding
}

func (s *Service) indexFact(f store.CRMFact) {
	if s.embed == nil {
		return
	}
	res, err := s.embed.Embed(factText(f))
	if err != nil || res == nil {
		return
	}
	s.mu.Lock()
	s.vectors[f.ID] = res.Embedding
	s.mu.Unlock()
}

// ─── Advocacy sync (ClientVoice) ─────────────────────────────────────────────

// syncAdvocacy compiles the profile's approved advocacy facts into a
// ClientVoice brief for every matter of the roster client.
func (s *Service) syncAdvocacy(ctx context.Context, profile *store.CRMProfile) {
	cv := s.clientVoice()
	if cv == nil || s.clients == nil {
		return
	}
	rosterClient := s.clients.Get(profile.ClientID)
	if rosterClient == nil || len(rosterClient.Matters) == 0 {
		return
	}
	approved, err := s.repo.ListCRMFacts(ctx, profile.ID, StatusApproved)
	if err != nil {
		return
	}
	var entries []types.ClientVoiceEntry
	for i := len(approved) - 1; i >= 0; i-- { // oldest-first reads naturally
		f := approved[i]
		if !advocacyCategories[f.Category] {
			continue
		}
		note := f.Value
		if f.Note != "" {
			note += " (" + f.Note + ")"
		}
		entries = append(entries, types.ClientVoiceEntry{
			Category: f.Category,
			Note:     strutil.Truncate(note, 2000),
			At:       f.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	if len(entries) == 0 {
		return
	}
	for _, m := range rosterClient.Matters {
		cv.SetVoice(types.ClientVoice{
			MatterNumber: m.MatterNumber,
			ClientID:     rosterClient.ID,
			Source:       "crm",
			Entries:      entries,
		})
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "crm.advocacy_synced",
		ActorID: "crm",
		Data: map[string]interface{}{
			"profileId": profile.ID, "entries": len(entries), "matters": len(rosterClient.Matters),
		},
	})
}

// SyncAdvocacyForProfile re-compiles the advocacy brief (e.g. after a new
// matter is opened for the client).
func (s *Service) SyncAdvocacyForProfile(ctx context.Context, profileID string) {
	p, ok, err := s.repo.GetCRMProfile(ctx, profileID)
	if err != nil || !ok {
		return
	}
	s.syncAdvocacy(ctx, p)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// Sentinel errors the API layer maps to status codes.
var (
	ErrNotFound   = fmt.Errorf("crm: not found")
	ErrNotPending = fmt.Errorf("crm: not pending")
	ErrForbidden  = fmt.Errorf("crm: forbidden")
)

func coerceCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	for _, known := range Categories {
		if c == known {
			return c
		}
	}
	return "note"
}

func factText(f store.CRMFact) string {
	parts := []string{f.Category}
	if f.Predicate != "" {
		parts = append(parts, f.Predicate)
	}
	parts = append(parts, f.Value)
	if f.Note != "" {
		parts = append(parts, f.Note)
	}
	return strings.Join(parts, ": ")
}

func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, cut := range []string{",", "."} {
		s = strings.ReplaceAll(s, cut, "")
	}
	return strings.Join(strings.Fields(s), " ")
}

// lexicalScore is the no-embeddings fallback: fraction of query terms present.
func lexicalScore(query, text string) float64 {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return 0
	}
	lower := strings.ToLower(text)
	hits := 0
	for _, t := range terms {
		if strings.Contains(lower, t) {
			hits++
		}
	}
	return float64(hits) / float64(len(terms))
}
