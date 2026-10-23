// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/discover-legal/BigLaw/internal/agent"
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
// analysis, within Allocation.Cover. It runs once per run (its kCover row marks it done).
func (e *Engine) coverTool(rt *agent.Runtime, r *run) agent.Tool {
	return agent.GoTool{S: spec("cover"), Fn: func(ctx context.Context, c agent.Call) (any, agent.Usage, error) {
		a := Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD)
		b := agent.NewBudget(min(c.Budget.Calls, a.Cover), min(c.Budget.USD, a.dollars(a.Cover)), 0)
		todo, err := e.touched(ctx, c.TaskID)
		if err != nil {
			return nil, agent.Usage{}, err
		}
		me := agent.Agent{Name: "cover", Visible: []string{"analyze_issue", "draft_section"}}
		call := agent.Call{TaskID: c.TaskID, DocumentIDs: c.DocumentIDs}
		for _, is := range todo {
			if calls, _ := b.Remaining(); calls == 0 {
				break
			}
			in, _ := json.Marshal(map[string]any{"issueId": is.ID, "question": is.Question, "jurisdiction": r.t.Jurisdiction, "packs": nil})
			_, err := rt.Invoke(ctx, me, call, "analyze_issue", in, b, false, 0)
			if modelOrJournal(err) {
				return nil, b.Spent(), err
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
				return nil, b.Spent(), err
			}
		}
		left, err := e.touched(ctx, c.TaskID)
		if err != nil {
			return nil, b.Spent(), err
		}
		res := Cover{Analysed: []string{}, Missing: []string{}}
		for _, is := range todo {
			if slices.ContainsFunc(left, func(x Issue) bool { return x.ID == is.ID }) {
				res.Missing = append(res.Missing, is.ID)
			} else {
				res.Analysed = append(res.Analysed, is.ID)
			}
		}
		return map[string]any{"analysed": res.Analysed, "missing": res.Missing}, b.Spent(), e.write(ctx, c.TaskID, kCover, "pass", res, false)
	}}
}

// coverPass runs the coverage pass as a step of its own (a run finishing from its reserve) when a
// plan issue is only touched and the pass has not run, before the last review; it returns the last
// step. A refused or failed pass is in the step log, and finish discloses what is still touched.
func (e *Engine) coverPass(ctx context.Context, rt *agent.Runtime, call agent.Call, r *run, last int) (int, error) {
	done, err := e.covered(ctx, r.t.ID)
	todo, terr := e.touched(ctx, r.t.ID)
	if err = cmpErr(err, terr); err != nil || done || len(todo) == 0 {
		return last, err
	}
	_, err = rt.Invoke(ctx, agent.Agent{Name: "cover", Visible: []string{"cover"}}, call, "cover", json.RawMessage(`{}`), r.b, false, last+1)
	if ctx.Err() != nil || errors.Is(err, errLease) || modelOrJournal(err) {
		return last, cmpErr(ctx.Err(), err)
	}
	return last + 1, nil
}
