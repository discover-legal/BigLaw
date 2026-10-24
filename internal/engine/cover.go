// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
)

// The coverage pass (bench, SEC referral: the memo touched scienter under 206(1)/(2), the § 2462
// limitations period, failure to supervise and the summed gains, but analysed none): before the
// deliverable, every plan issue must have been analysed, not just touched. Once per run, Go spends
// the share kept for it (Allocation.Cover) on one analyze_issue and one redraft per touched issue,
// in plan order; whatever is still touched after it is disclosed in Limitations.

// analyses is the run's latest analyze_issue answer per issue.
func (e *Engine) analyses(ctx context.Context, taskID string) (map[string]Analysis, error) {
	rs, err := e.rows(ctx, taskID, kAnalysis)
	out := map[string]Analysis{}
	for _, r := range rs {
		var a Analysis
		if json.Unmarshal(r.Data, &a) == nil {
			out[r.Key] = a
		}
	}
	return out, err
}

// coverRow is the coverage pass's outcome, nil while it has not run.
func (e *Engine) coverRow(ctx context.Context, taskID string) (*Cover, error) {
	cs, err := load[Cover](ctx, e, taskID, kCover)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return &cs[0], nil
}

// covered reports whether the coverage pass has run.
func (e *Engine) covered(ctx context.Context, taskID string) (bool, error) {
	c, err := e.coverRow(ctx, taskID)
	return c != nil, err
}

// needsLaw is the issue classes that ask what the law makes of the facts: their answer is an
// analysis, not a list of facts (a Clause or RedFlag issue is answered by its findings).
func needsLaw(c string) bool { return c == "Conduct" || c == "Requirement" }

// touched is the plan issues not yet analysed, in plan order. An issue is analysed when its drafted
// section cites a usable finding and, for a Conduct or Requirement issue, the issue has an analysis
// (analyze_issue's, or a usable check_deviation finding of it). A section drafted from facts alone,
// or no section, is only touched.
func (e *Engine) touched(ctx context.Context, taskID string) ([]Issue, error) {
	issues, err := load[Issue](ctx, e, taskID, kIssue)
	secs, serr := load[Section](ctx, e, taskID, kSection)
	use, uerr := e.usable(ctx, taskID)
	as, aerr := e.analyses(ctx, taskID)
	if err = cmpErr(err, serr, uerr, aerr); err != nil {
		return nil, err
	}
	usable := map[string]bool{}
	for _, f := range use {
		usable[f.FID] = true
	}
	var out []Issue
	for _, is := range issues {
		k := slices.IndexFunc(secs, func(s Section) bool { return s.IssueID == is.ID })
		cites := k >= 0 && slices.ContainsFunc(fidRe.FindAllStringSubmatch(secs[k].Markdown, -1), func(m []string) bool { return usable[m[1]] })
		_, analysed := as[is.ID]
		analysed = analysed || slices.ContainsFunc(use, func(f Finding) bool { return f.IssueID == is.ID && f.Arm.Tool == "check_deviation" })
		if !cites || needsLaw(string(is.Class)) && !analysed {
			out = append(out, is)
		}
	}
	return out, nil
}

// coverFIDs is what a touched issue's redraft is given: the usable findings its section cites,
// then the issue's own usable findings (its analysis' new ones among them), at most 60.
func (e *Engine) coverFIDs(ctx context.Context, taskID, issueID string) ([]string, error) {
	secs, err := load[Section](ctx, e, taskID, kSection)
	use, uerr := e.usable(ctx, taskID)
	if err = cmpErr(err, uerr); err != nil {
		return nil, err
	}
	var out []string
	add := func(fid string) {
		if len(out) < 60 && !slices.Contains(out, fid) && slices.ContainsFunc(use, func(f Finding) bool { return f.FID == fid }) {
			out = append(out, fid)
		}
	}
	if k := slices.IndexFunc(secs, func(s Section) bool { return s.IssueID == issueID }); k >= 0 {
		for _, m := range fidRe.FindAllStringSubmatch(secs[k].Markdown, -1) {
			add(m[1])
		}
	}
	for _, f := range use {
		if f.IssueID == issueID {
			add(f.FID)
		}
	}
	return out, nil
}

const coverRevise = "Coverage pass: this section touched its issue without analysing it. Answer the issue's question with the analysis in the request (rule, application, conclusion) applied to the findings: state the rule and its source plainly, apply it to the cited facts, and conclude."

