// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// finish ends the run (README § The engine, stages 7–8): check is a dry run of submit's checks
// (lint, coverage, the P6 preconditions); submit renders and opens the gates only when they pass,
// or when the run is finishing from its reserve (budget or max steps), which discloses instead.
type finish struct {
	e       *Engine
	rt      *agent.Runtime
	r       *run
	held    int  // calls this finish call's own grant holds beyond its meta call: free for the run's checks
	covered bool // the coverage pass has run (its kept share is free again)
}

func (e *Engine) finishTool(rt *agent.Runtime, r *run) agent.Tool { return finish{e: e, rt: rt, r: r} }

// Spec is finish's spec with the calls one submit may spend: its meta call, plus the coverage pass
// and the review of its redrafts (the shares kept for them), so a crash mid-finish loses no more.
func (f finish) Spec() agent.Spec {
	s, a := f.r.sp().Spec("finish"), Allocate(f.e.cfg.BudgetCalls, f.e.cfg.BudgetUSD)
	s.MaxTurns += a.Cover + a.Cycle
	return s
}

type finishErr struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// report is finish's answer for both ops.
type report struct {
	Ready             bool                `json:"ready"`
	Accepted          bool                `json:"accepted"`
	Errors            []finishErr         `json:"errors"`
	Lint              []string            `json:"lint"`
	MissingIssues     []string            `json:"missingIssues"`
	UncoveredParties  []analysis.PartyGap `json:"uncoveredParties"`
	MissingFigures    []analysis.Handle   `json:"missingFigures"`
	OpenPreconditions []string            `json:"openPreconditions"`
	Truncated         bool                `json:"truncated"` // the lists hold their first items: fix those and check again
	limits            []string
	computed          []computedFig // the Computed figures block's items, by section
}

// page is the report as finish returns it: one page, the first items of each list by priority.
func (rep report) page() report {
	pg := agent.NewPage()
	rep.Errors, rep.MissingIssues, rep.OpenPreconditions = agent.Take(pg, rep.Errors), agent.Take(pg, rep.MissingIssues), agent.Take(pg, rep.OpenPreconditions)
	rep.Lint, rep.MissingFigures, rep.UncoveredParties = agent.Take(pg, rep.Lint), agent.Take(pg, rep.MissingFigures), agent.Take(pg, rep.UncoveredParties)
	rep.Truncated = pg.Truncated
	return rep
}

type limitation struct {
	IssueID *string `json:"issueId"`
	Text    string  `json:"text"`
}

