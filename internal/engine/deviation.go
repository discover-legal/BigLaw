// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
)

// The deviation path (v1 deviations.go, trust-compare 9 → 14/23), checked by Go: a deviation
// stands only on two verified quotes (the controlling requirement and the reviewed text), an
// omission only when Go confirms the provision is absent from the named reviewed document, and a
// correction never carries a figure the quotes do not state (or simply add up to).

var conformsRe = regexp.MustCompile(`(?i)\b(correctly (states|provides|reflects)|conforms? (to|with)|is consistent with|as required)\b`)

const genericCorrection = "Conform the document under review to the quoted controlling-source language."

type verdict struct {
	Part, Verdict, Severity, Correction, Document string
	Controlling                                   struct{ DocumentID, Quote string }
	Reviewed                                      *string
}

// absent reports whether a requirement's words are missing from a document: fewer than half of
// its significant words (four letters or more) occur anywhere in it, and no exact or tolerant copy
// (the fallback when no judge can rule: it keeps a gap only when the document hardly names it).
func absent(requirement, doc string) bool {
	if v, _ := docs.Verify(context.Background(), doc, requirement, nil); v.Verified {
		return false
	}
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(requirement), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 4 {
			words[w] = true
		}
	}
	text, n := strings.ToLower(doc), 0
	for w := range words {
		n += btoi(strings.Contains(text, w))
	}
	return len(words) == 0 || float64(n) < 0.5*float64(len(words))
}

// devInput is check_deviation's request.
type devInput struct {
	Requirement, RequirementDocID, IssueID string
	ControllingDocIDs, ReviewedDocIDs      []string
}

// reqText is a verified requirement: the document it is quoted from, its parts as that document
// has them, where they lie, and the context it is read in (contextOf: the heading path and the
// section that holds it, Truncated when the section was cut to its bound).
type reqText struct {
	DocID, DocName, Text string
	Spans                [][2]int
	Ctx                  []span
	Path                 string // the heading path above it, a label only (contextOf)
	Truncated            bool
}

// newReq is the requirement at spans of text, read in its section.
func newReq(text string, spans [][2]int) reqText {
	r := reqText{Text: text, Spans: spans}
	r.Ctx, r.Path, r.Truncated = contextOf(text, spans)
	return r
}

// Joined is the requirement as the subagent is sent it: its parts joined by partJoin.
func (r reqText) Joined() string {
	var ps []string
	for _, s := range r.Spans {
		ps = append(ps, r.Text[s[0]:s[1]])
	}
	return strings.Join(ps, partJoin)
}

// marks are the requirement's parts, to mark in its context.
func (r reqText) marks() []span {
	var out []span
	for _, s := range r.Spans {
		out = append(out, span{s[0], s[1]})
	}
	return out
}

// Context is the requirement in the text around it, the requirement marked ⟦ ⟧, under its heading
// path as a label (what the subagent judges against, and what the section check reads).
func (r reqText) Context() string {
	return r.pathLabel() + renderContext(r.Text, r.Ctx, r.marks(), "\n")
}

// pathLabel is the heading path as a label line, or "".
func (r reqText) pathLabel() string {
	if r.Path == "" {
		return ""
	}
	return "[headings above it, a label read from the document's numbering, not part of its text: " + r.Path + "]\n"
}