// coverTool is the coverage pass (a Go fan-out the engine runs, never the lead): for each touched
// issue, in plan order, analyze_issue and then draft_section with the issue's findings and the
// analysis, within Allocation.Cover. It runs once per run (its kCover row marks it done, also when
// a model failure ends it, so a retried run does not spend a second share on it). Its spec's turns
// are its share's calls, so its grant (what a crash charges as lost) is no more than its share.
func (e *Engine) coverTool(rt *agent.Runtime, r *run) agent.Tool {
	a := Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD)
	s := spec("cover")
	s.MaxTurns = a.Cover
	return agent.GoTool{S: s, Fn: func(ctx context.Context, c agent.Call) (any, agent.Usage, error) {
		b := agent.NewBudget(min(c.Budget.Calls, a.Cover), min(c.Budget.USD, a.dollars(a.Cover)), 0)
		todo, err := e.touched(ctx, c.TaskID)
		if err != nil {
			return nil, agent.Usage{}, err
		}
		me := agent.Agent{Name: "cover", Visible: []string{"analyze_issue", "draft_section"}}
		call := agent.Call{TaskID: c.TaskID, DocumentIDs: c.DocumentIDs}
		due, _, err := e.placements(ctx, c.TaskID)
		if err != nil {
			return nil, agent.Usage{}, err
		}
		keep := 0.0 // what the analyses leave for placing the left-out evidence (bench5 SEC: they spent all but $0.14, and nothing was placed)
		if len(due) > 0 {
			keep = placeShare * b.Value().USD
		}
		var failed error // a model failure: the pass is recorded as run, then the run ends
		for _, is := range todo {
			if calls, usd := b.Remaining(); calls == 0 || usd <= keep {
				break
			}
			in, _ := json.Marshal(map[string]any{"issueId": is.ID, "question": is.Question, "jurisdiction": r.t.Jurisdiction, "packs": nil})
			_, err := rt.Invoke(ctx, me, call, "analyze_issue", in, b, false, 0)
			if modelOrJournal(err) {
				failed = err
				break
			} else if errors.Is(err, agent.ErrBudget) {
				break
			} else if err != nil { // no analysis to redraft with: it stays touched, and is disclosed
				continue
			}
			fids, ferr := e.coverFIDs(ctx, c.TaskID, is.ID)
			if ferr != nil {
				return nil, b.Spent(), ferr
			}
			if len(fids) == 0 { // nothing verified to draft from: it stays touched, and is disclosed
				continue
			}
			in, _ = json.Marshal(map[string]any{"issueId": is.ID, "fids": fids, "revise": coverRevise})
			if _, err := rt.Invoke(ctx, me, call, "draft_section", in, b, false, 0); modelOrJournal(err) {
				failed = err
				break
			}
		}
		if failed == nil { // then the left-out evidence: one redraft per issue that has some
			ids, place, perr := e.placements(ctx, c.TaskID)
			if perr != nil {
				return nil, b.Spent(), perr
			}
			for _, id := range ids {
				if calls, _ := b.Remaining(); calls == 0 {
					break
				}
				base, ferr := e.coverFIDs(ctx, c.TaskID, id)
				if ferr != nil {
					return nil, b.Spent(), ferr
				}
				fids := slices.Clone(place[id].FIDs)
				for _, x := range place[id].Conflicts {
					for _, v := range x.Values {
						fids = append(fids, v.FID)
					}
				}
				fids = slices.Compact(append(fids, base...))
				for i := len(fids) - 1; i >= 0; i-- { // a finding named twice: once, where it first appears
					if slices.Index(fids, fids[i]) < i {
						fids = slices.Delete(fids, i, i+1)
					}
				}
				in, _ := json.Marshal(map[string]any{"issueId": id, "fids": fids[:min(60, len(fids))], "revise": placeRevise(place[id])})
				if _, err := rt.Invoke(ctx, me, call, "draft_section", in, b, false, 0); modelOrJournal(err) {
					failed = err
					break
				} else if errors.Is(err, agent.ErrBudget) {
					break
				}
			}
		}
		if errors.Is(failed, agent.ErrJournal) {
			return nil, b.Spent(), failed
		}
		left, err := e.touched(ctx, c.TaskID)
		if err != nil {
			return nil, b.Spent(), err
		}
		res := Cover{Analysed: []string{}, Missing: []string{}, LeftOut: []string{}} // what is still to disclose after it
		if lo, err := e.leftOut(ctx, c.TaskID); err != nil {
			return nil, b.Spent(), err
		} else {
			for _, f := range lo {
				res.LeftOut = append(res.LeftOut, f.FID)
			}
		}
		if _, open, err := e.discrepancies(ctx, c.TaskID); err != nil {
			return nil, b.Spent(), err
		} else {
			res.Unaddressed = len(open)
		}
		for _, is := range todo {
			if slices.ContainsFunc(left, func(x Issue) bool { return x.ID == is.ID }) {
				res.Missing = append(res.Missing, is.ID)
			} else {
				res.Analysed = append(res.Analysed, is.ID)
			}
		}
		if err := e.write(ctx, c.TaskID, kCover, "pass", res, false); err != nil || failed != nil {
			return nil, b.Spent(), cmpErr(failed, err)
		}
		return map[string]any{"analysed": res.Analysed, "missing": res.Missing}, b.Spent(), nil
	}}
}