func (f finish) Call(ctx context.Context, c agent.Call) (res agent.Result, err error) {
	// the memo's own figures are read first, from the grant before anything else (bench7: the judge
	// spent the whole grant, every memo figure was then unread and the lead stripped them in redraft
	// after redraft); then the join's pairs not yet judged, so check and submit report every
	// conflict the judge finds, from what is left beside the drafting share the lead may still use
	rb := agent.NewBudget(c.Budget.Calls, c.Budget.USD, 0)
	if err := f.e.readSections(ctx, f.rt, c, rb); err != nil {
		return agent.Result{Usage: rb.Spent()}, err
	}
	pre := rb.Spent()
	keep, err := f.e.draftLeft(ctx, c.TaskID)
	if err != nil {
		return agent.Result{Usage: pre}, err
	}
	jg := judgeGrant(agent.Grant{Calls: c.Budget.Calls - pre.Calls, USD: c.Budget.USD - pre.USD}, keep)
	jb := agent.NewBudget(jg.Calls, jg.USD, 0)                    // a quarter of its calls and USD (judge.go, lab-r4 r8 L3)
	if fids, err := f.e.claimsUnread(ctx, c.TaskID); err != nil { // the join's sums come from claims too: a valueless finding's claim is read first, from the judge's share (r1 MEDIUM-3)
		return agent.Result{Usage: pre}, err
	} else if _, err := f.e.readFindings(ctx, f.rt, c, jb, "finish", fids); err != nil {
		u := jb.Spent()
		u.Add(pre)
		return agent.Result{Usage: u}, err
	}
	if err := f.e.judgeJoin(ctx, f.rt, c, jb, "finish"); err != nil {
		u := jb.Spent()
		u.Add(pre)
		return agent.Result{Usage: u}, err
	}
	pre.Add(jb.Spent())
	c.Budget.Calls, c.Budget.USD = c.Budget.Calls-pre.Calls, max(0, c.Budget.USD-pre.USD)
	defer func() { res.Usage.Add(pre) }()
	f.held = max(0, c.Budget.Calls-1) // its grant holds the coverage pass's and the review's calls too
	if f.covered, err = f.e.covered(ctx, c.TaskID); err != nil {
		return agent.Result{}, err
	}
	var in struct {
		Op          string
		Limitations []limitation
	}
	if err := agent.Decode(c, &in); err != nil {
		return agent.Result{}, err
	}
	rep, err := f.check(ctx, in.Limitations)
	if err != nil {
		return agent.Result{}, err
	}
	out := func(rep report, u agent.Usage, final bool) (agent.Result, error) {
		data, err := json.Marshal(rep.page())
		return agent.Result{Output: data, Usage: u, Final: final}, err
	}
	if in.Op == "check" {
		return out(rep, agent.Usage{}, false)
	}
	if !rep.Ready && f.r.forced == "" {
		return out(rep, agent.Usage{}, false)
	}
	var spent agent.Usage
	if f.r.forced == "" { // the coverage pass, once, before a deliverable (a forced finish ran it as a step)
		done, err := f.e.covered(ctx, c.TaskID)
		due, derr := f.e.coverDue(ctx, c.TaskID)
		if err = cmp.Or(err, derr); err != nil {
			return agent.Result{}, err
		}
		if !done && due {
			if spent, err = f.cover(ctx, c); err != nil {
				return agent.Result{Usage: spent}, err
			}
			f.held, f.covered = max(0, c.Budget.Calls-spent.Calls-1), true
			if rep, err = f.check(ctx, in.Limitations); err != nil {
				return agent.Result{Usage: spent}, err
			} else if !rep.Ready {
				return out(rep, spent, false)
			}
		}
	}
	if esc, err := f.escalate(ctx); err != nil || esc != nil {
		if esc != nil {
			rep.Errors = append(rep.Errors, *esc)
		}
		return out(rep, spent, false)
	}
	if h, ok := f.rt.Hooks.(hookRunner); ok { // task_finish's rules, before the deliverable is rendered
		hb := agent.NewBudget(c.Budget.Calls-spent.Calls, max(0, c.Budget.USD-spent.USD), 0)
		v, err := h.finishHooks(ctx, c.TaskID, hb, cmpStr(f.r.forced != "", f.r.forced, "finish"))
		spent.Add(hb.Spent())
		if err != nil {
			return agent.Result{Usage: spent}, err
		}
		for _, n := range v.notes {
			if !slices.Contains(rep.limits, n) {
				rep.limits = append(rep.limits, n)
			}
		}
		switch {
		case v.blocked != "" && f.r.forced == "":
			rep.Errors = append(rep.Errors, finishErr{"hook_blocked", v.blocked})
			rep.OpenPreconditions = append(rep.OpenPreconditions, v.blocked)
			return out(rep, spent, false)
		case v.blocked != "": // a forced finish delivers what there is: the refusal is disclosed instead
			rep.limits = append(rep.limits, "A configurable hook would have refused this deliverable, which was forced ("+f.r.forced+"): "+v.blocked)
		}
	}
	rep.Accepted = true
	b := agent.NewBudget(c.Budget.Calls-spent.Calls, c.Budget.USD-spent.USD, 0)
	meta, err := f.meta(ctx, b)
	if errors.Is(err, agent.ErrBudget) { // a USD-bound run's floor may not fund it (4a r2 L8): deliver without
		meta, err, rep.limits = "", nil, append(rep.limits, "The note to the reviewing lawyer was not written: the budget left could not fund its call.")
	}
	if err != nil {
		return agent.Result{Usage: b.Spent()}, err
	}
	var flagged []string
	if f.r.forced != "" {
		flagged = rep.OpenPreconditions // every lint line is one of them
	}
	d, err := f.e.render(ctx, f.r.t, rep.limits, blockOf(rep.computed), meta, flagged)
	if err == nil {
		err = f.e.write(ctx, c.TaskID, kNote, "deliverable", d.Markdown, true)
	}
	if err == nil {
		err = f.gates(ctx, d.Markdown)
	}
	if err != nil {
		return agent.Result{Usage: b.Spent()}, err
	}
	spent.Add(b.Spent())
	return out(rep, spent, true)
}

