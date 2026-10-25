// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
)

//go:embed specs
var specFS embed.FS

// specs holds every engine tool's and subagent's spec by name (JSON data beside the prompts).
var specs = func() map[string]agent.Spec {
	out := map[string]agent.Spec{}
	for _, dir := range []string{"specs", "specs/agents"} {
		files, _ := fs.Glob(specFS, dir+"/*.json")
		for _, f := range files {
			var s agent.Spec
			data, err := specFS.ReadFile(f)
			if err == nil {
				err = json.Unmarshal(data, &s)
			}
			if err != nil {
				panic("engine: " + f + ": " + err.Error())
			}
			out[s.Name] = s
		}
	}
	for _, n := range []string{"retrieve", "verify_quote", "numbers", "join_discrepancies"} {
		out[n] = agent.Spec{Name: n, Class: "free"}
	}
	return out
}()

func spec(name string) agent.Spec { return specs[name] }

func prompt(name string) string {
	b, err := specFS.ReadFile("specs/" + name + ".md")
	if err != nil {
		panic("engine: " + err.Error())
	}
	return string(b)
}

// MemoVisible is the memo lead's tool list (README § Tools): 11 tools, in a fixed order.
var MemoVisible = []string{"retrieve", "ledger", "sweep", "extract_facts", "analyze_issue", "check_deviation", "numbers", "join_discrepancies", "draft_section", "committee", "finish"}

