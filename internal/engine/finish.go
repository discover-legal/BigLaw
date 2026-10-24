// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	s, a := spec("finish"), Allocate(f.e.cfg.BudgetCalls, f.e.cfg.BudgetUSD)
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
	limits            []string
}

type limitation struct {
	IssueID *string `json:"issueId"`
	Text    string  `json:"text"`
}

func (f finish) Call(ctx context.Context, c agent.Call) (agent.Result, error) {
	f.held = max(0, c.Budget.Calls-1) // its grant holds the coverage pass's and the review's calls too
	var err error
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
		data, err := json.Marshal(rep)
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
		if err = cmpErr(err, derr); err != nil {
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
	d, err := f.e.render(ctx, f.r.t, rep.limits, meta, flagged)
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
	if _, err := f.rt.Invoke(ctx, me, call, "cover", json.RawMessage(`{}`), b, false, 0); modelOrJournal(err) {
		return b.Spent(), err
	}
	after, err := load[Section](ctx, f.e, c.TaskID, kSection)
	if err != nil || version(after) == version(before) { // nothing redrafted: the last review still holds
		return b.Spent(), err
	}
	if _, err := f.rt.Invoke(ctx, me, call, "committee", json.RawMessage(`{}`), b, false, 0); modelOrJournal(err) {
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
	if err = cmpErr(err, serr, uerr, cerr, rerr, herr, nerr, lerr); err != nil {
		return report{}, err
	}
	rep := report{Errors: []finishErr{}, Lint: []string{}, OpenPreconditions: []string{}}
	bad := func(code, detail string) {
		rep.Errors = append(rep.Errors, finishErr{code, detail})
		rep.OpenPreconditions = append(rep.OpenPreconditions, detail)
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
			rep.Lint = append(rep.Lint, lint(is.ID, secs[k], byFID, sheets)...)
			met = slices.ContainsFunc(fidRe.FindAllStringSubmatch(secs[k].Markdown, -1), func(m []string) bool { _, ok := byFID[m[1]]; return ok })
		}
		if !met && !disclosed[is.ID] {
			bad("criterion_unmet", is.ID+" has no drafted section citing a verified finding: draft it from verified findings, or disclose it in limitations with its issueId")
		}
	}
	if _, open, err := e.discrepancies(ctx, id); err != nil { // every figure conflict is stated in the memo (bench r3)
		return report{}, err
	} else {
		for _, x := range open {
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
	if err = cmpErr(err, lerr); err != nil {
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
			for _, x := range lo[:min(maxPlaced, len(lo))] {
				ids = append(ids, x.FID)
			}
			rep.limits = append(rep.limits, fmt.Sprintf("Coverage: %d verified %s that state a deviation, omission, violation or salient figure never reached the memo: %s%s.",
				len(lo), plural(len(lo), "finding", "findings"), strings.Join(ids, ", "), cmpStr(len(lo) > maxPlaced, fmt.Sprintf(" and %d more", len(lo)-maxPlaced), "")))
		}
	}
	cov := analysis.CheckCoverage(ais, texts, an, nil)
	rep.MissingIssues, rep.UncoveredParties, rep.MissingFigures = cov.MissingIssues, cov.UncoveredParties, cov.MissingFigures
	for _, p := range cov.UncoveredParties {
		rep.limits = append(rep.limits, "Coverage: "+p.Note)
	}
	for _, h := range cov.MissingFigures {
		rep.limits = append(rep.limits, fmt.Sprintf("Coverage: the figure %s (%s, %s) never appears in the memo.", h.Value, h.Label, h.FID))
	}
	funded := f.funded()
	switch {
	case len(cyc) == 0 && len(secs) > 0:
		bad("no_committee", "no committee has reviewed the draft: run committee")
	case len(cyc) > 0 && cyc[len(cyc)-1].Version != version(secs) && funded:
		bad("committee_stale", "the draft changed after the last committee: run committee on the current draft")
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
	if err = cmpErr(err, cerr, serr); err != nil || len(cyc) == 0 || f.r.forced != "" {
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
	if err = cmpErr(err, cerr, yerr); err != nil {
		return "", err
	}
	ok, open := 0, 0
	for _, x := range all {
		ok += btoi(x.Verified)
	}
	for _, x := range crit {
		open += btoi(x.Status == "open")
	}
	facts := fmt.Sprintf("Findings: %d verified, %d quarantined. Committee cycles: %d; critic findings: %d, %d still open. Stop: %s.",
		ok, len(all)-ok, len(cyc), len(crit), open, cmpStr(f.r.forced != "", f.r.forced, "finish accepted"))
	resp, _, err := f.rt.ChatWithin(ctx, model.Request{Agent: "meta", Role: model.Lead, MaxTokens: 400,
		Messages: []model.Message{{Role: "system", Content: prompt("meta")}, {Role: "user", Content: facts}}}, b)
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
	if err = cmpErr(err, cerr, gerr); err != nil {
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
