// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine/figtest"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

func TestMain(m *testing.M) {
	docs.MaybeRunChild()
	leaseFor, beat = 2*time.Second, 200*time.Millisecond
	os.Exit(m.Run())
}

var testRoles = model.Roles{Lead: "lead-m", Specialist: "spec-m", Critic: "crit-m"}

// fixture is a test matter: an engine over a fresh schema, a scripted model and documents.
type fixture struct {
	t    *testing.T
	e    *Engine
	st   *store.Store
	d    *docs.Service
	docs []docs.Document
	// parties are the parties the figure reader's stand-in gives spans written so (figreader_test.go):
	// a test's "right labels" for whose figure a number is.
	parties map[string]string
	// reads are the figure reader's labels for spans, by "<passage kind>:<span text>" ("quote:4,000"):
	// a test's right labels where the stand-in would read otherwise (its Span is set by the stand-in).
	reads map[string]analysis.Label
	// measures are what the stand-in reads spans written so as measuring (its own reading is the
	// analyst's metric for a value's digits): a test's right labels for one figure typed two ways.
	measures map[string]string
	// judge decides the figure judge's stand-in's pairs where it answers (judge.go; "" leaves the
	// stand-in's own verdict): a test about the join's verdicts.
	judge func(a, b figtest.JudgeSide) string
}

const complaintText = `1. Parties
Greystone Packaging Co. sued Talbot Fresh Foods, Inc. on October 9, 2026.

2. Damages
Greystone claims damages of $644,910 for unpaid invoices.

3. Notice
The Notice of Dispute was delivered on September 25, 2026.
`

const docketText = `Docket
Talbot was served on 10/21/2026 via its registered agent. The answer period is 21 days.
`

