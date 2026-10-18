// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"context"
	"fmt"
	"regexp"
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

// normalize straightens curly quotes and hyphens, folds case and collapses each white-space run to
// one space.
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

var wordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

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
}

// VerifyQuote runs the ladder on one document in scope.
func (s *Service) VerifyQuote(ctx context.Context, scope []string, docID, quote string, judge Judge) (VerifiedQuote, error) {
	d, err := s.Get(ctx, scope, docID)
	if err != nil {
		return VerifiedQuote{}, err
	}
	v, err := Verify(ctx, d.Text, quote, judge)
	out := VerifiedQuote{Verification: v}
	if err != nil || !v.Verified {
		return out, err
	}
	var ord int
	at := utf8.RuneCountInString(d.Text[:v.Start])
	if err := s.db.QueryRowContext(ctx, `SELECT ord, number FROM sections WHERE document_id = $1 AND char_start <= $2 AND $2 < greatest(char_end, char_start + 1)
		ORDER BY level DESC, ord DESC LIMIT 1`, docID, at).Scan(&ord, &out.Number); err != nil {
		return VerifiedQuote{}, fmt.Errorf("docs: verify: locate: %w", err)
	}
	out.SectionID = sectionID(ord)
	return out, nil
}
