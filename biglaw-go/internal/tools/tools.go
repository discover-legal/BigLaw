// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Tool system — all agent-callable capabilities.
// Tools implement the ToolRegistry interface consumed by agents/base.go.

package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/rag"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// ToolImpl holds the schema and executor for a single tool.
type ToolImpl struct {
	Name   string
	Schema providers.ToolParam
	Exec   func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error)
}

// Registry implements agents.ToolRegistry.
type Registry struct {
	tools   map[string]*ToolImpl
	cfg     *config.Config
	provReg *providers.Registry
	costs   *cost.Store
	rag     *rag.Service
}

func NewRegistry(cfg *config.Config, provReg *providers.Registry, costs *cost.Store, ragSvc *rag.Service) *Registry {
	r := &Registry{
		tools:   map[string]*ToolImpl{},
		cfg:     cfg,
		provReg: provReg,
		costs:   costs,
		rag:     ragSvc,
	}
	r.registerAll()
	return r
}

func (r *Registry) SchemasFor(toolNames []string) []providers.ToolParam {
	out := make([]providers.ToolParam, 0, len(toolNames))
	for _, name := range toolNames {
		if t, ok := r.tools[name]; ok {
			out = append(out, t.Schema)
		}
	}
	return out
}

func (r *Registry) Execute(name string, input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	start := time.Now()
	actor := ctx.ResponsibleLawyerID
	if actor == "" {
		actor = audit.ActorSystem
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "tool.call",
		ActorID: actor,
		TaskID:  ctx.TaskID,
		Data:    map[string]interface{}{"tool": name, "input": input},
	})
	result, err := t.Exec(input, ctx)
	durationMs := time.Since(start).Milliseconds()
	audit.Default.Write(audit.WriteRequest{
		Event:      "tool.result",
		ActorID:    actor,
		TaskID:     ctx.TaskID,
		DurationMs: &durationMs,
		Data:       map[string]interface{}{"tool": name, "ok": err == nil},
	})
	return result, err
}

func (r *Registry) Register(t *ToolImpl) {
	r.tools[t.Name] = t
}

func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

func strInput(input map[string]interface{}, key string) string {
	v, _ := input[key].(string)
	return v
}