// hookOut is what an evidence subagent returns to its caller once Go has verified its findings,
// each kept value under the ref numbers takes ("F3.v1").
var hookOut = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["findings","dropped","notes"],"properties":{
 "findings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["fid","issueId","claim","verified","methods","values"],
  "properties":{"fid":{"type":"string"},"issueId":{"type":"string"},"claim":{"type":"string"},"verified":{"type":"boolean"},"methods":{"type":"array","items":{"type":"string"}},
   "values":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["ref","value"],"properties":{"ref":{"type":"string"},"value":{"type":"string"}}}}}}},
 "dropped":{"type":"integer"},"notes":{"type":"array","items":{"type":"string"}}}}`)

// admitted is a recorded finding as its caller sees it: what it claims, how its quotes verified,
// and its values by ref.
func admitted(f Finding) map[string]any {
	methods, values := []string{}, []map[string]string{}
	for _, c := range f.Citations {
		methods = append(methods, c.Method)
	}
	for i, v := range f.Values {
		values = append(values, map[string]string{"ref": fmt.Sprintf("%s.v%d", f.FID, i+1), "value": v.Value})
	}
	return map[string]any{"fid": f.FID, "issueId": f.IssueID, "claim": f.Claim, "verified": f.Verified, "methods": methods, "values": values}
}

// runtime builds the run's registry and runtime: the docs and analysis tools, the engine's Go
// tools, and the subagents from their specs; evidence subagents and draft_section behind Go hooks.
func (e *Engine) runtime(r *run) (*agent.Runtime, error) {
	rt := &agent.Runtime{Chat: e.chat, Roles: e.roles, Prices: e.prices, Reg: agent.NewRegistry(), Policy: policy{r}, Journal: r, Log: e.log}
	rt.Bandit = &agent.Bandit{Stats: stats{r}, Sig: agent.Signature(r.t.WorkflowType, "", r.t.Jurisdiction, r.t.LeadModel, len(r.t.DocumentIDs)),
		BudgetCalls: e.cfg.BudgetCalls, Off: !r.t.ToolBandit}
	tools := e.docs.Tools(rt)
	an := analysis.Tools(judging{e, rt})
	tools = append(tools, sheets{an[0], e}, aliasJoin{an[1], e}, e.ledgerTool(), e.sweepTool(rt, r), e.committeeTool(rt, r), e.finishTool(rt, r), e.coverTool(rt, r),
		e.figureContextTool(), exactCalc{})
	sub, _ := fs.Sub(specFS, "specs/agents")
	subs, err := rt.LoadSubagents(sub)
	if err != nil {
		return nil, err
	}
	for _, s := range subs {
		if n := s.Spec().Name; n == "extract_facts" || n == "analyze_issue" || n == "check_deviation" || n == "draft_section" {
			s = hooked{s, e, rt}
		}
		tools = append(tools, s)
	}
	for _, t := range tools {
		if err := rt.Reg.Register(t); err != nil {
			return nil, err
		}
	}
	return rt, nil
}

// sheets keeps every numbers{op:"calc"} worksheet in the ledger (the deliverable's totals and
// computed dates trace to one, G8, and a drafter writes it as {W…}), and answers op "handles" from
// the run's kept codenames, so the lead and the drafters see one codename per figure.
type sheets struct {
	agent.Tool
	e *Engine
}

func (s sheets) Call(ctx context.Context, c agent.Call) (agent.Result, error) {
	var in struct {
		Op         string
		FIDs       []string `json:"fids"`
		SalientTop int
	}
	if json.Unmarshal(c.Input, &in) == nil && in.Op == "handles" {
		use, err := s.e.usable(ctx, c.TaskID)
		hs, herr := s.e.handleSet(ctx, c.TaskID, use)
		if err = cmpErr(err, herr); err != nil {
			return agent.Result{}, err
		}
		hs = slices.DeleteFunc(hs, func(h analysis.Handle) bool {
			return h.FID == "" || len(in.FIDs) > 0 && !slices.Contains(in.FIDs, h.FID)
		})
		pg := agent.NewPage() // one page, saying truncated (bench6: the answer lacked the schema's truncated key)
		list := agent.Take(pg, append([]analysis.Handle{}, hs[:min(len(hs), cmp.Or(in.SalientTop, len(hs)))]...))
		out, _ := json.Marshal(map[string]any{"wid": nil, "result": nil, "unit": nil, "worksheet": nil, "literalOperands": nil, "tableMarkdown": nil,
			"handles": list, "truncated": pg.Truncated})
		return agent.Result{Output: out}, nil
	}
	res, err := s.Tool.Call(ctx, c)
	var w analysis.Worksheet
	if err == nil && json.Unmarshal(res.Output, &w) == nil && w.WID != "" {
		err = s.e.write(ctx, c.TaskID, kSheet, w.WID, w, false)
	}
	return res, err
}

// aliasJoin keeps the aliases the lead passes join_discrepancies for the run (kAlias), so finish's and
// the coverage pass's own join (discrepancies) unify the same parties (bench r3: aliases were the
// lead's only way to pair one firm's figures under two names, and finish never saw them).
type aliasJoin struct {
	agent.Tool
	e *Engine
}

func (j aliasJoin) Call(ctx context.Context, c agent.Call) (agent.Result, error) {
	var in struct{ Aliases []analysis.Alias }
	_ = json.Unmarshal(c.Input, &in)
	if len(in.Aliases) > 0 { // the call itself uses its own and the run's
		was, err := load[analysis.Alias](ctx, j.e, c.TaskID, kAlias)
		if err != nil {
			return agent.Result{}, err
		}
		for _, a := range in.Aliases {
			key := strings.ToLower(norm(a.Name))
			if key == "" {
				continue
			}
			if i := slices.IndexFunc(was, func(x analysis.Alias) bool { return strings.ToLower(norm(x.Name)) == key }); i >= 0 {
				for _, o := range a.Aliases {
					if !slices.Contains(was[i].Aliases, o) {
						was[i].Aliases = append(was[i].Aliases, o)
					}
				}
				a = was[i]
			}
			if err := j.e.write(ctx, c.TaskID, kAlias, key, a, true); err != nil {
				return agent.Result{}, err
			}
		}
	}
	return j.Tool.Call(ctx, c)
}

// hooked is a subagent behind Go: its request is completed (a section's text, a drafter's
// findings and handles) and its answer is checked (every quote through the ladder) before
// anything reaches the ledger (G2) or the caller.
type hooked struct {
	agent.Tool
	e  *Engine
	rt *agent.Runtime
}

// goTurns are the calls Go makes after a hooked subagent out of its grant, held back from it: the
// figure reader of a drafted section or of an evidence call's findings (lab-r4 r5 L4: reserved before
// the subagent runs, and from the quotes' judges, never what they left). They are not the
// subagent's turns: P4's class share does not count them against the subagent.
func goTurns(name string) int {
	if name == "draft_section" || name == "extract_facts" || name == "analyze_issue" {
		return 1
	}
	return 0
}

func (h hooked) Spec() agent.Spec {
	s := h.Tool.Spec()
	if s.Name == "check_deviation" { // its grant holds the requirement's check beside the subagent's own turns
		s.MaxTurns++
	}
	s.MaxTurns += goTurns(s.Name)
	if s.Name == "draft_section" {
		s.Out = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["issueId","version","words"],"properties":{"issueId":{"type":"string"},"version":{"type":"integer"},"words":{"type":"integer"}}}`)
	} else {
		s.Out = hookOut
	}
	return s
}

func (h hooked) Call(ctx context.Context, c agent.Call) (agent.Result, error) {
	name := h.Tool.Spec().Name
	var in map[string]any
	_ = json.Unmarshal(c.Input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	var draft draftReq
	var rq reqText
	var preUse agent.Usage // spent before the subagent ran (check_deviation's requirement check)
	var held agent.Grant   // kept from the subagent for Go's calls after it (draft_section's figure reader)
	switch name {
	case "extract_facts":
		if str("issueId") == "" { // the plan's issues: each finding names the one it bears on (bench5 SEC: 896 findings, most on none)
			issues, err := load[Issue](ctx, h.e, c.TaskID, kIssue)
			if err != nil {
				return agent.Result{}, err
			}
			only, part := ctx.Value(issuesKey{}).([]string) // a page read again for the issues added since: only those
			var list []map[string]string
			for _, is := range issues {
				if !part || slices.Contains(only, is.ID) {
					list = append(list, map[string]string{"id": is.ID, "question": is.Question})
				}
			}
			if len(list) > 0 {
				in["issues"] = list
			}
		}
		if str("documentId") != "" && str("sectionId") != "" {
			off, _ := in["offset"].(float64)
			if str("through") != "" {
				text, ok := h.e.runText(ctx, c.DocumentIDs, str("documentId"), str("sectionId"), str("through"))
				if !ok || off != 0 {
					return agent.Result{}, agent.Fail(fmt.Sprintf("extract_facts: through must be a top-level section at or after sectionId, the run at most %d tokens, with offset 0 or null", docs.MaxSectionTokens))
				}
				in["sectionText"], in["truncated"], in["next"] = text, false, 0
			} else if p, err := h.e.docs.Section(ctx, c.DocumentIDs, str("documentId"), str("sectionId"), int(off), sweepTokens); err == nil {
				in["sectionText"], in["truncated"], in["next"] = p.Text, p.Truncated, p.Next
				if p.Header != "" { // a later page of a table: its columns, by reference to the header row
					in["header"] = p.Header
				}
			}
		}
	case "analyze_issue":
		for _, p := range SelectPacks(str("jurisdiction"), "") {
			in["packText"] = str("packText") + "\n\n" + p.Body
		}
	case "check_deviation":
		var err error
		if rq, err = h.e.quotedRequirement(ctx, c); err != nil {
			return agent.Result{}, err
		}
		in["requirement"] = rq.Joined()
		in["requirementContext"] = rq.Context()                                // what it judges against: the requirement with what governs it
		var su agent.Usage                                                     // the passages' query embedding, metered (lab-r4 verifier L3)
		in["reviewedPassages"], su = h.e.reviewedPassages(ctx, c, rq.Joined()) // bench4, trust: every call spent its five turns searching and checking quotes
		pre := agent.NewBudget(c.Budget.Calls, max(0, c.Budget.USD-su.USD), 0) // the requirement's check comes out of the grant first (bench5: unfunded checks dropped real deviations)
		if rq.Check, rq.CheckWhy, err = h.e.checkSection(ctx, h.rt, pre, rq); err != nil {
			u := pre.Spent()
			u.Add(su)
			return agent.Result{Usage: u}, err
		}
		preUse = pre.Spent()
		preUse.Add(su)
		c.Budget = agent.Grant{Calls: max(0, c.Budget.Calls-preUse.Calls), USD: max(0, c.Budget.USD-preUse.USD)}
		if rq.Check != nil && rq.Check.Answer == "no" { // nothing to compare with: the document does not require it
			return agent.Result{Usage: preUse}, agent.Fail(fmt.Sprintf("check_deviation: an independent reading of %s around the requirement found that it does not impose the requirement as quoted (negated or overridden): %s. Quote the requirement together with what governs it (its lead-in, exception or override), as the document states what it requires, and call again",
				rq.DocName, fenceText.Replace(rq.Check.Reason)))
		}
	case "draft_section":
		var err error
		if draft, err = h.e.draftRequest(ctx, c.TaskID, c.DocumentIDs, str("issueId"), c.Input); err != nil {
			return agent.Result{}, err
		}
		in = draft.request
	}
	if r := readerReserve(ctx, h.rt); goTurns(name) > 0 && ctx.Value(readQueueKey{}) == nil && c.Budget.Calls > r.Calls && (r.USD == 0 || c.Budget.USD > r.USD) { // a sweep's visit reads nothing itself (readFindings) // the figure reader of its answer, held back from the subagent (lab-r4 r5 L4)
		held, c.Budget = r, agent.Grant{Calls: c.Budget.Calls - r.Calls, USD: c.Budget.USD - r.USD}
	}
	c.Input, _ = json.Marshal(in)
	res, err := h.Tool.Call(ctx, c)
	res.Usage.Add(preUse)
	if err == nil {
		err = awaitTurn(ctx) // a parallel sweep's visit admits in plan order
	}
	if err != nil {
		return res, err
	}
	b := agent.NewBudget(c.Budget.Calls-res.Usage.Calls+preUse.Calls+held.Calls, c.Budget.USD-res.Usage.USD+preUse.USD+held.USD, 0) // c.Budget is already net of preUse and held
	var out any
	var reward float64
	switch name {
	case "draft_section":
		out, err = h.e.saveSection(ctx, h.rt, c, b, draft, res.Output)
		reward = 1
	case "check_deviation":
		out, reward, err = h.e.deviations(ctx, h.rt, c, b, res, rq)
	default:
		var ans struct {
			Findings                      []draftFinding
			Gaps                          []string
			Rule, Application, Conclusion string
			Authorities                   []struct {
				Cite     string
				InRecord bool
			}
		}
		_ = json.Unmarshal(res.Output, &ans)
		notes := slices.Clone(ans.Gaps)
		for k, v := range map[string]string{"rule": ans.Rule, "application": ans.Application, "conclusion": ans.Conclusion} {
			if v != "" {
				notes = append(notes, k+": "+v)
			}
		}
		slices.Sort(notes)
		for _, a := range ans.Authorities {
			if !a.InRecord {
				notes = append(notes, "authority not in the record (flagged if cited): "+a.Cite)
			}
		}
		out, reward, err = h.e.admit(ctx, h.rt, c, b, held, name, str("issueId"), res.Model, ans.Findings, notes)
		if err == nil && name == "analyze_issue" && strings.TrimSpace(ans.Conclusion) != "" && str("issueId") != "" { // for the issue's drafter (cover.go)
			err = h.e.write(ctx, c.TaskID, kAnalysis, str("issueId"), Analysis{truncate(ans.Rule, 1500), truncate(ans.Application, 3000), truncate(ans.Conclusion, 1000)}, true)
		}
	}
	res.Usage.Add(b.Spent())
	if err != nil {
		return res, err
	}
	res.Output, _ = json.Marshal(out)
	res.Reward = &reward
	return res, nil
}

// draftFinding is a subagent's finding before Go has checked it.
type draftFinding struct {
	IssueID    *string // the plan issue it bears on (extract_facts without an issueId of its own)
	Claim      string
	Quotes     []analysis.Quote
	Values     []analysis.Value
	Entities   []analysis.Entity
	Relations  []analysis.Relation
	Confidence float64
}

type hookResult struct {
	Findings []map[string]any `json:"findings"`
	Dropped  int              `json:"dropped"`
	Notes    []string         `json:"notes"`
}

// verify runs the ladder on one quote: exact and tolerant in Go, then (while the grant has calls)
// the registered verify_quote tool with its paraphrase judge. A judge failure is an error (G6).
func (e *Engine) verify(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller string, q analysis.Quote) (Citation, error) {
	cit := Citation{DocumentID: q.DocumentID, Quote: q.Quote, Locator: q.Locator, Method: "none"}
	id, err := e.docs.Resolve(ctx, c.DocumentIDs, q.DocumentID)
	if err != nil {
		cit.Note = "the document is not one of this task's documents"
		return cit, nil
	}
	cit.DocumentID = id
	v, err := e.docs.VerifyQuote(ctx, c.DocumentIDs, id, q.Quote, nil)
	if err != nil {
		return cit, err
	}
	if calls, _ := b.Remaining(); !v.Verified && calls > 0 && rt != nil {
		in, _ := json.Marshal(map[string]any{"documentId": id, "quote": q.Quote, "claim": nil})
		res, err := rt.Invoke(ctx, agent.Agent{Name: caller, Visible: []string{"verify_quote"}}, agent.Call{TaskID: c.TaskID, DocumentIDs: c.DocumentIDs, Depth: 1}, "verify_quote", in, b, false, 0)
		var f *agent.Failure
		var ref *agent.Refusal
		switch {
		case errors.As(err, &f) || errors.As(err, &ref):
			cit.Note = "not verified: " + err.Error()
			return cit, nil
		case err != nil:
			return cit, err
		}
		var o struct {
			Verified                                bool
			Method, SectionID, Locator, Match, Note string
			Confidence                              float64
			LowConfidence                           bool
		}
		_ = json.Unmarshal(res.Output, &o)
		cit.Method, cit.Confidence, cit.SectionID, cit.Match, cit.Note, cit.Low = o.Method, o.Confidence, o.SectionID, o.Match, o.Note, o.LowConfidence
		if o.Locator != "" {
			cit.Locator = o.Locator
		}
		if !o.Verified {
			cit.Method = "none"
		}
		return cit, nil
	}
	if v.Verified {
		cit.Method, cit.Confidence, cit.SectionID, cit.Match, cit.Low = v.Method, v.Confidence, v.SectionID, v.Match, v.LowConfidence
		cit.Locator = cmpStr(v.Number != "", v.Number, cit.Locator)
	}
	cit.Note = v.Note
	return cit, nil
}

// admit checks a subagent's findings and records them (G2): every quote through the ladder (the
// finding verifies only when all of them do; otherwise it is quarantined), each value kept only
// when its own quote states it, each relation typed by BELO-lite, near-duplicates merged into the
// first. The reward is the share of quotes that verified.
func (e *Engine) admit(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, reserve agent.Grant, tool, issueID, model string, drafts []draftFinding, notes []string) (hookResult, float64, error) {
	out := hookResult{Findings: []map[string]any{}, Notes: append([]string{}, notes...)}
	all, err := load[Finding](ctx, e, c.TaskID, kFinding)
	if err != nil {
		return out, 0, err
	}
	var plan []Issue
	if tool == "extract_facts" && issueID == "" {
		if plan, err = load[Issue](ctx, e, c.TaskID, kIssue); err != nil {
			return out, 0, err
		}
	}
	scoped := len(plan) > 0
	if only, ok := ctx.Value(issuesKey{}).([]string); ok && scoped { // a page read again: the issues added since
		plan = slices.DeleteFunc(plan, func(is Issue) bool { return !slices.Contains(only, is.ID) })
	}
	quotes, ok, off := 0, 0, 0
	type pending struct {
		f    Finding
		d    draftFinding
		text string
		it   *reading
	}
	var pend []pending
	// the quotes' judges spend all but the figure reader's reserve (lab-r4 r5 L4: the reader is never
	// what they left), unless no finding states a number, when the reader makes no call (r7 L4)
	queue, deferred := ctx.Value(readQueueKey{}).(*readQueue) // a sweep reads its findings together at its end
	numbers := !deferred && slices.ContainsFunc(drafts, func(d draftFinding) bool {
		return len(d.Values) > 0 && len(findingReading(Finding{Finding: analysis.Finding{Claim: d.Claim, Entities: d.Entities}}, d.Quotes, d.Values).ps) > 0
	})
	vb := b
	if rc, ru := b.Remaining(); numbers && reserve.Calls > 0 && rc > reserve.Calls {
		if g, err := b.Grant(rc-reserve.Calls, max(0, ru-reserve.USD), agent.LevelWork); err == nil {
			vb = g
			defer g.Close()
		}
	}
	for _, d := range drafts {
		fi := issueID
		if scoped { // a fact on none of the plan's issues is not admitted
			if fi = deref(d.IssueID); !slices.ContainsFunc(plan, func(is Issue) bool { return is.ID == fi }) {
				off++
				continue
			}
		}
		f := Finding{Finding: analysis.Finding{IssueID: fi, Claim: strings.TrimSpace(d.Claim), Quotes: d.Quotes, Entities: d.Entities}, Severity: "medium", Arm: agent.Arm{Tool: tool, Model: model}}
		var text strings.Builder
		var vq []analysis.Quote
		for _, q := range d.Quotes {
			cit, err := e.verify(ctx, rt, c, vb, tool, q)
			if err != nil {
				return out, 0, err
			}
			f.Citations = append(f.Citations, cit)
			quotes++
			if cit.Method != "none" {
				ok++
				text.WriteString(q.Quote + "\n")
				vq = append(vq, analysis.Quote{DocumentID: cit.DocumentID, Quote: q.Quote, Locator: q.Locator})
			}
		}
		f.Verified = len(f.Citations) > 0 && !slices.ContainsFunc(f.Citations, func(c Citation) bool { return c.Method == "none" })
		pend = append(pend, pending{f, d, text.String(), findingReading(f, vq, d.Values)})
	}
	if vb != b {
		vb.Close() // its unspent part back to the call's grant, for the reader
	}
	var items []*reading // one figure reading for the call's findings (cached by content), out of the call's grant
	for _, p := range pend {
		if len(p.text) > 0 && !deferred && len(p.d.Values) > 0 { // a finding with no typed value is read when a section cites it
			items = append(items, p.it)
		}
	}
	if err := e.readFigures(ctx, rt, c, b, tool, items); err != nil {
		return out, 0, err
	}
	for _, p := range pend {
		f, d := p.f, p.d
		if len(p.text) > 0 && (deferred || len(d.Values) == 0) {
			f.Values, f.Unread = d.Values, true // read later: by the sweep at its end, or when a section cites it
		} else {
			applyReading(&f, d.Values, p.it, p.text)
		}
		f.Relations, _ = analysis.Relations(d.Entities, d.Relations)
		if i := slices.IndexFunc(all, func(x Finding) bool {
			return x.Verified && f.Verified && restated(x, f)
		}); i >= 0 {
			for _, c := range f.Citations {
				if !slices.ContainsFunc(all[i].Citations, func(x Citation) bool { return x.DocumentID == c.DocumentID && x.Quote == c.Quote }) {
					all[i].Citations = append(all[i].Citations, c)
					all[i].Quotes = append(all[i].Quotes, analysis.Quote{DocumentID: c.DocumentID, Quote: c.Quote, Locator: c.Locator})
				}
			}
			for _, n := range f.ValueNotes { // the duplicate's dropped values are the kept finding's (shown with it, disclosed)
				if !slices.Contains(all[i].ValueNotes, n) {
					all[i].ValueNotes = append(all[i].ValueNotes, n)
				}
			}
			if err := e.write(ctx, c.TaskID, kFinding, all[i].FID, all[i], true); err != nil {
				return out, 0, err
			}
			out.Notes = append(out.Notes, "merged into "+all[i].FID+" (an exact duplicate): "+f.Claim)
			for _, n := range f.ValueNotes {
				out.Notes = append(out.Notes, all[i].FID+": "+n)
			}
			continue
		}
		f.FID = nextID("F", len(all))
		all = append(all, f)
		if deferred && f.Unread && len(f.Values) > 0 {
			queue.add(f.FID)
		}
		if err := e.write(ctx, c.TaskID, kFinding, f.FID, f, false); err != nil {
			return out, 0, err
		}
		out.Findings = append(out.Findings, admitted(f))
		for _, n := range f.ValueNotes {
			out.Notes = append(out.Notes, f.FID+": "+n)
		}
		if !f.Verified {
			out.Dropped++
		}
	}
	if off > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("dropped %d %s that named none of the plan's issues", off, plural(off, "finding", "findings")))
	}
	if quotes == 0 {
		return out, map[bool]float64{true: 1, false: 0}[len(drafts) == 0], nil
	}
	return out, float64(ok) / float64(quotes), nil
}

