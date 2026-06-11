// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// PrecedentGenerator — firm-precedent document assembly.
// Step 1 (knowledge search): find closest firm precedent documents.
// Step 2 (Haiku): determine clause structure for the document type.
// Step 3: resolve playbook cascade per clause.
// Step 4 (Opus): draft the complete document from firm precedent + playbook.

package precedent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Types ────────────────────────────────────────────────────────────────────

// Clause is a single drafted clause in the precedent document.
type Clause struct {
	Heading    string `json:"heading"`
	DraftText  string `json:"draftText"`
	Source     string `json:"source"`
	HasRedLine bool   `json:"hasRedLine"`
	Notes      string `json:"notes,omitempty"`
	Fallback   string `json:"fallback,omitempty"`
}

// Document is the complete generated precedent.
type Document struct {
	ID                    string   `json:"id"`
	DocumentType          string   `json:"documentType"`
	Title                 string   `json:"title"`
	PracticeArea          string   `json:"practiceArea,omitempty"`
	Jurisdiction          string   `json:"jurisdiction,omitempty"`
	ActingFor             string   `json:"actingFor,omitempty"`
	SourcePrecedentCount  int      `json:"sourcePrecedentCount"`
	PlaybookPositionCount int      `json:"playbookPositionCount"`
	Clauses               []Clause `json:"clauses"`
	DocumentText          string   `json:"document"`
	DraftingNotes         []string `json:"draftingNotes"`
	GeneratedAt           string   `json:"generatedAt"`
}

// GenerateOpts parameterises a document generation run.
type GenerateOpts struct {
	PracticeArea        string
	Jurisdiction        string
	ActingFor           string
	MatterNumber        string
	ClientID            string
	ProfileID           string
	SpecialInstructions string
	TaskID              string
}

// KnowledgeSearcher is the subset of the knowledge store the generator needs.
type KnowledgeSearcher interface {
	Search(query string, topK int) []types.SearchResult
}

type posEntry struct {
	clauseType string
	resolved   *playbook.ResolvedClause
}

// ─── Generator ────────────────────────────────────────────────────────────────

// Generator produces firm-precedent starting-point documents.
type Generator struct {
	provider providers.Provider
	opus     string
	haiku    string
}

// New creates a PrecedentGenerator.
func New(provider providers.Provider, opusModel, haikuModel string) *Generator {
	return &Generator{provider: provider, opus: opusModel, haiku: haikuModel}
}

// Generate assembles a precedent document from firm knowledge + playbook.
func (g *Generator) Generate(documentType string, ks KnowledgeSearcher, store *playbook.Store, opts GenerateOpts) (*Document, error) {
	// Step 1 — find firm precedents
	precedents := g.findPrecedents(documentType, ks, opts)

	// Step 2 — determine clause structure
	clauseTypes := g.determineClauseStructure(documentType, opts)

	// Step 3 — resolve playbook for each clause type
	positions := make([]posEntry, len(clauseTypes))
	playbookPositionCount := 0
	for i, ct := range clauseTypes {
		r := store.Resolve(ct, playbook.ResolveOpts{
			PracticeArea: opts.PracticeArea,
			MatterNumber: opts.MatterNumber,
			ClientID:     opts.ClientID,
			ProfileID:    opts.ProfileID,
		})
		positions[i] = posEntry{clauseType: ct, resolved: r}
		if r != nil {
			playbookPositionCount++
		}
	}

	// Step 4 — draft the document
	clauses, docText, draftingNotes := g.draftDocument(documentType, precedents, positions, opts)

	title := buildTitle(documentType, opts)
	doc := &Document{
		ID:                    uuid.New().String(),
		DocumentType:          documentType,
		Title:                 title,
		PracticeArea:          opts.PracticeArea,
		Jurisdiction:          opts.Jurisdiction,
		ActingFor:             opts.ActingFor,
		SourcePrecedentCount:  len(precedents),
		PlaybookPositionCount: playbookPositionCount,
		Clauses:               clauses,
		DocumentText:          docText,
		DraftingNotes:         draftingNotes,
		GeneratedAt:           time.Now().UTC().Format(time.RFC3339),
	}

	slog.Info("Precedent document generated", "id", doc.ID, "type", documentType, "clauses", len(clauses), "precedents", len(precedents))
	return doc, nil
}