// judgeGrant is finish's judge's grant out of what is left after its reads: a quarter of its calls
// and USD beside the drafting share the lead may still use (keep), and when the root is below that
// keep, a quarter of its floor, judgeFloor calls, of what is left (r1 LOW-5: it was 0, every pair
// unjudged); what it leaves unjudged is disclosed (joined.lines).
func judgeGrant(left agent.Grant, keep agent.Usage) agent.Grant {
	calls, usd := max(0, left.Calls-keep.Calls), max(0, left.USD-keep.USD)
	if calls < judgeFloor {
		calls, usd = min(judgeFloor, max(0, left.Calls)), max(0, left.USD)
	}
	return agent.Grant{Calls: judgeCalls(calls), USD: judgeShare * usd}
}

// claimsUnread are the usable findings never read whose claims state a number: the join reads their
// claims' sums (claimSums), so finish reads them before it judges.
func (e *Engine) claimsUnread(ctx context.Context, taskID string) ([]string, error) {
	use, err := e.usable(ctx, taskID)
	var out []string
	for _, f := range use {
		if f.Unread && len(analysis.PassageOf("c", f.Claim).Spans) > 0 {
			out = append(out, f.FID)
		}
	}
	return out, err
}

// draftLeft is what the drafting share has left (Allocate's Draft less what the drafters spent):
// the judge leaves it to the lead's redrafts.
func (e *Engine) draftLeft(ctx context.Context, taskID string) (agent.Usage, error) {
	a := Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD)
	spent, err := e.classSpend(ctx, taskID)
	if err != nil {
		return agent.Usage{}, err
	}
	d := spent["draft"]
	return agent.Usage{Calls: max(0, a.Draft-d.Calls), USD: max(0, a.dollars(a.Draft)-d.USD)}, nil
}

// cover runs the coverage pass inside a submit, from the share kept for it, and, when it redrafted
// a section, the committee on the changed draft (from the cycle kept for the last review), so the
// delivered draft is always the one the critics read; its findings go through the usual checks.
func (f finish) cover(ctx context.Context, c agent.Call) (agent.Usage, error) {
	b := agent.NewBudget(c.Budget.Calls, c.Budget.USD, 0)
	me, call := agent.Agent{Name: "finish", Visible: []string{"cover", "committee"}}, agent.Call{TaskID: c.TaskID, DocumentIDs: c.DocumentIDs}
	before, err := load[Section](ctx, f.e, c.TaskID, kSection)
	if err != nil {
		return b.Spent(), err
	}
	if _, err := f.rt.Invoke(ctx, me, call, "cover", json.RawMessage(`{}`), b, false, 0); agent.EndsRun(err) {
		return b.Spent(), err
	}
	after, err := load[Section](ctx, f.e, c.TaskID, kSection)
	if err != nil || version(after) == version(before) { // nothing redrafted: the last review still holds
		return b.Spent(), err
	}
	if _, err := f.rt.Invoke(ctx, me, call, "committee", json.RawMessage(`{}`), b, false, 0); agent.EndsRun(err) {
		return b.Spent(), err
	}
	return b.Spent(), nil
}