// coverPass runs the coverage pass as a step of its own (a run finishing from its reserve) when it
// has work (coverDue) and has not run, before the last review; it returns the last step. A refused or
// failed pass is in the step log, and finish discloses what is still touched or left out.
func (e *Engine) coverPass(ctx context.Context, rt *agent.Runtime, call agent.Call, r *run, last int) (int, error) {
	done, err := e.covered(ctx, r.t.ID)
	due, derr := e.coverDue(ctx, r.t.ID)
	if err = cmpErr(err, derr); err != nil || done || !due {
		return last, err
	}
	b, a := r.b, Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD) // its grant is its share, not all the review level's free budget
	if _, fu := r.b.RemainingAt(agent.LevelReview); a.Cover > 0 {
		if g, gerr := r.b.Grant(a.Cover, min(fu, a.dollars(a.Cover)), agent.LevelReview); gerr == nil {
			defer g.Close()
			b = g
		}
	}
	_, err = rt.Invoke(ctx, agent.Agent{Name: "cover", Visible: []string{"cover"}}, call, "cover", json.RawMessage(`{}`), b, false, last+1)
	if ctx.Err() != nil || errors.Is(err, errLease) || modelOrJournal(err) {
		return last, cmpErr(ctx.Err(), err)
	}
	return last + 1, nil
}

// The coverage pass's second job (bench r3): what the record proves must reach the memo. Two kinds of
// verified evidence were lost between the ledger and the deliverable: salient findings no section
// ever cited (trust: the incapacity standard, the disinheritance clause, the contingent guardian,
// the no-contest clause), and figures two documents state differently (SEC: 4,217 vs 4,312 trades,
// $92,600 vs $103,800, April 22 vs April 15, 73% vs 78%). The pass redrafts the issue each belongs to
// with them; a conflict the memo still does not state is a finish check (lint, bounded like G8's),
// and a salient finding still left out is disclosed.

// coverDue reports whether the coverage pass has work: a touched issue, a salient finding no section
// cites, or a figure conflict the memo does not address.
func (e *Engine) coverDue(ctx context.Context, taskID string) (bool, error) {
	todo, err := e.touched(ctx, taskID)
	ids, _, perr := e.placements(ctx, taskID)
	return len(todo) > 0 || len(ids) > 0, cmpErr(err, perr)
}

// placement is the evidence one issue's redraft must carry.
type placement struct {
	FIDs      []string
	Conflicts []analysis.Conflict
}

// breachRe is a claim that says a document departs from what it should be (a deviation, an
// omission, a violation): with a breach relation, a severity or a deviation check, a salient finding.
var breachRe = regexp.MustCompile(`(?i)\b(deviat\w*|omit\w*|omission|does not|do not|did not|fails? to|failed to|inconsistent|contrary to|instead of|rather than|missing|absent|lacks?|prohibit\w*|violat\w*|breach\w*|discrepan\w*|conflicts? with)\b`)

