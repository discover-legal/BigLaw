// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
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
//
// This file carries the fix-wave port (the machinery that took the enforcement arc 34→49):
//
//  1. GROUNDED VALUES — every quote is substring-locked against the retrieved passages, and
//     every VALUE the model's summary/recommendation asserts is verified against those passages
//     (devUnverifiedValues). A summary carrying a value found nowhere is withheld and replaced
//     with the verbatim quotes plus a flag — never emitted as-is. Both verbatim quotes always
//     travel with the finding, as mechanically-verified citations.
//  2. MECHANICAL NUMERIC JOIN — the controlling-source vs document-under-review pair is a
//     cross-document comparison, so the figure harvest's raw []figureHit records (the seam
//     harvestAndBindFigures exposes) are joined on crossdoc.go's metric identity (canonical
//     quantity label + value kind + referent floor): a requirement value and a document value
//     that share a metric but disagree are caught mechanically, with both verbatim quotes,
//     and fed to the adjudicator (deviationNumericJoin).
//  3. MULTI-PART DECOMPOSITION — a requirement with enumerable sub-details (an amount AND an
//     identifier AND a condition) is adjudicated PER PART; partial coverage renders as
//     "implemented only in part" with the missing/wrong parts itemized — it can never present
//     as full conformance.
//  4. SATURATION BOTH SIDES — requirement enumeration rides the deterministic section-walk
//     (sectionChunks) over the CONTROLLING document, and per-requirement retrieval walks the
//     section chunks of EVERY document under review (devCorpus), so a second draft (the
//     pour-over will beside the trust) is always represented — an absent side reads as an
//     explicit "(no provision found in this document)" signal, not silence.
//  5. DELIVERABLE DISCIPLINE — findings are rendered by mechanical Go (the authorship-layer
//     principle: the model adjudicates, Go composes the deliverable line) and ride
//     appendDiscrepancies' "## Deviations Identified" section into the written deliverable.

const (
	devChunkTok    = 400  // retrieval granularity of the deterministic section-walk corpus
	devEnumTok     = 1500 // enumeration window (mirrors the spine/harvest section-walk size)
	devPerDocK     = 4    // deterministic passages per document per requirement
	devPerDocMax   = 6    // hard cap per document after semantic augmentation
	devPassageTok  = 220  // per-passage token budget in the adjudication context
	devCtxTok      = 3600 // total adjudication context budget
	devDraftCtxTok = 2400 // omission-check draft context budget
	devMaxParts    = 6    // bound per-part processing (and its omission-check calls)
	devNumJoinCap  = 15   // bound numeric-join adjudication model calls
	devMaxGvals    = 80   // bound the derived-arithmetic search space
)

// ─── Compare corpus: deterministic section-walk over BOTH sides ───────────────

// devDoc is one document in the compare corpus: its full text, the deterministic
// section-walk chunks over it (sectionChunks — same text in, same chunks out), a
// normalized copy for substring locks, and which side of the comparison it is.
type devDoc struct {
	title       string
	text        string
	norm        string // figNorm(text) — substring-lock target
	chunks      []string
	controlling bool
}

// devCorpus is the matter's compare corpus: the controlling source(s) beside every
// document under review, each carrying its own deterministic section walk.
type devCorpus struct{ docs []devDoc }

// newDevCorpus builds the corpus and classifies sides. Titles that read as the
// controlling source (instruction memo, background) are controlling; the rest are
// under review. If the title heuristic fails to produce both sides (and there are
// ≥2 documents), the document densest in instruction/requirement language per byte
// becomes the controlling source — the same signal chargingDocChunks ranks by.
func newDevCorpus(titles, texts []string) *devCorpus {
	c := &devCorpus{}
	for i := range titles {
		if i >= len(texts) || strings.TrimSpace(texts[i]) == "" {
			continue
		}
		c.docs = append(c.docs, devDoc{
			title:       titles[i],
			text:        texts[i],
			norm:        figNorm(texts[i]),
			chunks:      sectionChunks(texts[i], devChunkTok),
			controlling: !isDraftSource(titles[i]),
		})
	}
	anyCtrl, anyRev := false, false
	for _, d := range c.docs {
		if d.controlling {
			anyCtrl = true
		} else {
			anyRev = true
		}
	}
	if len(c.docs) > 1 && (!anyCtrl || !anyRev) {
		best, bestScore := 0, -1.0
		for i, d := range c.docs {
			s := float64(len(reAllegationTerm.FindAllStringIndex(d.text, -1))) / float64(len(d.text)+1)
			if s > bestScore {
				best, bestScore = i, s
			}
		}
		for i := range c.docs {
			c.docs[i].controlling = i == best
		}
	}
	return c
}

// controllingSrc reports which side a retrieval source label belongs to: a corpus
// title match decides; unknown sources fall back to the title heuristic.
func (c *devCorpus) controllingSrc(src string) bool {
	if c != nil {
		ls := strings.ToLower(src)
		for _, d := range c.docs {
			lt := strings.ToLower(d.title)
			if ls == lt || strings.Contains(ls, lt) || strings.Contains(lt, ls) {
				return d.controlling
			}
		}
	}
	return !isDraftSource(src)
}

// buildDeviationCorpus loads every ingested document's full text into a devCorpus.
// Returns nil when no document yields usable text (callers degrade to the semantic-
// retrieval-only path).
func (o *Orchestrator) buildDeviationCorpus(task *types.Task) *devCorpus {
	if o.knowledge == nil {
		return nil
	}
	const perDocTokenCap = 40000 // mirror the harvest's bound on a pathological doc
	var titles, texts []string
	for _, docID := range task.DocumentIDs {
		txt, err := o.knowledge.GetFullText(docID)
		if err != nil || strings.TrimSpace(txt) == "" {
			continue
		}
		title := docID
		if d := o.knowledge.GetByID(docID); d != nil && strings.TrimSpace(d.Title) != "" {
			title = d.Title
		}
		if len(txt) > perDocTokenCap*4 { // ~4 chars/token
			txt = txt[:perDocTokenCap*4]
		}
		titles = append(titles, title)
		texts = append(texts, txt)
	}
	c := newDevCorpus(titles, texts)
	if len(c.docs) == 0 {
		return nil
	}
	return c
}