// check runs every finish check without changing anything.
func (f finish) check(ctx context.Context, limits []limitation) (report, error) {
	e, id := f.e, f.r.t.ID
	issues, err := load[Issue](ctx, e, id, kIssue)
	secs, serr := load[Section](ctx, e, id, kSection)
	use, uerr := e.usable(ctx, id)
	cyc, cerr := load[Cycle](ctx, e, id, kCycle)
	crit, rerr := load[Critic](ctx, e, id, kCritic)
	sheets, herr := e.worksheets(ctx, id)
	notes, nerr := e.rows(ctx, id, kNote)
	rounds, lerr := e.lintRounds(ctx, id)
	if err = cmp.Or(err, serr, uerr, cerr, rerr, herr, nerr, lerr); err != nil {
		return report{}, err
	}
	rep := report{Errors: []finishErr{}, Lint: []string{}, OpenPreconditions: []string{}}
	bad := func(code, detail string) {
		rep.Errors = append(rep.Errors, finishErr{code, detail})
		rep.OpenPreconditions = append(rep.OpenPreconditions, detail)
	}
	limit := func(l string) { // a Limitations line, when there is one
		if l != "" {
			rep.limits = append(rep.limits, l)
		}
	}
	byFID := map[string]Finding{}
	var an []analysis.Finding
	for _, x := range use {
		byFID[x.FID], an = x, append(an, x.Finding)
	}
	disclosed := map[string]bool{}
	for _, l := range limits {
		if l.IssueID != nil {
			disclosed[*l.IssueID] = true
			rep.limits = append(rep.limits, *l.IssueID+": "+l.Text)
		} else {
			rep.limits = append(rep.limits, l.Text)
		}
	}
	for _, n := range notes {
		var s string
		if json.Unmarshal(n.Data, &s) == nil && s != "" && n.Key != "deliverable" {
			rep.limits = append(rep.limits, s)
		}
	}
	if all, err := load[Finding](ctx, e, id, kFinding); err != nil {
		return report{}, err
	} else {
		limit(hookQuarantined(all))
	}
	if os, err := load[HookOutcome](ctx, e, id, kHookRun); err != nil {
		return report{}, err
	} else {
		limit(leadOnlyLine(os))
	}
	if l, err := e.staleLine(ctx, id, issues); err != nil {
		return report{}, err
	} else {
		limit(l)
	}
	if len(issues) == 0 {
		bad("no_plan", "no plan is recorded: record one with ledger op plan")
	}
	if len(secs) == 0 {
		bad("no_draft", "nothing is drafted: draft each plan issue with draft_section")
	}
	texts, ais := map[string]string{}, []analysis.Issue{}
	for _, is := range issues {
		ais = append(ais, is.Issue)
		k := slices.IndexFunc(secs, func(s Section) bool { return s.IssueID == is.ID })
		met := false
		if k >= 0 {
			texts[is.ID] = substituteOutsideQuotes(secs[k].Markdown, secs[k].Handles)
			lines, limited, gate, computed := lintLimited(is.ID, secs[k], byFID, sheets)
			for _, c := range computed { // every figure the section states that a worksheet over typed inputs computes, or a reader's label alone traces: one block (lab-r4 r9 H1, r10)
				c.Section = is.ID
				rep.computed = append(rep.computed, c)
			}
			rep.Lint = append(rep.Lint, lines...)
			for _, fid := range slices.Sorted(maps.Keys(gate)) { // a memo figure traced only as written to its never-read quote, or at another scale: a lawyer confirms it (r6 M2, r7 M4)
				if x := byFID[fid]; !x.Low {
					x.Low = true
					x.ValueNotes = append(x.ValueNotes, map[string]string{limitedUnread: "gated for a lawyer: a memo figure traces only as written to this finding's quotes, which the figure reader never read",
						limitedUnit:       "gated for a lawyer: a memo period is this finding's only by the figure reader's unit label: the memo's unit words differ from the words it read here",
						limitedMemoUnread: "gated for a lawyer: a memo figure the figure reader never read (its call unfunded or failed) traces to this finding only as written",
						limitedScale:      "gated for a lawyer: a memo figure states this finding's figure at another scale (one side written out, the other read at a scale from its words), the same figure only by the figure reader's reading of those words"}[gate[fid]])
					byFID[fid] = x
					if err := e.write(ctx, id, kFinding, fid, x, true); err != nil {
						return report{}, err
					}
				}
			}
			if l := limited[limitedUnread]; len(l) > 0 {
				rep.limits = append(rep.limits, fmt.Sprintf("Figures: in the section on %s, %s %s traced only as written: the figure reader never read the cited quotes that state %s (its call was unfunded or failed), so what %s is unchecked.",
					is.ID, strings.Join(l, ", "), plural(len(l), "is", "are"), plural(len(l), "it", "them"), plural(len(l), "it means", "they mean")))
			}
			if l := limited[limitedScale]; len(l) > 0 {
				rep.limits = append(rep.limits, fmt.Sprintf("Figures: in the section on %s, %s %s written at another scale than the cited findings state %s (one written out digit for digit, the other read at a scale from its words): %s the same figure only by the figure reader's reading of those words, and the findings are gated; confirm %s against the cited quotes.",
					is.ID, strings.Join(l, ", "), plural(len(l), "is", "are"), plural(len(l), "it", "them"), plural(len(l), "it is", "they are"), plural(len(l), "it", "them")))
			}
			if r := secs[k].Read; r != nil && r.Unread != "" { // lab-r4 r5 L4: a section G8 could not trace by its readings
				rep.limits = append(rep.limits, fmt.Sprintf("Figures: in the section on %s, %s: G8 traces only figures the figure reader read clearly, so each of them that the drafter wrote out (not as a handle) and the reader's call never read traces only as written to the cited findings whose own quotes or values state it (listed under Computed figures, those findings gated for a lawyer), and any other was flagged as not traced.", is.ID, r.Unread))
			}
			met = slices.ContainsFunc(fidRe.FindAllStringSubmatch(secs[k].Markdown, -1), func(m []string) bool { _, ok := byFID[m[1]]; return ok })
		}
		if !met && !disclosed[is.ID] {
			bad("criterion_unmet", is.ID+" has no drafted section citing a verified finding: draft it from verified findings, or disclose it in limitations with its issueId")
		}
	}
	if fids, err := e.claimsUnread(ctx, id); err != nil {
		return report{}, err
	} else if len(fids) > 0 { // r1 MEDIUM-3: what the budget left unread is counted, never silent
		rep.limits = append(rep.limits, fmt.Sprintf("Figures: %d %s whose %s a figure %s never read (the figure reader was unfunded or its call failed), so the cross-document comparison could not see %s sums: %s.",
			len(fids), plural(len(fids), "finding", "findings"), plural(len(fids), "claim states", "claims state"), plural(len(fids), "was", "were"), plural(len(fids), "its claim's", "their claims'"), strings.Join(fids[:min(len(fids), 20)], ", ")))
	}
	if j, err := e.joined(ctx, id); err != nil { // every figure conflict is stated in the memo (bench r3)
		return report{}, err
	} else {
		rep.limits = append(rep.limits, j.lines()...) // what the judge could not decide, or did not judge (judge.go)
		for _, x := range j.open {
			var vs []string
			for _, v := range x.Values {
				vs = append(vs, "["+v.FID+"] "+v.Value)
			}
			rep.Lint = append(rep.Lint, fmt.Sprintf("memo: the documents state %s differently (%s), and the memo does not state both: redraft the section it bears on to state both figures, cite both findings and say what the difference means",
				cmpStr(strings.TrimSpace(x.Entity+" "+x.Metric) != "", strings.TrimSpace(x.Entity+" "+x.Metric), "a figure"), strings.Join(vs, " vs ")))
		}
	}
	for _, l := range rep.Lint { // after maxLintRounds reports, disclosed: redrafting for the lint is bounded
		if rounds >= maxLintRounds {
			rep.limits = append(rep.limits, fmt.Sprintf("Delivered with an unresolved check after %d rounds of fixes: %s", rounds, l))
		} else {
			bad("lint", l)
		}
	}
	pass, err := e.coverRow(ctx, id)
	left, lerr := e.touched(ctx, id)
	if err = cmp.Or(err, lerr); err != nil {
		return report{}, err
	}
	for _, is := range left { // after the coverage pass (or from the reserve): what it could not analyse
		if (pass != nil || f.r.forced != "") && !disclosed[is.ID] {
			rep.limits = append(rep.limits, fmt.Sprintf("Coverage: %s (%s) was not analysed: %s.", is.ID, is.Question,
				cmpStr(slices.ContainsFunc(secs, func(s Section) bool { return s.IssueID == is.ID }), "its section states facts without an analysis of the issue (rule, application, conclusion)", "it has no section resting on a verified finding")))
		}
	}
	if pass != nil || f.r.forced != "" { // what the pass could not place: disclosed
		if lo, err := e.leftOut(ctx, id); err != nil {
			return report{}, err
		} else if len(lo) > 0 {
			var ids []string
			for _, x := range lo {
				ids = append(ids, x.FID)
			}
			rep.limits = append(rep.limits, fmt.Sprintf("Coverage: %d verified %s a deviation, omission, violation or salient figure never reached the memo: %s.",
				len(lo), plural(len(lo), "finding that states", "findings that state"), firstN(ids, maxPlaced, ", ")))
		}
	}
	if pass != nil { // the coverage pass's list of usable findings with a dropped typed value (lab-r4 r3 M7): one line
		limit(unadmittedLine(pass.Unadmitted, byFID))
	}
	cov := analysis.CheckCoverage(ais, texts, an, nil)
	rep.MissingIssues, rep.UncoveredParties, rep.MissingFigures = cov.MissingIssues, cov.UncoveredParties, cov.MissingFigures
	limit(partyLine(cov.UncoveredParties)) // one line each, not one per party (bench5 SEC: 101 "Coverage:" lines)
	limit(figureLine(cov.MissingFigures))
	funded := f.funded()
	switch {
	case len(cyc) == 0 && len(secs) > 0:
		bad("no_committee", "no committee has reviewed the draft: run committee")
	case len(cyc) > 0 && cyc[len(cyc)-1].Version != version(secs) && funded && len(cyc) < maxCycles:
		bad("committee_stale", "the draft changed after the last committee: run committee on the current draft")
	case len(cyc) >= maxCycles && cyc[len(cyc)-1].Version != version(secs):
		rep.limits = append(rep.limits, fmt.Sprintf("Review: the committee reviewed the draft %d times (the run's cap); the changes after its last cycle were not reviewed again.%s", len(cyc), unreviewedFixes(crit, len(cyc))))
	}
	for _, x := range crit {
		switch {
		case x.Status == "disclosed":
			rep.limits = append(rep.limits, fmt.Sprintf("Critic finding %s (%s, %s) disclosed: %s. %s", x.ID, x.Severity, x.Critic, x.Text, x.Note))
		case x.Status == "open" && (x.Severity == "HIGH" || x.Severity == "MEDIUM") && funded:
			bad("open_findings", fmt.Sprintf("critic finding %s (%s) is open: fix it and resolve it as fixed, or resolve it as disclosed: %s", x.ID, x.Severity, x.Text))
		case x.Status == "open" && (x.Severity == "HIGH" || x.Severity == "MEDIUM"):
			rep.limits = append(rep.limits, fmt.Sprintf("Open critic finding %s (%s, %s), left open because the budget could not fund another fix and review: %s", x.ID, x.Severity, x.Critic, x.Text))
		}
	}
	if f.r.forced != "" {
		rep.limits = append(rep.limits, map[string]string{"budget_exhausted": "The run's budget ran out, so it finished from its reserve: the open items above were not worked through.",
			"max_steps": "The run reached its step limit (maxSteps), so it finished from its reserve: the open items above were not worked through."}[f.r.forced])
	}
	rep.Ready = len(rep.Errors) == 0
	return rep, nil
}

