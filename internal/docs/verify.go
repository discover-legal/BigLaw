// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The ladder's rungs and their confidences (ported from v1 tools/tabcite.go): a model's opinion
// never outranks a string match, and an ensemble never outranks a single confident judge.
const (
	ExactConfidence    = 1.0
	TolerantConfidence = 0.95
	JudgeCap           = 0.8  // a single judge's confidence is capped here
	ensembleBand       = 0.75 // a judge less sure than this escalates to the ensemble
	ensembleVotes      = 3
	ensembleScale      = 0.75 // ensemble confidence = 0.75 × supporting votes / 3, so ≤ 0.75 < JudgeCap
	windowRadius       = 1500 // bytes of source on each side of the best-matching span
	repairOverlap      = 0.6  // share of the quote's words a span must hold to count as the quoted text
)

// Judge asks a model whether passage supports quote (an excerpt, a close paraphrase, or the same
// text with transcription noise). vote is true for an ensemble vote, which must be sampled
// independently. A failed call returns an error; it is never a verdict.
type Judge func(ctx context.Context, quote, passage string, vote bool) (supported bool, confidence float64, err error)

// Verification is the ladder's result. Start and End locate the matched text in the document
// (exact and tolerant: the match; judge and ensemble: the best-matching span, or the judged
// window when no span holds enough of the quote's words). Method is exact, tolerant, judge,
// ensemble or none.
type Verification struct {
	Verified   bool    `json:"verified"`
	Method     string  `json:"method"`
	Confidence float64 `json:"confidence"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Note       string  `json:"note,omitempty"`
}

// Verify climbs the ladder for quote against text, stopping at the first rung that succeeds:
// exact substring; tolerant substring (quotes straightened, white space collapsed, case folded on
// both sides); one judge call over the best-matching window; and, when the judge is unsure,
// three independent votes. A judge failure is returned as an error, never as "unverified".
func Verify(ctx context.Context, text, quote string, judge Judge) (Verification, error) {
	none := func(note string) Verification { return Verification{Method: "none", Note: note} }
	if strings.TrimSpace(quote) == "" {
		return none("the quote is empty"), nil
	}
	if i := strings.Index(text, quote); i >= 0 {
		return Verification{Verified: true, Method: "exact", Confidence: ExactConfidence, Start: i, End: i + len(quote)}, nil
	}
	nt, back := normalize(text)
	nq, _ := normalize(strings.TrimSpace(quote))
	if nq = strings.TrimSpace(nq); nq != "" {
		if i := strings.Index(nt, nq); i >= 0 {
			return Verification{Verified: true, Method: "tolerant", Confidence: TolerantConfidence, Start: back[i], End: back[i+len(nq)]}, nil
		}
	}
	if judge == nil {
		return none("the quote is not in the document, and no judge is configured for paraphrases"), nil
	}
	start, end, overlap := bestSpan(text, quote)
	lo, hi := runeStart(text, start-windowRadius), runeStart(text, end+windowRadius)
	passage := text[lo:hi]
	if overlap < repairOverlap {
		start, end = lo, hi
	}
	ok, conf, err := judge(ctx, quote, passage, false)
	if err != nil {
		return none("the paraphrase judge failed"), fmt.Errorf("docs: verify: judge: %w", err)
	}
	conf = min(max(conf, 0), 1)
	if conf >= ensembleBand {
		if !ok {
			return none(fmt.Sprintf("the paraphrase judge found the quote unsupported (confidence %.2f)", conf)), nil
		}
		return Verification{Verified: true, Method: "judge", Confidence: min(conf, JudgeCap), Start: start, End: end}, nil
	}
	votes := 0
	for range ensembleVotes {
		ok, _, err := judge(ctx, quote, passage, true)
		if err != nil { // any lost vote could have decided the majority: never a verdict on fewer
			return none("an ensemble vote failed"), fmt.Errorf("docs: verify: judge: %w", err)
		} else if ok {
			votes++
		}
	}
	if votes*2 <= ensembleVotes {
		return none(fmt.Sprintf("the ensemble found the quote unsupported (%d of %d votes)", votes, ensembleVotes)), nil
	}
	return Verification{Verified: true, Method: "ensemble", Confidence: ensembleScale * float64(votes) / ensembleVotes, Start: start, End: end}, nil
}

// Text is a document's text prepared for many quote searches: normalised once (Verify's tolerant
// rung), so a caller placing several quotes in one document does not normalise it for each.
type Text struct {
	raw, norm string
	back      []int
}

// NewText prepares s for Find.
func NewText(s string) *Text {
	n, b := normalize(s)
	return &Text{s, n, b}
}

// Find is Verify's string rungs (exact, tolerant) for quote within text[from:to]: the byte offsets of
// the earliest match (the exact one when both start at the same byte). ok is false when neither holds it.
func (t *Text) Find(quote string, from, to int) (start, end int, ok bool) {
	from, to = max(from, 0), min(to, len(t.raw))
	if from > to || strings.TrimSpace(quote) == "" {
		return 0, 0, false
	}
	start = -1
	if i := strings.Index(t.raw[from:to], quote); i >= 0 {
		start, end = from+i, from+i+len(quote)
	}
	nq, _ := normalize(strings.TrimSpace(quote))
	lo, hi := sort.SearchInts(t.back, from), sort.SearchInts(t.back, to) // t.back is non-decreasing
	if nq = strings.TrimSpace(nq); nq != "" && lo <= hi && hi <= len(t.norm) {
		if i := strings.Index(t.norm[lo:hi], nq); i >= 0 && (start < 0 || t.back[lo+i] < start) {
			start, end = t.back[lo+i], t.back[lo+i+len(nq)]
		}
	}
	return start, end, start >= 0
}

// normalize straightens curly quotes and hyphens, folds case, drops format characters (zero-width
// spaces and joiners, bidi marks, soft hyphens) and collapses each white-space run to one space.
// back maps every byte of the result (and one past its end) to a byte offset in s.
func normalize(s string) (string, []int) {
	var b strings.Builder
	back := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			j := i
			for j < len(s) {
				r2, n2 := utf8.DecodeRuneInString(s[j:])
				if !unicode.IsSpace(r2) {
					break
				}
				j += n2
			}
			b.WriteByte(' ')
			back = append(back, i)
			i = j
			continue
		}
		if unicode.Is(unicode.Cf, r) { // a zero-width or format character renders as nothing: "N\u200bOT" is "NOT"
			i += n
			continue
		}
		switch r {
		case '‘', '’', '‚', '‛':
			r = '\''
		case '“', '”', '„', '‟':
			r = '"'
		case '\u2010', '\u2011': // hyphen, non-breaking hyphen (a .docx's <w:noBreakHyphen/>)
			r = '-'
		default:
			r = unicode.ToLower(r)
		}
		before := b.Len()
		b.WriteRune(r)
		for range b.Len() - before {
			back = append(back, i)
		}
		i += n
	}
	return b.String(), append(back, len(s))
}

var wordRe, edgeRe = regexp.MustCompile(`[\p{L}\p{N}]+`), regexp.MustCompile(`[\p{L}\p{N}]\x00|\x00[\p{L}\p{N}]|\x00[.,]\p{N}|\p{N}[., ]\x00`) // edgeRe: a letter or digit, or a figure's separator, next to a quote's copy (NUL)

// bestSpan finds the run of the quote's length in words that holds the largest share of the
// quote's distinct words (v1 protocols.repairQuote), as byte offsets of text.
func bestSpan(text, quote string) (start, end int, overlap float64) {
	want := map[string]bool{}
	qw := wordRe.FindAllString(strings.ToLower(quote), -1)
	for _, w := range qw {
		want[w] = true
	}
	locs := wordRe.FindAllStringIndex(text, -1)
	if len(want) == 0 || len(locs) == 0 {
		return 0, min(len(text), 2*windowRadius), 0
	}
	lower := func(k int) string { return strings.ToLower(text[locs[k][0]:locs[k][1]]) }
	count, hits := map[string]int{}, 0 // a sliding window of len(qw) words
	for k := 0; k < len(locs); k++ {
		if w := lower(k); want[w] {
			if count[w]++; count[w] == 1 {
				hits++
			}
		}
		if first := k - len(qw); first >= 0 {
			if w := lower(first); want[w] {
				if count[w]--; count[w] == 0 {
					hits--
				}
			}
		}
		if ov := float64(hits) / float64(len(want)); ov > overlap {
			overlap, start, end = ov, locs[max(0, k-len(qw)+1)][0], locs[k][1]
		}
	}
	if overlap == 0 {
		return 0, min(len(text), 2*windowRadius), 0
	}
	return start, end, overlap
}

// Passage is where a document best matches a quote: the section holding it and its exact text.
type Passage struct {
	SectionID, Number, Text string
}

// Nearest is the passage of a document that best matches quote (the run of the quote's length in
// words with most of its words), as the document has it: the whole sentences or lines that hold it
// when they fit in max bytes, else cut at a word boundary to at most max bytes, and the section that holds it: the words a paraphrase restates, to copy instead. ok is
// false when no word of quote is in the document.
func (s *Service) Nearest(ctx context.Context, scope []string, docID, quote string, max int) (p Passage, ok bool, err error) {
	d, err := s.Get(ctx, scope, docID)
	if err != nil {
		return Passage{}, false, err
	}
	start, end, overlap := bestSpan(d.Text, quote)
	if overlap == 0 {
		return Passage{}, false, nil
	}
	if ls, le := sentenceStart(d.Text, start), sentenceEnd(d.Text, end); le-ls <= max { // the whole sentences or lines that hold it
		start, end = ls, le
	} else if end-start > max {
		cut := strings.LastIndexFunc(d.Text[start:start+max+1], unicode.IsSpace)
		end = start + cmpInt(cut > 0, cut, max)
		end = runeStart(d.Text, end)
	}
	p.Text = strings.TrimSpace(d.Text[start:end])
	p.SectionID, p.Number, err = s.locate(ctx, docID, utf8.RuneCountInString(d.Text[:start]))
	return p, err == nil, err
}

// sentenceStart is where the sentence or line holding byte i of text begins: after the last line
// break, or sentence end (. ! ? and white space), before it.
func sentenceStart(text string, i int) int {
	i = runeStart(text, i)
	for j := i; j > 0; j-- {
		if text[j-1] == '\n' || j >= 2 && strings.ContainsRune(".!?", rune(text[j-2])) && (text[j-1] == ' ' || text[j-1] == '\t') {
			return j
		}
	}
	return 0
}

// sentenceEnd is where the sentence or line holding the byte before i of text ends: at the next line
// break, or just after the next sentence end (. ! ? before white space or the end).
func sentenceEnd(text string, i int) int {
	for j := max(runeStart(text, i), 1); j < len(text); j++ {
		if text[j] == '\n' {
			return j
		}
		if strings.ContainsRune(".!?", rune(text[j-1])) && (text[j] == ' ' || text[j] == '\t') {
			return j
		}
	}
	return len(text)
}

func cmpInt(c bool, a, b int) int {
	if c {
		return a
	}
	return b
}

// locate is the innermost section holding character at of a document.
func (s *Service) locate(ctx context.Context, docID string, at int) (string, string, error) {
	var ord int
	var number string
	if err := s.db.QueryRowContext(ctx, `SELECT ord, number FROM sections WHERE document_id = $1 AND char_start <= $2 AND $2 < greatest(char_end, char_start + 1)
		ORDER BY level DESC, ord DESC LIMIT 1`, docID, at).Scan(&ord, &number); err != nil {
		return "", "", fmt.Errorf("docs: verify: locate: %w", err)
	}
	return sectionID(ord), number, nil
}

// runeStart clamps i into text and moves it back to a rune boundary.
func runeStart(text string, i int) int {
	i = min(max(i, 0), len(text))
	for i > 0 && i < len(text) && !utf8.RuneStart(text[i]) {
		i--
	}
	return i
}

// VerifiedQuote is Verify's result with the section that holds the match.
type VerifiedQuote struct {
	Verification
	SectionID string `json:"sectionId,omitempty"`
	Number    string `json:"number,omitempty"`
	// TranscribedBy names the vision model when the match is in transcribed text: the method gets
	// "@transcript", the confidence at most TranscriptCap, and LowConfidence marks a page whose two
	// readings agreed less than lowBelow (a finding resting only on it is gated, PR 4b).
	TranscribedBy string `json:"transcribedBy,omitempty"`
	LowConfidence bool   `json:"lowConfidence,omitempty"`
	Match         string `json:"match,omitempty"` // the source text matched (exact, tolerant) or judged
}

// VerifyQuote runs the ladder on one document in scope.
func (s *Service) VerifyQuote(ctx context.Context, scope []string, docID, quote string, judge Judge) (VerifiedQuote, error) {
	d, err := s.Get(ctx, scope, docID)
	if err != nil {
		return VerifiedQuote{}, err
	}
	v, err := Verify(ctx, d.Text, quote, judge)
	if err != nil || !v.Verified {
		return VerifiedQuote{Verification: v}, err
	}
	for i := v.Start + 1; v.Method == "exact" && d.Transcript.covers(v.Start, v.End) != nil && strings.Contains(d.Text[i:], quote); i++ { // a later whole-word copy outside the transcript (a text layer's)
		if i += strings.Index(d.Text[i:], quote); !edgeRe.MatchString(d.Text[max(0, i-4):i] + "\x00" + d.Text[i+len(quote):min(len(d.Text), i+len(quote)+4)]) {
			v.Start, v.End = i, i+len(quote)
		}
	}
	out := VerifiedQuote{Verification: v, Match: d.Text[v.Start:v.End]}
	if out.SectionID, out.Number, err = s.locate(ctx, docID, utf8.RuneCountInString(d.Text[:v.Start])); err != nil {
		return VerifiedQuote{}, err
	}
	if p := d.Transcript.covers(v.Start, v.End); p != nil {
		out.Method, out.Confidence = v.Method+"@transcript", min(v.Confidence, TranscriptCap)
		out.TranscribedBy, out.LowConfidence = d.Transcript.Model, p.low()
		out.Note = "machine transcription of an image (" + d.Transcript.Model + "): check against the original"
	}
	return out, nil
}
