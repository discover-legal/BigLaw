// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

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

// quotedRequirement checks check_deviation's requirement before its subagent runs: the words of a
// controlling document (requirementDocId, one of controllingDocIds), verified like any quote, and
// returns them as that document has them. A paraphrase (bench: the lead's restatement of the
// requirement, which the subagent then quoted as the source) is refused with the passage to quote.
func (e *Engine) quotedRequirement(ctx context.Context, c agent.Call) (string, error) {
	var in devInput
	_ = json.Unmarshal(c.Input, &in)
	if !slices.Contains(in.ControllingDocIDs, in.RequirementDocID) {
		return "", agent.Fail("check_deviation: requirementDocId must be one of controllingDocIds: the controlling document the requirement is quoted from")
	}
	d, err := e.docs.Get(ctx, c.DocumentIDs, in.RequirementDocID)
	if err != nil {
		return "", agent.Fail("check_deviation: requirementDocId is not one of this task's documents")
	}
	v, err := e.docs.VerifyQuote(ctx, c.DocumentIDs, d.ID, in.Requirement, nil)
	if err != nil {
		return "", err
	} else if v.Verified {
		return v.Match, nil
	}
	msg := "check_deviation: the requirement is not quoted from " + d.Name + ", so it would be checked as a paraphrase: pass the controlling document's own words, copied from retrieve op section, and call again"
	if sid, num, ok, err := e.docs.Nearest(ctx, c.DocumentIDs, d.ID, in.Requirement); err != nil {
		return "", err
	} else if ok { // where to read them: never the document's text in an error (BigLaw's own words only)
		msg += fmt.Sprintf("; the words it restates are closest in section %s%s", sid, cmpStr(num != "", " ("+num+")", ""))
	}
	return "", agent.Fail(msg)
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

// deviations turns check_deviation's verdicts into findings (G2 for both sides). A verdict that
// restated its requirement instead of quoting it (restates) stands on the request's verified
// requirement.
func (e *Engine) deviations(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, res agent.Result) (hookResult, float64, error) {
	var in devInput
	_ = json.Unmarshal(c.Input, &in)
	var ans struct{ Verdicts []verdict }
	_ = json.Unmarshal(res.Output, &ans)
	out := hookResult{Findings: []map[string]any{}, Notes: []string{}}
	all, err := load[Finding](ctx, e, c.TaskID, kFinding)
	if err != nil {
		return out, 0, err
	}
	req, err := e.verify(ctx, nil, c, b, "check_deviation", quoteOf(in.RequirementDocID, in.Requirement))
	if err != nil {
		return out, 0, err
	}
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
		ctrl, err := e.verify(ctx, nil, c, b, "check_deviation", quoteOf(v.Controlling.DocumentID, v.Controlling.Quote))
		if err != nil {
			return out, 0, err
		}
		if ctrl.Method == "none" && req.Method != "none" && restates(v.Controlling.Quote, in.Requirement) { // restated, not quoted (bench: two real deviations dropped)
			out.Notes = append(out.Notes, fmt.Sprintf("the %s verdict on %q restated its requirement instead of quoting it: the requirement quoted from %s stands for it", v.Verdict, v.Part, name(in.RequirementDocID)))
			ctrl, v.Controlling.DocumentID, v.Controlling.Quote = req, in.RequirementDocID, in.Requirement
		}
		docOK := slices.Contains(in.ReviewedDocIDs, v.Document) && slices.Contains(in.ControllingDocIDs, v.Controlling.DocumentID)
		f := Finding{Finding: analysis.Finding{IssueID: in.IssueID}, Severity: strings.ToLower(v.Severity), Arm: agent.Arm{Tool: "check_deviation", Model: res.Model}}
		f.Citations = []Citation{ctrl}
		ground := v.Controlling.Quote
		reason := ""
		switch {
		case !docOK:
			reason = "it names a document outside the request's controlling or reviewed documents"
		case ctrl.Method == "none":
			reason = "its controlling quote is not in the controlling document"
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
		f.Claim = fmt.Sprintf("%s (%s): %s. %s %s the controlling source, %s, which requires “%s”", label, v.Severity, v.Part, name(v.Document),
			map[bool]string{true: "omits a provision required by", false: "deviates from"}[v.Verdict == "omitted"], name(v.Controlling.DocumentID), v.Controlling.Quote)
		if v.Verdict == "deviate" {
			f.Claim += fmt.Sprintf("; it reads “%s”", deref(v.Reviewed))
		} else if deref(v.Reviewed) != "" {
			f.Claim += fmt.Sprintf("; the closest provision reads “%s”", deref(v.Reviewed))
		}
		f.Claim += ". Correction: " + corr
		if len(conform) > 0 {
			f.Claim += " The requirement is implemented only in part (conforming: " + strings.Join(conform, "; ") + ")."
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