func intInput(input map[string]interface{}, key string, def int) int {
	switch v := input[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

// ─── registerAll wires all tool implementations ────────────────────────────

func (r *Registry) registerAll() {
	r.Register(r.webSearchTool())
	r.Register(r.searchKnowledgeTool())
	r.Register(r.searchChunksTool())
	r.Register(r.extractSpecificsTool())
	r.Register(r.getOutlineTool())
	r.Register(r.readSectionTool())
	r.Register(r.queryMemoryTool())
	r.Register(r.extractFromDocumentTool())
	r.Register(r.translateTool())
	r.Register(r.citationCheckTool())
	r.Register(r.readDocumentTool())
	r.Register(r.listDocumentsTool())
	r.Register(r.findInDocumentTool())
	// Connector stubs — return structured errors when unconfigured.
	r.registerConnectors()
	r.registerClioTools()
	// Document production: docx generation, replication, tracked-change redlining.
	r.registerDocxTools()
	r.registerTrackedChangesTools()
	r.registerPdfTools()
	r.registerDocusealTools()
	r.registerTabularTools()
	r.registerDocumentExtraTools()
}

// ─── web_search ──────────────────────────────────────────────────────────────

func (r *Registry) webSearchTool() *ToolImpl {
	return &ToolImpl{
		Name: "web_search",
		Schema: providers.ToolParam{
			Name:        "web_search",
			Description: "Search the web for legal information. Prioritise official legislation portals, court and regulator sites, and reputable legal databases for the relevant jurisdiction.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":       map[string]interface{}{"type": "string", "description": "Search query"},
					"max_results": map[string]interface{}{"type": "number", "description": "Maximum results (default 5)"},
				},
				"required": []string{"query"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			query := strInput(input, "query")
			maxResults := intInput(input, "max_results", 5)
			if r.cfg.SearchTavily == "" {
				return map[string]interface{}{
					"results": []interface{}{},
					"warning": "Web search unavailable: TAVILY_API_KEY not configured",
				}, nil
			}
			return tavilySearch(r.cfg.SearchTavily, query, maxResults)
		},
	}
}

// ─── search_knowledge ─────────────────────────────────────────────────────────

// passagePreviewTokens bounds each search_knowledge snippet — a small,
// query-relevant verbatim window an agent can quote, kept well under a local
// model's context window (~180 tokens ≈ a long paragraph).
const passagePreviewTokens = 400

func (r *Registry) searchKnowledgeTool() *ToolImpl {
	return &ToolImpl{
		Name: "search_knowledge",
		Schema: providers.ToolParam{
			Name:        "search_knowledge",
			Description: "Semantic search across ingested documents in the knowledge store.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string", "description": "Search query"},
					"top_k": map[string]interface{}{"type": "number", "description": "Number of results (default 6)"},
				},
				"required": []string{"query"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			if ctx.KnowledgeStore == nil {
				return map[string]interface{}{"results": []interface{}{}}, nil
			}
			query := strInput(input, "query")
			results, err := ctx.KnowledgeStore.Search(query, ctx.OwnerID, intInput(input, "top_k", 6))
			if err != nil {
				return nil, err
			}
			// Return small, query-relevant VERBATIM passages — not whole
			// documents. Tool calling is meant to pull only what's needed; a
			// full-document dump (tens of KB) blows a small model's context
			// window and forces it to paraphrase. A focused passage keeps the
			// context lean and gives the agent exact text it can quote.
			out := make([]map[string]interface{}, 0, len(results))
			for _, r := range results {
				out = append(out, map[string]interface{}{
					"id":      r.Document.ID,
					"title":   r.Document.Title,
					"score":   r.Score,
					"snippet": relevantPassage(r.Document.Content, query, passagePreviewTokens),
				})
			}
			return map[string]interface{}{"results": out}, nil
		},
	}
}

// ─── search_chunks / get_outline / read_section (hybrid RAG) ───────────────────

func (r *Registry) searchChunksTool() *ToolImpl {
	return &ToolImpl{
		Name: "search_chunks",
		Schema: providers.ToolParam{
			Name:        "search_chunks",
			Description: "Hybrid semantic + keyword search over the matter's documents. Returns the most relevant VERBATIM passages, each with its document title and section locator — quote from these.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string", "description": "What to find"},
					"top_k": map[string]interface{}{"type": "number", "description": "Number of passages (default 6)"},
				},
				"required": []string{"query"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if r.rag == nil {
				return map[string]interface{}{"results": []interface{}{}}, nil
			}
			chunks := r.rag.Search(strInput(input, "query"), intInput(input, "top_k", 6))
			out := make([]map[string]interface{}, 0, len(chunks))
			for _, c := range chunks {
				m := map[string]interface{}{
					"id": c.ID, "title": c.DocTitle, "locator": c.Locator, "snippet": c.Text,
				}
				if c.Context != "" { // table rows: the sheet + column headers, so a cryptic row reads clearly
					m["context"] = c.Context
				}
				out = append(out, m)
			}
			return map[string]interface{}{"results": out}, nil
		},
	}
}

// extractSpecificsTool is the on-demand "specifics hunter": a figure-targeted
// retrieval that surfaces the exact data points a conceptual query misses — dollar
// amounts, percentages, dates, counts, account numbers, statutory citations. It
// augments the topic with quantitative terms and keeps only passages that actually
// contain figures, so the table rows (now findable via table-aware chunking) come
// back ready for the staged extractor to copy verbatim. Same result shape as
// search_chunks, so its passages flow straight into finding extraction.
func (r *Registry) extractSpecificsTool() *ToolImpl {
	return &ToolImpl{
		Name: "extract_specifics",
		Schema: providers.ToolParam{
			Name:        "extract_specifics",
			Description: "Retrieve the SPECIFIC FIGURES for a topic — exact dollar amounts, percentages, dates, counts, account numbers, and statutory citations — that a general search misses. Use this whenever the task needs hard numbers or precise references. Returns verbatim passages (mostly exhibit/table rows); quote the figures from them exactly.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topic": map[string]interface{}{"type": "string", "description": "The subject whose specific figures/references you need"},
					"top_k": map[string]interface{}{"type": "number", "description": "Number of passages (default 12)"},
				},
				"required": []string{"topic"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if r.rag == nil {
				return map[string]interface{}{"results": []interface{}{}}, nil
			}
			topic := strInput(input, "topic")
			topK := intInput(input, "top_k", 12)
			// Search the TOPIC directly, then keep only the figure-bearing rows. An
			// earlier version blended the topic with a soup of quantitative keywords
			// ("amount percent date account …"), which matched figure-rows in EVERY
			// exhibit and BURIED the topic's own rows (e.g. the cherry-picking $7.8M row
			// under generic bank-statement amounts). Pure-topic + figure filter keeps
			// the topic signal and still returns only specifics. Over-fetch, then trim.
			chunks := r.rag.Search(topic, topK*3)
			out := make([]map[string]interface{}, 0, topK)
			for _, c := range chunks {
				if len(out) >= topK {
					break
				}
				if !containsFigure(c.Text) && !containsFigure(c.EmbedText) {
					continue // keep only passages that actually carry specifics
				}
				m := map[string]interface{}{
					"id": c.ID, "title": c.DocTitle, "locator": c.Locator, "snippet": c.Text,
				}
				if c.Context != "" {
					m["context"] = c.Context
				}
				out = append(out, m)
			}
			return map[string]interface{}{"results": out}, nil
		},
	}
}