func newFixture(t *testing.T, texts ...string) *fixture {
	t.Helper()
	dsn := storetest.DSN(t)
	st, err := store.Open(context.Background(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{t: t, st: st, d: docs.New(st)}
	if len(texts) == 0 {
		texts = []string{complaintText, docketText}
	}
	for i, x := range texts {
		d, err := f.d.IngestText(context.Background(), fmt.Sprintf("doc%d.md", i+1), x)
		if err != nil {
			t.Fatal(err)
		}
		f.docs = append(f.docs, d)
	}
	return f
}

func (f *fixture) ids() []string {
	var out []string
	for _, d := range f.docs {
		out = append(out, d.ID)
	}
	return out
}

// engine builds the engine over scripts with cfg (zero: 150 calls, $5, 0.5, strict).
func (f *fixture) engine(scripts map[string][]model.Reply, cfg Config) (*Engine, *model.Fake) {
	if cfg.BudgetCalls == 0 {
		cfg = Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}
	}
	fake := newFake(scripts)
	if fake.Respond != nil && (f.parties != nil || f.reads != nil || f.measures != nil) {
		fake.Respond = oracleWith(f.parties, f.reads, f.measures)
	}
	if f.judge != nil && fake.Respond != nil {
		read, judge := fake.Respond, figtest.Judge(f.judge)
		fake.Respond = func(req model.Request) (model.Reply, bool) {
			if r, ok := judge(req); ok {
				return r, ok
			}
			return read(req)
		}
	}
	f.e = New(f.st, f.d, fake, testRoles, nil, cfg, nil)
	return f.e, fake
}

func (f *fixture) submit(maxSteps int) Task {
	t, err := f.e.Submit(context.Background(), Task{Description: "Defense intake memo for the Greystone matter.", WorkflowType: "memo", Jurisdiction: "US",
		MatterNumber: "M-1", DocumentIDs: f.ids(), MaxSteps: maxSteps, ToolBandit: true})
	if err != nil {
		f.t.Fatal(err)
	}
	return t
}

// step claims the next runnable task and runs it to its next stop, synchronously.
func (f *fixture) step(ctx context.Context) {
	f.t.Helper()
	r, err := f.e.claim(ctx)
	if err != nil || r == nil {
		f.t.Fatalf("claim: %v, %v", r, err)
	}
	rctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	f.e.drive(rctx, r)
}

func (f *fixture) get(id string) TaskView {
	v, err := f.e.Get(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) steps(id string) string {
	ss, _ := f.e.Steps(context.Background(), id, 0)
	var b []string
	for _, s := range ss {
		b = append(b, fmt.Sprintf("%d:%s:%s:%s", s.Step, s.Kind, s.Tool, s.Status))
	}
	return strings.Join(b, " ")
}

// tc is a tool call with a JSON argument object.
func tc(name string, args any) model.ToolCall {
	b, _ := json.Marshal(args)
	return model.ToolCall{Function: model.Function{Name: name, Arguments: string(b)}}
}

func calls(cs ...model.ToolCall) model.Reply { return model.Reply{ToolCalls: cs} }

func text(v any) model.Reply {
	if s, ok := v.(string); ok {
		return model.Reply{Text: s}
	}
	b, _ := json.Marshal(v)
	return model.Reply{Text: string(b)}
}

type m = map[string]any

func plan(issues ...m) model.ToolCall {
	return tc("ledger", m{"op": "plan", "issues": issues, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": nil})
}

func issue(id, q, class string) m {
	return m{"id": id, "question": q, "class": class, "practice": "litigation"}
}

func ledgerOp(op string, kv ...any) model.ToolCall {
	a := m{"op": op, "issues": nil, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": nil}
	for i := 0; i < len(kv); i += 2 {
		a[kv[i].(string)] = kv[i+1]
	}
	return tc("ledger", a)
}

func extract(doc, query, issue string) model.ToolCall {
	return tc("extract_facts", m{"question": "What happened?", "documentId": doc, "sectionId": nil, "through": nil, "offset": nil, "query": query, "issueId": issue})
}

func draft(issue string, fids ...string) model.ToolCall {
	return tc("draft_section", m{"issueId": issue, "fids": fids, "revise": nil})
}

func committee() model.ToolCall { return tc("committee", m{}) }

func finishCall(op string, limits ...m) model.ToolCall {
	var l any
	if len(limits) > 0 {
		l = limits
	}
	return tc("finish", m{"op": op, "limitations": l})
}

// fd is an extract_facts finding.
func fd(claim, doc, quote string, values ...m) m {
	if values == nil {
		values = []m{}
	}
	return m{"issueId": nil, "claim": claim, "quotes": []m{{"documentId": doc, "quote": quote, "locator": nil}}, "values": values, "entities": []m{}, "relations": []m{}, "confidence": 0.9}
}

// on is f naming the plan issue it bears on (a sweep visit's finding names one; the call names none).
func on(issue string, f m) m {
	f["issueId"] = issue
	return f
}

func val(entity, metric, value, class string) m {
	return m{"entity": entity, "metric": metric, "value": value, "unit": nil, "date": nil, "class": class}
}

func facts(fs ...m) model.Reply { return text(m{"findings": append([]m{}, fs...), "gaps": []string{}}) }

func section(md, summary string) model.Reply { return text(m{"markdown": md, "summary": summary}) }

var (
	supported = text(m{"verdicts": []m{{"claim": "c", "verdict": "supported", "why": "the quote says it"}}})
	noAnswers = text(m{"answers": []m{}})
	noGaps    = text(m{"gaps": []m{}})
	metaNote  = model.Reply{Text: "One issue was checked; review the damages figure first."}
)

// happy is the minimal memo run: plan, one finding, one section, a committee, finish.
func (f *fixture) happy() map[string][]model.Reply {
	d1 := f.docs[0].ID
	return map[string][]model.Reply{
		"lead": {
			calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))),
			calls(extract(d1, "damages", "I1")),
			calls(draft("I1", "F1")),
			calls(committee()),
			calls(finishCall("submit")),
		},
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices",
			val("Greystone", "damages", "$644,910", "Money")))},
		"draft_section": {section("Greystone claims damages of {Zephyr} for unpaid invoices [F1].", "Damages {Zephyr} [F1].")},
		"prosecute":     {supported},
		"reproduce":     {noAnswers},
		"audit_gaps":    {noGaps},
		"meta":          {metaNote},
		"section_check": checks("yes", 24),
	}
}

// checks is n scripted section-check answers (deviation.go checkSection).
func checks(answer string, n int) []model.Reply {
	var out []model.Reply
	for range n {
		out = append(out, text(m{"answer": answer, "reason": "the section's words impose it as stated"}))
	}
	return out
}

// with returns the happy scripts with lead (and any other agents) replaced.
func (f *fixture) with(over map[string][]model.Reply) map[string][]model.Reply {
	s := f.happy()
	for k, v := range over {
		s[k] = v
	}
	return s
}

// outputs lists the outputs of a task's steps of tool, in order.
func (f *fixture) outputs(id, tool string) []string {
	ss, _ := f.e.Steps(context.Background(), id, 0)
	var out []string
	for _, s := range ss {
		if s.Tool == tool && s.Kind == "tool_call" {
			var b bytes.Buffer
			_ = json.Compact(&b, s.Output)
			out = append(out, s.Status+" "+b.String()+" "+s.Summary)
		}
	}
	return out
}

// run submits a task over scripts and runs it to its first stop.
func (f *fixture) run(scripts map[string][]model.Reply, cfg Config) TaskView {
	f.engine(scripts, cfg)
	task := f.submit(0)
	f.step(context.Background())
	return f.get(task.ID)
}

func stop(v TaskView) string {
	if v.Run.StopReason == nil {
		return v.Status
	}
	return v.Status + "/" + *v.Run.StopReason
}

type docsEntry = docs.OutlineEntry

func jsonDecode(s string, v any) { _ = json.Unmarshal([]byte(strings.TrimPrefix(s, "Request:\n")), v) }
