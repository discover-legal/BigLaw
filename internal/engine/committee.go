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

func modelFailed(err error) bool {
	for _, k := range []error{model.ErrNotConfigured, model.ErrRateLimited, model.ErrUnavailable, model.ErrTimeout, model.ErrNoContent, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, k) {
			return true
		}
	}
	return false
}

var fidRe = regexp.MustCompile(`\[(F\d+)\]`)

// maxClaimFIDs is the most findings one claim names (prosecute's input schema).
const maxClaimFIDs = 20

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
			for len(fids) > 0 && len(out) < 80 { // at most 20 findings a claim (prosecute's schema): a longer list is checked in parts
				k := min(len(fids), maxClaimFIDs)
				out = append(out, map[string]any{"text": substituteOutsideQuotes(sent, s.Handles), "fids": fids[:k]})
				fids = fids[k:]
			}
		}
	}
	return out
}

// committeeTool runs the three critics in parallel, each context-free and on its own explicitly
// sized grant (a tool grant would otherwise take all free USD), and records their findings.
func (e *Engine) committeeTool(rt *agent.Runtime, r *run) agent.Tool {
	return agent.GoTool{S: r.sp().Spec("committee"), Fn: func(ctx context.Context, c agent.Call) (any, agent.Usage, error) {
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
			map[string]any{"plan": plan, "headings": append([]string{}, heads...), "checklist": append([]string{}, checklistOf(packsFor(ctx, r.t.Jurisdiction, practiceOf(issues)))...)}}
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
		if line, fids := admissionLine(cl, fs, old); line != "" { // after the other critics, one line, once per finding per run
			add("admission", "LOW", fids[0], line)
			found[len(found)-1].FIDs = fids
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
		return criticPage(n, found), b.Spent(), nil
	}}
}

// maxAdmissionLine bounds the committee's one admission line (lab-r4 r3 M7: 80 notes, one LOW line
// each, pushed a HIGH audit gap off the committee's page).
const maxAdmissionLine = 600

// admissionLine is the committee's one LOW line for the cited findings whose typed values admission
// dropped (Finding.ValueNotes) and no earlier cycle raised: their ids, the first notes while the
// line has room, and how many more.
func admissionLine(claims []map[string]any, fs []Finding, old []Critic) (string, []string) {
	raised := map[string]bool{}
	for _, c := range old {
		if c.Critic == "admission" {
			for _, fid := range append(c.FIDs, c.FID) {
				raised[fid] = true
			}
		}
	}
	var fids, notes []string
	for _, x := range claims {
		for _, fid := range x["fids"].([]string) {
			if j := slices.IndexFunc(fs, func(f Finding) bool { return f.FID == fid }); j >= 0 && !raised[fid] && len(fs[j].ValueNotes) > 0 {
				raised[fid] = true
				fids = append(fids, fid)
				for _, n := range fs[j].ValueNotes {
					notes = append(notes, fid+": "+n)
				}
			}
		}
	}
	if len(fids) == 0 {
		return "", nil
	}
	line := fmt.Sprintf("%d cited %s typed values admission dropped (%s)", len(fids), plural(len(fids), "finding has", "findings have"), strings.Join(fids, ", "))
	shown := 0
	for _, n := range notes {
		if len(line)+len(n)+2 > maxAdmissionLine {
			break
		}
		line, shown = line+cmpStr(shown == 0, ": ", "; ")+n, shown+1
	}
	if shown < len(notes) {
		line += fmt.Sprintf(" (and %d more %s on the findings themselves)", len(notes)-shown, plural(len(notes)-shown, "note", "notes"))
	}
	if len(line) > maxAdmissionLine+200 { // very many ids: count them only
		line = fmt.Sprintf("%d cited findings have typed values admission dropped (first %s; each finding lists its notes)", len(fids), strings.Join(fids[:min(maxListed, len(fids))], ", "))
	}
	return line, fids
}

// criticPage is committee's answer: one page of the cycle's findings, the most severe first (the
// rest: ledger op critics), so no LOW line ever pushes a HIGH or MEDIUM one off the page.
func criticPage(n int, found []Critic) map[string]any {
	list := []map[string]any{}
	found = slices.Clone(found)
	rank := map[string]int{"HIGH": 0, "MEDIUM": 1, "LOW": 2}
	slices.SortStableFunc(found, func(a, b Critic) int { return rank[a.Severity] - rank[b.Severity] })
	for _, f := range found {
		list = append(list, map[string]any{"id": f.ID, "critic": f.Critic, "severity": f.Severity, "fid": nilIf(f.FID), "text": f.Text})
	}
	pg := agent.NewPage()
	return map[string]any{"cycle": n, "findings": agent.Take(pg, list), "truncated": pg.Truncated}
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
