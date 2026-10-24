// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
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
// has them, where they lie, and the context it is read in (contextOf: its paragraph or list item,
// the introductions that govern it and its heading).
type reqText struct {
	DocID, DocName, Text string
	Spans                [][2]int
	Ctx                  []piece
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

// Context is the requirement in its context, trimmed to about ctxMax bytes around it, the
// requirement marked ⟦ ⟧ (what the subagent judges against).
func (r reqText) Context() string {
	return renderContext(r.Text, trimContext(r.Text, r.Ctx, span{r.Spans[0][0], r.Spans[len(r.Spans)-1][1]}), r.marks(), "\n")
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
		r.Ctx = contextOf(d.Text, r.Spans)
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
	reqSpan := span{rq.Spans[0][0], rq.Spans[len(rq.Spans)-1][1]}
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
		focus, inCtx := span{}, false
		if id, err := e.docs.Resolve(ctx, c.DocumentIDs, v.Controlling.DocumentID); err == nil && id == rq.DocID {
			focus, inCtx = within(idx, rq.Ctx, v.Controlling.Quote)
		}
		reason := ""
		if !inCtx {
			if restates(v.Controlling.Quote, rq.Joined()) { // restated, not quoted (bench: two real deviations dropped)
				out.Notes = append(out.Notes, fmt.Sprintf("the %s verdict on %q restated its requirement instead of quoting it: the requirement quoted from %s stands for it", v.Verdict, v.Part, rq.DocName))
				focus, v.Controlling.Quote = reqSpan, rq.Joined()
			} else {
				reason = "its controlling quote is not in the requirement's passage in " + rq.DocName + " (the requirement, the paragraph or list item that holds it, the introductions that govern it and its heading)"
			}
		}
		v.Controlling.DocumentID = rq.DocID
		shown := trimContext(rq.Text, rq.Ctx, focus) // the controlling text the finding quotes, about ctxMax bytes
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
		f.Claim = fmt.Sprintf("%s (%s): %s %s the controlling source, %s, whose text reads, with the heading and introductions that govern it (the words the verdict rests on marked ⟦ ⟧): “%s”", label, v.Severity, name(v.Document),
			map[bool]string{true: "omits a provision required by", false: "deviates from"}[v.Verdict == "omitted"], rq.DocName, renderContext(rq.Text, shown, []span{focus}, " / "))
		if v.Verdict == "deviate" {
			f.Claim += fmt.Sprintf("; it reads “%s”", deref(v.Reviewed))
		} else if deref(v.Reviewed) != "" {
			f.Claim += fmt.Sprintf("; the closest provision reads “%s”", deref(v.Reviewed))
		}
		f.Claim += ". The analyst's reading (not a quotation; check it against the controlling text above): " + strings.TrimRight(v.Part, ". ") + ". The analyst's correction (not a quotation): " + corr
		if len(conform) > 0 {
			f.Claim += " The analyst found the requirement implemented only in part (conforming: " + strings.Join(conform, "; ") + ")."
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