// applyReading sets a finding's readings from it (the reader's labels of its verified quotes, its
// typed values and its claim; text is its verified quotes): Figures, what G8 traces a memo figure
// citing it to; each of vals kept only when its quotes state it (valueStated: a value its quotes do
// not state is dropped, never silently, lab-r4 r3), and its claim's sums.
func applyReading(f *Finding, vals []analysis.Value, it *reading, text string) {
	rs, refused := it.readings()
	for _, n := range refused {
		f.ValueNotes = append(f.ValueNotes, "figure reader: "+n)
	}
	f.Figures, f.Values, f.Unread = quoteReadings(rs), nil, false
	for j, v := range vals {
		fig, ok, low, why := valueStated(v, j, rs, text)
		if !ok {
			f.ValueNotes = append(f.ValueNotes, fmt.Sprintf("value %q (%s) not admitted: %s", v.Value, cmpStr(v.Metric != "", v.Metric, "no metric"), why))
			continue
		}
		v.Fig = fig
		if fig != nil && fig.Kind == analysis.KindMoney && v.Unit == "" && fig.Currency != "USD" && fig.Currency != "" {
			v.Unit = fig.Currency // the currency the reader read it in; "" is USD
		}
		if low { // admitted, gated for a lawyer: the reader could not read it with no doubt
			f.Low = true
			f.ValueNotes = append(f.ValueNotes, lowNote(v, fig))
		}
		f.Values = append(f.Values, v)
	}
	f.Values = append(f.Values, claimSums(f.Claim, rs, f.Values)...)
}