// funded is the run's funded (P6, P7) as if this finish call held only its own call.
func (f finish) funded() bool { return f.r.b == nil || f.r.funded(f.r.b, f.held, f.covered) }

// escalate is P7: a submit within 2 calls of a committee that left HIGH/MEDIUM findings open is
// refused once per cycle with the uncompromising instruction.
func (f finish) escalate(ctx context.Context) (*finishErr, error) {
	e, id := f.e, f.r.t.ID
	cyc, err := load[Cycle](ctx, e, id, kCycle)
	crit, cerr := load[Critic](ctx, e, id, kCritic)
	spent, serr := e.classSpend(ctx, id)
	if err = cmp.Or(err, cerr, serr); err != nil || len(cyc) == 0 || f.r.forced != "" {
		return nil, err
	}
	last := cyc[len(cyc)-1]
	open := slices.ContainsFunc(crit, func(x Critic) bool { return x.Cycle == last.N && (x.Severity == "HIGH" || x.Severity == "MEDIUM") })
	done, _ := e.rows(ctx, id, kEscalate)
	if !open || !f.funded() || spent["*"].Calls-last.Calls > 2 || slices.ContainsFunc(done, func(r row) bool { return r.Key == fmt.Sprint(last.N) }) {
		return nil, nil
	}
	if err := e.write(ctx, id, kEscalate, fmt.Sprint(last.N), true, false); err != nil {
		return nil, err
	}
	return &finishErr{"escalation", "You are submitting right after a committee whose HIGH or MEDIUM findings were resolved without new work. Be uncompromising: " +
		"re-read each such finding, fix what the record lets you fix (more evidence, a redraft, a calculation) and disclose only what it cannot, then run committee again. This stretch is funded."}, nil
}

