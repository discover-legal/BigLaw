// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// PlaybookStore — four-tier clause-position repository + AI builder.
// Cascade: client (3) > matter (2) > personal (1) > firm (0).
// PlaybookBuilder searches the knowledge store and uses Haiku to extract
// the firm's market positions from precedent documents.

package playbook

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Priority map ─────────────────────────────────────────────────────────────

var scopePriority = map[types.PlaybookScope]int{
	types.PlaybookScopeFirm:     0,
	types.PlaybookScopePersonal: 1,
	types.PlaybookScopeMatter:   2,
	types.PlaybookScopeClient:   3,
}

// ─── Extended types ───────────────────────────────────────────────────────────

// ResolvedClause is the winner of a cascade resolution.
type ResolvedClause struct {
	ClauseType       string              `json:"clauseType"`
	PracticeArea     string              `json:"practiceArea"`
	EffectiveEntry   types.PlaybookEntry `json:"effectiveEntry"`
	ResolvedFrom     types.PlaybookScope `json:"resolvedFrom"`
	AvailableTiers   []types.PlaybookScope `json:"availableTiers"`
	PersonalNote     string              `json:"personalNote,omitempty"`
}

// PlaybookQueryResult is the full output of resolveAll.
type PlaybookQueryResult struct {
	ClauseType      string           `json:"clauseType"`
	PracticeArea    string           `json:"practiceArea,omitempty"`
	MatterNumber    string           `json:"matterNumber,omitempty"`
	ClientID        string           `json:"clientId,omitempty"`
	ProfileID       string           `json:"profileId,omitempty"`
	Resolved        []ResolvedClause `json:"resolved"`
	CascadeSummary  string           `json:"cascadeSummary"`
	QueriedAt       string           `json:"queriedAt"`
}

// ResolveOpts parameterises a cascade resolution.
type ResolveOpts struct {
	PracticeArea string
	MatterNumber string
	ClientID     string
	ProfileID    string
}

// ─── Store ────────────────────────────────────────────────────────────────────

// Store holds all playbooks in memory and persists to a JSON file.
type Store struct {
	mu        sync.RWMutex
	playbooks []types.Playbook
	path      string
}

// New creates an empty Store backed by path.
func New(path string) *Store { return &Store{path: path} }

// Init loads persisted playbooks from disk.
func (s *Store) Init() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(data, &s.playbooks)
}

// List returns playbooks matching the optional filters.
func (s *Store) List(scope types.PlaybookScope, ownerID, practiceArea string) []types.Playbook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.playbooks[:0:0]
	for _, p := range s.playbooks {
		if scope != "" && p.Scope != scope {
			continue
		}
		if ownerID != "" && p.OwnerID != ownerID {
			continue
		}
		if practiceArea != "" && p.PracticeArea != practiceArea {
			continue
		}
		out = append(out, p)
	}
	return out
}

// GetByID returns a playbook by ID, or nil.
func (s *Store) GetByID(id string) *types.Playbook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.playbooks {
		if s.playbooks[i].ID == id {
			cp := s.playbooks[i]
			return &cp
		}
	}
	return nil
}

// Upsert adds or updates a playbook.
func (s *Store) Upsert(p types.Playbook) {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	for i := range s.playbooks {
		if s.playbooks[i].ID == p.ID {
			p.UpdatedAt = now
			s.playbooks[i] = p
			s.mu.Unlock()
			s.persist()
			return
		}
	}
	s.playbooks = append(s.playbooks, p)
	s.mu.Unlock()
	s.persist()
}

// Delete removes a playbook. Returns false if not found.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	for i, p := range s.playbooks {
		if p.ID == id {
			s.playbooks = append(s.playbooks[:i], s.playbooks[i+1:]...)
			s.mu.Unlock()
			s.persist()
			return true
		}
	}
	s.mu.Unlock()
	return false
}