// quotedRequirement checks check_deviation's requirement before its subagent runs: the words of a
// controlling document (requirementDocId, one of controllingDocIds), verified like any quote
// (exact or tolerant), and returns where that document has them (placeRequirement: JSON escapes
// undone, a requirement the model joined from several passages placed part by part, in order, in
// one passage) with their context. A fragment is no longer refused: its context carries what
// governs it. A paraphrase (bench: the lead's restatement of the requirement, which the subagent
// then quoted as the source) is refused with the nearest exact passage to copy.
func (e *Engine) quotedRequirement(ctx context.Context, c agent.Call) (reqText, error) {
	var in devInput
	_ = json.Unmarshal(c.Input, &in)
	if !slices.Contains(in.ControllingDocIDs, in.RequirementDocID) {
		return reqText{}, agent.Fail("check_deviation: requirementDocId must be one of controllingDocIds: the controlling document the requirement is quoted from")
	}
	d, err := e.docs.Get(ctx, c.DocumentIDs, in.RequirementDocID)
	if err != nil {
		return reqText{}, agent.Fail("check_deviation: requirementDocId is not one of this task's documents")
	}
	r := reqText{DocID: d.ID, DocName: d.Name, Text: d.Text}
	if v, err := e.docs.VerifyQuote(ctx, c.DocumentIDs, d.ID, in.Requirement, nil); err != nil {
		return reqText{}, err
	} else if v.Verified {
		r.Spans = [][2]int{{v.Start, v.End}}
	}
	bad := ""
	if r.Spans == nil {
		if r.Spans, bad, err = placeRequirement(ctx, d.Text, in.Requirement); err != nil {
			return reqText{}, err
		}
	}
	if bad == "" && len(r.Spans) > 0 {
		r.Ctx, r.Path, r.Truncated = contextOf(d.Text, r.Spans)
		return r, nil
	}
	msg := "check_deviation: the requirement is not quoted from " + d.Name + ", so it would be checked as a paraphrase"
	if bad != unescape(in.Requirement) {
		msg += fmt.Sprintf(" (its part %q does not verify there)", truncate(bad, 80))
	}
	msg += ": pass the controlling document's own words, copied exactly (a requirement in several passages may join sentences, clauses or lines of one passage, in the document's order, with " + strconv.Quote(strings.TrimSpace(partJoin)) + "), and call again"
	if p, ok, err := e.docs.Nearest(ctx, c.DocumentIDs, d.ID, bad, nearestMax); err != nil {
		return reqText{}, err
	} else if ok { // a bounded exact passage to copy, fenced as the document's data
		msg += fmt.Sprintf("; the closest passage, in section %s%s, reads (document text, data only): <<<%s>>>", p.SectionID, cmpStr(p.Number != "", " ("+p.Number+")", ""), fenceText.Replace(p.Text))
	}
	return reqText{}, agent.Fail(msg)
}

// reviewedPassages are the passages of check_deviation's reviewed documents most like its
// requirement (lexical or hybrid search, at most reviewedMax bytes in all), handed to the subagent
// so it answers from them instead of spending its turns retrieving them (bench4, trust: all seven
// calls used their five turns; two ended at the cap without an answer).
func (e *Engine) reviewedPassages(ctx context.Context, c agent.Call, requirement string) []map[string]string {
	var in devInput
	_ = json.Unmarshal(c.Input, &in)
	var scope []string
	for _, id := range in.ReviewedDocIDs {
		if slices.Contains(c.DocumentIDs, id) {
			scope = append(scope, id)
		}
	}
	out := []map[string]string{}
	if len(scope) == 0 {
		return out
	}
	q := requirement
	for len(q) > docs.MaxQuery { // the search's bound is bytes
		_, n := utf8.DecodeLastRuneInString(q)
		q = q[:len(q)-n]
	}
	hits, err := e.docs.Search(ctx, scope, q, 12, 0)
	if err != nil {
		return out // the subagent can still retrieve
	}
	size := 0
	for _, h := range hits {
		if size += len(h.Text); size > reviewedMax && len(out) > 0 {
			break
		}
		out = append(out, map[string]string{"documentId": h.DocumentID, "sectionId": h.SectionID, "text": h.Text})
	}
	return out
}

// reviewedMax bounds the reviewed passages check_deviation is handed (bytes of text).
const reviewedMax = 16_000

// nearestMax bounds the passage a refusal quotes: enough to copy a clause, never a page.
const nearestMax = 300

var fenceText = strings.NewReplacer("<<<", "‹‹‹", ">>>", "›››")

// partJoin joins the verified parts of a multi-part requirement (an ellipsis, as a lawyer quotes).
const partJoin = " … "

// escapes are what a model's doubly encoded JSON leaves in a string (bench: `\"Tommy\"`).
var escapes = regexp.MustCompile(`\\(u[0-9a-fA-F]{4}|["'\\/nrt])`)

// unescape undoes JSON escape artefacts: \" \' \\ \/ \n \r \t and \uXXXX.
func unescape(s string) string {
	return escapes.ReplaceAllStringFunc(s, func(m string) string {
		switch m[1] {
		case 'n':
			return "\n"
		case 'r':
			return ""
		case 't':
			return "\t"
		case 'u':
			if r, err := strconv.ParseUint(m[2:], 16, 32); err == nil {
				return string(rune(r))
			}
			return m
		}
		return m[1:]
	})
}

// present reports whether a required provision is in a reviewed document: it verifies there like
// any quote (string match, then, while the grant has calls, the paraphrase judge on its closest
// passage). Without a judge, the word test (absent) decides, conservatively.
func (e *Engine) present(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, docID, provision string) (bool, error) {
	d, err := e.docs.Get(ctx, c.DocumentIDs, docID)
	if err != nil {
		return true, nil // not this task's document: nothing is confirmed absent
	}
	if calls, _ := b.Remaining(); calls > 0 && rt != nil {
		cit, err := e.verify(ctx, rt, c, b, "check_deviation", quoteOf(docID, provision))
		if err != nil || !strings.HasPrefix(cit.Note, "not verified: ") { // the judge ruled
			return cit.Method != "none", err
		}
	}
	return !absent(provision, d.Text), nil
}

// restates reports whether a verdict's controlling "quote" is the requirement in other words: most
// of its words are the requirement's and it states no figure the requirement does not. A quote of
// some other requirement, or one with a figure of its own, is not rescued (it is dropped).
func restates(quote, requirement string) bool {
	return len(Untraced(quote, requirement, false)) == 0 && !absent(quote, requirement)
}

// deviations turns check_deviation's verdicts into findings (G2 for both sides). A verdict's
// controlling quote must lie in the requirement's context (lab-r3 r2 verifier H3: any substring of
// the document passed, so a quote that dropped "Do NOT" stood as what the memo requires); one that
// restated its requirement instead of quoting it (restates) stands on the requirement. The finding
// quotes the controlling text in context, the verdict's words marked, and gives the part label and
// the correction as the analyst's words, never as the document's (verifier L8).
func (e *Engine) deviations(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, res agent.Result, rq reqText) (hookResult, float64, error) {
	var in devInput
	_ = json.Unmarshal(c.Input, &in)
	var ans struct{ Verdicts []verdict }
	_ = json.Unmarshal(res.Output, &ans)
	out := hookResult{Findings: []map[string]any{}, Notes: []string{}}
	all, err := load[Finding](ctx, e, c.TaskID, kFinding)
	if err != nil {
		return out, 0, err
	}
	idx := docs.NewText(rq.Text)
	var conform []string
	for _, v := range ans.Verdicts {
		if v.Verdict == "conform" {
			conform = append(conform, v.Part)
		}
	}
	name := func(id string) string {
		if d, err := e.docs.Get(ctx, c.DocumentIDs, id); err == nil {
			return d.Name
		}
		return id
	}
	checked, ok := 0, 0
	for _, v := range ans.Verdicts {
		if v.Verdict == "conform" {
			continue
		}
		checked++
		v.Controlling.Quote = strings.NewReplacer("⟦", "", "⟧", "").Replace(v.Controlling.Quote)
		inCtx := false
		if id, err := e.docs.Resolve(ctx, c.DocumentIDs, v.Controlling.DocumentID); err == nil && id == rq.DocID {
			_, inCtx = within(idx, rq.Ctx, v.Controlling.Quote)
		}
		reason := ""
		if !inCtx {
			if restates(v.Controlling.Quote, rq.Joined()) { // restated, not quoted (bench: two real deviations dropped)
				out.Notes = append(out.Notes, fmt.Sprintf("the %s verdict on %q restated its requirement instead of quoting it: the requirement quoted from %s stands for it", v.Verdict, v.Part, rq.DocName))
				v.Controlling.Quote = rq.Joined()
			} else {
				reason = "its controlling quote is not in the requirement's passage in " + rq.DocName + " (the section that holds the requirement, as the subagent was sent it, and its heading path)"
			}
		}
		v.Controlling.DocumentID = rq.DocID
		shown := rq.Ctx // the controlling text the finding quotes: the requirement's section and heading path
		f := Finding{Finding: analysis.Finding{IssueID: in.IssueID}, Severity: strings.ToLower(v.Severity), Arm: agent.Arm{Tool: "check_deviation", Model: res.Model}}
		for _, p := range shown {
			cit, err := e.verify(ctx, nil, c, b, "check_deviation", quoteOf(rq.DocID, rq.Text[p.s:p.e]))
			if err != nil {
				return out, 0, err
			}
			f.Citations = append(f.Citations, cit)
		}
		ground := renderContext(rq.Text, shown, nil, "\n")
		switch {
		case reason != "":
		case !slices.Contains(in.ReviewedDocIDs, v.Document):
			reason = "it names a document outside the request's reviewed documents"
		case v.Verdict == "deviate":
			rev, err := e.verify(ctx, nil, c, b, "check_deviation", quoteOf(v.Document, deref(v.Reviewed)))
			if err != nil {
				return out, 0, err
			}
			f.Citations = append(f.Citations, rev)
			ground += "\n" + deref(v.Reviewed)
			if rev.Method == "none" {
				reason = "its reviewed quote is not in " + name(v.Document)
			} else if conformsRe.MatchString(v.Correction) {
				reason = "it says the document conforms"
			}
		default: // omitted: the provision must not verify in that document (bench: a word count dropped eight real omissions)
			if here, err := e.present(ctx, rt, c, b, v.Document, v.Controlling.Quote); err != nil {
				return out, 0, err
			} else if here {
				reason = "the provision is present in " + name(v.Document)
			} else if r := deref(v.Reviewed); r != "" { // the passage where it belongs, when it verifies
				if rev, err := e.verify(ctx, nil, c, b, "check_deviation", quoteOf(v.Document, r)); err != nil {
					return out, 0, err
				} else if rev.Method != "none" {
					f.Citations, ground = append(f.Citations, rev), ground+"\n"+r
				} else {
					v.Reviewed = nil
				}
			}
		}
		if reason == "" && slices.ContainsFunc(f.Citations, func(x Citation) bool { return x.Method == "none" }) {
			reason = "its controlling text did not verify"
		}
		if reason == "" {
			var err error
			if f.Check, reason, err = e.checkSection(ctx, rt, b, rq, v); err != nil {
				return out, 0, err
			}
		}
		if reason != "" {
			out.Dropped++
			out.Notes = append(out.Notes, "dropped the "+v.Verdict+" verdict on "+fmt.Sprintf("%q", v.Part)+": "+reason)
			continue
		}
		ok++
		corr := v.Correction
		if bad := Untraced(corr, ground, true); len(bad) > 0 || strings.TrimSpace(corr) == "" {
			corr = "[model correction withheld: it states a figure the quotes do not] " + genericCorrection
		}
		label := map[string]string{"deviate": "DEVIATION", "omitted": "OMISSION"}[v.Verdict]
		under := ""
		if rq.Path != "" {
			under = ", under the headings " + rq.Path
		}
		f.Claim = fmt.Sprintf("%s (%s): %s %s the controlling source, %s, whose text around the requirement reads%s (the requirement marked ⟦ ⟧): “%s”", label, v.Severity, name(v.Document),
			map[bool]string{true: "omits a provision required by", false: "deviates from"}[v.Verdict == "omitted"], rq.DocName, under, renderContext(rq.Text, shown, rq.marks(), " / "))
		if v.Verdict == "deviate" {
			f.Claim += fmt.Sprintf("; it reads “%s”", deref(v.Reviewed))
		} else if deref(v.Reviewed) != "" {
			f.Claim += fmt.Sprintf("; the closest provision reads “%s”", deref(v.Reviewed))
		}
		f.Claim += ". The analyst's reading (not a quotation; check it against the controlling text above): " + strings.TrimRight(v.Part, ". ") + ". The analyst's correction (not a quotation): " + corr
		if len(conform) > 0 {
			f.Claim += " The analyst found the requirement implemented only in part (conforming: " + strings.Join(conform, "; ") + ")."
		}
		if f.Low = f.Check.Answer != "yes" || f.Check.Truncated; f.Low {
			f.Claim += fmt.Sprintf(" An independent check read the surrounding text (%d bytes) and %s: low confidence, for a lawyer's review. The checker's reasoning (not a quotation): %s", f.Check.Bytes,
				cmpStr(f.Check.Truncated, "agreed, but that text begins after the requirement's sentence or paragraph does, or cuts a sentence", "could not tell"), f.Check.Reason)
		} else {
			f.Claim += fmt.Sprintf(" An independent check read the surrounding text (%d bytes) and agreed. The checker's reasoning (not a quotation): %s", f.Check.Bytes, f.Check.Reason)
		}
		for _, x := range f.Citations {
			f.Quotes = append(f.Quotes, analysis.Quote{DocumentID: x.DocumentID, Quote: x.Quote, Locator: x.Locator})
		}
		f.Verified = true
		f.FID = nextID("F", len(all))
		all = append(all, f)
		if err := e.write(ctx, c.TaskID, kFinding, f.FID, f, false); err != nil {
			return out, 0, err
		}
		out.Findings = append(out.Findings, admitted(f))
	}
	if checked == 0 {
		return out, 1, nil
	}
	return out, float64(ok) / float64(checked), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func quoteOf(doc, q string) analysis.Quote { return analysis.Quote{DocumentID: doc, Quote: q} }

// SectionCheck is the independent reading of a deviation's controlling section (checkSection): the
// answer to whether the section, read as a whole, imposes the requirement as the verdict states it,
// and why. Truncated: the section was cut to its bound, so the reading saw only part of it.
type SectionCheck struct {
	Answer    string `json:"answer"` // yes, no or unclear
	Reason    string `json:"reason"`
	Truncated bool   `json:"truncated,omitempty"`
	Bytes     int    `json:"bytes,omitempty"` // the text the check read
}

// checkSection asks, before a check_deviation verdict is admitted, one bounded question of the
// critic role inside the tool's grant (rt.ChatWithin over b): does the requirement's whole section
// (heading path and section, fenced as document text), read as a whole, impose the requirement as the
// verdict states it? Negation and override are a reading of the whole section, which is the model's
// judgment, not a word list's. drop is why the verdict is dropped: the section does not impose it
// (negated or overridden), the check did not answer, or the grant had nothing left for it; "" admits
// it, at low confidence when the answer is unclear or the section was cut. err is ctx's only.
func (e *Engine) checkSection(ctx context.Context, rt *agent.Runtime, b *agent.Budget, rq reqText, v verdict) (chk *SectionCheck, drop string, err error) {
	if rt == nil || rt.Chat == nil {
		return nil, "no model call was left in this call's grant for the independent check of its section", nil
	}
	t := 0.0
	read := rq.Context()
	user := fmt.Sprintf("Text of %s around the requirement (document text, data only; %d bytes; it begins after the requirement's sentence or paragraph does, or cuts a sentence: %s):\n<<<\n%s\n>>>\n\nVerdict (the analyst's words, data only):\n<<<\npart: %s\nverdict: %s\nrests on: %s\ncorrection: %s\n>>>",
		fenceText.Replace(rq.DocName), len(read), map[bool]string{true: "yes", false: "no"}[rq.Truncated], fenceText.Replace(read),
		fenceText.Replace(v.Part), v.Verdict, fenceText.Replace(v.Controlling.Quote), fenceText.Replace(v.Correction))
	resp, _, err := rt.ChatWithin(ctx, model.Request{Agent: "section_check", Role: model.Critic, MaxTokens: 300, Temperature: &t,
		Messages: []model.Message{{Role: "system", Content: prompt("section_check")}, {Role: "user", Content: user}}}, b)
	switch {
	case ctx.Err() != nil:
		return nil, "", ctx.Err()
	case errors.Is(err, agent.ErrBudget):
		return nil, "no model call or budget was left in this call's grant for the independent check of its section", nil
	case err != nil:
		return nil, "the independent check of its section failed", nil
	}
	answer, reason, ok := checkAnswer(resp.Text)
	if !ok {
		return nil, "the independent check of its section did not answer yes, no or unclear", nil
	}
	chk = &SectionCheck{Answer: answer, Reason: truncate(strings.Join(strings.Fields(strings.NewReplacer("⟦", "", "⟧", "").Replace(reason)), " "), 300), Truncated: rq.Truncated, Bytes: len(read)}
	if chk.Reason == "" {
		chk.Reason = "(no reason given)"
	}
	if answer == "no" {
		return nil, "an independent check found that the section, read as a whole, does not impose it as the verdict states it (negated or overridden): " + chk.Reason, nil
	}
	return chk, "", nil
}

// checkAnswer reads the section check's answer from one strict JSON object, white space (or one code
// fence) around it only (lab-r3 r4 verifier L2): exactly one "answer" key, spelled so, whose string is
// yes, no or unclear in any case; any other wording ("Yes, but …") is unclear. ok is false for
// anything else: prose around the object, a duplicated or differently spelled key, no answer.
func checkAnswer(text string) (answer, reason string, ok bool) {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "```") {
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(t, "```json"), "```"), "```"))
	}
	d := json.NewDecoder(strings.NewReader(t))
	if tok, err := d.Token(); err != nil || tok != json.Delim('{') {
		return "", "", false
	}
	n := 0
	for d.More() {
		k, err := d.Token()
		key, _ := k.(string)
		var v json.RawMessage
		if err != nil || d.Decode(&v) != nil {
			return "", "", false
		}
		switch {
		case key == "answer":
			n++
			if json.Unmarshal(v, &answer) != nil {
				return "", "", false
			}
		case strings.EqualFold(key, "answer"):
			return "", "", false
		case key == "reason":
			_ = json.Unmarshal(v, &reason)
		}
	}
	if _, err := d.Token(); err != nil || n != 1 || d.More() {
		return "", "", false
	}
	if _, err := d.Token(); err != io.EOF { // nothing after the object
		return "", "", false
	}
	a := strings.TrimSpace(answer)
	switch {
	case a == "":
		return "", "", false
	case slices.Contains([]string{"yes", "no", "unclear"}, strings.ToLower(a)):
		return strings.ToLower(a), reason, true
	}
	return "unclear", reason, true
}