// salientFinding is a verified finding the memo must not leave out: a deviation check's, one rated
// high or critical, one whose typed relations say a breach (deviatesFrom, violates, prohibits,
// harmed), one stating a salient figure, or one whose claim says a document departs from another.
func salientFinding(f Finding) bool {
	if f.Arm.Tool == "check_deviation" || f.Severity == "high" || f.Severity == "critical" || breachRe.MatchString(f.Claim) {
		return true
	}
	for _, r := range f.Relations {
		if r.Predicate == "deviatesFrom" || r.Predicate == "violates" || r.Predicate == "prohibits" || r.Predicate == "harmed" {
			return true
		}
	}
	return slices.ContainsFunc(f.Values, func(v analysis.Value) bool { return analysis.Salience(v.Value) >= 55 })
}

// placeShare is the part of the coverage pass's USD its analyses leave for placing left-out evidence,
// when there is some: bench5's analyses cost about $0.15 an issue and a placement redraft about $0.03–0.05,
// with a worst case near $0.17 that must fit before it is sent.
const placeShare = 0.4

// maxPlaced bounds the left-out findings the pass places (it redrafts each issue once) and lists;
// one issue's redraft takes at most maxPlacedPerIssue of them.
const (
	maxPlaced         = 40
	maxPlacedPerIssue = 12
)

// salienceRank orders left-out findings, strongest evidence first: a deviation check's or a high
// one, then a typed breach, then a claim that says so, then a salient figure alone.
func salienceRank(f Finding) int {
	switch {
	case f.Arm.Tool == "check_deviation" || f.Severity == "high" || f.Severity == "critical":
		return 0
	case slices.ContainsFunc(f.Relations, func(r analysis.Relation) bool {
		return r.Predicate == "deviatesFrom" || r.Predicate == "violates" || r.Predicate == "prohibits" || r.Predicate == "harmed"
	}):
		return 1
	case breachRe.MatchString(f.Claim):
		return 2
	}
	return 3
}

// memoText is the drafted sections as the memo will state them (handles substituted).
func memoText(secs []Section) string {
	var b strings.Builder
	for _, s := range secs {
		b.WriteString(substituteOutsideQuotes(s.Markdown, s.Handles) + "\n\n")
	}
	return b.String()
}

// addressed reports whether the memo states both of a conflict's values.
func addressed(c analysis.Conflict, memo string) bool {
	ds, ns := figures(memo, true)
	for _, v := range c.Values {
		kind, canon, _ := strings.Cut(v.Canon, ":")
		if kind == "date" {
			d, _ := analysis.ParseDate(canon)
			if !slices.ContainsFunc(ds, func(k string) bool { x, _ := analysis.ParseDate(k); return x == d }) {
				return false
			}
		} else if !slices.Contains(ns, canon) {
			return false
		}
	}
	return true
}

// discrepancies is the cross-document join over every usable finding (analysis.Join, labels and
// near values alike, with the aliases the lead gave join_discrepancies), and those the memo does not
// yet address.
func (e *Engine) discrepancies(ctx context.Context, taskID string) (all, open []analysis.Conflict, err error) {
	use, err := e.usable(ctx, taskID)
	secs, serr := load[Section](ctx, e, taskID, kSection)
	aliases, aerr := load[analysis.Alias](ctx, e, taskID, kAlias) // the lead's, from join_discrepancies
	if err = cmpErr(err, serr, aerr); err != nil {
		return nil, nil, err
	}
	an := make([]analysis.Finding, 0, len(use))
	for _, f := range use {
		an = append(an, f.Finding)
	}
	memo := memoText(secs)
	for _, c := range analysis.Join(an, aliases) {
		all = append(all, c)
		if !addressed(c, memo) {
			open = append(open, c)
		}
	}
	return all, open, nil
}

// leftOut is the salient usable findings no section cites, strongest first (salienceRank), else in
// ledger order.
func (e *Engine) leftOut(ctx context.Context, taskID string) ([]Finding, error) {
	use, err := e.usable(ctx, taskID)
	secs, serr := load[Section](ctx, e, taskID, kSection)
	if err = cmpErr(err, serr); err != nil {
		return nil, err
	}
	cited := map[string]bool{}
	for _, s := range secs {
		for _, m := range fidRe.FindAllStringSubmatch(s.Markdown, -1) {
			cited[m[1]] = true
		}
	}
	var out []Finding
	for _, f := range use {
		if !cited[f.FID] && salientFinding(f) {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b Finding) int { return salienceRank(a) - salienceRank(b) })
	return out, nil
}