// meta is the one reserved call for the process note to the reviewing lawyer.
func (f finish) meta(ctx context.Context, b *agent.Budget) (string, error) {
	e, id := f.e, f.r.t.ID
	all, err := load[Finding](ctx, e, id, kFinding)
	crit, cerr := load[Critic](ctx, e, id, kCritic)
	cyc, yerr := load[Cycle](ctx, e, id, kCycle)
	if err = cmp.Or(err, cerr, yerr); err != nil {
		return "", err
	}
	ok, open := 0, 0
	for _, x := range all {
		ok += btoi(x.ok())
	}
	for _, x := range crit {
		open += btoi(x.Status == "open")
	}
	facts := fmt.Sprintf("Findings: %d verified, %d quarantined. Committee cycles: %d; critic findings: %d, %d still open. Stop: %s.",
		ok, len(all)-ok, len(cyc), len(crit), open, cmpStr(f.r.forced != "", f.r.forced, "finish accepted"))
	resp, _, err := f.rt.ChatWithin(ctx, model.Request{Agent: "meta", Role: model.Lead, MaxTokens: 400,
		Messages: []model.Message{{Role: "system", Content: prompt(ctx, "meta")}, {Role: "user", Content: facts}}}, b)
	if err != nil {
		return "", err
	}
	return processRe.ReplaceAllString(resp.Text, ""), nil
}