// verifiedText is a finding's verified quotes (applyReading's text) and the quotes the reader reads.
func verifiedText(f Finding) (string, []analysis.Quote) {
	var text strings.Builder
	var vq []analysis.Quote
	for _, c := range f.Citations {
		if c.Method != "none" {
			text.WriteString(c.Quote + "\n")
			vq = append(vq, analysis.Quote{DocumentID: c.DocumentID, Quote: c.Quote, Locator: c.Locator})
		}
	}
	return text.String(), vq
}

// restated reports whether f is an exact duplicate of the admitted x (bench5: the referral's
// figures restated page after page, and the lead's extract_facts re-extracting what the sweep had):
// one issue, the same quotes in the same documents (normalised: case and white space; identical
// lines of two invoices are two findings, lab-r4 r2 L4), and the same typed values (entity,
// metric and figure, each normalised). Nothing else merges: a near restatement (a reversed term, a
// mirrored party's clause, an amendment) stays a finding of its own, which coverage and the drafters
// can cite beside the first (lab-r4 verifier H2, M1).
func restated(x, f Finding) bool {
	key := func(vs []analysis.Value) []string {
		var out []string
		for _, v := range vs {
			c := strings.ToLower(norm(v.Value))
			if _, k, _, ok := analysis.Canon(v); ok {
				c = k
			}
			out = append(out, strings.ToLower(norm(v.Entity))+"|"+strings.ToLower(norm(v.Metric))+"|"+c)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	quotes := func(f Finding) []string {
		var out []string
		for _, q := range f.Quotes {
			out = append(out, q.DocumentID+"|"+strings.ToLower(norm(q.Quote)))
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return x.IssueID == f.IssueID && len(f.Quotes) > 0 && slices.Equal(quotes(x), quotes(f)) && slices.Equal(key(x.Values), key(f.Values)) &&
		!(salientFinding(f) && !salientFinding(x)) // a finding that says a breach is never folded into one that does not
}

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }
