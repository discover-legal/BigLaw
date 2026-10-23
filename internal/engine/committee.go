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
	"sync"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/model"
)

var critics = []string{"prosecute", "reproduce", "audit_gaps"}

func modelFailed(err error) bool {
	for _, k := range []error{model.ErrNotConfigured, model.ErrRateLimited, model.ErrUnavailable, model.ErrTimeout, model.ErrNoContent, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, k) {
			return true
		}
	}
	return false
}

var fidRe = regexp.MustCompile(`\[(F\d+)\]`)

// claims splits the drafted sections into the sentences that cite findings, for prosecute.
func claims(sections []Section) []map[string]any {
	out := []map[string]any{}
	for _, s := range sections {
		for _, sent := range splitSentences(s.Markdown) {
			var fids []string
			for _, m := range fidRe.FindAllStringSubmatch(sent, -1) {
				if !slices.Contains(fids, m[1]) {
					fids = append(fids, m[1])
				}
			}
			if len(fids) > 0 && len(out) < 80 {
				out = append(out, map[string]any{"text": substituteOutsideQuotes(sent, s.Handles), "fids": fids})
			}
		}
	}
	return out
}

// committeeTool runs the three critics in parallel, each context-free and on its own explicitly
// sized grant (a tool grant would otherwise take all free USD), and records their findings.
func (e *Engine) committeeTool(rt *agent.Runtime, r *run) agent.Tool {
	return agent.GoTool{S: spec("committee"), Fn: func(ctx context.Context, c agent.Call) (any, agent.Usage, error) {
		id := c.TaskID
		issues, err := load[Issue](ctx, e, id, kIssue)
		secs, serr := load[Section](ctx, e, id, kSection)
		cycles, cerr := load[Cycle](ctx, e, id, kCycle)
		old, oerr := load[Critic](ctx, e, id, kCritic)
		if err = cmpErr(err, serr, cerr, oerr); err != nil {
			return nil, agent.Usage{}, err
		} else if len(secs) == 0 {
			return nil, agent.Usage{}, agent.Fail("committee: nothing is drafted yet; draft the plan's issues with draft_section first")
		}
		var plan, qs, heads []string
		for _, is := range issues {
			plan, qs = append(plan, is.ID+": "+is.Question), append(qs, is.Question)
			if slices.ContainsFunc(secs, func(s Section) bool { return s.IssueID == is.ID }) {
				heads = append(heads, is.Question)
			}
		}
		inputs := []any{map[string]any{"claims": claims(secs)}, map[string]any{"questions": qs},
			map[string]any{"plan": plan, "headings": append([]string{}, heads...), "checklist": append([]string{}, Checklist(r.t.Jurisdiction, practiceOf(issues))...)}}
		a := Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD)
		limit := min(c.Budget.USD, a.CycleUSD) // a cycle's share, never the whole free USD (DESIGN § Budgets)
		b := agent.NewBudget(c.Budget.Calls, limit, 0)
		outs, errs := make([]agent.Result, 3), make([]error, 3)
		var wg sync.WaitGroup
		for i, name := range critics {
			usd := limit * float64(a.Critics[i]) / float64(max(a.Cycle, 1))
			if i == len(critics)-1 {
				usd = -1 // the rest (shares computed in floating point need not add up exactly)
			}
			g, err := b.Grant(min(a.Critics[i], c.Budget.Calls), usd, agent.LevelWork)
			if err != nil {
				errs[i] = err
				continue
			}
			in, _ := json.Marshal(inputs[i])
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer g.Close()
				outs[i], errs[i] = rt.Invoke(ctx, agent.Agent{Name: "committee", Visible: critics}, agent.Call{TaskID: id, DocumentIDs: c.DocumentIDs}, name, in, g, false, 0)
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if modelOrJournal(err) {
				return nil, b.Spent(), err
			}
		}
		n := len(cycles) + 1
		draftText := ""
		for _, s := range secs {
			draftText += substituteOutsideQuotes(s.Markdown, s.Handles) + "\n"
		}
		var found []Critic
		add := func(critic, sev, fid, text string) {
			found = append(found, Critic{ID: nextID("C", len(old)+len(found)), Cycle: n, Critic: critic, Severity: sev, FID: fid, Text: text, Status: "open"})
		}
		for i, err := range errs {
			if err != nil {
				e.log.Warn("engine: a critic failed", "task", id, "critic", critics[i], "err", err)
				add(critics[i], "MEDIUM", "", critics[i]+" did not return a usable answer, so its check did not run this cycle")
			}
		}
		var pros struct {
			Verdicts []struct{ Claim, Verdict, Why string }
		}
		_ = json.Unmarshal(outs[0].Output, &pros)
		cl := inputs[0].(map[string]any)["claims"].([]map[string]any)
		fs, err := load[Finding](ctx, e, id, kFinding)
		if err != nil {
			return nil, b.Spent(), err
		}
		for i, v := range pros.Verdicts {
			var fids []string
			if i < len(cl) {
				fids = cl[i]["fids"].([]string)
			}
			credit := map[string]float64{"supported": 0.5, "unsupported": -0.5}[v.Verdict]
			for _, fid := range fids { // delayed critic credit to the arm that produced the finding
				if j := slices.IndexFunc(fs, func(f Finding) bool { return f.FID == fid }); j >= 0 && credit != 0 {
					_ = rt.Bandit.Stats.Add(ctx, rt.Bandit.Sig, fs[j].Arm, agent.Stat{Reward: credit})
				}
			}
			if sev := map[string]string{"overstated": "MEDIUM", "unsupported": "HIGH"}[v.Verdict]; sev != "" {
				add("prosecute", sev, first(fids), fmt.Sprintf("%s: %q. %s", v.Verdict, v.Claim, v.Why))
			}
		}
		var rep struct {
			Answers []struct{ Question, Value, DocumentID, Quote string }
		}
		_ = json.Unmarshal(outs[1].Output, &rep)
		for _, ans := range rep.Answers {
			cit, err := e.verify(ctx, nil, c, b, "reproduce", quoteOf(ans.DocumentID, ans.Quote))
			if err != nil {
				return nil, b.Spent(), err
			}
			if cit.Method != "none" && !statesValue(draftText, ans.Value) {
				add("reproduce", "MEDIUM", "", fmt.Sprintf("For %q the record gives %q (%q), which the draft does not state.", ans.Question, ans.Value, ans.Quote))
			}
		}
		var aud struct {
			Gaps []struct{ Item, Severity, Why string }
		}
		_ = json.Unmarshal(outs[2].Output, &aud)
		for _, g := range aud.Gaps {
			add("audit_gaps", g.Severity, "", g.Item+": "+g.Why)
		}
		for _, f := range found {
			if err := e.write(ctx, id, kCritic, f.ID, f, false); err != nil {
				return nil, b.Spent(), err
			}
		}
		spent, _ := e.classSpend(ctx, id)
		cy := Cycle{N: n, Version: version(secs), Calls: spent["*"].Calls + b.Spent().Calls}
		if err := e.write(ctx, id, kCycle, fmt.Sprint(n), cy, false); err != nil {
			return nil, b.Spent(), err
		}
		list := []map[string]any{}
		for _, f := range found {
			list = append(list, map[string]any{"id": f.ID, "critic": f.Critic, "severity": f.Severity, "fid": nilIf(f.FID), "text": f.Text})
		}
		return map[string]any{"cycle": n, "findings": list}, b.Spent(), nil
	}}
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func nilIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// version is the draft's version: the latest section write.
func version(secs []Section) int {
	v := 0
	for _, s := range secs {
		v = max(v, s.Version)
	}
	return v
}

// statesValue reports whether text states value: as text, or every figure and date in it.
func statesValue(text, value string) bool {
	if strings.Contains(strings.ToLower(norm(text)), strings.ToLower(norm(value))) {
		return true
	}
	return covered(value, text)
}

func practiceOf(issues []Issue) string {
	for _, is := range issues {
		if is.Practice != "" {
			return is.Practice
		}
	}
	return ""
}