// placements assigns the left-out findings and the unaddressed conflicts to plan issues: a finding
// to its own issue, else to the issue whose question its claim matches best (BM25; the first issue
// when none matches); a conflict to the issue of a section citing either value's finding, else to
// either finding's issue, else as its metric matches. It returns them by issue, strongest first (an
// issue with a conflict, then by its strongest left-out finding), so a pass whose share runs out has
// placed the most salient evidence.
func (e *Engine) placements(ctx context.Context, taskID string) ([]string, map[string]*placement, error) {
	issues, err := load[Issue](ctx, e, taskID, kIssue)
	secs, serr := load[Section](ctx, e, taskID, kSection)
	left, lerr := e.leftOut(ctx, taskID)
	_, open, oerr := e.discrepancies(ctx, taskID)
	use, uerr := e.usable(ctx, taskID)
	if err = cmpErr(err, serr, lerr, oerr, uerr); err != nil || len(issues) == 0 {
		return nil, nil, err
	}
	qs := make([][]string, len(issues))
	for i, is := range issues {
		qs[i] = terms(is.Question)
	}
	best := func(text string) string {
		sc := bm25(qs, terms(text)) // the questions as the corpus, the text as the query
		k := 0
		for i := range sc {
			if sc[i] > sc[k] {
				k = i
			}
		}
		return issues[k].ID
	}
	inPlan := func(id string) bool { return slices.ContainsFunc(issues, func(is Issue) bool { return is.ID == id }) }
	out := map[string]*placement{}
	at := func(id string) *placement {
		if out[id] == nil {
			out[id] = &placement{}
		}
		return out[id]
	}
	for _, f := range left[:min(maxPlaced, len(left))] {
		id := cmpStr(inPlan(f.IssueID), f.IssueID, "")
		if id == "" {
			id = best(f.Claim)
		}
		if p := at(id); len(p.FIDs) < maxPlacedPerIssue {
			p.FIDs = append(p.FIDs, f.FID)
		}
	}
	for _, c := range open {
		id := ""
		for _, s := range secs {
			if id == "" && slices.ContainsFunc(c.Values, func(v analysis.ConflictVal) bool { return strings.Contains(s.Markdown, "["+v.FID+"]") }) {
				id = s.IssueID
			}
		}
		for _, v := range c.Values {
			if i := slices.IndexFunc(use, func(f Finding) bool { return f.FID == v.FID }); id == "" && i >= 0 && inPlan(use[i].IssueID) {
				id = use[i].IssueID
			}
		}
		if id == "" || !inPlan(id) {
			id = best(c.Entity + " " + c.Metric)
		}
		at(id).Conflicts = append(at(id).Conflicts, c)
	}
	var order []string // strongest evidence first: an issue with a conflict, then by its strongest left-out finding (left is ranked)
	for _, is := range issues {
		if out[is.ID] != nil {
			order = append(order, is.ID)
		}
	}
	rank := func(id string) int {
		if len(out[id].Conflicts) > 0 {
			return -1
		}
		return slices.IndexFunc(left, func(f Finding) bool { return slices.Contains(out[id].FIDs, f.FID) })
	}
	slices.SortStableFunc(order, func(a, b string) int { return rank(a) - rank(b) })
	return order, out, nil
}

// placeRevise is the redraft instruction for one issue's placement.
func placeRevise(p *placement) string {
	var b strings.Builder
	b.WriteString("Coverage pass: the memo leaves out verified evidence that bears on this issue. Keep what the section says and add it.")
	if len(p.FIDs) > 0 {
		b.WriteString(" These findings never reached the memo: " + strings.Join(p.FIDs, ", ") + ". State each one that bears on the question (a deviation, an omission, a violation or a key fact), citing it.")
	}
	for _, c := range p.Conflicts {
		var vs []string
		for _, v := range c.Values {
			vs = append(vs, fmt.Sprintf("[%s] states %s", v.FID, v.Value))
		}
		fmt.Fprintf(&b, " The documents disagree on %s: %s. State both figures, cite both findings, and say what the difference means for the matter.",
			cmpStr(strings.TrimSpace(c.Entity+" "+c.Metric) != "", strings.TrimSpace(c.Entity+" "+c.Metric), "one figure"), strings.Join(vs, "; "))
	}
	return b.String()
}
