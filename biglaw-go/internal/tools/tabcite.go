// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Citation verification for tabular review. Every [[page:N||quote:...]] marker
// embedded in a cell summary is checked against the source document at
// extraction time, climbing a ladder of methods (stop at the first success):
//
//  1. exact_match       — the quote is a verbatim substring (confidence 1.0)
//  2. tolerant_match    — substring after normalising curly quotes/apostrophes
//                         and whitespace runs on both sides (confidence 0.95)
//  3. paraphrase_judge  — one extraction-tier model call over a candidate
//                         window of the source (confidence capped at 0.8)
//  4. ensemble_majority — when the single judge is uncertain: 3 independent
//                         judge calls, majority verdict, confidence scaled
//                         below the paraphrase cap
//
// A citation that fails every rung is recorded as method "unverified" with
// confidence 0. Judge failures degrade that one citation — never the cell or
// the matrix. Judge calls run inside the per-cell goroutine, which already
// holds a slot of the invocation-wide extraction semaphore, so the total
// number of in-flight model calls stays bounded by maxConcurrentCellCalls.

package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
)

// Verification methods, in ladder order. "unverified" is the terminal state.
const (
	citeMethodExact      = "exact_match"
	citeMethodTolerant   = "tolerant_match"
	citeMethodParaphrase = "paraphrase_judge"
	citeMethodEnsemble   = "ensemble_majority"
	citeMethodUnverified = "unverified"
)

const (
	// citeExactConfidence / citeTolerantConfidence are the fixed confidences
	// for the two string-matching rungs.
	citeExactConfidence    = 1.0
	citeTolerantConfidence = 0.95
	// citeParaphraseCap bounds the confidence a single paraphrase judge can
	// confer — a model opinion never outranks a string match.
	citeParaphraseCap = 0.8
	// citeEnsembleBand is the uncertainty band: a paraphrase judge whose
	// stated confidence falls below this threshold escalates to the 3-vote
	// ensemble. Verdicts the judge itself doubts are exactly the ones that
	// need votes, so the band runs from 0 up to this bound.
	citeEnsembleBand = 0.75
	// citeEnsembleVotes is the number of independent judge calls in the
	// ensemble rung.
	citeEnsembleVotes = 3
	// citeEnsembleScale maps the vote share into the ladder: confidence =
	// scale × supportedVotes/3, so even a unanimous ensemble (0.75) sits
	// below the paraphrase cap (0.8).
	citeEnsembleScale = 0.75
	// citeJudgeMaxTokens bounds each judge response — a small JSON verdict.
	citeJudgeMaxTokens = 200
	// citeWindowChars caps the candidate source window shipped to the judge.
	citeWindowChars = 6000
	// citeWindowRadius is the half-width of a rare-token candidate window.
	citeWindowRadius = 1500
)

// Citation is one inline [[page:N||quote:...]] marker parsed from a cell
// summary, in order of appearance, annotated with its verification outcome.
type Citation struct {
	Page       int     `json:"page"`
	Quote      string  `json:"quote"`
	Verified   bool    `json:"verified"`
	Method     string  `json:"method"`
	Confidence float64 `json:"confidence"`
	// Note carries a degradation cause (judge error, unsupported verdict)
	// when the ladder could not verify the citation.
	Note string `json:"note,omitempty"`
}

// CitationTally is the matrix-level verification roll-up.
type CitationTally struct {
	Total    int            `json:"total"`
	Verified int            `json:"verified"`
	ByMethod map[string]int `json:"byMethod"`
}

func newCitationTally() *CitationTally {
	return &CitationTally{ByMethod: map[string]int{
		citeMethodExact:      0,
		citeMethodTolerant:   0,
		citeMethodParaphrase: 0,
		citeMethodEnsemble:   0,
		citeMethodUnverified: 0,
	}}
}

// add folds one cell's citations into the tally.
func (t *CitationTally) add(cites []Citation) {
	for _, c := range cites {
		t.Total++
		if c.Verified {
			t.Verified++
		}
		t.ByMethod[c.Method]++
	}
}

// ─── Parsing ─────────────────────────────────────────────────────────────────

const (
	citeOpen  = "[[page:"
	citeSep   = "||quote:"
	citeClose = "]]"
)

// parseCitations extracts every [[page:N||quote:...]] marker from a cell
// summary, in order. Malformed markers (missing separator or close) are
// skipped; a non-numeric page records as 0 rather than dropping the citation.
func parseCitations(summary string) []Citation {
	cites := []Citation{}
	rest := summary
	for {
		start := strings.Index(rest, citeOpen)
		if start < 0 {
			break
		}
		body := rest[start+len(citeOpen):]
		sep := strings.Index(body, citeSep)
		if sep < 0 {
			break
		}
		// A fresh opener before the separator means the current marker is
		// malformed — resume parsing from the inner opener.
		if inner := strings.Index(body[:sep], citeOpen); inner >= 0 {
			rest = body[inner:]
			continue
		}
		end := strings.Index(body[sep+len(citeSep):], citeClose)
		if end < 0 {
			break
		}
		pageStr := strings.TrimSpace(body[:sep])
		quote := strings.TrimSpace(body[sep+len(citeSep) : sep+len(citeSep)+end])
		rest = body[sep+len(citeSep)+end+len(citeClose):]
		if quote == "" {
			continue
		}
		page, _ := strconv.Atoi(pageStr) // non-numeric → 0
		cites = append(cites, Citation{Page: page, Quote: quote})
	}
	return cites
}