// gates opens the human gates for a rendered deliverable (G7): one per cited finding resting only
// on low-confidence transcript pages, admitted at low confidence (a deviation its section check
// could not confirm) or marked unsupported by the prosecutor and still open; while
// any is undecided the run pauses there, otherwise the deliverable's gate opens.
func (f finish) gates(ctx context.Context, md string) error {
	e, id := f.e, f.r.t.ID
	use, err := e.usable(ctx, id)
	crit, cerr := load[Critic](ctx, e, id, kCritic)
	gs, gerr := load[Gate](ctx, e, id, kGate)
	if err = cmp.Or(err, cerr, gerr); err != nil {
		return err
	}
	pending := false
	for _, x := range use {
		unsupported := slices.ContainsFunc(crit, func(c Critic) bool { return c.FID == x.FID && c.Severity == "HIGH" && c.Status == "open" })
		if !strings.Contains(md, "["+x.FID+"]") || !gated(x, unsupported) {
			continue
		}
		g := Gate{GateID: "G-" + x.FID, Kind: "finding", FindingID: x.FID}
		if i := slices.IndexFunc(gs, func(y Gate) bool { return y.GateID == g.GateID }); i >= 0 {
			pending = pending || gs[i].Decision == ""
			continue
		}
		pending = true
		if err := e.write(ctx, id, kGate, g.GateID, g, false); err != nil {
			return err
		}
	}
	if slices.ContainsFunc(gs, func(g Gate) bool { return g.Kind == "hook" && g.Decision == "" }) { // a call a hook holds for a lawyer
		pending = true
	}
	if pending || slices.ContainsFunc(gs, func(g Gate) bool { return g.Kind == "deliverable" && g.Decision == "" }) {
		return nil
	}
	n := 1 + len(slices.DeleteFunc(slices.Clone(gs), func(g Gate) bool { return g.Kind != "deliverable" }))
	g := Gate{GateID: fmt.Sprintf("G-deliverable-%d", n), Kind: "deliverable"}
	return e.write(ctx, id, kGate, g.GateID, g, false)
}