// ─── Step 1: precedent search ─────────────────────────────────────────────────

type precedentDoc struct {
	Title   string
	Content string
}

func (g *Generator) findPrecedents(docType string, ks KnowledgeSearcher, opts GenerateOpts) []precedentDoc {
	if ks == nil {
		return nil
	}
	q := strings.TrimSpace(docType + " " + opts.PracticeArea + " " + opts.Jurisdiction + " precedent standard form")
	results := ks.Search(q, 5)
	out := make([]precedentDoc, 0, len(results))
	for _, r := range results {
		content := r.Excerpt
		if len(r.Document.Content) > 0 && len(r.Document.Content) < 3000 {
			content = r.Document.Content
		}
		if len(content) < 50 {
			continue
		}
		out = append(out, precedentDoc{Title: r.Document.Title, Content: truncate(content, 3000)})
	}
	return out
}

// ─── Step 2: clause structure ─────────────────────────────────────────────────

func (g *Generator) determineClauseStructure(docType string, opts GenerateOpts) []string {
	start := time.Now()
	jur := opts.Jurisdiction
	if jur == "" {
		jur = "English law"
	}
	area := opts.PracticeArea
	if area == "" {
		area = "transactional"
	}

	prompt := fmt.Sprintf(`List the standard clause headings for a %s agreement.
Context: %s, %s.
Return a JSON array of clause type strings:
["Parties","Recitals","Definitions","..."]
Include 8–20 clauses appropriate for this document type. No other text.`, docType, area, jur)

	resp, err := g.provider.Chat(providers.ChatParams{
		Model:     g.haiku,
		MaxTokens: 500,
		Messages:  []providers.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return defaultClauseTypes(docType)
	}

	dms := time.Since(start).Milliseconds()
	recordCost(g.haiku, resp, dms, opts.TaskID)

	raw := textFrom(resp)
	s := strings.Index(raw, "[")
	eIdx := strings.LastIndex(raw, "]")
	if s < 0 || eIdx <= s {
		return defaultClauseTypes(docType)
	}
	var clauses []string
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &clauses); err != nil {
		return defaultClauseTypes(docType)
	}
	return clauses
}

var defaultsByType = map[string][]string{
	"nda": {"Parties", "Recitals", "Definitions", "Confidentiality obligation", "Permitted disclosures",
		"No licence", "Return / destruction", "Duration", "Governing law", "Dispute resolution"},
	"spa": {"Parties", "Recitals", "Definitions", "Sale and purchase", "Consideration",
		"Conditions", "Completion", "Warranties", "Indemnification", "Limitation on claims",
		"MAC/MAE", "Non-compete", "Governing law", "Dispute resolution"},
	"employment": {"Parties", "Appointment", "Duties", "Remuneration", "Benefits",
		"Confidentiality", "IP assignment", "Non-compete", "Non-solicitation",
		"Termination", "Garden leave", "Governing law"},
}

func defaultClauseTypes(docType string) []string {
	if ct, ok := defaultsByType[docType]; ok {
		return ct
	}
	return []string{"Parties", "Recitals", "Definitions", "Operative clauses",
		"Representations and warranties", "Covenants", "Termination", "General provisions", "Governing law"}
}

// ─── Step 4: document drafting (Opus) ────────────────────────────────────────

func (g *Generator) draftDocument(
	docType string,
	precedents []precedentDoc,
	positions []posEntry,
	opts GenerateOpts,
) ([]Clause, string, []string) {
	start := time.Now()

	// Build precedent block
	var precLines []string
	if len(precedents) == 0 {
		precLines = []string{"FIRM PRECEDENT: None found in knowledge store — draft from market standard."}
	} else {
		precLines = []string{fmt.Sprintf("FIRM PRECEDENT EXTRACTS (%d documents):", len(precedents))}
		for i, p := range precedents {
			title := ""
			if p.Title != "" {
				title = ": " + p.Title
			}
			precLines = append(precLines, fmt.Sprintf("--- Precedent %d%s ---\n%s", i+1, title, p.Content))
		}
	}
	precedentBlock := strings.Join(precLines, "\n\n")

	// Build playbook block
	var pbLines []string
	for _, pos := range positions {
		if pos.resolved == nil {
			pbLines = append(pbLines, pos.clauseType+": No playbook position")
			continue
		}
		e := pos.resolved.EffectiveEntry
		fb := "—"
		if e.FallbackPosition != "" {
			fb = e.FallbackPosition
		}
		rl := "none"
		if len(e.RedLines) > 0 {
			rl = strings.Join(e.RedLines, "; ")
		}
		pbLines = append(pbLines, fmt.Sprintf("%s [%s]:\n  Standard: %s\n  Fallback: %s\n  Red lines: %s",
			pos.clauseType, string(pos.resolved.ResolvedFrom), e.StandardPosition, fb, rl))
	}
	playbookBlock := strings.Join(pbLines, "\n\n")

	actingFor := opts.ActingFor
	if actingFor == "" {
		actingFor = "our client (party position TBC)"
	}
	jur := opts.Jurisdiction
	if jur == "" {
		jur = "English law"
	}
	area := opts.PracticeArea
	if area == "" {
		area = "transactional"
	}
	special := ""
	if opts.SpecialInstructions != "" {
		special = "\nSPECIAL INSTRUCTIONS: " + opts.SpecialInstructions
	}

	system := fmt.Sprintf(`You are a senior transactional lawyer at a major law firm drafting a %s agreement.

Your task: produce a complete, clause-ready %s from the firm's own precedent and playbook positions.

ACTING FOR: %s
JURISDICTION: %s
PRACTICE AREA: %s%s

DRAFTING RULES:
1. Prefer firm precedent language — extract verbatim where the passage is appropriate
2. Where playbook positions exist, embed the STANDARD POSITION as the draft text
3. Where a clause has RED LINES, embed a comment in square brackets: [FIRM RED LINE: ...]
4. Use clean, modern drafting — no archaic formulations
5. Mark any placeholder the lawyer must complete as [INSERT: description]
6. Produce output as a JSON object with three keys:
   "clauses": array of clause objects
   "document": the full assembled document as a Markdown string
   "draftingNotes": array of 3–8 short notes for the associate

Clause object schema:
{"heading":"...","draftText":"...","source":"firm|knowledge_store|generated","hasRedLine":false,"notes":"...","fallback":"..."}`,
		docType, docType, actingFor, jur, area, special)

	userContent := precedentBlock + "\n\nPLAYBOOK POSITIONS:\n" + playbookBlock

	resp, err := g.provider.Chat(providers.ChatParams{
		Model:       g.opus,
		MaxTokens:   8000,
		System:      system,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: userContent}},
	})
	if err != nil {
		slog.Warn("PrecedentGenerator: draft failed", "error", err)
		return g.fallbackDraft(docType, positions)
	}

	dms := time.Since(start).Milliseconds()
	recordCost(g.opus, resp, dms, opts.TaskID)

	raw := textFrom(resp)
	s := strings.Index(raw, "{")
	eIdx := strings.LastIndex(raw, "}")
	if s < 0 || eIdx <= s {
		return g.fallbackDraft(docType, positions)
	}

	var parsed struct {
		Clauses       []map[string]interface{} `json:"clauses"`
		Document      string                   `json:"document"`
		DraftingNotes []string                 `json:"draftingNotes"`
	}
	if err := json.Unmarshal([]byte(raw[s:eIdx+1]), &parsed); err != nil {
		return g.fallbackDraft(docType, positions)
	}

	clauses := make([]Clause, 0, len(parsed.Clauses))
	for _, c := range parsed.Clauses {
		clauses = append(clauses, Clause{
			Heading:    strVal(c["heading"]),
			DraftText:  strVal(c["draftText"]),
			Source:     strVal(c["source"]),
			HasRedLine: boolVal(c["hasRedLine"]),
			Notes:      strVal(c["notes"]),
			Fallback:   strVal(c["fallback"]),
		})
	}

	docText := parsed.Document
	if docText == "" {
		docText = "(draft generation failed — see clauses array)"
	}
	return clauses, docText, parsed.DraftingNotes
}