// devTerms extracts the distinctive lowercase terms of a requirement heading for the
// deterministic lexical walk (≥4 chars, stop words out, singularized).
func devTerms(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, ".,;:()[]{}\"'`—–")
		w = strings.TrimSuffix(w, "'s")
		if len(w) < 4 {
			continue
		}
		w = crossDocSingular(w)
		if crossDocStop[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// topChunks returns the document's k best section chunks for a term set — scored by
// distinct-term hits, ties broken by document order, results in document order.
// Deterministic: the same document and requirement always yield the same passages.
func (d *devDoc) topChunks(terms []string, k int) []string {
	type sc struct{ i, score int }
	var scs []sc
	for i, ch := range d.chunks {
		n := devNorm(ch)
		if n == "" {
			continue
		}
		s := 0
		for _, t := range terms {
			if strings.Contains(n, t) {
				s++
			}
		}
		if s > 0 {
			scs = append(scs, sc{i, s})
		}
	}
	sort.SliceStable(scs, func(a, b int) bool { return scs[a].score > scs[b].score })
	if len(scs) > k {
		scs = scs[:k]
	}
	sort.SliceStable(scs, func(a, b int) bool { return scs[a].i < scs[b].i })
	out := make([]string, 0, len(scs))
	for _, s := range scs {
		out = append(out, strings.Join(strings.Fields(d.chunks[s.i]), " "))
	}
	return out
}

// ─── Requirement enumeration (retrieval floor, controlling side) ──────────────

// extractRequirementsSystem drives the COMPREHENSIVE requirement enumeration — the retrieval
// floor for compare/review. Every distinct instruction the client states must become a check,
// or the deviation the rubric scores (a wrong residuary split, a missing trust) is never looked
// for. This reads the controlling document's OWN enumeration, exhaustively.
const extractRequirementsSystem = "List every OPERATIVE requirement the controlling document imposes that the document(s) under review must satisfy — the concrete things you would check the reviewed document against. Cover: amounts, percentages, and figures; named parties, roles, and appointments; dates, deadlines, and durations; conditions and triggers; and provisions that must be INCLUDED or EXCLUDED. This spans every practice area — e.g. a residuary share split or successor appointment (estates), an indemnity cap or governing-law or termination clause (contracts), a vesting schedule or liquidation preference (equity/transactions), a notice period or non-compete (employment), a required disclosure or filing deadline (regulatory). SKIP pure descriptive facts — asset values, account balances, valuations, inventories, biographical details, and dates of meetings — UNLESS the controlling document attaches a specific requirement to them (an instruction to EXCLUDE an asset IS a requirement; a bare valuation is not). Write each as a short, concrete heading in the source's own terms. One heading per line, no numbering, no preamble."

// devControllingChunks walks the corpus's controlling document(s) over their PageIndex
// section trees (sectionChunks — deterministic, every section exactly once) up to a token
// budget. This is the enumeration retrieval floor: the same controlling text always yields
// the same enumeration windows, so a dispositive provision late in the memo is never lost
// to a chunking lottery.
func devControllingChunks(corpus *devCorpus, tokenBudget int) []string {
	if corpus == nil {
		return nil
	}
	var out []string
	used := 0
	for _, d := range corpus.docs {
		if !d.controlling {
			continue
		}
		for _, ch := range sectionChunks(d.text, devEnumTok) {
			if strings.TrimSpace(ch) == "" {
				continue
			}
			t := strutil.EstimateTokens(ch)
			if used+t > tokenBudget {
				return out
			}
			used += t
			out = append(out, ch)
		}
	}
	return out
}

// enumerateRequirements reads the CONTROLLING document (via the deterministic section walk;
// chargingDocChunks as the fallback when no corpus text is available) and extracts every
// distinct requirement, so the deviation pass checks all of them, not just the subset the
// graph caught.
func (o *Orchestrator) enumerateRequirements(task *types.Task, corpus *devCorpus, prov providers.Provider, model string) []string {
	chunks := devControllingChunks(corpus, 12000)
	if len(chunks) == 0 {
		chunks = o.chargingDocChunks(task, 12000)
	}
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
		if len(out) >= 150 {
			break // safety bound; process all chunks so late dispositive provisions are reached
		}
	}
	// Prioritize operative requirements over descriptive background before the adjudication cap:
	// controlling documents state parties/assets/dates first and dispositive terms last, so raw
	// document order buries the very requirements the rubric scores. Stable sort keeps document
	// order within an equal score.
	sort.SliceStable(out, func(i, j int) bool { return dispositiveScore(out[i]) > dispositiveScore(out[j]) })
	return out
}

// dispositiveScore ranks how operative a requirement heading is — how much it reads like a term
// the reviewed document must implement (shall/appoint/exclude/vest/indemnify/govern…) versus
// descriptive background (a valuation, a balance, a biography). Practice-area-agnostic.
func dispositiveScore(req string) int {
	r := strings.ToLower(req)
	s := 0
	for _, m := range dispositiveMarkers {
		if strings.Contains(r, m) {
			s++
		}
	}
	for _, m := range descriptiveMarkers {
		if strings.Contains(r, m) {
			s--
		}
	}
	return s
}

var dispositiveMarkers = []string{
	"shall", "must", "split", "percent", "%", "terminat", "appoint", "exclude", "include",
	"prohibit", "distribut", "provision", "clause", "trustee", "guardian", "spendthrift",
	"contest", "terrorem", "vest", "indemnif", "govern", "notice", "deadline", "condition",
	"successor", "beneficiar", "power", "share", "require", "covenant", "warrant", "obligation",
	// disposition/instrument nouns — carry dispositive weight across estate/tax/transactional
	"trust", "fund", "bequest", "gift", "devise", "protector", "disinherit", "scholarship",
	"election", "grantor", "remainder", "annuity", "election authority",
}
var descriptiveMarkers = []string{
	"fair market value", "market value", "current balance", "death benefit", "estimated gross",
	"resides", "retired", "biograph", "date of birth",
}

// ─── Detection entry point ─────────────────────────────────────────────────────