// gated reports whether a cited finding needs a lawyer's decision at its own gate: marked
// unsupported by the prosecutor, admitted at low confidence, or resting only on low-confidence
// transcript pages.
func gated(x Finding, unsupported bool) bool {
	return unsupported || x.Low || allCitations(x, func(c Citation) bool { return c.Low })
}

// maxListed bounds the names a summarised Coverage line lists before "and N more".
const maxListed = 12

// unadmittedLine is the Limitations line for the usable findings that kept fewer typed values than
// their subagent gave: each dropped value's note is on the finding (the API's finding view).
func unadmittedLine(fids []string, use map[string]Finding) string {
	var ids []string
	for _, fid := range fids {
		if _, ok := use[fid]; ok {
			ids = append(ids, fid)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return fmt.Sprintf("Evidence: %d verified %s a typed value that admission dropped because %s quotes did not state it as typed (each finding's value notes say why): %s.",
		len(ids), plural(len(ids), "finding had", "findings had"), plural(len(ids), "its", "their"), firstN(ids, maxListed, ", "))
}

// unreviewedFixes names the critic findings resolved "fixed" after the committee's last cycle (or
// raised by it): no critic read those fixes, so they are what a reader must check again (lab-r4 r4
// verifier L7).
func unreviewedFixes(crit []Critic, cycles int) string {
	var ls []string
	for _, x := range crit {
		if x.Status == "fixed" && (x.Cycle == cycles || x.Resolved >= cycles) {
			t := x.Text
			if r := []rune(t); len(r) > 240 {
				t = string(r[:240]) + "…"
			}
			ls = append(ls, fmt.Sprintf("%s (%s, %s): %s", x.ID, x.Severity, x.Critic, t))
		}
	}
	if len(ls) == 0 {
		return ""
	}
	return " Resolved as fixed after it, unreviewed: " + strings.Join(ls[:min(maxListed, len(ls))], "; ") + cmpStr(len(ls) > maxListed, fmt.Sprintf("; and %d more", len(ls)-maxListed), "") + "."
}

// partyLine is the Limitations line for the parties the findings name and the memo never does.
func partyLine(ps []analysis.PartyGap) string {
	if len(ps) == 0 {
		return ""
	}
	var names []string
	for _, p := range ps {
		names = append(names, fmt.Sprintf("%s (%s)", p.Name, strings.Join(p.FIDs[:min(3, len(p.FIDs))], ", ")))
	}
	return fmt.Sprintf("Coverage: %d %s named in verified findings never %s in the memo: %s.", len(ps), plural(len(ps), "party", "parties"), plural(len(ps), "appears", "appear"),
		firstN(names, maxListed, "; "))
}

// figureLine is the Limitations line for the salient figures that never reach the memo.
func figureLine(hs []analysis.Handle) string {
	if len(hs) == 0 {
		return ""
	}
	var figs []string
	for _, h := range hs {
		figs = append(figs, fmt.Sprintf("%s (%s)", h.Value, strings.Join(slices.DeleteFunc([]string{h.Label, h.FID}, func(s string) bool { return s == "" }), ", ")))
	}
	return fmt.Sprintf("Coverage: %d salient %s never %s in the memo: %s.", len(hs), plural(len(hs), "figure", "figures"), plural(len(hs), "appears", "appear"),
		firstN(figs, maxListed, "; "))
}