// Resolve runs the cascade for a single clauseType.
// Returns nil when no playbook has a position for it.
func (s *Store) Resolve(clauseType string, opts ResolveOpts) *ResolvedClause {
	scopes := s.applicableScopes(opts)
	ownerFor := map[types.PlaybookScope]string{
		types.PlaybookScopeClient:   opts.ClientID,
		types.PlaybookScopeMatter:   opts.MatterNumber,
		types.PlaybookScopePersonal: opts.ProfileID,
	}

	byScope := map[types.PlaybookScope]types.PlaybookEntry{}
	s.mu.RLock()
	for _, scope := range scopes {
		oid := ownerFor[scope]
		for _, pb := range s.playbooks {
			if pb.Scope != scope {
				continue
			}
			if scope != types.PlaybookScopeFirm && pb.OwnerID != oid {
				continue
			}
			if opts.PracticeArea != "" && pb.PracticeArea != opts.PracticeArea {
				continue
			}
			for _, e := range pb.Entries {
				if _, already := byScope[scope]; !already &&
					strings.EqualFold(e.ClauseType, clauseType) {
					byScope[scope] = e
				}
			}
		}
	}
	s.mu.RUnlock()

	if len(byScope) == 0 {
		return nil
	}

	winner := types.PlaybookScopeFirm
	for _, sc := range scopes {
		if _, ok := byScope[sc]; ok {
			if scopePriority[sc] > scopePriority[winner] {
				winner = sc
			}
		}
	}
	if _, ok := byScope[winner]; !ok {
		for _, sc := range scopes {
			if _, ok2 := byScope[sc]; ok2 {
				winner = sc
				break
			}
		}
	}

	tiers := make([]types.PlaybookScope, 0, len(byScope))
	for _, sc := range scopes {
		if _, ok := byScope[sc]; ok {
			tiers = append(tiers, sc)
		}
	}

	personalNote := ""
	if pe, ok := byScope[types.PlaybookScopePersonal]; ok && winner != types.PlaybookScopePersonal {
		personalNote = pe.StandardPosition
	}

	effective := byScope[winner]
	return &ResolvedClause{
		ClauseType:     clauseType,
		PracticeArea:   effective.PracticeArea,
		EffectiveEntry: effective,
		ResolvedFrom:   winner,
		AvailableTiers: tiers,
		PersonalNote:   personalNote,
	}
}

// ResolveAll cascades all clause types across applicable playbooks.
func (s *Store) ResolveAll(opts ResolveOpts) PlaybookQueryResult {
	scopes := s.applicableScopes(opts)
	ownerFor := map[types.PlaybookScope]string{
		types.PlaybookScopeClient:   opts.ClientID,
		types.PlaybookScopeMatter:   opts.MatterNumber,
		types.PlaybookScopePersonal: opts.ProfileID,
	}

	clauseSet := map[string]struct{}{}
	s.mu.RLock()
	for _, scope := range scopes {
		oid := ownerFor[scope]
		for _, pb := range s.playbooks {
			if pb.Scope != scope {
				continue
			}
			if scope != types.PlaybookScopeFirm && pb.OwnerID != oid {
				continue
			}
			if opts.PracticeArea != "" && pb.PracticeArea != opts.PracticeArea {
				continue
			}
			for _, e := range pb.Entries {
				clauseSet[e.ClauseType] = struct{}{}
			}
		}
	}
	s.mu.RUnlock()

	resolved := make([]ResolvedClause, 0, len(clauseSet))
	for ct := range clauseSet {
		if r := s.Resolve(ct, opts); r != nil {
			resolved = append(resolved, *r)
		}
	}

	tierLabels := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		tierLabels = append(tierLabels, string(sc))
	}
	clientWins := 0
	for _, r := range resolved {
		if r.ResolvedFrom == types.PlaybookScopeClient {
			clientWins++
		}
	}
	summary := fmt.Sprintf(
		"Resolved %d clause types. Cascade [%s]. Client requirements applied in %d/%d clauses.",
		len(resolved), strings.Join(tierLabels, " → "), clientWins, len(resolved),
	)

	return PlaybookQueryResult{
		ClauseType:     "*",
		PracticeArea:   opts.PracticeArea,
		MatterNumber:   opts.MatterNumber,
		ClientID:       opts.ClientID,
		ProfileID:      opts.ProfileID,
		Resolved:       resolved,
		CascadeSummary: summary,
		QueriedAt:      time.Now().UTC().Format(time.RFC3339),
	}
}

func (s *Store) applicableScopes(opts ResolveOpts) []types.PlaybookScope {
	scopes := []types.PlaybookScope{types.PlaybookScopeFirm}
	if opts.ProfileID != "" {
		scopes = append(scopes, types.PlaybookScopePersonal)
	}
	if opts.MatterNumber != "" {
		scopes = append(scopes, types.PlaybookScopeMatter)
	}
	if opts.ClientID != "" {
		scopes = append(scopes, types.PlaybookScopeClient)
	}
	return scopes
}