// detectDeviations adjudicates each requirement for a draft-vs-instruction deviation and returns
// the confirmed ones as findings (routed to their section at synthesis + summarised). rawFigs is
// the figure harvest's raw record seam (harvestAndBindFigures' second return): when present, the
// mechanical numeric join runs over it first, so requirement-value vs document-value mismatches
// are caught even when the per-requirement LLM pass misses them.
func (o *Orchestrator) detectDeviations(task *types.Task, g *evidencegraph.Graph, prov providers.Provider, model string, rawFigs []figureHit, figProv providers.Provider, figModel string) []types.Finding {
	if prov == nil || model == "" || g == nil {
		return nil
	}
	corpus := o.buildDeviationCorpus(task)
	// Comprehensive requirement list from the controlling doc; fall back to the graph's issues.
	reqs := o.enumerateRequirements(task, corpus, prov, model)
	if len(reqs) == 0 {
		reqs = g.Issues()
	}
	return o.deviationFindings(task, corpus, reqs, rawFigs, prov, model, figProv, figModel)
}

// deviationFindings is the corpus-injected core (testable without a knowledge store): the
// mechanical numeric join first — its findings lead and seed the dedup set — then the
// per-requirement adjudication loop.
func (o *Orchestrator) deviationFindings(task *types.Task, corpus *devCorpus, reqs []string, rawFigs []figureHit, prov providers.Provider, model string, figProv providers.Provider, figModel string) []types.Finding {
	out, keptSigs := o.deviationNumericJoin(task.ID, rawFigs, corpus, figProv, figModel)
	// Comprehensive: adjudicate ALL dispositive requirements, not a small top-N — the arbitrary
	// cap was the biggest source of run-to-run variance (a different subset each run, so different
	// issues caught → the score bounced 8↔12). The dispositive sort still puts real requirements
	// first, so the high bound only trims a long background tail. Slower, but reproducible coverage.
	const maxReqs = 80
	seenReq := map[string]bool{}
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
		ctx := o.retrieveForDeviation(task, corpus, req)
		if strings.TrimSpace(ctx) == "" {
			continue
		}
		adjudicated++
		// A requirement can carry MULTIPLE independent deviations (e.g. a three-way split with
		// two wrong shares) — adjudicateDeviation returns every one it can ground, not just one.
		for _, gd := range o.adjudicateDeviation(task, corpus, prov, model, req, ctx) {
			if gd.text == "" {
				continue
			}
			// Dedup by content overlap — two requirements (or two rows on the same requirement)
			// can surface the SAME deviation (e.g. both "first successor trustee" and "exclude
			// Sophia" flag Sophia-as-trustee). Compare the CLAIM CORE (the summary), not the full
			// string: divergent "Recommended correction:" tails dragged whole-string overlap below
			// the threshold and let near-identical claims through.
			sig := devSignature(devCore(gd.text))
			dup := false
			for _, prev := range keptSigs {
				if jaccard(sig, prev) > 0.5 {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			keptSigs = append(keptSigs, sig)
			status := types.EvidenceStatus("")
			if len(gd.cites) > 0 {
				status = types.EvidenceGrounded
			}
			out = append(out, types.Finding{
				ID:             uuid.NewString(),
				AgentID:        "deviation-detector",
				AgentName:      "Deviation Detector",
				Content:        gd.text,
				Citations:      gd.cites,
				Confidence:     0.8,
				EvidenceStatus: status,
				Timestamp:      time.Now(),
			})
		}
	}
	return out
}

// ─── Retrieval: paired, per-document, floor-guaranteed ─────────────────────────

// deviationSearch runs the semantic chunk retrieval for one query and returns
// (source, snippet) pairs. Nil-safe when no tool registry is wired (tests, degraded runs).
func (o *Orchestrator) deviationSearch(task *types.Task, query string, k int) [][2]string {
	if o.tools == nil {
		return nil
	}
	res, err := o.tools.Execute("search_chunks", map[string]interface{}{"query": query, "top_k": k}, agents.ToolContext{TaskID: task.ID})
	if err != nil {
		return nil
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		return nil
	}
	rows, _ := m["results"].([]map[string]interface{})
	var out [][2]string
	for _, r := range rows {
		sn, _ := r["snippet"].(string)
		if strings.TrimSpace(sn) == "" {
			continue
		}
		// search_chunks returns the document under "title" (DocTitle) — there is no "source" key.
		// This label is what splits controlling-source from document-under-review, so it must be
		// the real document identity, not a literal fallback.
		src, _ := r["title"].(string)
		if src == "" {
			if v, ok := r["id"].(string); ok {
				src = v
			}
		}
		if src == "" {
			src = "document"
		}
		out = append(out, [2]string{src, strings.Join(strings.Fields(sn), " ")})
	}
	return out
}

// retrieveForDeviation retrieves the requirement's passages and presents them PAIRED — what the
// CONTROLLING source requires beside what EACH DOCUMENT under review actually says — so the model
// compares "should be" against "is" directly, per document. The deterministic section-walk floor
// (corpus topChunks) guarantees every document under review is represented — a second draft the
// semantic index never surfaces still gets its own labeled section, and an empty section is an
// explicit OMISSION signal ("no provision found in this document"), not silence. Semantic
// retrieval augments the floor when the tool registry is wired.
func (o *Orchestrator) retrieveForDeviation(task *types.Task, corpus *devCorpus, req string) string {
	type bucket struct {
		title    string
		ctrl     bool
		passages []string
	}
	var buckets []bucket
	idx := map[string]int{}
	add := func(title string, ctrl bool, ps ...string) {
		i, ok := idx[title]
		if !ok {
			i = len(buckets)
			idx[title] = i
			buckets = append(buckets, bucket{title: title, ctrl: ctrl})
		}
		for _, p := range ps {
			p = strutil.TruncateToTokens(strings.Join(strings.Fields(p), " "), devPassageTok)
			if strings.TrimSpace(p) == "" || len(buckets[i].passages) >= devPerDocMax {
				continue
			}
			dup := false
			np := devNorm(p)
			for _, prev := range buckets[i].passages {
				if devNorm(prev) == np {
					dup = true
					break
				}
			}
			if !dup {
				buckets[i].passages = append(buckets[i].passages, p)
			}
		}
	}
	// Deterministic floor first: every corpus document gets a bucket (even empty — the
	// per-document "(no provision found)" marker below is the omission signal).
	terms := devTerms(req)
	if corpus != nil {
		for _, d := range corpus.docs {
			add(d.title, d.controlling, d.topChunks(terms, devPerDocK)...)
		}
	}
	// Semantic augmentation.
	for _, rc := range o.deviationSearch(task, req, 18) {
		add(rc[0], corpus.controllingSrc(rc[0]), rc[1])
	}
	hasAny := false
	for _, b := range buckets {
		if len(b.passages) > 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return ""
	}
	var out strings.Builder
	nCtrl, nRev := 0, 0
	render := func(ctrl bool) {
		for _, b := range buckets {
			if b.ctrl != ctrl {
				continue
			}
			if ctrl {
				nCtrl++
				fmt.Fprintf(&out, "CONTROLLING SOURCE — %s — what is required:\n", b.title)
				if len(b.passages) == 0 {
					out.WriteString("(No matching passage found in this source.)\n")
				}
			} else {
				nRev++
				fmt.Fprintf(&out, "DOCUMENT UNDER REVIEW — %s — what it actually says:\n", b.title)
				if len(b.passages) == 0 {
					out.WriteString("(No provision addressing this requirement was found in this document.)\n")
				}
			}
			for _, p := range b.passages {
				fmt.Fprintf(&out, "- %s\n", p)
			}
			out.WriteString("\n")
		}
	}
	render(true)
	if nCtrl == 0 {
		out.WriteString("CONTROLLING SOURCE — what is required:\n(No matching passage found in the controlling source.)\n\n")
	}
	render(false)
	if nRev == 0 {
		out.WriteString("DOCUMENT UNDER REVIEW — what it actually says:\n(No provision addressing this was found in the document under review.)\n")
	}
	return strutil.TruncateToTokens(out.String(), devCtxTok)
}

// ─── Adjudication ──────────────────────────────────────────────────────────────

// deviationSystem applies the SAME grounding discipline as the rest of the pipeline: the model
// must COPY the exact instruction text and the exact draft text (verbatim) before it may assert
// a deviation. The Go side then verifies both quotes appear in the retrieved passages (substring
// lock) and drops any deviation whose quotes don't verify — a model that must copy "Twenty-Five
// Percent (25%)" from the instruction cannot then claim the instruction says 30%.
const deviationSystem = "You check ONE requirement against the document(s) under review. The passages are grouped into labeled sections: 'CONTROLLING SOURCE — <document> — what is required' (client instructions, a playbook, a regulation, a term sheet, or a prior agreement) and one 'DOCUMENT UNDER REVIEW — <document> — what it actually says' section PER reviewed document (a draft, a contract, a filing, a policy). Use ONLY these passages; do NOT rely on memory. A deviation is either a CONFLICT (a reviewed document addresses the requirement but with a wrong value, name, or term) or an OMISSION (the CONTROLLING SOURCE requires it but a reviewed document does not implement it — including when that document's section shows no matching provision). If the CONTROLLING SOURCE attaches TWO OR MORE enumerable sub-details to a SINGLE deviation (e.g. an amount AND an identifier AND a condition; a named item AND its serial number AND a holding instruction), assess EACH sub-detail separately in that deviation's \"parts\" — a requirement implemented only IN PART is a deviation; NEVER report it as conforming. Separately, if the requirement itself covers MULTIPLE INDEPENDENT items (e.g. a three-way split among named parties where two shares are wrong), each independent item is its OWN deviation object — do not merge unrelated conflicts into one. Output ONLY a JSON ARRAY — one object per DISTINCT conflict or omission, or an empty array [] if every reviewed document conforms. Each object: {\"type\": \"conflict|omission|none\", \"document\": \"<the reviewed document the deviation is in, copied from its section label; empty if none>\", \"instructionQuote\": \"<the EXACT verbatim words from the CONTROLLING SOURCE section stating the requirement, including any specific value it names>\", \"draftQuote\": \"<for a CONFLICT, the EXACT verbatim words from that reviewed document's section; empty for an omission>\", \"requiredProvision\": \"<for an OMISSION, a short name for the missing provision>\", \"summary\": \"<one sentence naming the required value and the document's value; if the deviation has a material practical CONSEQUENCE — it creates a risk given a known fact about a party, or it affects another provision's calculation — state that consequence too>\", \"severity\": \"critical|high|medium|low\", \"recommendation\": \"<the specific correction, stating the required value>\", \"parts\": [{\"part\": \"<short name of the sub-detail>\", \"status\": \"conforms|conflict|omission\", \"instructionQuote\": \"<EXACT verbatim words from the CONTROLLING SOURCE for this sub-detail>\", \"draftQuote\": \"<for a conflict, EXACT verbatim words from the reviewed document; empty otherwise>\", \"note\": \"<one short clause>\"}]} — include \"parts\" ONLY when ONE deviation has 2+ enumerable sub-details; otherwise use []. Quotes MUST be copied word-for-word from the passages — never invent. type=conflict ONLY if the two quotes actually conflict; type=omission ONLY if the requirement is imposed but not implemented; otherwise omit that object entirely."

type devPart struct {
	Part             string `json:"part"`
	Status           string `json:"status"`
	InstructionQuote string `json:"instructionQuote"`
	DraftQuote       string `json:"draftQuote"`
	Note             string `json:"note"`
}

type devVerdict struct {
	Type              string    `json:"type"`
	Document          string    `json:"document"`
	InstructionQuote  string    `json:"instructionQuote"`
	DraftQuote        string    `json:"draftQuote"`
	RequiredProvision string    `json:"requiredProvision"`
	Summary           string    `json:"summary"`
	Severity          string    `json:"severity"`
	Recommendation    string    `json:"recommendation"`
	Parts             []devPart `json:"parts"`
}

// groundedDev is one grounded, rendered deviation finding awaiting dedup + emission.
type groundedDev struct {
	text  string
	cites []types.Citation
}

// adjudicateDeviation asks the model for EVERY distinct deviation on this requirement — not
// just one. A multi-part requirement (e.g. a three-way residuary split with two wrong shares)
// can carry SEVERAL independent conflicts/omissions; a single-verdict response caught only one
// (often the first or the most fabricated-sounding), silently dropping the rest. Each returned
// verdict is independently grounded: substring locks on every quote, per-part sub-verdicts,
// value verification on the summary/recommendation, and mechanically-verified citations.
func (o *Orchestrator) adjudicateDeviation(task *types.Task, corpus *devCorpus, prov providers.Provider, model, req, ctx string) []groundedDev {
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   1400,
		System:      deviationSystem,
		Messages:    []providers.Message{{Role: "user", Content: "REQUIREMENT: " + req + "\n\nPASSAGES:\n" + ctx}},
		CacheSystem: true,
		Temperature: &zero,
	})
	if err != nil {
		return nil
	}
	o.recordCost(resp, model, cost.ContextSynthesis, task.ID)
	var text string
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			text = blk.Text
		}
	}
	t := strings.TrimSpace(text)
	i, j := strings.Index(t, "["), strings.LastIndex(t, "]")
	if i < 0 || j <= i {
		return nil
	}
	var rows []devVerdict
	if json.Unmarshal([]byte(t[i:j+1]), &rows) != nil {
		return nil
	}
	const maxRowsPerReq = 6 // bound a pathological over-generation; a real multi-part split is small
	var out []groundedDev
	for ri, d := range rows {
		if ri >= maxRowsPerReq {
			break
		}
		if s, c := o.groundDeviation(task, corpus, prov, model, req, ctx, d); s != "" {
			out = append(out, groundedDev{text: s, cites: c})
		}
	}
	return out
}