// ─── The ladder ──────────────────────────────────────────────────────────────

// verifyCellCitations parses the cell's inline citations and verifies each one
// against the source document, annotating the cell in place. Always sets a
// non-nil Citations slice so the JSON return shape is stable.
func (r *Registry) verifyCellCitations(prov providers.Provider, modelID, taskID, docText string, cell *ReviewCell) {
	cites := parseCitations(cell.Summary)
	for i := range cites {
		cites[i] = r.verifyCitation(prov, modelID, taskID, docText, cites[i])
		if cites[i].Verified {
			cell.CitationsVerified++
		}
	}
	cell.Citations = cites
	cell.CitationsTotal = len(cites)
}

// verifyCitation climbs the ladder for one citation, stopping at the first
// rung that succeeds.
func (r *Registry) verifyCitation(prov providers.Provider, modelID, taskID, docText string, c Citation) Citation {
	unverified := func(note string) Citation {
		c.Verified, c.Method, c.Confidence, c.Note = false, citeMethodUnverified, 0, note
		return c
	}
	if strings.TrimSpace(docText) == "" {
		return unverified("no source text available to verify against")
	}

	// Rung 1: verbatim substring.
	if strings.Contains(docText, c.Quote) {
		c.Verified, c.Method, c.Confidence = true, citeMethodExact, citeExactConfidence
		return c
	}

	// Rung 2: substring after normalising both sides (curly→straight quotes
	// and apostrophes, whitespace runs collapsed, trimmed) — the same
	// normalisation the redline anchoring uses (normalizeText, redline.go).
	normDoc := normalizeText(docText)
	normQuote := strings.TrimSpace(normalizeText(c.Quote))
	if normQuote != "" && strings.Contains(normDoc, normQuote) {
		c.Verified, c.Method, c.Confidence = true, citeMethodTolerant, citeTolerantConfidence
		return c
	}

	// Rung 3: one paraphrase judge call over a candidate window of the source.
	window := citationWindow(docText, c.Page, c.Quote)
	supported, conf, err := r.judgeCitation(prov, modelID, taskID, c.Quote, c.Page, window, nil)
	if err != nil {
		return unverified(fmt.Sprintf("judge call failed: %v", err))
	}
	if conf >= citeEnsembleBand {
		if !supported {
			return unverified(fmt.Sprintf("paraphrase judge found the quote unsupported (confidence %.2f)", conf))
		}
		c.Verified, c.Method = true, citeMethodParaphrase
		c.Confidence = conf
		if c.Confidence > citeParaphraseCap {
			c.Confidence = citeParaphraseCap
		}
		return c
	}

	// Rung 4: the judge is uncertain — 3 independent votes, majority verdict.
	votesFor, votesCast := 0, 0
	var lastErr error
	temp := 0.7 // sampling diversity keeps the three votes independent
	for i := 0; i < citeEnsembleVotes; i++ {
		s, _, jerr := r.judgeCitation(prov, modelID, taskID, c.Quote, c.Page, window, &temp)
		if jerr != nil {
			lastErr = jerr
			continue
		}
		votesCast++
		if s {
			votesFor++
		}
	}
	if votesCast < 2 {
		return unverified(fmt.Sprintf("ensemble degraded: only %d of %d judge calls succeeded (last error: %v)",
			votesCast, citeEnsembleVotes, lastErr))
	}
	if votesFor*2 <= votesCast { // no strict majority for "supported"
		return unverified(fmt.Sprintf("ensemble majority found the quote unsupported (%d/%d supported)", votesFor, votesCast))
	}
	c.Verified, c.Method = true, citeMethodEnsemble
	c.Confidence = citeEnsembleScale * float64(votesFor) / float64(citeEnsembleVotes)
	return c
}

// ─── The judge ───────────────────────────────────────────────────────────────

// citationJudgePrompt is the paraphrase-judge system prompt. Wording authored
// fresh for this implementation.
const citationJudgePrompt = `You verify whether a quoted excerpt attributed to a legal document is genuinely supported by a passage from that document.

Respond with a single JSON object and nothing else — no prose, no markdown fences:
{"supported": true, "confidence": 0.0}

"supported" is true only if the passage actually states what the quote asserts — an excerpt of text present in the passage, a close paraphrase of it, or the same text with minor OCR or transcription noise. It is false if the quote asserts anything the passage does not say, contradicts, or materially exceeds.
"confidence" is a number between 0.0 and 1.0 stating how certain you are of your verdict.`