// containsFigure reports whether text carries a specific data point worth hunting:
// a digit (covers amounts, %, dates, counts, account numbers, statute subsections).
func containsFigure(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func (r *Registry) getOutlineTool() *ToolImpl {
	return &ToolImpl{
		Name: "get_outline",
		Schema: providers.ToolParam{
			Name:        "get_outline",
			Description: "List the section outline (section locators) of a document so you can navigate to a specific part.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"doc_id": map[string]interface{}{"type": "string", "description": "Document id"}},
				"required":   []string{"doc_id"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if r.rag == nil {
				return map[string]interface{}{"sections": []interface{}{}}, nil
			}
			seen := map[string]bool{}
			secs := []string{}
			for _, c := range r.rag.Outline(strInput(input, "doc_id")) {
				if c.Locator != "" && !seen[c.Locator] {
					seen[c.Locator] = true
					secs = append(secs, c.Locator)
				}
			}
			return map[string]interface{}{"sections": secs}, nil
		},
	}
}

func (r *Registry) readSectionTool() *ToolImpl {
	return &ToolImpl{
		Name: "read_section",
		Schema: providers.ToolParam{
			Name:        "read_section",
			Description: "Read the verbatim text of a specific section of a document, by the locator from get_outline.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"doc_id":  map[string]interface{}{"type": "string", "description": "Document id"},
					"locator": map[string]interface{}{"type": "string", "description": "Section locator from get_outline"},
				},
				"required": []string{"doc_id", "locator"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if r.rag == nil {
				return map[string]interface{}{"text": ""}, nil
			}
			var b strings.Builder
			for _, c := range r.rag.Section(strInput(input, "doc_id"), strInput(input, "locator")) {
				b.WriteString(c.Text)
				b.WriteString("\n")
			}
			return map[string]interface{}{"text": strings.TrimSpace(b.String()), "locator": strInput(input, "locator")}, nil
		},
	}
}

// relevantPassage returns a verbatim, query-relevant excerpt of content within a
// maxTokens token budget, snapped to rune and word boundaries. It anchors on the
// densest cluster of query-term matches and returns the surrounding window. The
// result is ALWAYS a contiguous, byte-exact substring of content, so an agent can
// copy a sentence from it and have the citation gate verify the quote against the
// source. Falls back to the head of content when no query term matches.
func relevantPassage(content, query string, maxTokens int) string {
	content = strings.TrimSpace(content)
	maxLen := strutil.TokenBudgetToChars(maxTokens)
	if maxLen <= 0 || len(content) <= maxLen {
		return content
	}

	start := 0
	if positions := termPositions(content, query); len(positions) > 0 {
		bestCount := -1
		for _, p := range positions {
			s := p - maxLen/4
			if s < 0 {
				s = 0
			}
			end := s + maxLen
			count := 0
			for _, q := range positions {
				if q >= s && q < end {
					count++
				}
			}
			if count > bestCount {
				bestCount, start = count, s
			}
		}
	}

	end := start + maxLen
	if end > len(content) {
		end = len(content)
		if start = end - maxLen; start < 0 {
			start = 0
		}
	}
	// Snap to rune boundaries so the slice never splits a multibyte character.
	for start > 0 && !utf8.RuneStart(content[start]) {
		start++
	}
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	excerpt := content[start:end]
	// Snap to SENTENCE boundaries so the passage is whole sentences. A passage that
	// cut a sentence yields a quote the downstream extractor must complete (breaking
	// verbatim) or that fails the substring check and is dropped. Start after the
	// first sentence terminator; end at the last one. Fall back to word boundaries
	// when no terminator is present. Still a contiguous verbatim substring.
	if start > 0 {
		if i := strings.IndexAny(excerpt, ".!?"); i >= 0 && i < len(excerpt)-1 {
			excerpt = strings.TrimLeft(excerpt[i+1:], " \n\t")
		} else if i := strings.IndexAny(excerpt, " \n\t"); i >= 0 && i < len(excerpt)-1 {
			excerpt = excerpt[i+1:]
		}
	}
	if end < len(content) {
		if i := strings.LastIndexAny(excerpt, ".!?"); i > 0 {
			excerpt = excerpt[:i+1]
		} else if i := strings.LastIndexAny(excerpt, " \n\t"); i > 0 {
			excerpt = excerpt[:i]
		}
	}
	return strings.TrimSpace(excerpt)
}

// termPositions returns the byte offsets in content (case-insensitive) of every
// occurrence of each distinct query term of 3+ characters. Offsets index into
// content; they only steer windowing, so the rare ToLower length shift on exotic
// Unicode cannot affect the verbatim bytes relevantPassage returns.
func termPositions(content, query string) []int {
	lc := strings.ToLower(content)
	seen := map[string]bool{}
	var positions []int
	for _, t := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len(t) < 3 || seen[t] {
			continue
		}
		seen[t] = true
		for i := 0; ; {
			j := strings.Index(lc[i:], t)
			if j < 0 {
				break
			}
			positions = append(positions, i+j)
			i += j + len(t)
		}
	}
	return positions
}

// ─── query_memory ─────────────────────────────────────────────────────────────

func (r *Registry) queryMemoryTool() *ToolImpl {
	return &ToolImpl{
		Name: "query_memory",
		Schema: providers.ToolParam{
			Name:        "query_memory",
			Description: "Query inter-round memory for findings and summaries from earlier rounds of this task.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":    map[string]interface{}{"type": "string", "description": "What to look for in memory"},
					"top_k":    map[string]interface{}{"type": "number", "description": "Number of memory entries (default 6)"},
					"agent_id": map[string]interface{}{"type": "string", "description": "Optional: restrict to a specific agent's memories"},
				},
				"required": []string{"query"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			if ctx.MemoryStore == nil {
				return map[string]interface{}{"entries": []interface{}{}}, nil
			}
			return ctx.MemoryStore.Query(
				strInput(input, "query"),
				ctx.TaskID,
				strInput(input, "agent_id"),
				0,
				intInput(input, "top_k", 6),
			)
		},
	}
}

// ─── extract_from_document ────────────────────────────────────────────────────

func (r *Registry) extractFromDocumentTool() *ToolImpl {
	return &ToolImpl{
		Name: "extract_from_document",
		Schema: providers.ToolParam{
			Name:        "extract_from_document",
			Description: "Extract structured data from a specific ingested document.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"doc_id":        map[string]interface{}{"type": "string", "description": "Document ID"},
					"extract_type":  map[string]interface{}{"type": "string", "enum": []string{"clauses", "parties", "dates", "obligations", "defined_terms", "amounts", "full_text"}},
					"context_query": map[string]interface{}{"type": "string", "description": "Optional focus"},
				},
				"required": []string{"doc_id", "extract_type"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			if ctx.KnowledgeStore == nil {
				return map[string]interface{}{"error": "knowledge store unavailable"}, nil
			}
			docID := strInput(input, "doc_id")
			extractType := strInput(input, "extract_type")
			if extractType == "full_text" {
				text, _ := ctx.KnowledgeStore.GetFullText(docID)
				return map[string]interface{}{"docId": docID, "extractType": extractType, "text": text}, nil
			}
			query := strInput(input, "context_query")
			if query == "" {
				query = extractType
			}
			results, err := ctx.KnowledgeStore.Search(query, ctx.OwnerID, 10)
			if err != nil {
				return nil, err
			}
			var excerpts []interface{}
			for _, res := range results {
				if res.Document.ID == docID {
					excerpts = append(excerpts, map[string]interface{}{
						"excerpt": res.Excerpt,
						"score":   res.Score,
					})
				}
			}
			return map[string]interface{}{"docId": docID, "extractType": extractType, "excerpts": excerpts}, nil
		},
	}
}

// ─── translate ────────────────────────────────────────────────────────────────

func (r *Registry) translateTool() *ToolImpl {
	return &ToolImpl{
		Name: "translate",
		Schema: providers.ToolParam{
			Name:        "translate",
			Description: "Translate legal text across languages, preserving legal terms of art.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text":            map[string]interface{}{"type": "string", "description": "Text to translate"},
					"source_language": map[string]interface{}{"type": "string", "description": "Source language code (e.g. 'FR', 'DE')"},
					"target_language": map[string]interface{}{"type": "string", "description": "Target language code (e.g. 'EN')"},
				},
				"required": []string{"text", "target_language"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			source := strInput(input, "source_language")
			if source == "" {
				source = "auto-detect"
			}
			target := strInput(input, "target_language")
			text := strInput(input, "text")
			model := routing.SelectModel(r.cfg, routing.SelectParams{TaskType: routing.TaskExtraction})
			prov, err := r.provReg.Get(model)
			if err != nil {
				return nil, err
			}
			resp, err := prov.Chat(providers.ChatParams{
				Model:     routing.ResolveModelID(model),
				MaxTokens: 2000,
				System:    "You are a legal translation specialist. Preserve all legal terms of art.",
				Messages: []providers.Message{{
					Role:    "user",
					Content: fmt.Sprintf("Translate from %s to %s. Preserve legal terms of art.\n\nTEXT:\n%s", source, target, text),
				}},
			})
			if err != nil {
				return nil, err
			}
			for _, b := range resp.Content {
				if b.Type == providers.BlockText {
					return map[string]interface{}{"translation": b.Text, "sourceLang": source, "targetLang": target}, nil
				}
			}
			return map[string]interface{}{"translation": "", "error": "no text in response"}, nil
		},
	}
}

// ─── citation_check ───────────────────────────────────────────────────────────

func (r *Registry) citationCheckTool() *ToolImpl {
	return &ToolImpl{
		Name: "citation_check",
		Schema: providers.ToolParam{
			Name:        "citation_check",
			Description: "Verify a citation by checking whether the quoted text appears verbatim in the source document.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"source": map[string]interface{}{"type": "string", "description": "Document ID to check against"},
					"quote":  map[string]interface{}{"type": "string", "description": "Exact quoted text to verify"},
				},
				"required": []string{"source", "quote"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			if ctx.KnowledgeStore == nil {
				return map[string]interface{}{"verdict": "UNAVAILABLE"}, nil
			}
			source := strInput(input, "source")
			quote := strInput(input, "quote")
			fullText, _ := ctx.KnowledgeStore.GetFullText(source)
			if fullText != "" {
				verified := strings.Contains(fullText, quote)
				verdict := "NOT_FOUND"
				if verified {
					verdict = "VERIFIED"
				}
				return map[string]interface{}{"source": source, "quote": quote, "verdict": verdict, "method": "exact_string_match"}, nil
			}
			return map[string]interface{}{"source": source, "quote": quote, "verdict": "NOT_FOUND", "note": "Source not found in knowledge store"}, nil
		},
	}
}

// ─── read_document ────────────────────────────────────────────────────────────

func (r *Registry) readDocumentTool() *ToolImpl {
	return &ToolImpl{
		Name: "read_document",
		Schema: providers.ToolParam{
			Name:        "read_document",
			Description: "Read the full text of an ingested document by its ID.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"doc_id": map[string]interface{}{"type": "string", "description": "Document ID"}},
				"required":   []string{"doc_id"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			if ctx.KnowledgeStore == nil {
				return map[string]interface{}{"text": ""}, nil
			}
			text, _ := ctx.KnowledgeStore.GetFullText(strInput(input, "doc_id"))
			return map[string]interface{}{"text": text}, nil
		},
	}
}

// ─── list_documents ───────────────────────────────────────────────────────────

func (r *Registry) listDocumentsTool() *ToolImpl {
	return &ToolImpl{
		Name: "list_documents",
		Schema: providers.ToolParam{
			Name:        "list_documents",
			Description: "List documents in the knowledge store with optional search.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"query": map[string]interface{}{"type": "string", "description": "Optional search query"}},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			if ctx.KnowledgeStore == nil {
				return map[string]interface{}{"documents": []interface{}{}}, nil
			}
			q := strInput(input, "query")
			if q == "" {
				q = "legal document"
			}
			results, err := ctx.KnowledgeStore.Search(q, ctx.OwnerID, 20)
			if err != nil {
				return nil, err
			}
			docs := make([]map[string]interface{}, len(results))
			for i, res := range results {
				docs[i] = map[string]interface{}{
					"id":    res.Document.ID,
					"title": res.Document.Title,
					"score": res.Score,
				}
			}
			return map[string]interface{}{"documents": docs}, nil
		},
	}
}

// ─── find_in_document ─────────────────────────────────────────────────────────

func (r *Registry) findInDocumentTool() *ToolImpl {
	return &ToolImpl{
		Name: "find_in_document",
		Schema: providers.ToolParam{
			Name:        "find_in_document",
			Description: "Find specific text or clauses within a document.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"doc_id": map[string]interface{}{"type": "string", "description": "Document ID"},
					"query":  map[string]interface{}{"type": "string", "description": "Text or clause to find"},
				},
				"required": []string{"doc_id", "query"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			if ctx.KnowledgeStore == nil {
				return map[string]interface{}{"matches": []interface{}{}}, nil
			}
			docID := strInput(input, "doc_id")
			query := strInput(input, "query")
			fullText, _ := ctx.KnowledgeStore.GetFullText(docID)
			if fullText == "" {
				return map[string]interface{}{"matches": []interface{}{}, "note": "document not found"}, nil
			}
			lower := strings.ToLower(query)
			content := strings.ToLower(fullText)
			var matches []map[string]interface{}
			offset := 0
			for {
				idx := strings.Index(content[offset:], lower)
				if idx == -1 || len(matches) >= 10 {
					break
				}
				abs := offset + idx
				start := abs - 100
				if start < 0 {
					start = 0
				}
				end := abs + len(query) + 100
				if end > len(fullText) {
					end = len(fullText)
				}
				matches = append(matches, map[string]interface{}{
					"position": abs,
					"excerpt":  fullText[start:end],
				})
				offset = abs + 1
			}
			return map[string]interface{}{"docId": docID, "query": query, "matches": matches}, nil
		},
	}
}

// ─── Connector stubs ──────────────────────────────────────────────────────────

var connectorDefs = []struct {
	name        string
	description string
	envKey      string
}{
	{"court_listener_search", "Search CourtListener for federal and state court opinions.", ""},
	{"court_listener_opinion", "Retrieve a specific opinion from CourtListener.", ""},
	{"court_listener_docket", "Retrieve a case docket from CourtListener.", ""},
	{"westlaw_research", "Search Westlaw for legal research.", "WESTLAW_API_KEY"},
	{"westlaw_check_citation", "Check a citation on Westlaw.", "WESTLAW_API_KEY"},
	{"everlaw_search_documents", "Search Everlaw document review set.", "EVERLAW_API_KEY"},
	{"everlaw_get_review_set", "Get an Everlaw review set.", "EVERLAW_API_KEY"},
	{"trellis_search_cases", "Search Trellis for state court cases.", "TRELLIS_API_KEY"},
	{"trellis_get_docket", "Get a docket from Trellis.", "TRELLIS_API_KEY"},
	{"trellis_judge_analytics", "Get judge analytics from Trellis.", "TRELLIS_API_KEY"},
	{"descrybe_search_cases", "Search Descrybe for case law.", "DESCRYBE_API_KEY"},
	{"descrybe_check_citation", "Check a citation on Descrybe.", "DESCRYBE_API_KEY"},
	{"ironclad_search_contracts", "Search Ironclad contract repository.", "IRONCLAD_API_KEY"},
	{"ironclad_get_contract", "Get a contract from Ironclad.", "IRONCLAD_API_KEY"},
	{"docusign_search_contracts", "Search DocuSign CLM.", "DOCUSIGN_API_KEY"},
	{"docusign_get_envelope", "Get a DocuSign envelope.", "DOCUSIGN_API_KEY"},
	{"imanage_search", "Search iManage DMS.", "IMANAGE_API_KEY"},
	{"imanage_get_document", "Get a document from iManage.", "IMANAGE_API_KEY"},
	{"definely_analyze_structure", "Analyse contract structure with Definely.", "DEFINELY_API_KEY"},
	{"definely_resolve_definition", "Resolve a defined term with Definely.", "DEFINELY_API_KEY"},
	{"lawve_review_contract", "Review a contract with Lawve AI.", "LAWVE_API_KEY"},
	{"lawve_search_clauses", "Search clauses with Lawve AI.", "LAWVE_API_KEY"},
	{"google_drive_search", "Search Google Drive documents.", "GOOGLE_DRIVE_API_KEY"},
	{"google_drive_get_file", "Get a Google Drive file.", "GOOGLE_DRIVE_API_KEY"},
	{"box_search", "Search Box documents.", "BOX_API_KEY"},
	{"box_get_file", "Get a Box file.", "BOX_API_KEY"},
	{"slack_search", "Search Slack messages.", "SLACK_API_KEY"},
	{"slack_send_message", "Send a Slack message.", "SLACK_API_KEY"},
	{"topcounsel_route_matter", "Route a matter via TopCounsel.", "TOPCOUNSEL_API_KEY"},
	{"topcounsel_get_panel", "Get TopCounsel panel information.", "TOPCOUNSEL_API_KEY"},
	{"solve_intelligence_search_patents", "Search patents with Solve Intelligence.", "SOLVE_INTELLIGENCE_API_KEY"},
	{"solve_intelligence_draft_claims", "Draft patent claims with Solve Intelligence.", "SOLVE_INTELLIGENCE_API_KEY"},
}

func (r *Registry) registerConnectors() {
	for _, def := range connectorDefs {
		name := def.name
		description := def.description
		envKey := def.envKey
		r.Register(&ToolImpl{
			Name: name,
			Schema: providers.ToolParam{
				Name:        name,
				Description: description,
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{"type": "string", "description": "Search query or identifier"},
					},
					"required": []string{"query"},
				},
			},
			Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
				if envKey != "" && !r.cfg.Connectors.Has(envKey) {
					return map[string]interface{}{
						"error": fmt.Sprintf("not configured: %s is not set", envKey),
					}, nil
				}
				return r.callConnector(name, input)
			},
		})
	}
}

func (r *Registry) callConnector(name string, input map[string]interface{}) (interface{}, error) {
	endpoint := r.cfg.Connectors.EndpointFor(name)
	if endpoint == "" {
		return map[string]interface{}{"error": "connector endpoint not configured"}, nil
	}
	body, _ := json.Marshal(input)
	req, err := http.NewRequest("POST", endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}, nil
	}
	defer resp.Body.Close()
	var result interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return map[string]interface{}{"error": "failed to decode connector response"}, nil
	}
	return result, nil
}

// ─── Tavily web search helper ─────────────────────────────────────────────────

type tavilySearchReq struct {
	APIKey      string `json:"api_key"`
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results"`
	SearchDepth string `json:"search_depth"`
}

type tavilyResult struct {
	URL           string  `json:"url"`
	Title         string  `json:"title"`
	Content       string  `json:"content"`
	Score         float64 `json:"score"`
	PublishedDate string  `json:"published_date,omitempty"`
}

type tavilyResponse struct {
	Results []tavilyResult `json:"results"`
}

func tavilySearch(apiKey, query string, maxResults int) (interface{}, error) {
	reqBody := tavilySearchReq{
		APIKey:      apiKey,
		Query:       query,
		MaxResults:  maxResults,
		SearchDepth: "advanced",
	}
	body, _ := json.Marshal(reqBody)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post("https://api.tavily.com/search", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return map[string]interface{}{"results": []interface{}{}, "error": err.Error()}, nil
	}
	defer resp.Body.Close()
	var tr tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return map[string]interface{}{"results": []interface{}{}}, nil
	}
	results := make([]map[string]interface{}, 0, len(tr.Results))
	for _, r := range tr.Results {
		content := r.Content
		if len(content) > 800 {
			content = strutil.Truncate(content, 800)
		}
		results = append(results, map[string]interface{}{
			"url":           r.URL,
			"title":         r.Title,
			"content":       content,
			"score":         r.Score,
			"publishedDate": r.PublishedDate,
		})
	}
	return map[string]interface{}{"results": results}, nil
}

// ─── Verify types satisfy interface ──────────────────────────────────────────

var _ agents.ToolRegistry = (*Registry)(nil)
