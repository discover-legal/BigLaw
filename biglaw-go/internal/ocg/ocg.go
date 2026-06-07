// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// OcgStore — persists and queries Outside Counsel Guidelines documents.
// Two-phase compliance check: mechanical (math/regex) then semantic (Haiku).

package ocg

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	semanticBatchSize = 8
)

var taskVerbRe = regexp.MustCompile(`(?i)\b(review|draft|research|analyz|prepar|attend|correspond|negotiate|revise|edit|call|confer|meet|discuss|investigat|file|respond|communicat|strateg)\b`)

var vaguePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(reviewed?|drafted?|researched?|analyzed?|prepared?|attended?|discussed?|met|called?|conferr?ed?)\s*\.?$`),
	regexp.MustCompile(`(?i)^(review|draft|research|analysis|preparation|call|meeting)\s*\.?$`),
}

var validMechCheckTypes = map[types.OcgMechCheckType]bool{
	types.OcgMechMinDurationHours:    true,
	types.OcgMechMaxDurationHours:    true,
	types.OcgMechMaxAgeDays:          true,
	types.OcgMechMaxBillingRateUSD:   true,
	types.OcgMechMinDescriptionChars: true,
	types.OcgMechNoBlockBilling:      true,
	types.OcgMechNoVagueEntries:      true,
	types.OcgMechRequireMatterRef:    true,
}

var validCategories = map[string]types.OcgRuleCategory{
	"billing_increments": types.OcgCategoryBillingIncrements,
	"entry_specificity":  types.OcgCategoryEntrySpecificity,
	"prohibited_tasks":   types.OcgCategoryProhibitedTasks,
	"rate_limits":        types.OcgCategoryRateLimits,
	"staffing":           types.OcgCategoryStaffing,
	"description_format": types.OcgCategoryDescriptionFormat,
	"timing":             types.OcgCategoryTiming,
	"other":              types.OcgCategoryOther,
}

// Store persists OCG documents and runs compliance checks.
type Store struct {
	mu       sync.RWMutex
	docs     map[string]*types.OcgDocument // clientID → doc
	path     string
	provider providers.Provider
	haiku    string
}

// NewStore creates an OcgStore backed by the given JSON file path.
func NewStore(path string, provider providers.Provider, haikuModel string) *Store {
	return &Store{
		docs:     make(map[string]*types.OcgDocument),
		path:     path,
		provider: provider,
		haiku:    haikuModel,
	}
}

// Init loads persisted documents from disk.
func (s *Store) Init() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for clientID, docJSON := range raw {
		var doc types.OcgDocument
		if err := json.Unmarshal(docJSON, &doc); err != nil {
			continue
		}
		s.docs[clientID] = &doc
	}
	slog.Info("OCG store loaded", "count", len(s.docs))
	return nil
}

// GetByClient returns the OCG document for a client, or nil.
func (s *Store) GetByClient(clientID string) *types.OcgDocument {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.docs[clientID]
}

// Remove deletes the OCG document for a client.
func (s *Store) Remove(clientID string) error {
	s.mu.Lock()
	delete(s.docs, clientID)
	s.mu.Unlock()
	return s.persist()
}

// Ingest extracts structured rules from OCG text via Haiku and persists the document.
func (s *Store) Ingest(clientID, title, text string) (*types.OcgDocument, error) {
	sanitized := sanitizeOCG(text)
	if len(sanitized) > 60000 {
		sanitized = sanitized[:60000]
	}
	excerpt := sanitized
	if len(excerpt) > 500 {
		excerpt = excerpt[:500]
	}

	prompt := fmt.Sprintf(`You are extracting billing rules from an Outside Counsel Guidelines document.