// groundDeviation applies the grounding discipline to a parsed verdict and renders the
// finding mechanically (the authorship-layer principle: the model adjudicates, Go composes).
func (o *Orchestrator) groundDeviation(task *types.Task, corpus *devCorpus, prov providers.Provider, model, req, ctx string, d devVerdict) (string, []types.Citation) {
	nctx := devNorm(ctx)
	locked := func(q string) bool {
		q = strings.TrimSpace(q)
		return len(q) >= 4 && strings.Contains(nctx, devNorm(q))
	}
	iq := strings.TrimSpace(d.InstructionQuote)
	dq := strings.TrimSpace(d.DraftQuote)

	// Attribute the deviation to a specific document under review: trust the model's label
	// only when it matches a corpus review doc; otherwise locate the draft quote's document.
	docTitle := ""
	if corpus != nil {
		if md := strings.ToLower(strings.TrimSpace(d.Document)); md != "" {
			for _, dd := range corpus.docs {
				lt := strings.ToLower(dd.title)
				if !dd.controlling && (strings.Contains(lt, md) || strings.Contains(md, lt)) {
					docTitle = dd.title
					break
				}
			}
		}
		if docTitle == "" && dq != "" {
			for _, dd := range corpus.docs {
				if !dd.controlling && strings.Contains(dd.norm, figNorm(dq)) {
					docTitle = dd.title
					break
				}
			}
		}
	}
	docLabel := docTitle
	if docLabel == "" {
		docLabel = "the document under review"
	}
	ctrlTitle := "controlling source"
	if corpus != nil && iq != "" {
		for _, dd := range corpus.docs {
			if dd.controlling && strings.Contains(dd.norm, figNorm(iq)) {
				ctrlTitle = dd.title
				break
			}
		}
	}

	// MULTI-PART: per-part verdicts, each independently grounded. A part that fails its
	// substring lock is dropped (never emitted ungrounded); an omission part is confirmed
	// against the reviewed document's own sections.
	type partVerdict struct{ name, status, iq, dq, note string }
	var conforming []string
	var deviating []partVerdict
	for pi, p := range d.Parts {
		if pi >= devMaxParts {
			break
		}
		name := strings.TrimSpace(p.Part)
		if name == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(p.Status)) {
		case "conforms":
			conforming = append(conforming, name)
		case "conflict":
			if locked(p.InstructionQuote) && locked(p.DraftQuote) {
				deviating = append(deviating, partVerdict{name, "conflict", strings.TrimSpace(p.InstructionQuote), strings.TrimSpace(p.DraftQuote), strings.TrimSpace(p.Note)})
			}
		case "omission":
			if locked(p.InstructionQuote) && o.confirmOmission(task, corpus, prov, model, name+" — "+req, docTitle) {
				deviating = append(deviating, partVerdict{name, "omission", strings.TrimSpace(p.InstructionQuote), "", strings.TrimSpace(p.Note)})
			}
		}
	}

	sev := strings.ToLower(strings.TrimSpace(d.Severity))
	if sev == "" {
		sev = "medium"
	}

	// GROUNDED VALUES: the recommendation may state values; any value not present in the
	// retrieved passages is fabricated — replace with the generic correction, never as-is.
	rec := strings.TrimSpace(d.Recommendation)
	if rec != "" && len(devUnverifiedValues(rec, ctx)) > 0 {
		rec = "Conform the document under review to the quoted controlling-source language."
	}

	if len(deviating) > 0 {
		// PARTIAL IMPLEMENTATION: itemized per-part verdicts. Partial coverage can never
		// present as full — the missing/wrong parts are named, with their verbatim quotes.
		var b strings.Builder
		fmt.Fprintf(&b, "DEVIATION (partial implementation — %s severity) — %s: %s implements this requirement only in part.", sev, req, docLabel)
		if len(conforming) > 0 {
			fmt.Fprintf(&b, " Conforming sub-parts: %s.", strings.Join(conforming, "; "))
		}
		b.WriteString(" Deviating sub-parts:")
		cites := []types.Citation{}
		for k, p := range deviating {
			switch p.status {
			case "conflict":
				fmt.Fprintf(&b, " (%d) %s — CONFLICT: the CONTROLLING SOURCE states %q but the document states %q.", k+1, p.name, p.iq, p.dq)
				cites = append(cites,
					types.Citation{Source: ctrlTitle, Quote: p.iq, MechanicallyVerified: true},
					types.Citation{Source: docLabel, Quote: p.dq, MechanicallyVerified: true})
			default:
				fmt.Fprintf(&b, " (%d) %s — OMITTED: the CONTROLLING SOURCE states %q but no implementing provision was found.", k+1, p.name, p.iq)
				cites = append(cites, types.Citation{Source: ctrlTitle, Quote: p.iq, MechanicallyVerified: true})
			}
			if p.note != "" && len(devUnverifiedValues(p.note, ctx)) == 0 {
				fmt.Fprintf(&b, " (%s)", p.note)
			}
		}
		if rec != "" {
			b.WriteString(" Recommended correction: " + rec)
		}
		return b.String(), cites
	}

	// SINGLE VERDICT path.
	if strings.TrimSpace(d.Summary) == "" {
		return "", nil
	}
	// Conform-leak guard: the model sometimes emits a "deviation" whose own summary says the
	// document CONFORMS ("the document correctly states…", "already includes…"). Those are not
	// deviations — drop them so they don't clutter the report and waste a slot.
	lowSum := strings.ToLower(d.Summary)
	// Precise conformance phrases only — must not false-positive on a real deviation that says
	// "does not correctly apply …" (bare "correctly" would). These affirm the document conforms.
	for _, conform := range []string{"the document correctly", "already includes", "already contains", "already provides", "already reflects", "conforms to the", "is consistent with the", "matches the requirement", "matches the instruction", "no deviation", "no conflict", "does not deviate from"} {
		if strings.Contains(lowSum, conform) {
			return "", nil
		}
	}
	typ := strings.ToLower(strings.TrimSpace(d.Type))
	// The instruction quote must be VERBATIM in the retrieved passages for BOTH types — the
	// requirement must genuinely be instructed (no fabricated "the client wanted …").
	if !locked(iq) {
		return "", nil
	}
	label := "DEVIATION"
	switch typ {
	case "conflict":
		// CONFLICT — the draft value must ALSO be verbatim, or it's a fabricated conflict.
		if !locked(dq) {
			return "", nil
		}
	case "omission":
		// OMISSION — there is no draft quote (the provision is absent). Ground it with a focused
		// second look: retrieve the reviewed document's own sections on this provision and have
		// the model judge PRESENT vs ABSENT, told explicitly that a HEMS-style mention ("health,
		// education, maintenance, support") is NOT the provision. A keyword check can't make that
		// call — the word "education" is in every trust; a separate education trust may still be
		// missing.
		if !o.confirmOmission(task, corpus, prov, model, strings.TrimSpace(d.RequiredProvision), docTitle) {
			return "", nil
		}
		label = "OMISSION"
	default:
		return "", nil // type=none / unknown
	}

	// GROUNDED VALUES: every value the summary asserts must appear in the retrieved passages
	// (or be simple arithmetic over values that do). A summary carrying a value found nowhere
	// is withheld — the verbatim quotes speak instead, with an explicit flag.
	sum := strings.TrimSpace(d.Summary)
	if len(devUnverifiedValues(sum, ctx)) > 0 {
		sum = ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s severity) — ", label, sev)
	if sum != "" {
		b.WriteString(sum)
	} else {
		if typ == "conflict" {
			fmt.Fprintf(&b, "the CONTROLLING SOURCE requires %q but %s states %q", iq, docLabel, dq)
		} else {
			fmt.Fprintf(&b, "the CONTROLLING SOURCE requires %q but %s does not implement it", iq, docLabel)
		}
		b.WriteString(" [model summary withheld: it asserted a value not present in the quoted sources]")
	}
	// Both verbatim quotes always travel with the finding — the grounded evidence for the
	// claim, and the document under review is named (a deliverable must say WHICH draft).
	if typ == "conflict" {
		fmt.Fprintf(&b, " The CONTROLLING SOURCE states: %q — the DOCUMENT UNDER REVIEW (%s) states: %q.", iq, docLabel, dq)
	} else {
		fmt.Fprintf(&b, " The CONTROLLING SOURCE states: %q — no implementing provision found in %s.", iq, docLabel)
	}
	if rec != "" {
		b.WriteString(" Recommended correction: " + rec)
	}
	cites := []types.Citation{{Source: ctrlTitle, Quote: iq, MechanicallyVerified: true}}
	if typ == "conflict" {
		cites = append(cites, types.Citation{Source: docLabel, Quote: dq, MechanicallyVerified: true})
	}
	return b.String(), cites
}

// ─── Grounded-value discipline ─────────────────────────────────────────────────

// reDevValueTok scans assertions for value tokens: money ($92,600), percentages (35%),
// and substantial numbers (612847; 200,000). Small bare integers (list positions, "3
// physicians") are deliberately out of scope — the discipline targets asserted VALUES.
var reDevValueTok = regexp.MustCompile(`\$\s?\d[\d,]*(?:\.\d+)?|\d+(?:\.\d+)?\s*%|\b\d[\d,]{3,}\b`)

// devCanonValue normalizes a value token for comparison ($ , % and spaces stripped,
// numeric canonical form) so "$92,600" ≡ "$92600" and "35 %" ≡ "35%".
func devCanonValue(tok string) string {
	s := strings.NewReplacer("$", "", "%", "", ",", "", " ", "").Replace(tok)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// devUnverifiedValues returns the value tokens asserted in `assertion` that neither appear
// in `ground` (the retrieved passages) nor derive from grounded values by simple arithmetic.
// The arithmetic allowance keeps legitimate derived totals ("40% + 35% + 25% = 100%") and
// deltas alive; a genuinely fabricated value verifies against nothing.
func devUnverifiedValues(assertion, ground string) []string {
	toks := reDevValueTok.FindAllString(assertion, -1)
	if len(toks) == 0 {
		return nil
	}
	gset := map[string]bool{}
	var gvals []float64
	for _, g := range reDevValueTok.FindAllString(ground, -1) {
		c := devCanonValue(g)
		if c == "" || gset[c] {
			continue
		}
		gset[c] = true
		if f, err := strconv.ParseFloat(c, 64); err == nil && len(gvals) < devMaxGvals {
			gvals = append(gvals, f)
		}
	}
	var bad []string
	for _, tok := range toks {
		c := devCanonValue(tok)
		if c == "" || gset[c] || devDerivable(c, gvals) {
			continue
		}
		bad = append(bad, tok)
	}
	return bad
}

// devDerivable reports whether a value equals a sum/difference of two, or a sum of three,
// grounded values — computed totals and deltas are legitimate arithmetic, not fabrication.
func devDerivable(canon string, vals []float64) bool {
	want, err := strconv.ParseFloat(canon, 64)
	if err != nil {
		return false
	}
	const eps = 1e-6
	eq := func(a float64) bool { return a-want < eps && want-a < eps }
	for i := 0; i < len(vals); i++ {
		for j := i + 1; j < len(vals); j++ {
			if eq(vals[i]+vals[j]) || eq(vals[i]-vals[j]) || eq(vals[j]-vals[i]) {
				return true
			}
			for k := j + 1; k < len(vals); k++ {
				if eq(vals[i] + vals[j] + vals[k]) {
					return true
				}
			}
		}
	}
	return false
}

// ─── Mechanical numeric join (controlling ↔ under-review) ─────────────────────

const devNumJoinNote = "The document under review does not carry the value the controlling source requires; conform it to the controlling source."

// deviationNumericJoin joins the figure harvest's raw records (the harvestAndBindFigures
// seam — already normalized to canonical quantity labels) across the compare corpus:
// values sharing a metric identity (canonical label + value kind; crossdoc.go's machinery)
// where the controlling source and a document under review DISAGREE are mechanical
// deviation candidates. Each is substring-locked against its document, guarded by the
// enumeration rule (a value the controlling source also carries is conformance restated,
// not deviation) and crossdoc's referent floor / duration false-friend rules, adjudicated
// (adjudicateContradiction) when a judge is available, and emitted with BOTH verbatim
// quotes as mechanically-verified citations. Returns the findings plus their dedup
// signatures so the per-requirement pass doesn't restate them.
func (o *Orchestrator) deviationNumericJoin(taskID string, raw []figureHit, corpus *devCorpus, prov providers.Provider, model string) ([]types.Finding, []map[string]bool) {
	if len(raw) == 0 || corpus == nil {
		return nil, nil
	}
	ctrl := map[string]bool{}
	norm := map[string]string{}
	hasCtrl, hasRev := false, false
	for _, d := range corpus.docs {
		ctrl[d.title] = d.controlling
		norm[d.title] = d.norm
		if d.controlling {
			hasCtrl = true
		} else {
			hasRev = true
		}
	}
	if !hasCtrl || !hasRev {
		return nil, nil
	}
	type entry struct {
		hit     figureHit
		kind    string
		canon   string
		date    crossDocDate
		section string
	}
	var entries []entry
	seen := map[string]bool{}
	for _, h := range raw {
		if strings.TrimSpace(h.Value) == "" || strings.TrimSpace(h.Quote) == "" || strings.TrimSpace(h.Source) == "" {
			continue
		}
		n, inCorpus := norm[h.Source]
		if !inCorpus || !strings.Contains(n, figNorm(h.Quote)) {
			continue // outside the compare corpus, or fails the substring lock
		}
		kind, canon, dt := classifyFigureValue(h.Value)
		if kind == crossDocOther {
			continue
		}
		k := figNorm(h.Source) + "|" + canon + "|" + figNorm(h.Measures) + "|" + figNorm(h.Quote)
		if seen[k] {
			continue
		}
		seen[k] = true
		ctxw := h.Context
		if ctxw == "" {
			ctxw = h.Quote
		}
		entries = append(entries, entry{h, kind, canon, dt, crossDocSection(ctxw)})
	}
	// Group by metric identity: canonical quantity label + value kind.
	byKey := map[string][]int{}
	var order []string
	for i, e := range entries {
		lbl := figNorm(e.hit.Measures)
		if lbl == "" {
			continue // a bare number is never compared
		}
		k := lbl + "|" + e.kind
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], i)
	}
	adjBudget := devNumJoinCap
	var out []types.Finding
	var sigs []map[string]bool
	for _, gk := range order {
		var ctrlList []entry
		revLists := map[string][]entry{}
		var revOrder []string
		seenVal := map[string]bool{}
		for _, i := range byKey[gk] {
			e := entries[i]
			vk := e.hit.Source + "|" + e.canon
			if seenVal[vk] {
				continue
			}
			seenVal[vk] = true
			if ctrl[e.hit.Source] {
				ctrlList = append(ctrlList, e)
			} else {
				if _, ok := revLists[e.hit.Source]; !ok {
					revOrder = append(revOrder, e.hit.Source)
				}
				revLists[e.hit.Source] = append(revLists[e.hit.Source], e)
			}
		}
		if len(ctrlList) == 0 || len(ctrlList) > crossDocMaxValuesPerMetric {
			continue
		}
		ctrlCanon := map[string]bool{}
		for _, e := range ctrlList {
			ctrlCanon[e.canon] = true
		}
		for _, src := range revOrder {
			rl := revLists[src]
			if len(rl) == 0 || len(rl) > crossDocMaxValuesPerMetric {
				continue
			}
			// Enumeration guard: a reviewed value the controlling source also states is a
			// restatement (conformance), not a deviation — the sets must be DISJOINT.
			disjoint := true
			for _, e := range rl {
				if ctrlCanon[e.canon] {
					disjoint = false
					break
				}
			}
			if !disjoint {
				continue
			}
			var a, b entry
			found := false
			for _, ea := range ctrlList {
				for _, eb := range rl {
					if ea.kind == crossDocDateKind {
						if !ea.date.compatible(eb.date) {
							a, b, found = ea, eb, true
						}
					} else {
						a, b, found = ea, eb, true
					}
					if found {
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				continue
			}
			noJudge := prov == nil || model == ""
			if noJudge {
				// Deterministic guards (crossdoc's): bare durations are the deadline-vs-deadline
				// false-friend shape, and the referent floor must hold between the pair.
				if a.kind == crossDocDuration {
					continue
				}
				if crossDocSharedReferents(a.hit, b.hit) < crossDocMinSharedReferents {
					continue
				}
			} else {
				if adjBudget <= 0 {
					continue
				}
				adjBudget--
				ent, meas := clusterLabel([]figureHit{a.hit, b.hit})
				real, _ := o.adjudicateContradiction(prov, model, ent, meas, []figureHit{a.hit, b.hit})
				if !real {
					continue
				}
			}
			_, meas := clusterLabel([]figureHit{a.hit, b.hit})
			if strings.TrimSpace(meas) == "" || meas == "the same quantity" {
				meas = "required value"
			}
			locA, locB := a.hit.Source, b.hit.Source
			if a.section != "" {
				locA += ", " + a.section
			}
			if b.section != "" {
				locB += ", " + b.section
			}
			content := fmt.Sprintf("DEVIATION (numeric mismatch, high severity) — %s: the CONTROLLING SOURCE (%s) states %s — %q — but the DOCUMENT UNDER REVIEW (%s) states %s — %q. %s",
				meas, locA, a.hit.Value, a.hit.Quote, locB, b.hit.Value, b.hit.Quote, devNumJoinNote)
			sig := devSignature(meas + " " + a.hit.Value + " " + b.hit.Value + " " + a.hit.Source + " " + b.hit.Source)
			dup := false
			for _, prev := range sigs {
				if jaccard(sig, prev) > 0.5 {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			sigs = append(sigs, sig)
			out = append(out, types.Finding{
				ID:        uuid.NewString(),
				AgentID:   "deviation-detector",
				AgentName: "Deviation Detector",
				Content:   content,
				Citations: []types.Citation{
					{Source: a.hit.Source, Quote: a.hit.Quote, MechanicallyVerified: true},
					{Source: b.hit.Source, Quote: b.hit.Quote, MechanicallyVerified: true},
				},
				Confidence:     0.95,
				EvidenceStatus: types.EvidenceGrounded,
				Timestamp:      time.Now(),
			})
		}
	}
	_ = taskID
	return out, sigs
}

// ─── Dedup helpers ─────────────────────────────────────────────────────────────

// devNorm normalizes for the substring lock (collapse whitespace, lowercase) so a verbatim quote
// verifies despite spacing/case drift, but a fabricated value still fails.
func devNorm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// devCore extracts the CLAIM HEAD from a rendered deviation — "who did what wrong" — for dedup.
// It strips the severity label, the "Recommended correction:" tail, AND the impact/subordinate
// clause (", which …", "… despite …"), because two findings of the SAME underlying issue share
// the head but diverge in their impact wording — comparing the full summary let near-duplicates
// through (jaccard fell to 0.43). The head is the stable dedup key; "why it matters" is variable.
func devCore(dev string) string {
	s := dev
	if i := strings.Index(s, "— "); i >= 0 {
		s = s[i+len("— "):]
	}
	if i := strings.Index(s, "Recommended correction:"); i >= 0 {
		s = s[:i]
	}
	lo := strings.ToLower(s)
	cut := len(s)
	for _, mk := range []string{", which ", ", creating ", ", resulting ", ", posing ", ", risking ", ", so that ", ", potentially ", " despite ", " which conflicts", " which creates"} {
		if i := strings.Index(lo, mk); i >= 0 && i < cut {
			cut = i
		}
	}
	return s[:cut]
}

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

// ─── Omission confirmation ─────────────────────────────────────────────────────

// omissionCheckSystem verifies whether a DRAFT actually establishes a required provision, told
// explicitly that a related word in another context (HEMS) is not the provision. One word out.
const omissionCheckSystem = "You verify whether a DOCUMENT under review actually implements a REQUIRED provision. You are given the required provision and the document's own sections most relevant to it. Answer with ONLY one word: PRESENT if the document genuinely establishes or implements that provision, or ABSENT if it does not. IMPORTANT: a passing mention of a related term does NOT count — a word appearing inside a boilerplate list, a different defined standard, or unrelated context is not the required standalone provision (e.g. 'education' inside a 'health, education, maintenance, and support' standard is not a separate education trust; the word 'indemnify' in a recital is not an indemnification clause). Only a genuine, structural implementation of the required provision counts as PRESENT."

// confirmOmission grounds an OMISSION claim: it retrieves the reviewed document's OWN sections
// on the provision (scoped to the named document when the deviation is attributed — a provision
// present in the trust must not mask its absence from the will) and asks the model whether the
// provision is genuinely established, guarding against the keyword false-friend (the word is
// present, the provision is not). No sections at all → omitted; the model's ABSENT verdict →
// confirmed.
func (o *Orchestrator) confirmOmission(task *types.Task, corpus *devCorpus, prov providers.Provider, model, provision, docTitle string) bool {
	if strings.TrimSpace(provision) == "" {
		return false
	}
	draftCtx := o.retrieveDraftContext(task, corpus, provision, docTitle)
	if strings.TrimSpace(draftCtx) == "" {
		return true // the reviewed document says nothing on this provision → omitted
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

// retrieveDraftContext pulls passages on a topic from the documents under review only —
// scoped to one named document when given (per-document omission judgment), excluding the
// controlling source. Deterministic section-walk floor plus semantic augmentation.
func (o *Orchestrator) retrieveDraftContext(task *types.Task, corpus *devCorpus, query, docTitle string) string {
	var b strings.Builder
	if corpus != nil {
		terms := devTerms(query)
		for _, d := range corpus.docs {
			if d.controlling || (docTitle != "" && d.title != docTitle) {
				continue
			}
			for _, ch := range d.topChunks(terms, devPerDocK) {
				fmt.Fprintf(&b, "%s\n", ch)
			}
		}
	}
	for _, rc := range o.deviationSearch(task, query, 14) {
		if corpus.controllingSrc(rc[0]) {
			continue
		}
		if docTitle != "" && rc[0] != docTitle {
			continue
		}
		fmt.Fprintf(&b, "%s\n", rc[1])
	}
	return strutil.TruncateToTokens(b.String(), devDraftCtxTok)
}

// isDraftSource reports whether a retrieval source is a draft under review (not the controlling
// instruction memo / background summary).
func isDraftSource(src string) bool {
	s := strings.ToLower(src)
	return s != "" && !strings.Contains(s, "instruction") && !strings.Contains(s, "memo") && !strings.Contains(s, "background")
}
