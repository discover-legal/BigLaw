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
	an := analysis.Tools(e)
	tools = append(tools, sheets{an[0], e}, an[1], e.ledgerTool(), e.sweepTool(rt, r), e.committeeTool(rt, r), e.finishTool(rt, r))
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
		out, _ := json.Marshal(map[string]any{"wid": nil, "result": nil, "unit": nil, "worksheet": nil, "literalOperands": nil, "tableMarkdown": nil,
			"handles": append([]analysis.Handle{}, hs[:min(len(hs), cmp.Or(in.SalientTop, len(hs)))]...)})
		return agent.Result{Output: out}, nil
	}
	res, err := s.Tool.Call(ctx, c)
	var w analysis.Worksheet
	if err == nil && json.Unmarshal(res.Output, &w) == nil && w.WID != "" {
		err = s.e.write(ctx, c.TaskID, kSheet, w.WID, w, false)
	}
	return res, err
}

// hooked is a subagent behind Go: its request is completed (a section's text, a drafter's
// findings and handles) and its answer is checked (every quote through the ladder) before
// anything reaches the ledger (G2) or the caller.
type hooked struct {
	agent.Tool
	e  *Engine
	rt *agent.Runtime
}

func (h hooked) Spec() agent.Spec {
	s := h.Tool.Spec()
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
	switch name {
	case "extract_facts":
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
	case "draft_section":
		var err error
		if draft, err = h.e.draftRequest(ctx, c.TaskID, c.DocumentIDs, str("issueId"), c.Input); err != nil {
			return agent.Result{}, err
		}
		in = draft.request
	}
	c.Input, _ = json.Marshal(in)
	res, err := h.Tool.Call(ctx, c)
	if err == nil {
		err = awaitTurn(ctx) // a parallel sweep's visit admits in plan order
	}
	if err != nil {
		return res, err
	}
	b := agent.NewBudget(c.Budget.Calls-res.Usage.Calls, c.Budget.USD-res.Usage.USD, 0)
	var out any
	var reward float64
	switch name {
	case "draft_section":
		out, err = h.e.saveSection(ctx, c.TaskID, draft, res.Output)
		reward = 1
	case "check_deviation":
		out, reward, err = h.e.deviations(ctx, h.rt, c, b, res)
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
		out, reward, err = h.e.admit(ctx, h.rt, c, b, name, str("issueId"), res.Model, ans.Findings, notes)
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

var currencyIn = [][2]string{{"£", "GBP"}, {"€", "EUR"}, {"¥", "JPY"}, {"CHF", "CHF"}, {"C$", "CAD"}, {"A$", "AUD"}}

// currencyOf is a money value's currency as its quote shows it: a symbol in the value, else just
// before or after the value where the quote states it; "" (USD) when the quote shows none.
func currencyOf(value, quote string) string {
	at := strings.Index(quote, value)
	for _, c := range currencyIn {
		if strings.Contains(value, c[0]) || at >= 0 && strings.Contains(quote[max(0, at-4):min(len(quote), at+len(value)+4)], c[0]) {
			return c[1]
		}
	}
	return ""
}

// admit checks a subagent's findings and records them (G2): every quote through the ladder (the
// finding verifies only when all of them do; otherwise it is quarantined), each value kept only
// when its own quote states it, each relation typed by BELO-lite, near-duplicates merged into the
// first. The reward is the share of quotes that verified.
func (e *Engine) admit(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, tool, issueID, model string, drafts []draftFinding, notes []string) (hookResult, float64, error) {
	out := hookResult{Findings: []map[string]any{}, Notes: append([]string{}, notes...)}
	all, err := load[Finding](ctx, e, c.TaskID, kFinding)
	if err != nil {
		return out, 0, err
	}
	quotes, ok := 0, 0
	for _, d := range drafts {
		f := Finding{Finding: analysis.Finding{IssueID: issueID, Claim: strings.TrimSpace(d.Claim), Quotes: d.Quotes, Entities: d.Entities}, Severity: "medium", Arm: agent.Arm{Tool: tool, Model: model}}
		var text strings.Builder
		for _, q := range d.Quotes {
			cit, err := e.verify(ctx, rt, c, b, tool, q)
			if err != nil {
				return out, 0, err
			}
			f.Citations = append(f.Citations, cit)
			quotes++
			if cit.Method != "none" {
				ok++
				text.WriteString(q.Quote + "\n")
			}
		}
		f.Verified = len(f.Citations) > 0 && !slices.ContainsFunc(f.Citations, func(c Citation) bool { return c.Method == "none" })
		for _, v := range d.Values {
			if inQuote(v.Value, text.String()) {
				if analysis.ClassOf(v) == "Money" && v.Unit == "" {
					v.Unit = currencyOf(v.Value, text.String())
				}
				f.Values = append(f.Values, v)
			}
		}
		f.Relations, _ = analysis.Relations(d.Entities, d.Relations)
		if i := slices.IndexFunc(all, func(x Finding) bool {
			return x.Verified && f.Verified && x.IssueID == f.IssueID && nearDuplicate(x.Claim, f.Claim)
		}); i >= 0 {
			for _, c := range f.Citations {
				if !slices.ContainsFunc(all[i].Citations, func(x Citation) bool { return x.DocumentID == c.DocumentID && x.Quote == c.Quote }) {
					all[i].Citations = append(all[i].Citations, c)
					all[i].Quotes = append(all[i].Quotes, analysis.Quote{DocumentID: c.DocumentID, Quote: c.Quote, Locator: c.Locator})
				}
			}
			if err := e.write(ctx, c.TaskID, kFinding, all[i].FID, all[i], true); err != nil {
				return out, 0, err
			}
			out.Notes = append(out.Notes, "merged into "+all[i].FID+" (a near-duplicate): "+f.Claim)
			continue
		}
		f.FID = nextID("F", len(all))
		all = append(all, f)
		if err := e.write(ctx, c.TaskID, kFinding, f.FID, f, false); err != nil {
			return out, 0, err
		}
		out.Findings = append(out.Findings, admitted(f))
		if !f.Verified {
			out.Dropped++
		}
	}
	if quotes == 0 {
		return out, map[bool]float64{true: 1, false: 0}[len(drafts) == 0], nil
	}
	return out, float64(ok) / float64(quotes), nil
}

// inQuote reports whether a value is stated in the quotes: as text, or as the same number or date.
func inQuote(v, quotes string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if strings.Contains(strings.ToLower(norm(quotes)), strings.ToLower(norm(v))) {
		return true
	}
	return covered(v, quotes)
}

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }
