// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/google/uuid"
)

// Stage 2 — DEVIATION DETECTION, the compliance analogue of contradiction detection. On an
// enforcement matter the epistemic issues are Conducts and the finding is "the Division alleges
// X"; on a compare/review matter the issues are Requirements and the finding the rubric scores is
// "the DRAFT DEVIATES from the client's INSTRUCTION on X". Describing each requirement (what the
// pipeline did) scores nothing; FINDING where the draft is wrong is the whole task. Per
// Requirement issue, retrieve the passages addressing it across the instruction memo AND the
// drafts, then adjudicate: conform, or deviate (with severity + the specific correction)?

// deviationSystem applies the SAME grounding discipline as the rest of the pipeline: the model
// must COPY the exact instruction text and the exact draft text (verbatim) before it may assert
// a deviation. The Go side then verifies both quotes appear in the retrieved passages (substring
// lock) and drops any deviation whose quotes don't verify — a model that must copy "Twenty-Five
// Percent (25%)" from the instruction cannot then claim the instruction says 30%.
const deviationSystem = "You compare a client's INSTRUCTIONS against a DRAFT legal document for ONE requirement, using ONLY the passages given (each tagged with its SOURCE). Do NOT rely on memory. A deviation is either a CONFLICT (the draft implements the requirement but with a wrong value/name/term) or an OMISSION (the client requires something the draft does NOT contain at all). Output ONLY JSON: {\"type\": \"conflict|omission|none\", \"instructionQuote\": \"<the EXACT verbatim words from the client-instruction source stating the requirement>\", \"draftQuote\": \"<for a CONFLICT, the EXACT verbatim words from the DRAFT source; empty for an omission>\", \"requiredProvision\": \"<for an OMISSION, a short name for the missing provision, e.g. 'separate education trust for the grandchildren'>\", \"summary\": \"<one sentence>\", \"severity\": \"critical|high|medium|low\", \"recommendation\": \"<the specific correction>\"}. Quotes MUST be copied word-for-word from the passages — never invent. type=conflict ONLY if instructionQuote and draftQuote actually conflict; type=omission ONLY if the requirement is instructed but the draft passages do not implement it; otherwise type=none."

// extractRequirementsSystem drives the COMPREHENSIVE requirement enumeration — the retrieval
// floor for compare/review. Every distinct instruction the client states must become a check,
// or the deviation the rubric scores (a wrong residuary split, a missing trust) is never looked
// for. This reads the controlling document's OWN enumeration, exhaustively.
const extractRequirementsSystem = "List every DISPOSITIVE instruction that the DRAFT documents must IMPLEMENT — the things you would check the draft against: specific shares/percentages, distributions and their conditions, named trustees / guardians / beneficiaries, ages and dates that govern the plan, provisions to INCLUDE or EXCLUDE (spendthrift, no-contest/in terrorem, an education trust), powers, and terminations. Do NOT list background family descriptions, meeting dates, asset values, or drafting logistics UNLESS the client instructs a specific treatment of them. Write each as a short heading in the client's own terms (e.g. 'Residuary estate split 40/35/25', 'Trust terminates at a specified age', 'Establish a separate education trust for the grandchildren', 'Name a specific successor trustee', 'Two licensed physicians certify incapacity'). One heading per line, no numbering, no preamble."

// enumerateRequirements reads the CONTROLLING document (the client instruction memo — the doc
// densest in instruction language, via chargingDocChunks) and extracts every distinct
// requirement, so the deviation pass checks all of them, not just the subset the graph caught.
func (o *Orchestrator) enumerateRequirements(task *types.Task, prov providers.Provider, model string) []string {
	chunks := o.chargingDocChunks(task, 12000)
	if len(chunks) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	zero := 0.0
	for _, ch := range chunks {
		resp, err := prov.Chat(providers.ChatParams{
			Model: model, MaxTokens: 700, System: extractRequirementsSystem,
			Messages: []providers.Message{{Role: "user", Content: "PASSAGE:\n" + ch}}, CacheSystem: true, Temperature: &zero,
		})
		if err != nil {
			continue
		}
		o.recordCost(resp, model, cost.ContextSynthesis, task.ID)
		var text string
		for _, b := range resp.Content {
			if b.Type == providers.BlockText {
				text = b.Text
			}
		}
		for _, ln := range strings.Split(text, "\n") {
			ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "-*•0123456789.) \t"))
			ln = strings.TrimSpace(strings.Trim(ln, "*_#:"))
			if n := len(ln); n < 6 || n > 110 {
				continue
			}
			if k := strings.ToLower(ln); !seen[k] {
				seen[k] = true
				out = append(out, ln)
			}
		}
		if len(out) >= 30 {
			break
		}
	}
	return out
}