func (s *Store) persist() {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.playbooks, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		slog.Warn("PlaybookStore persist marshal failed", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		slog.Warn("PlaybookStore persist mkdir failed", "error", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Warn("PlaybookStore persist write failed", "error", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Warn("PlaybookStore persist rename failed", "error", err)
	}
}

// ─── Builder ──────────────────────────────────────────────────────────────────

// KnowledgeSearcher is the subset of the knowledge store the builder needs.
type KnowledgeSearcher interface {
	Search(query string, opts interface{}) ([]types.SearchResult, error)
}

// BuildOpts configures a playbook build.
type BuildOpts struct {
	Scope        types.PlaybookScope
	OwnerID      string
	OwnerName    string
	PracticeArea string
	Jurisdiction string
	Name         string
	Description  string
	ClauseTypes  []string
	TaskID       string
}

// Builder uses AI to extract firm positions from knowledge store precedents.
type Builder struct {
	provider providers.Provider
	haiku    string
}

// NewBuilder creates a Builder.
func NewBuilder(provider providers.Provider, haikuModel string) *Builder {
	return &Builder{provider: provider, haiku: haikuModel}
}

// Build searches for precedents and extracts clause positions, persisting to store.
func (b *Builder) Build(store *Store, kSearch func(query string, topK int) []types.SearchResult, opts BuildOpts) (*types.Playbook, error) {
	clauseTypes := opts.ClauseTypes
	if len(clauseTypes) == 0 {
		clauseTypes = clauseTypesForArea(opts.PracticeArea)
	}

	q := strings.TrimSpace(opts.PracticeArea + " " + opts.Jurisdiction + " contract precedent clauses positions")
	results := kSearch(q, 30)

	docIDs := map[string]struct{}{}
	for _, r := range results {
		docIDs[r.Document.ID] = struct{}{}
	}
	docCount := len(docIDs)

	excerpts := make([]string, 0, 15)
	for i, r := range results {
		if i >= 15 {
			break
		}
		excerpts = append(excerpts, r.Excerpt)
	}
	excerptText := strings.Join(excerpts, "\n\n---\n\n")

	const batchSize = 5
	var entries []types.PlaybookEntry
	for i := 0; i < len(clauseTypes); i += batchSize {
		end := i + batchSize
		if end > len(clauseTypes) {
			end = len(clauseTypes)
		}
		batch := clauseTypes[i:end]
		be := b.extractBatch(batch, excerptText, opts, docCount)
		entries = append(entries, be...)
	}

	ct := make([]string, 0, len(entries))
	for _, e := range entries {
		ct = append(ct, e.ClauseType)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	pb := types.Playbook{
		ID:               uuid.New().String(),
		Scope:            opts.Scope,
		OwnerID:          opts.OwnerID,
		OwnerName:        opts.OwnerName,
		Name:             opts.Name,
		Description:      opts.Description,
		PracticeArea:     opts.PracticeArea,
		Jurisdiction:     opts.Jurisdiction,
		ClauseTypes:      ct,
		Entries:          entries,
		DocumentCount:    docCount,
		CreatedAt:        now,
		UpdatedAt:        now,
		GeneratedByTaskID: opts.TaskID,
	}
	store.Upsert(pb)
	slog.Info("Playbook built", "id", pb.ID, "scope", opts.Scope, "area", opts.PracticeArea, "entries", len(entries))
	return &pb, nil
}

func (b *Builder) extractBatch(clauseTypes []string, excerpts string, opts BuildOpts, docCount int) []types.PlaybookEntry {
	start := time.Now()

	numbered := make([]string, len(clauseTypes))
	for i, c := range clauseTypes {
		numbered[i] = fmt.Sprintf("%d. %s", i+1, c)
	}

	scopeLabel := map[types.PlaybookScope]string{
		types.PlaybookScopeFirm:     "firm-wide defaults",
		types.PlaybookScopeClient:   "client-specific positions",
		types.PlaybookScopeMatter:   "deal-specific negotiated positions",
		types.PlaybookScopePersonal: "personal lawyer preferences",
	}[opts.Scope]

	jur := opts.Jurisdiction
	if jur == "" {
		jur = "the governing jurisdiction"
	}

	system := fmt.Sprintf(`You are a senior %s transactional lawyer extracting the firm's market positions from precedent documents.

SCOPE: %s — %s.

For each clause type, extract:
- standardPosition: the firm's typical opening position (1–3 sentences)
- fallbackPosition: the acceptable compromise position (1–2 sentences)
- redLines: list of 2–4 absolute limits
- dealPoints: list of 2–4 key negotiating observations
- exampleLanguage: up to 2 short verbatim excerpt fragments

Return a JSON array. One object per clause type:
[{"clauseType":"...","standardPosition":"...","fallbackPosition":"...","redLines":["..."],"dealPoints":["..."],"exampleLanguage":["..."]}]

If insufficient data: standardPosition: "Insufficient precedent data — apply firm standard market position for %s." redLines: ["Do not agree without partner sign-off"]`,
		opts.PracticeArea, strings.ToUpper(string(opts.Scope)), scopeLabel, jur)

	userMsg := fmt.Sprintf("Extract firm positions for these %s clause types:\n%s\n\nSource precedent excerpts (%d documents):\n%s",
		opts.PracticeArea, strings.Join(numbered, "\n"), docCount, truncate(excerpts, 8000))

	resp, err := b.provider.Chat(providers.ChatParams{
		Model:       b.haiku,
		MaxTokens:   2048,
		System:      system,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: userMsg}},
	})
	if err != nil {
		slog.Warn("PlaybookBuilder batch failed", "error", err)
		return fallbackEntries(clauseTypes, opts.PracticeArea)
	}

	dms := time.Since(start).Milliseconds()
	cw, cr := 0, 0
	if resp.Usage.CacheWriteTokens != nil {
		cw = *resp.Usage.CacheWriteTokens
	}
	if resp.Usage.CacheReadTokens != nil {
		cr = *resp.Usage.CacheReadTokens
	}
	costUSD := cost.CalcCostUSD(b.haiku, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	cost.Default.Record(cost.RecordRequest{
		Model: b.haiku, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheReadTokens: resp.Usage.CacheReadTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "playbook_build", TaskID: opts.TaskID,
	})

	raw := ""
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			raw = blk.Text
			break
		}
	}
	s := strings.Index(raw, "[")
	eIdx := strings.LastIndex(raw, "]")
	if s < 0 || eIdx <= s {
		return fallbackEntries(clauseTypes, opts.PracticeArea)
	}

	var parsed []map[string]interface{}
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &parsed); err != nil {
		return fallbackEntries(clauseTypes, opts.PracticeArea)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entries := make([]types.PlaybookEntry, 0, len(parsed))
	for _, p := range parsed {
		e := types.PlaybookEntry{
			ClauseType:          strVal(p["clauseType"]),
			PracticeArea:        opts.PracticeArea,
			StandardPosition:    strVal(p["standardPosition"]),
			FallbackPosition:    strVal(p["fallbackPosition"]),
			RedLines:            strSlice(p["redLines"]),
			DealPoints:          strSlice(p["dealPoints"]),
			ExampleLanguage:     strSlice(p["exampleLanguage"]),
			SourceDocumentCount: docCount,
			LastUpdated:         now,
		}
		entries = append(entries, e)
	}
	return entries
}

func fallbackEntries(clauseTypes []string, practiceArea string) []types.PlaybookEntry {
	now := time.Now().UTC().Format(time.RFC3339)
	entries := make([]types.PlaybookEntry, len(clauseTypes))
	for i, ct := range clauseTypes {
		entries[i] = types.PlaybookEntry{
			ClauseType:       ct,
			PracticeArea:     practiceArea,
			StandardPosition: "Extraction failed — review source documents manually.",
			RedLines:         []string{"Do not agree without partner sign-off"},
			DealPoints:       []string{},
			LastUpdated:      now,
		}
	}
	return entries
}

// ─── Clause type catalogue ────────────────────────────────────────────────────

var clauseTypesByArea = map[string][]string{
	"Corporate & M&A": {
		"MAC/MAE definition", "Representations and warranties", "Indemnification cap",
		"Indemnification basket/deductible", "Survival period", "Non-compete", "Non-solicitation",
		"Exclusivity", "Break fee", "Reverse break fee", "No-shop / no-talk",
		"Condition precedent to closing", "Regulatory approval condition",
		"Material contracts definition", "Earnout mechanism",
	},
	"Banking & Finance": {
		"Financial covenants", "Events of default", "Cross-default", "Change of control",
		"Prepayment mechanics", "Margin call", "Negative pledge", "Pari passu",
		"Restricted payments", "Permitted disposals", "Clean-up period",
	},
	"Employment & Labour": {
		"Garden leave", "Post-termination restrictions", "Notice period",
		"Confidentiality obligation", "IP assignment", "Bonus clawback",
		"Dispute resolution mechanism", "Jurisdiction clause",
	},
	"Real Estate": {
		"Rent review mechanism", "Break clause", "Alienation provisions",
		"Service charge cap", "Dilapidations regime", "SDLT treatment",
		"Lease length", "Repair obligation",
	},
	"Intellectual Property": {
		"IP ownership", "Background IP", "Foreground IP", "Licence scope",
		"Sub-licensing rights", "Royalty rate", "Audit rights", "Infringement indemnity",
	},
	"Data Privacy & Cybersecurity": {
		"Data processing agreement terms", "Sub-processor provisions",
		"Breach notification timeline", "Data retention periods",
		"Cross-border transfer mechanism", "Data subject rights handling",
	},
}

func clauseTypesForArea(practiceArea string) []string {
	if ct, ok := clauseTypesByArea[practiceArea]; ok {
		return ct
	}
	return clauseTypesByArea["Corporate & M&A"]
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func strVal(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func strSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, strVal(item))
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