// judgeVerdict is the JSON object each judge call must return.
type judgeVerdict struct {
	Supported  bool    `json:"supported"`
	Confidence float64 `json:"confidence"`
}

// judgeCitation runs one paraphrase-judge model call. The call is made while
// the caller's goroutine holds an extraction-semaphore slot, so judge traffic
// shares the invocation-wide concurrency budget; its cost is recorded against
// the task like every other tabular call.
func (r *Registry) judgeCitation(prov providers.Provider, modelID, taskID, quote string, page int, window string, temperature *float64) (supported bool, confidence float64, err error) {
	temp := 0.0
	if temperature != nil {
		temp = *temperature
	}
	user := fmt.Sprintf("QUOTE (attributed to page %d):\n%s\n\nSOURCE PASSAGE:\n%s", page, quote, window)
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(modelID),
		MaxTokens:   citeJudgeMaxTokens,
		System:      citationJudgePrompt,
		Messages:    []providers.Message{{Role: "user", Content: user}},
		CacheSystem: true,
		JSONMode:    true,
		Temperature: &temp,
	})
	if err != nil {
		return false, 0, err
	}
	r.recordTabularCost(resp, modelID, taskID)

	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var v judgeVerdict
	if uerr := json.Unmarshal([]byte(s), &v); uerr != nil {
		return false, 0, fmt.Errorf("could not parse judge verdict: %w", uerr)
	}
	if v.Confidence < 0 {
		v.Confidence = 0
	}
	if v.Confidence > 1 {
		v.Confidence = 1
	}
	return v.Supported, v.Confidence, nil
}

// ─── Candidate window location ───────────────────────────────────────────────

// citationWindow finds the region of the source most likely to contain the
// cited text, so the judge never receives the whole document. If the text
// carries form-feed page breaks the cited page (± one neighbour) is used;
// otherwise the window is located by rare-token overlap with the quote.
func citationWindow(docText string, page int, quote string) string {
	if strings.ContainsRune(docText, '\f') {
		pages := strings.Split(docText, "\f")
		if page >= 1 && page <= len(pages) {
			lo, hi := page-2, page // 0-based page-1, ± one neighbour
			if lo < 0 {
				lo = 0
			}
			if hi > len(pages)-1 {
				hi = len(pages) - 1
			}
			return truncateAtWord(strings.Join(pages[lo:hi+1], "\n"), citeWindowChars)
		}
	}
	return rareTokenWindow(docText, quote)
}

// rareTokenWindow locates the candidate window by rare-token overlap: the
// quote's longest words anchor candidate positions in the (case-folded,
// normalised) document, and the window around the anchor covering the most
// distinct quote words wins. The winning offset converts back to the original
// text through normalizeWithMap's byte map (the redline anchoring machinery).
// A quote sharing no rare tokens with the document falls back to the document
// head — the judge will (rightly) find it unsupported there.
func rareTokenWindow(docText, quote string) string {
	norm, backMap := normalizeWithMap(docText)
	docLower := asciiLower(norm)
	words := quoteAnchorWords(quote)

	bestStart, bestScore := 0, -1
	anchors := 0
	for _, w := range words {
		from := 0
		for anchors < 24 { // cap total anchor positions across all words
			i := strings.Index(docLower[from:], w)
			if i < 0 {
				break
			}
			pos := from + i
			anchors++
			from = pos + len(w)

			lo := pos - citeWindowRadius
			if lo < 0 {
				lo = 0
			}
			hi := pos + citeWindowRadius
			if hi > len(docLower) {
				hi = len(docLower)
			}
			win := docLower[lo:hi]
			score := 0
			for _, w2 := range words {
				if strings.Contains(win, w2) {
					score++
				}
			}
			if score > bestScore {
				bestScore, bestStart = score, lo
			}
		}
	}

	start := backMap[bestStart] // normalised offset → original-text offset
	end := start + 2*citeWindowRadius
	if end > len(docText) {
		end = len(docText)
	}
	return truncateAtWord(strings.TrimSpace(docText[start:end]), citeWindowChars)
}

// asciiLower lowercases A–Z byte-wise, preserving byte offsets exactly (the
// anchor search needs offsets that map back through normalizeWithMap's map;
// strings.ToLower can change byte lengths for some Unicode).
func asciiLower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// quoteAnchorWords picks the quote's rare-token anchors: distinct words of
// four or more characters, longest first (long words are the rarest and make
// the most selective anchors). Falls back to all words for very short quotes.
func quoteAnchorWords(quote string) []string {
	fields := strings.Fields(asciiLower(normalizeText(quote)))
	seen := map[string]bool{}
	var words []string
	for _, f := range fields {
		f = strings.Trim(f, `.,;:!?"'()[]{}`)
		if len(f) >= 4 && !seen[f] {
			seen[f] = true
			words = append(words, f)
		}
	}
	if len(words) == 0 {
		for _, f := range fields {
			if f != "" && !seen[f] {
				seen[f] = true
				words = append(words, f)
			}
		}
	}
	sort.SliceStable(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
	if len(words) > 8 {
		words = words[:8]
	}
	return words
}