// detectDeviations adjudicates each requirement for a draft-vs-instruction deviation and returns
// the confirmed ones as findings (routed to their section at synthesis + summarised).
func (o *Orchestrator) detectDeviations(task *types.Task, g *evidencegraph.Graph, prov providers.Provider, model string) []types.Finding {
	if prov == nil || model == "" || g == nil {
		return nil
	}
	// Comprehensive requirement list from the controlling doc; fall back to the graph's issues.
	reqs := o.enumerateRequirements(task, prov, model)
	if len(reqs) == 0 {
		reqs = g.Issues()
	}
	if len(reqs) == 0 {
		return nil
	}
	const maxReqs = 32 // bound the (slow) spine-model adjudications; dedup near-identical labels
	var out []types.Finding
	seenReq := map[string]bool{}
	var keptSigs []map[string]bool
	adjudicated := 0
	for _, req := range reqs {
		if adjudicated >= maxReqs {
			break
		}
		key := strings.ToLower(strings.TrimSpace(req))
		if key == "" || seenReq[key] {
			continue
		}
		seenReq[key] = true
		ctx := o.retrieveForDeviation(task, req)
		if strings.TrimSpace(ctx) == "" {
			continue
		}
		adjudicated++
		dev := o.adjudicateDeviation(task, prov, model, req, ctx)
		if dev == "" {
			continue
		}
		// Dedup by content overlap — two requirements can surface the SAME deviation (e.g. both
		// "first successor trustee" and "exclude Sophia" flag Sophia-as-trustee). Skip if a kept
		// finding shares >60% of its distinctive terms.
		sig := devSignature(dev)
		dup := false
		for _, prev := range keptSigs {
			if jaccard(sig, prev) > 0.6 {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		keptSigs = append(keptSigs, sig)
		out = append(out, types.Finding{
			ID:         uuid.NewString(),
			AgentID:    "deviation-detector",
			AgentName:  "Deviation Detector",
			Content:    dev,
			Confidence: 0.8,
			Timestamp:  time.Now(),
		})
	}
	return out
}

// retrieveForDeviation pulls passages addressing one requirement across ALL documents (so the
// instruction memo and the draft both appear), each tagged with its source so the adjudicator
// can tell instruction from draft.
func (o *Orchestrator) retrieveForDeviation(task *types.Task, req string) string {
	// Retrieval floor: pull generously so BOTH the instruction memo's statement AND the draft's
	// implementation of the requirement land in context — the grounded comparison needs both
	// verbatim, and a thin retrieval starves it (a missed side reads as "conforms").
	res, err := o.tools.Execute("search_chunks", map[string]interface{}{"query": req, "top_k": 12}, agents.ToolContext{TaskID: task.ID})
	if err != nil {
		return ""
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		return ""
	}
	rows, _ := m["results"].([]map[string]interface{})
	var b strings.Builder
	for _, r := range rows {
		sn, _ := r["snippet"].(string)
		if strings.TrimSpace(sn) == "" {
			continue
		}
		src, _ := r["source"].(string)
		if src == "" {
			if v, ok := r["document"].(string); ok {
				src = v
			}
		}
		if src == "" {
			src = "document"
		}
		fmt.Fprintf(&b, "[%s] %s\n", src, strings.Join(strings.Fields(sn), " "))
	}
	return strutil.TruncateToTokens(b.String(), 3200)
}

func (o *Orchestrator) adjudicateDeviation(task *types.Task, prov providers.Provider, model, req, ctx string) string {
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   400,
		System:      deviationSystem,
		Messages:    []providers.Message{{Role: "user", Content: "REQUIREMENT: " + req + "\n\nPASSAGES:\n" + ctx}},
		CacheSystem: true,
		Temperature: &zero,
	})
	if err != nil {
		return ""
	}
	o.recordCost(resp, model, cost.ContextSynthesis, task.ID)
	var text string
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			text = blk.Text
		}
	}
	t := strings.TrimSpace(text)
	i, j := strings.Index(t, "{"), strings.LastIndex(t, "}")
	if i < 0 || j <= i {
		return ""
	}
	var d struct {
		Type              string `json:"type"`
		InstructionQuote  string `json:"instructionQuote"`
		DraftQuote        string `json:"draftQuote"`
		RequiredProvision string `json:"requiredProvision"`
		Summary           string `json:"summary"`
		Severity          string `json:"severity"`
		Recommendation    string `json:"recommendation"`
	}
	if json.Unmarshal([]byte(t[i:j+1]), &d) != nil || strings.TrimSpace(d.Summary) == "" {
		return ""
	}
	typ := strings.ToLower(strings.TrimSpace(d.Type))
	// The instruction quote must be VERBATIM in the retrieved passages for BOTH types — the
	// requirement must genuinely be instructed (no fabricated "the client wanted …").
	iq := strings.TrimSpace(d.InstructionQuote)
	nctx := devNorm(ctx)
	if len(iq) < 4 || !strings.Contains(nctx, devNorm(iq)) {
		return ""
	}
	label := "DEVIATION"
	switch typ {
	case "conflict":
		// CONFLICT — the draft value must ALSO be verbatim, or it's a fabricated conflict.
		dq := strings.TrimSpace(d.DraftQuote)
		if len(dq) < 4 || !strings.Contains(nctx, devNorm(dq)) {
			return ""
		}
	case "omission":
		// OMISSION — there is no draft quote (the provision is absent). Ground it with a focused
		// second look: retrieve the DRAFT's own sections on this provision and have the model
		// judge PRESENT vs ABSENT, told explicitly that a HEMS-style mention ("health, education,
		// maintenance, support") is NOT the provision. A keyword check can't make that call — the
		// word "education" is in every trust; a separate education trust may still be missing.
		if !o.confirmOmission(task, prov, model, strings.TrimSpace(d.RequiredProvision)) {
			return ""
		}
		label = "OMISSION"
	default:
		return "" // type=none / unknown
	}
	sev := strings.ToLower(strings.TrimSpace(d.Severity))
	if sev == "" {
		sev = "medium"
	}
	out := fmt.Sprintf("%s (%s severity) — %s", label, sev, strings.TrimSpace(d.Summary))
	if r := strings.TrimSpace(d.Recommendation); r != "" {
		out += " Recommended correction: " + r
	}
	return out
}

// devNorm normalizes for the substring lock (collapse whitespace, lowercase) so a verbatim quote
// verifies despite spacing/case drift, but a fabricated value still fails.
func devNorm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// devSignature is the set of distinctive terms (≥5 chars) in a deviation string — used to dedup
// two requirements that surfaced the same underlying deviation.
func devSignature(s string) map[string]bool {
	sig := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, ".,;:()[]{}\"'`-—")
		if len(w) >= 5 {
			sig[w] = true
		}
	}
	return sig
}

// jaccard is the overlap ratio between two term sets (|A∩B| / |A∪B|).
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// omissionCheckSystem verifies whether a DRAFT actually establishes a required provision, told
// explicitly that a related word in another context (HEMS) is not the provision. One word out.
const omissionCheckSystem = "You verify whether a DRAFT legal document establishes a REQUIRED provision. You are given the required provision and the draft's own sections most relevant to it. Answer with ONLY one word: PRESENT if the draft actually establishes or contains that provision, or ABSENT if it does not. IMPORTANT: a mere mention of a related word does NOT count — e.g. the word 'education' inside a 'health, education, maintenance, and support' distribution standard is NOT a separate education trust. Only a genuine, structural implementation of the required provision counts as PRESENT."

// confirmOmission grounds an OMISSION claim: it retrieves the DRAFT's own sections on the
// provision and asks the model whether the provision is genuinely established, guarding against
// the keyword false-friend (the word is present, the provision is not). No draft sections at all
// → omitted; the model's ABSENT verdict → confirmed.
func (o *Orchestrator) confirmOmission(task *types.Task, prov providers.Provider, model, provision string) bool {
	if strings.TrimSpace(provision) == "" {
		return false
	}
	draftCtx := o.retrieveDraftContext(task, provision)
	if strings.TrimSpace(draftCtx) == "" {
		return true // the draft says nothing on this provision → omitted
	}
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model: model, MaxTokens: 8, System: omissionCheckSystem,
		Messages:    []providers.Message{{Role: "user", Content: "REQUIRED PROVISION: " + provision + "\n\nDRAFT SECTIONS:\n" + draftCtx}},
		CacheSystem: true, Temperature: &zero,
	})
	if err != nil {
		return false
	}
	o.recordCost(resp, model, cost.ContextSynthesis, task.ID)
	var txt string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			txt += b.Text
		}
	}
	return strings.Contains(strings.ToUpper(txt), "ABSENT")
}

// retrieveDraftContext pulls passages on a topic from the DRAFT documents only (excluding the
// controlling instruction memo), so an omission is judged against what the draft actually says.
func (o *Orchestrator) retrieveDraftContext(task *types.Task, query string) string {
	res, err := o.tools.Execute("search_chunks", map[string]interface{}{"query": query, "top_k": 14}, agents.ToolContext{TaskID: task.ID})
	if err != nil {
		return ""
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		return ""
	}
	rows, _ := m["results"].([]map[string]interface{})
	var b strings.Builder
	for _, r := range rows {
		sn, _ := r["snippet"].(string)
		if strings.TrimSpace(sn) == "" {
			continue
		}
		src, _ := r["source"].(string)
		if src == "" {
			if v, ok := r["document"].(string); ok {
				src = v
			}
		}
		if !isDraftSource(src) {
			continue // draft sections only
		}
		fmt.Fprintf(&b, "%s\n", strings.Join(strings.Fields(sn), " "))
	}
	return strutil.TruncateToTokens(b.String(), 2400)
}

// isDraftSource reports whether a retrieval source is a draft under review (not the controlling
// instruction memo / background summary).
func isDraftSource(src string) bool {
	s := strings.ToLower(src)
	return s != "" && !strings.Contains(s, "instruction") && !strings.Contains(s, "memo") && !strings.Contains(s, "background")
}