Return a JSON array of rules. Each rule must have:
  - category: one of billing_increments | entry_specificity | prohibited_tasks | rate_limits | staffing | description_format | timing | other
  - text: the rule in plain English, concise (max 200 chars)
  - severity: "hard" (billing violation, will be rejected) or "soft" (style preference)
  - mechCheck: (optional) a structured object for rules that can be checked with pure math or string analysis:
      {"type":"min_duration_hours","value":0.1}
      {"type":"max_duration_hours","value":8}
      {"type":"max_age_days","value":30}
      {"type":"max_billing_rate_usd","value":750}
      {"type":"min_description_chars","value":50}
      {"type":"no_block_billing"}
      {"type":"no_vague_entries"}
      {"type":"require_matter_reference"}
    Omit mechCheck entirely for rules that require judgment or context to evaluate.

Focus only on billing and time-entry rules. Ignore unrelated provisions.

OCG text:
%s

Respond with ONLY a valid JSON array, no markdown, no prose:
[{"category":"...","text":"...","severity":"..."},...]`, sanitized)

	raw := s.callHaiku(prompt, 4096, "")
	raw = strings.ReplaceAll(raw, "```json", "")
	raw = strings.ReplaceAll(raw, "```", "")
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")

	var rules []types.OcgRule
	if start >= 0 && end > start {
		var rawRules []map[string]interface{}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &rawRules); err == nil {
			for _, r := range rawRules {
				rText, _ := r["text"].(string)
				if rText == "" {
					continue
				}
				rText = strings.TrimSpace(rText)
				if len(rText) > 200 {
					rText = rText[:200]
				}
				catStr, _ := r["category"].(string)
				cat, ok := validCategories[catStr]
				if !ok {
					cat = types.OcgCategoryOther
				}
				sev := "soft"
				if sevStr, _ := r["severity"].(string); sevStr == "hard" {
					sev = "hard"
				}
				rule := types.OcgRule{
					ID:       uuid.New().String(),
					Category: cat,
					Text:     rText,
					Severity: sev,
				}
				if mc, ok := r["mechCheck"].(map[string]interface{}); ok {
					mcType, _ := mc["type"].(string)
					if validMechCheckTypes[types.OcgMechCheckType(mcType)] {
						check := &types.OcgMechCheck{Type: types.OcgMechCheckType(mcType)}
						if v, ok := mc["value"].(float64); ok {
							check.Value = &v
						}
						rule.MechCheck = check
					}
				}
				rules = append(rules, rule)
			}
		}
	}

	now := time.Now().UTC()
	s.mu.Lock()
	existing := s.docs[clientID]
	var docID string
	var createdAt time.Time
	if existing != nil {
		docID = existing.ID
		createdAt = existing.CreatedAt
	} else {
		docID = uuid.New().String()
		createdAt = now
	}
	doc := &types.OcgDocument{
		ID:        docID,
		ClientID:  clientID,
		Title:     strings.TrimSpace(title),
		Rules:     rules,
		Excerpt:   excerpt,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}
	s.docs[clientID] = doc
	s.mu.Unlock()

	if err := s.persist(); err != nil {
		slog.Warn("OCG persist failed", "error", err)
	}
	slog.Info("OCG ingested", "clientId", clientID, "title", title, "ruleCount", len(rules))
	return doc, nil
}

// CheckEntry checks one time entry against all rules in an OCG document.
// Returns suggestions (violations); empty slice means the entry passes.
func (s *Store) CheckEntry(entry types.TimeEntry, ocgDoc *types.OcgDocument) ([]types.OcgSuggestion, error) {
	if ocgDoc == nil || len(ocgDoc.Rules) == 0 {
		return nil, nil
	}

	legacyMech := map[types.OcgRuleCategory]bool{
		types.OcgCategoryBillingIncrements: true,
		types.OcgCategoryTiming:            true,
	}

	var mechRules, semanticRules []types.OcgRule
	for _, r := range ocgDoc.Rules {
		if r.MechCheck != nil || legacyMech[r.Category] {
			mechRules = append(mechRules, r)
		} else {
			semanticRules = append(semanticRules, r)
		}
	}

	mechanical := checkMechanically(entry, mechRules)
	semantic, err := s.checkSemantically(entry, semanticRules)
	if err != nil {
		return mechanical, err
	}
	return append(mechanical, semantic...), nil
}

// RecordViolations increments violation counts for each fired rule.
func (s *Store) RecordViolations(clientID string, suggestions []types.OcgSuggestion) {
	if len(suggestions) == 0 {
		return
	}
	s.mu.Lock()
	doc := s.docs[clientID]
	if doc != nil {
		if doc.RuleStats == nil {
			doc.RuleStats = make(map[string]*types.OcgRuleStat)
		}
		for _, sg := range suggestions {
			st := doc.RuleStats[sg.RuleID]
			if st == nil {
				st = &types.OcgRuleStat{}
				doc.RuleStats[sg.RuleID] = st
			}
			st.Violations++
		}
	}
	s.mu.Unlock()
	go s.persist() //nolint:errcheck
}

// RecordOutcome records that a suggestion was accepted or dismissed.
func (s *Store) RecordOutcome(clientID, ruleID, outcome string) {
	s.mu.Lock()
	doc := s.docs[clientID]
	if doc != nil {
		if doc.RuleStats == nil {
			doc.RuleStats = make(map[string]*types.OcgRuleStat)
		}
		st := doc.RuleStats[ruleID]
		if st == nil {
			st = &types.OcgRuleStat{}
			doc.RuleStats[ruleID] = st
		}
		if outcome == "accepted" {
			st.Accepted++
		} else {
			st.Dismissed++
		}
	}
	s.mu.Unlock()
	go s.persist() //nolint:errcheck
}

// ─── Mechanical checker ───────────────────────────────────────────────────────

func checkMechanically(entry types.TimeEntry, rules []types.OcgRule) []types.OcgSuggestion {
	var out []types.OcgSuggestion
	entryHours := float64(entry.DurationMs) / 3_600_000.0
	entryAgeMs := time.Since(entry.StartedAt).Milliseconds()
	desc := strings.TrimSpace(entry.Description)

	for _, rule := range rules {
		if rule.MechCheck != nil {
			mc := rule.MechCheck
			safeVal := 0.0
			if mc.Value != nil && *mc.Value > 0 {
				safeVal = *mc.Value
			}

			switch mc.Type {
			case types.OcgMechMinDurationHours:
				if safeVal > 0 && entry.DurationMs > 0 && entryHours < safeVal {
					out = append(out, makeSuggestion(rule, entry, fmt.Sprintf("Duration %.2fh is below required minimum %.2fh", entryHours, safeVal)))
				}
			case types.OcgMechMaxDurationHours:
				if safeVal > 0 && entry.DurationMs > 0 && entryHours > safeVal {
					out = append(out, makeSuggestion(rule, entry, fmt.Sprintf("Duration %.2fh exceeds maximum %.2fh per entry", entryHours, safeVal)))
				}
			case types.OcgMechMaxAgeDays:
				if safeVal > 0 && entryAgeMs > 0 {
					ageDays := float64(entryAgeMs) / 86_400_000.0
					if ageDays > safeVal {
						out = append(out, makeSuggestion(rule, entry, fmt.Sprintf("Entry is %d days old; must be submitted within %.0f days", int(ageDays), safeVal)))
					}
				}
			case types.OcgMechMaxBillingRateUSD:
				if safeVal > 0 && entry.BillingRate != nil && *entry.BillingRate > safeVal {
					out = append(out, makeSuggestion(rule, entry, fmt.Sprintf("Billing rate $%.0f/hr exceeds client cap of $%.0f/hr", *entry.BillingRate, safeVal)))
				}
			case types.OcgMechMinDescriptionChars:
				if safeVal > 0 && len(desc) < int(safeVal) {
					out = append(out, makeSuggestion(rule, entry, fmt.Sprintf("Description is %d characters; minimum required is %.0f", len(desc), safeVal)))
				}
			case types.OcgMechNoBlockBilling:
				matches := taskVerbRe.FindAllString(desc, -1)
				unique := map[string]bool{}
				for _, m := range matches {
					unique[strings.ToLower(m)] = true
				}
				if len(unique) >= 3 {
					out = append(out, makeSuggestion(rule, entry, fmt.Sprintf("Description appears to combine %d distinct tasks — potential block billing", len(unique))))
				}
			case types.OcgMechNoVagueEntries:
				for _, p := range vaguePatterns {
					if p.MatchString(desc) {
						out = append(out, makeSuggestion(rule, entry, fmt.Sprintf(`Description %q is too vague — must specify the subject matter`, desc)))
						break
					}
				}
			case types.OcgMechRequireMatterRef:
				if entry.MatterNumber == "" {
					out = append(out, makeSuggestion(rule, entry, "Entry is missing a matter number reference"))
				}
			}
			continue
		}

		// Legacy fallback for rules without MechCheck
		t := strings.ToLower(rule.Text)
		switch rule.Category {
		case types.OcgCategoryBillingIncrements:
			if entry.DurationMs > 0 {
				minHours := parseLegacyMinHours(t)
				if minHours > 0 && entryHours < minHours {
					out = append(out, makeSuggestion(rule, entry, fmt.Sprintf("Duration %.2fh below required minimum %.2fh", entryHours, minHours)))
				}
			}
		case types.OcgCategoryTiming:
			if entryAgeMs > 0 {
				maxDays := parseLegacyMaxDays(t)
				if maxDays > 0 {
					ageDays := float64(entryAgeMs) / 86_400_000.0
					if ageDays > float64(maxDays) {
						out = append(out, makeSuggestion(rule, entry, fmt.Sprintf("Entry is %d days old; must be submitted within %d days", int(ageDays), maxDays)))
					}
				}
			}
		}
	}
	return out
}

func makeSuggestion(rule types.OcgRule, entry types.TimeEntry, issue string) types.OcgSuggestion {
	_ = entry
	return types.OcgSuggestion{
		RuleID:   rule.ID,
		RuleText: rule.Text,
		Category: rule.Category,
		Severity: rule.Severity,
		Issue:    issue,
		Status:   "pending",
	}
}

var legacyHoursRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*-?\s*h(?:ou)?r`)
var legacyMinsRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*-?\s*min(?:ute)?`)
var legacyDaysRe = regexp.MustCompile(`(\d+)\s*days?`)

func parseLegacyMinHours(t string) float64 {
	if m := legacyHoursRe.FindStringSubmatch(t); m != nil {
		var v float64
		fmt.Sscanf(m[1], "%f", &v)
		return v
	}
	if m := legacyMinsRe.FindStringSubmatch(t); m != nil {
		var v float64
		fmt.Sscanf(m[1], "%f", &v)
		return v / 60
	}
	if strings.Contains(t, "one-tenth") || strings.Contains(t, "1/10") {
		return 0.1
	}
	return 0
}

func parseLegacyMaxDays(t string) int {
	if m := legacyDaysRe.FindStringSubmatch(t); m != nil {
		var v int
		fmt.Sscanf(m[1], "%d", &v)
		return v
	}
	return 0
}

// ─── Semantic checker (Haiku) ─────────────────────────────────────────────────

func (s *Store) checkSemantically(entry types.TimeEntry, rules []types.OcgRule) ([]types.OcgSuggestion, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	var all []types.OcgSuggestion
	ruleDict := map[string]types.OcgRule{}
	for _, r := range rules {
		ruleDict[r.ID] = r
	}

	for i := 0; i < len(rules); i += semanticBatchSize {
		end := i + semanticBatchSize
		if end > len(rules) {
			end = len(rules)
		}
		batch := rules[i:end]

		type entryData struct {
			Description  string `json:"description"`
			DurationHours string `json:"durationHours"`
			Event        string `json:"event"`
			BillingUnits int    `json:"billingUnits"`
		}
		type ruleItem struct {
			ID       string                `json:"id"`
			Category types.OcgRuleCategory `json:"category"`
			Text     string                `json:"text"`
			Severity string                `json:"severity"`
		}

		ed := entryData{
			Description:  entry.Description,
			DurationHours: fmt.Sprintf("%.2f", float64(entry.DurationMs)/3_600_000.0),
			Event:        string(entry.Event),
			BillingUnits: entry.BillingUnits,
		}
		edJSON, _ := json.Marshal(ed)

		items := make([]ruleItem, len(batch))
		for j, r := range batch {
			items[j] = ruleItem{ID: r.ID, Category: r.Category, Text: r.Text, Severity: r.Severity}
		}
		rulesJSON, _ := json.Marshal(items)

		prompt := fmt.Sprintf(`You are an Outside Counsel Guidelines (OCG) compliance checker.

Evaluate the time entry against each rule in the RULES array.
Return ONLY rules that are violated. Skip rules the entry already satisfies.

TIME ENTRY:
%s

RULES (each object has a unique "id" field):
%s

For each violated rule return an object with EXACTLY these fields:
{"ruleId":"<exact id value from the rule object>","issue":"<what the entry does wrong, max 120 chars>","suggestedDescription":"<rewritten description that would comply, max 300 chars>"}

Return a JSON array. Use [] if no violations. ONLY the array — no markdown, no prose.`,
			string(edJSON), string(rulesJSON))

		raw := s.callHaiku(prompt, 1024, "")
		raw = strings.ReplaceAll(raw, "```json", "")
		raw = strings.ReplaceAll(raw, "```", "")
		raw = strings.TrimSpace(raw)
		start := strings.Index(raw, "[")
		last := strings.LastIndex(raw, "]")
		if start < 0 || last <= start {
			continue
		}

		var violations []map[string]interface{}
		if err := json.Unmarshal([]byte(raw[start:last+1]), &violations); err != nil {
			continue
		}
		for _, v := range violations {
			ruleID, _ := v["ruleId"].(string)
			issue, _ := v["issue"].(string)
			if ruleID == "" || issue == "" {
				continue
			}
			rule, ok := ruleDict[ruleID]
			if !ok {
				continue
			}
			if len(issue) > 120 {
				issue = issue[:120]
			}
			suggested, _ := v["suggestedDescription"].(string)
			if len(suggested) > 300 {
				suggested = suggested[:300]
			}
			all = append(all, types.OcgSuggestion{
				RuleID:               rule.ID,
				RuleText:             rule.Text,
				Category:             rule.Category,
				Severity:             rule.Severity,
				Issue:                issue,
				SuggestedDescription: suggested,
				Status:               "pending",
			})
		}
	}
	return all, nil
}

func (s *Store) callHaiku(prompt string, maxTokens int, profileID string) string {
	start := time.Now()
	resp, err := s.provider.Chat(providers.ChatParams{
		Model:     s.haiku,
		MaxTokens: maxTokens,
		Messages:  []providers.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		slog.Warn("OCG Haiku call failed", "error", err)
		return ""
	}
	dms := time.Since(start).Milliseconds()
	costUSD := cost.CalcCostUSD(s.haiku, resp.Usage.InputTokens, resp.Usage.OutputTokens, 0, 0)
	cost.Default.Record(cost.RecordRequest{
		Model: s.haiku, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "ocg_extraction", ProfileID: profileID,
	})
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			return blk.Text
		}
	}
	return ""
}

func (s *Store) persist() error {
	s.mu.RLock()
	out := make(map[string]*types.OcgDocument, len(s.docs))
	for k, v := range s.docs {
		out[k] = v
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func sanitizeOCG(s string) string {
	s = strings.ReplaceAll(s, "FINDING:", "")
	s = strings.ReplaceAll(s, "END_FINDING", "")
	s = strings.ReplaceAll(s, "NO_FINDINGS", "")
	s = strings.ReplaceAll(s, "NO_CHALLENGE", "")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