func (g *Generator) fallbackDraft(docType string, positions []posEntry) ([]Clause, string, []string) {
	clauses := make([]Clause, len(positions))
	for i, pos := range positions {
		draftText := "[INSERT standard position]"
		src := "generated"
		hasRL := false
		fb := ""
		if pos.resolved != nil {
			e := pos.resolved.EffectiveEntry
			if e.StandardPosition != "" {
				draftText = e.StandardPosition
			}
			src = string(pos.resolved.ResolvedFrom)
			hasRL = len(e.RedLines) > 0
			fb = e.FallbackPosition
		}
		clauses[i] = Clause{
			Heading:    pos.clauseType,
			DraftText:  draftText,
			Source:     src,
			HasRedLine: hasRL,
			Notes:      "Draft generation failed — manually complete this clause.",
			Fallback:   fb,
		}
	}
	lines := make([]string, len(clauses))
	for i, c := range clauses {
		lines[i] = fmt.Sprintf("## %s\n\n%s", c.Heading, c.DraftText)
	}
	docText := fmt.Sprintf("# %s\n\n%s", strings.ToUpper(docType), strings.Join(lines, "\n\n"))
	return clauses, docText, []string{"Automatic draft generation failed. Review each clause manually against the playbook."}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

var titleLabels = map[string]string{
	"nda":               "Confidentiality Agreement (NDA)",
	"spa":               "Share Purchase Agreement",
	"asset_purchase":    "Asset Purchase Agreement",
	"facility":          "Facility Agreement",
	"employment":        "Contract of Employment",
	"service_agreement": "Professional Services Agreement",
	"supply_agreement":  "Supply and Distribution Agreement",
	"jv_agreement":      "Joint Venture Agreement",
	"ip_assignment":     "IP Assignment Agreement",
	"licence":           "Licence Agreement",
	"settlement":        "Settlement Agreement",
	"term_sheet":        "Heads of Terms",
}

func buildTitle(docType string, opts GenerateOpts) string {
	base, ok := titleLabels[docType]
	if !ok {
		base = docType + " Agreement"
	}
	parts := []string{base}
	if opts.Jurisdiction != "" {
		parts = append(parts, opts.Jurisdiction)
	}
	if opts.ActingFor != "" {
		parts = append(parts, "(acting for "+opts.ActingFor+")")
	}
	return strings.Join(parts, " — ")
}

func recordCost(model string, resp *providers.ChatResponse, dms int64, taskID string) {
	cw, cr := 0, 0
	if resp.Usage.CacheWriteTokens != nil {
		cw = *resp.Usage.CacheWriteTokens
	}
	if resp.Usage.CacheReadTokens != nil {
		cr = *resp.Usage.CacheReadTokens
	}
	costUSD := cost.CalcCostUSD(model, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	cost.Default.Record(cost.RecordRequest{
		Model: model, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheReadTokens: resp.Usage.CacheReadTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "precedent_draft", TaskID: taskID,
	})
}

func textFrom(resp *providers.ChatResponse) string {
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			return blk.Text
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func strVal(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func boolVal(v interface{}) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
