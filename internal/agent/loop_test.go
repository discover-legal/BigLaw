// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/discover-legal/BigLaw/internal/model"
)

func toolNames(ts []model.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func lastContent(r model.Request) string {
	c, _ := r.Messages[len(r.Messages)-1].Content.(string)
	return c
}

func byAgent(f *model.Fake, agent string) []model.Request {
	var out []model.Request
	for _, r := range f.Calls() {
		if r.Agent == agent {
			out = append(out, r)
		}
	}
	return out
}

const q = `{"q":"x","op":null}`

// G9 trap: a scripted subagent calls finish → refused and recorded in its transcript; it sees only
// its allowed tools, and finish never runs on its behalf.
func TestSubagentSeesOnlyAllowedTools(t *testing.T) {
	retrieve, fin := &counter{s: goSpec("retrieve")}, newFinish()
	rt, f := rig(t, map[string][]model.Reply{
		"lead":          {calls(call("extract_facts", q)), calls(call("finish", q))},
		"extract_facts": {calls(call("finish", q)), calls(call("retrieve", q)), {Text: `{"ok":true}`}},
	}, retrieve, fin, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "Extract."})
	res, err := rt.Run(context.Background(), lead("retrieve", "extract_facts", "finish"), Call{Input: json.RawMessage(`"Do it."`)}, NewBudget(50, 10, 0))
	if err != nil || !res.Final {
		t.Fatalf("run: %v %+v", err, res)
	}
	sub := byAgent(f, "extract_facts")
	for _, r := range sub {
		if got := toolNames(r.Tools); len(got) != 1 || got[0] != "retrieve" {
			t.Fatalf("the subagent was offered %v, want only [retrieve]", got)
		}
	}
	if c := lastContent(sub[1]); !strings.Contains(c, "refused: finish is not one of your tools") || !strings.Contains(c, "Next: call one of: retrieve") {
		t.Fatalf("the subagent's finish was not refused with a reason and a next step: %q", c)
	}
	if fin.runs != 1 || retrieve.runs != 1 {
		t.Fatalf("finish ran %d times (want 1: the lead's), retrieve %d", fin.runs, retrieve.runs)
	}
}

// P3: no nesting, by registration and at run time.
func TestSubagentNoNesting(t *testing.T) {
	rt, _ := rig(t, nil, &counter{s: goSpec("retrieve")}, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	if err := rt.Reg.Register(&Subagent{S: subSpec("analyze_issue", "extract_facts"), Prompt: "x", RT: rt}); err == nil || !strings.Contains(err.Error(), "subagents cannot call subagents") {
		t.Fatalf("a subagent allowed to call a subagent was registered: %v", err)
	}
	fan := goSpec("sweep")
	fan.Kind = "fanout"
	if err := rt.Reg.Register(&counter{s: fan}); err != nil {
		t.Fatal(err)
	}
	sub := Agent{Name: "extract_facts", Visible: []string{"extract_facts", "sweep"}}
	for _, name := range []string{"extract_facts", "sweep"} {
		_, err := rt.Invoke(context.Background(), sub, Call{Depth: 1}, name, json.RawMessage(q), NewBudget(10, 1, 0), false, 0)
		if !errors.Is(err, ErrNesting) {
			t.Fatalf("depth-1 call of %s: %v, want ErrNesting", name, err)
		}
	}
	if _, err := (&Subagent{S: subSpec("x"), RT: rt}).Call(context.Background(), Call{Depth: 1}); !errors.Is(err, ErrNesting) {
		t.Fatalf("a subagent called at depth 1: %v", err)
	}
}

func TestSubagentTurnCap(t *testing.T) {
	retrieve := &counter{s: goSpec("retrieve")}
	s := subSpec("extract_facts", "retrieve")
	s.MaxTurns = 2
	rt, f := rig(t, map[string][]model.Reply{
		"lead":          {calls(call("extract_facts", q))},
		"extract_facts": {calls(call("retrieve", q)), calls(call("retrieve", q)), {Text: `{"ok":true}`}},
	}, retrieve, &Subagent{S: s, Prompt: "x"})
	j := &journal{}
	rt.Journal = j
	l := lead("extract_facts")
	l.MaxTurns = 1
	_, err := rt.Run(context.Background(), l, Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0))
	if !errors.Is(err, ErrTurnCap) {
		t.Fatalf("lead: %v", err)
	}
	if n := len(byAgent(f, "extract_facts")); n != 2 {
		t.Fatalf("the subagent made %d model calls, want its cap of 2", n)
	}
	// Its last turn cannot use a tool's result, so it is asked for the answer without tools.
	if reqs := byAgent(f, "extract_facts"); reqs[0].ToolChoice != "auto" || reqs[1].ToolChoice != "none" || !strings.Contains(lastContent(reqs[1]), "last turn") {
		t.Fatalf("tool choice %q then %q; last turn's note %q", reqs[0].ToolChoice, reqs[1].ToolChoice, lastContent(reqs[1]))
	}
	steps := j.final()
	if st := steps[len(steps)-1]; st.Tool != "extract_facts" || st.Status != "failed" || !strings.Contains(st.Note, "turn cap") {
		t.Fatalf("the capped subagent's step: %+v", st)
	}
}

func usage(calls int, usd float64) model.Usage {
	return model.Usage{InputTokens: 100 * calls, OutputTokens: 10 * calls, CostUSD: &usd}
}

func TestNestedBudgetChargedToTask(t *testing.T) {
	rt, _ := rig(t, map[string][]model.Reply{
		"lead":          {{ToolCalls: []model.ToolCall{call("extract_facts", q)}, Usage: usage(1, 0.01)}, {ToolCalls: []model.ToolCall{call("finish", q)}, Usage: usage(1, 0.01)}},
		"extract_facts": {{ToolCalls: []model.ToolCall{call("retrieve", q)}, Usage: usage(1, 0.1)}, {Text: `{"ok":true}`, Usage: usage(1, 0.2)}},
	}, &counter{s: goSpec("retrieve"), use: Usage{InputTokens: 7, USD: 0.001}}, newFinish(), &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	b := NewBudget(50, 10, 0)
	if _, err := rt.Run(context.Background(), lead("extract_facts", "finish"), Call{Input: json.RawMessage(`"go"`)}, b); err != nil {
		t.Fatal(err)
	}
	got := b.Spent()
	if got.Calls != 4 || got.InputTokens != 407 || got.OutputTokens != 40 || fmt.Sprintf("%.3f", got.USD) != "0.321" || got.Unpriced != 0 {
		t.Fatalf("task ledger %+v: want 4 calls (2 lead, 2 nested), 407 in, 40 out, $0.321", got)
	}
	if c, _ := b.Remaining(); c != 46 {
		t.Fatalf("remaining %d calls, want 46: grants released", c)
	}
}

// P5: below the reserve only a Final tool runs.
func TestLeadToolRefusedBelowReserve(t *testing.T) {
	ext := &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"}
	fin := newFinish()
	rt, f := rig(t, map[string][]model.Reply{"lead": {calls(call("extract_facts", q)), calls(call("finish", q))}}, &counter{s: goSpec("retrieve")}, fin, ext)
	b, j := NewBudget(9, 10, 8), &journal{} // the first lead turn leaves 8: all reserve
	rt.Journal = j
	_, err := rt.Run(context.Background(), lead("extract_facts", "finish"), Call{Input: json.RawMessage(`"go"`)}, b)
	if !errors.Is(err, ErrBudget) || len(byAgent(f, "extract_facts")) != 0 || len(f.Calls()) != 1 {
		t.Fatalf("run %v; the subagent ran %d times; %d lead turns (the second cannot be funded above the reserve)", err, len(byAgent(f, "extract_facts")), len(f.Calls()))
	}
	if st := j.final()[1]; st.Status != "refused" || !strings.Contains(st.Note, "Next: call finish") {
		t.Fatalf("refusal: %+v", st)
	}
	res, err := rt.Invoke(context.Background(), lead("finish"), Call{}, "finish", json.RawMessage(q), b, false, 0)
	if err != nil || !res.Final || fin.runs != 1 || fin.got[0].Budget.Calls != 3 {
		t.Fatalf("finish below the reserve: %v %+v (grant %+v)", err, res, fin.got)
	}
}

func TestTranscriptCompactionKeepsHandles(t *testing.T) {
	var script []model.Reply
	for i := 0; i < 6; i++ {
		script = append(script, calls(call("echo", fmt.Sprintf(`{"q":"turn %d","op":null}`, i+1))))
	}
	script = append(script, calls(call("finish", q)))
	echo := &counter{s: goSpec("echo"), out: json.RawMessage(`{"ok":true}`)}
	rt, f := rig(t, map[string][]model.Reply{"lead": script}, echo, newFinish())
	if _, err := rt.Run(context.Background(), lead("echo", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatal(err)
	}
	last := f.Calls()[6] // the 7th turn's request: results of turns 1–2 are handles, 3–6 in full
	var tools []string
	for _, m := range last.Messages {
		if m.Role == "tool" {
			tools = append(tools, m.Content.(string))
		}
	}
	for i, c := range tools {
		compacted := strings.HasPrefix(c, fmt.Sprintf("[step %d: echo result compacted", 2*i+2))
		if compacted != (i < 2) || i >= 2 && !strings.Contains(c, `{"ok":true}`) {
			t.Fatalf("tool result %d: %q (want turns 1–2 as handles naming their step)", i+1, c)
		}
	}
}

// One repair retry, then a typed error; nothing of the bad answer reaches the caller.
func TestToolOutputSchemaEnforced(t *testing.T) {
	rt, f := rig(t, map[string][]model.Reply{
		"lead":          {calls(call("extract_facts", q)), calls(call("extract_facts", q)), calls(call("finish", q))},
		"extract_facts": {{Text: "not json"}, {Text: `{"ok":"yes"}`}, {Text: "nope"}, {Text: "```json\n{\"ok\":true}\n```"}},
	}, &counter{s: goSpec("retrieve")}, newFinish(), &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	j := &journal{}
	rt.Journal = j
	if _, err := rt.Run(context.Background(), lead("extract_facts", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if c := lastContent(byAgent(f, "extract_facts")[1]); !strings.Contains(c, "does not match the output schema") {
		t.Fatalf("no repair request: %q", c)
	}
	var tc []Step
	for _, s := range j.final() {
		if s.Kind == "tool_call" {
			tc = append(tc, s)
		}
	}
	if tc[0].Status != "failed" || !strings.Contains(tc[0].Note, "tool output does not match its schema") || tc[0].Output != nil {
		t.Fatalf("first call: %+v (want failed, no output)", tc[0])
	}
	if tc[1].Status != "committed" || string(tc[1].Output) != `{"ok":true}` {
		t.Fatalf("repaired call: %+v", tc[1])
	}
}

func TestParallelToolCallsEachAStep(t *testing.T) {
	ledger := goSpec("ledger")
	ledger.Solo = []string{"plan"}
	echo, led, fin := &counter{s: goSpec("echo")}, &counter{s: ledger}, newFinish()
	plan, findings := `{"q":null,"op":"plan"}`, `{"q":null,"op":"findings"}`
	rt, _ := rig(t, map[string][]model.Reply{"lead": {
		calls(call("echo", q), call("echo", q), call("echo", q)),
		calls(call("echo", q), call("finish", q)),
		calls(call("ledger", plan), call("echo", q)),
		calls(call("ledger", findings), call("echo", q)),
		calls(call("finish", q)),
	}}, echo, led, fin)
	j := &journal{}
	rt.Journal = j
	if _, err := rt.Run(context.Background(), lead("echo", "ledger", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range j.final() {
		got = append(got, fmt.Sprintf("%d:%s:%s:%s", s.N, s.Kind, s.Tool, s.Status))
	}
	want := "1:lead_turn:lead:committed 2:tool_call:echo:committed 3:tool_call:echo:committed 4:tool_call:echo:committed " +
		"5:lead_turn:lead:committed 6:tool_call:echo:committed 7:tool_call:finish:refused 8:lead_turn:lead:committed 9:tool_call:ledger:refused " +
		"10:tool_call:echo:committed 11:lead_turn:lead:committed 12:tool_call:ledger:committed 13:tool_call:echo:committed 14:lead_turn:lead:committed 15:tool_call:finish:committed"
	if strings.Join(got, " ") != want {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(got, " "), want)
	}
	if echo.runs != 6 || led.runs != 1 || fin.runs != 1 {
		t.Fatalf("runs: echo %d ledger %d finish %d", echo.runs, led.runs, fin.runs)
	}
}

type deny struct{}

func (deny) Allow(context.Context, Pending) *Refusal {
	return &Refusal{Kind: ErrPolicy, Reason: "the plan comes first", Next: "record a plan with ledger op plan"}
}

// Every refusal names its reason and the next step.
func TestRefusalsAreActionable(t *testing.T) {
	ledger := goSpec("ledger")
	ledger.Solo = []string{"plan"}
	big := subSpec("extract_facts", "retrieve")
	rt, _ := rig(t, nil, &counter{s: goSpec("retrieve")}, &counter{s: ledger}, &Subagent{S: big, Prompt: "x"})
	ctx, l := context.Background(), lead("retrieve", "ledger", "extract_facts")
	cases := map[error]func() error{
		ErrNotOffered: func() error {
			_, err := rt.Invoke(ctx, lead("retrieve"), Call{}, "ledger", json.RawMessage(q), NewBudget(9, 1, 0), false, 0)
			return err
		},
		ErrNesting: func() error {
			_, err := rt.Invoke(ctx, l, Call{Depth: 1}, "extract_facts", json.RawMessage(q), NewBudget(9, 1, 0), false, 0)
			return err
		},
		ErrAlone: func() error {
			_, err := rt.Invoke(ctx, l, Call{}, "ledger", json.RawMessage(`{"q":null,"op":"plan"}`), NewBudget(9, 1, 0), true, 0)
			return err
		},
		ErrToolInput: func() error {
			_, err := rt.Invoke(ctx, l, Call{}, "retrieve", json.RawMessage(`{"q":1}`), NewBudget(9, 1, 0), false, 0)
			return err
		},
		ErrBudget: func() error {
			_, err := rt.Invoke(ctx, l, Call{}, "extract_facts", json.RawMessage(q), NewBudget(8, 1, 8), false, 0)
			return err
		},
		ErrPolicy: func() error {
			rt.Policy = deny{}
			defer func() { rt.Policy = nil }()
			_, err := rt.Invoke(ctx, l, Call{}, "retrieve", json.RawMessage(q), NewBudget(9, 1, 0), false, 0)
			return err
		},
	}
	for kind, run := range cases {
		err := run()
		var r *Refusal
		if !errors.As(err, &r) || !errors.Is(err, kind) || len(strings.TrimSpace(r.Reason)) < 10 || len(strings.TrimSpace(r.Next)) < 10 ||
			!strings.HasPrefix(err.Error(), "refused: ") || !strings.Contains(err.Error(), ". Next: ") {
			t.Errorf("%v: refusal %v is not actionable (reason and next step)", kind, err)
		}
	}
}

func TestSchemaValidatorRejectsBadInput(t *testing.T) {
	tool := &counter{s: goSpec("retrieve")}
	rt, _ := rig(t, nil, tool)
	for _, in := range []string{`{"q":1,"op":null}`, `{"q":"x","op":null,"extra":1}`, `{"q":"x"}`, `[]`, `{"q":"x","op":null} {}`} {
		_, err := rt.Invoke(context.Background(), lead("retrieve"), Call{}, "retrieve", json.RawMessage(in), NewBudget(9, 1, 0), false, 0)
		if !errors.Is(err, ErrToolInput) {
			t.Errorf("%s: %v, want ErrToolInput", in, err)
		}
	}
	if tool.runs != 0 {
		t.Fatalf("the tool body ran %d times on bad input", tool.runs)
	}
}

// A context-length refusal compacts every tool result but the latest turn's and retries once; the
// lead keeps seeing its newest results afterwards (r1: the retry compacted the latest turn too, for
// the rest of the run, so the lead could never read a result again).
func TestContextLengthRefusalCompacts(t *testing.T) {
	var script []model.Reply
	for i := 0; i < 5; i++ {
		script = append(script, calls(call("echo", q)))
	}
	script = append(script, model.Reply{Err: &model.Error{Kind: model.ErrUnavailable, Detail: "HTTP 400", Status: 400}}, calls(call("echo", q)), calls(call("finish", q)))
	rt, f := rig(t, map[string][]model.Reply{"lead": script}, &counter{s: goSpec("echo")}, newFinish())
	if _, err := rt.Run(context.Background(), lead("echo", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatalf("a context-length refusal failed the run: %v", err)
	}
	for _, i := range []int{6, 7} { // the retry, and the turn after it
		var tools []string
		for _, m := range f.Calls()[i].Messages {
			if m.Role == "tool" {
				tools = append(tools, m.Content.(string))
			}
		}
		for k, c := range tools {
			if full := !strings.HasPrefix(c, "[step "); full != (k == len(tools)-1) {
				t.Fatalf("request %d, tool result %d of %d: %q (want only the latest turn's in full)", i+1, k+1, len(tools), c)
			}
		}
	}
	// An outage is not a refusal: no retry, the run fails with the typed error.
	rt, _ = rig(t, map[string][]model.Reply{"lead": {{Err: &model.Error{Kind: model.ErrUnavailable, Detail: "HTTP 502", Status: 502}}, calls(call("finish", q))}}, newFinish())
	if _, err := rt.Run(context.Background(), lead("finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); !errors.Is(err, model.ErrUnavailable) {
		t.Fatalf("an outage: %v", err)
	}
}

// A subagent whose grant runs out mid-run reports it actionably, never as "internal error".
func TestSubagentOutOfBudgetIsActionable(t *testing.T) {
	rt, _ := rig(t, map[string][]model.Reply{
		"lead":          {calls(call("extract_facts", q))},
		"extract_facts": {calls(call("retrieve", q)), calls(call("retrieve", q)), {Text: `{"ok":true}`}},
	}, &counter{s: goSpec("retrieve")}, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	j := &journal{}
	rt.Journal = j
	_, err := rt.Run(context.Background(), lead("extract_facts"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(3, 1, 0))
	steps := j.final()
	if st := steps[1]; !errors.Is(err, ErrBudget) || st.Tool != "extract_facts" || !strings.Contains(st.Note, "budget") || strings.Contains(st.Note, "internal error") {
		t.Fatalf("run %v; the subagent's step: %+v", err, st)
	}
}

func TestToolCallNameMustBeOffered(t *testing.T) {
	secret := &counter{s: goSpec("secret")}
	rt, f := rig(t, map[string][]model.Reply{"lead": {calls(call("secret", q), call("nosuch", q)), calls(call("finish", q))}}, secret, newFinish())
	if _, err := rt.Run(context.Background(), lead("finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if secret.runs != 0 {
		t.Fatal("a registered tool not offered to the lead was dispatched")
	}
	if c := lastContent(f.Calls()[1]); !strings.Contains(c, "nosuch is not one of your tools") {
		t.Fatalf("unknown tool: %q", c)
	}
}

func TestBilledFailureCharged(t *testing.T) {
	cost := 0.02
	billed := &model.Error{Kind: model.ErrNoContent, Detail: "refusal", Usage: &model.Usage{InputTokens: 100, OutputTokens: 5, CostUSD: &cost}}
	rt, _ := rig(t, map[string][]model.Reply{"lead": {{Err: billed}}})
	b := NewBudget(50, 1, 0)
	if _, err := rt.Run(context.Background(), lead(), Call{Input: json.RawMessage(`"go"`)}, b); !errors.Is(err, model.ErrNoContent) {
		t.Fatalf("run: %v", err)
	}
	if s := b.Spent(); s.Calls != 1 || s.USD != 0.02 || s.InputTokens != 100 {
		t.Fatalf("billed failure not charged: %+v", s)
	}
	// A 200 whose usage cannot be read is a call of unknown cost, not a free one.
	if u := ModelUsage(&model.Error{Kind: model.ErrUnavailable, Status: 200}, nil); u.Calls != 1 || u.Unpriced != 1 {
		t.Fatalf("unreadable 200: %+v", u)
	}
}

func TestToolDefinitionsStaticAcrossTurns(t *testing.T) {
	rt, f := rig(t, map[string][]model.Reply{"lead": {calls(call("echo", q)), calls(call("echo", q)), calls(call("echo", q)), calls(call("finish", q))}},
		&counter{s: goSpec("echo")}, newFinish())
	rt.Bandit = &Bandit{Stats: &MemStats{}, Sig: "memo", BudgetCalls: 50}
	if _, err := rt.Run(context.Background(), lead("echo", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(f.Calls()[0].Tools)
	for i, r := range f.Calls() {
		got, _ := json.Marshal(r.Tools)
		if string(got) != string(first) || r.Messages[0].Content != "You are the lead." {
			t.Fatalf("turn %d: the tool definitions or system prompt changed", i+1)
		}
		for j, m := range r.Messages[:len(r.Messages)-1] { // a hint, once written, is never edited away
			if prev := f.Calls()[max(i-1, 0)]; i > 0 && j < len(prev.Messages) && fmt.Sprint(prev.Messages[j].Content) != fmt.Sprint(m.Content) {
				t.Fatalf("turn %d rewrote message %d (the cached prefix)", i+1, j)
			}
		}
	}
	if !strings.Contains(lastContent(f.Calls()[1]), "Track record for this kind of task") {
		t.Fatal("no hint on the latest tool result")
	}
}

func TestBanditAblationOffSendsNoHint(t *testing.T) {
	for _, off := range []bool{true, false} {
		rt, f := rig(t, map[string][]model.Reply{"lead": {calls(call("echo", q)), calls(call("finish", q))}}, &counter{s: goSpec("echo")}, newFinish())
		rt.Bandit = &Bandit{Stats: &MemStats{}, Sig: "memo", BudgetCalls: 50, Off: off}
		if _, err := rt.Run(context.Background(), lead("echo", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(f.Calls())
		if strings.Contains(string(raw), "Track record") == off {
			t.Fatalf("toolBandit off=%v: hint present=%v", off, !off)
		}
	}
}

func TestBanditUpdatedFromEveryToolResult(t *testing.T) {
	broken := &counter{s: goSpec("broken"), err: Fail("it broke")}
	rt, _ := rig(t, map[string][]model.Reply{"lead": {calls(call("echo", q), call("echo", `{"bad":1}`), call("broken", q)), calls(call("finish", q))}},
		&counter{s: goSpec("echo")}, broken, newFinish())
	st := &MemStats{}
	rt.Bandit = &Bandit{Stats: st, Sig: "sig", BudgetCalls: 50}
	if _, err := rt.Run(context.Background(), lead("echo", "broken", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.Load(context.Background(), "sig")
	if e := rows[Arm{Tool: "echo"}]; e.N != 2 || e.Reward != 1 || e.Fails != 1 {
		t.Fatalf("echo: %+v (want 2 calls: one success, one refused scoring 0)", e)
	}
	if b := rows[Arm{Tool: "broken"}]; b.N != 1 || b.Reward != 0 || b.Fails != 1 {
		t.Fatalf("broken: %+v", b)
	}
	if fin := rows[Arm{Tool: "finish"}]; fin.N != 1 || fin.Reward != 1 {
		t.Fatalf("finish: %+v", fin)
	}
}

func TestLoadSubagents(t *testing.T) {
	spec, _ := json.Marshal(subSpec("extract_facts", "retrieve"))
	rt, _ := rig(t, nil, &counter{s: goSpec("retrieve")})
	tools, err := rt.LoadSubagents(fstest.MapFS{"extract_facts.json": {Data: spec}, "extract_facts.md": {Data: []byte("You extract facts.")}})
	if err != nil || len(tools) != 1 || rt.Reg.Register(tools[0]) != nil {
		t.Fatalf("load: %v", err)
	}
	// A real model sees only the prompt: it must carry the output schema, or the model omits required fields.
	var out bytes.Buffer
	_ = json.Compact(&out, subSpec("extract_facts", "retrieve").Out)
	if p := tools[0].(*Subagent).Prompt; !strings.HasPrefix(p, "You extract facts.") || !strings.Contains(p, out.String()) {
		t.Fatalf("prompt lacks the output schema: %q", p)
	}
	for name, fs := range map[string]fstest.MapFS{
		"no prompt":     {"extract_facts.json": {Data: spec}},
		"name mismatch": {"other.json": {Data: spec}, "other.md": {Data: []byte("x")}},
		"unknown field": {"extract_facts.json": {Data: []byte(`{"name":"extract_facts","kind":"subagent","bogus":1}`)}, "extract_facts.md": {Data: []byte("x")}},
	} {
		if _, err := rt.LoadSubagents(fs); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

// A hostile or broken answer with many tool calls runs at most maxCallsInTurn of them, and says so.
func TestToolCallsPerTurnCapped(t *testing.T) {
	var many []model.ToolCall
	for i := 0; i < 50; i++ {
		many = append(many, call("echo", q))
	}
	echo := &counter{s: goSpec("echo")}
	rt, f := rig(t, map[string][]model.Reply{"lead": {{ToolCalls: many}, calls(call("finish", q))}}, echo, newFinish())
	if _, err := rt.Run(context.Background(), lead("echo", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatal(err)
	}
	req := f.Calls()[1]
	if echo.runs != maxCallsInTurn || len(req.Messages[2].ToolCalls) != maxCallsInTurn || !strings.Contains(lastContent(req), "42 were dropped") {
		t.Fatalf("ran %d of 50 calls; history holds %d; note %q", echo.runs, len(req.Messages[2].ToolCalls), lastContent(req))
	}
}

// A real model may wrap its JSON answer in prose or a fence after a line of prose; the object is
// still taken, and an answer with no valid object is still refused.
func TestAnswerToleratesProseAroundJSON(t *testing.T) {
	out, err := Compile(testOut)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		`{"ok":true}`,
		"```json\n{\"ok\":true}\n```",
		"Here is my review.\n\n```json\n{\"ok\":true}\n```\nDone.",
		`My answer: {"ok":true} (end)`,
	} {
		if got, err := answer(text, out); err != nil || string(got) != `{"ok":true}` && strings.TrimSpace(string(got)) != `{"ok":true}` {
			t.Errorf("%q: %s, %v", text, got, err)
		}
	}
	for _, text := range []string{"no json here", `prose {"ok":"yes"}`, "```json\n{\"bad\":1}\n```"} {
		if _, err := answer(text, out); err == nil {
			t.Errorf("%q: accepted", text)
		}
	}
}

// A subagent's answer cut off at its token limit says so in the repair note (so the model can
// shorten it), and subagents get a larger limit than a lead turn.
func TestSubagentTruncatedAnswerSaysSo(t *testing.T) {
	rt, f := rig(t, map[string][]model.Reply{
		"lead":          {calls(call("extract_facts", q))},
		"extract_facts": {{Text: `{"ok":tr`, Truncated: true}, {Text: `{"ok":true}`}},
	}, &counter{s: goSpec("retrieve")}, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	l := lead("extract_facts")
	l.MaxTurns = 1
	_, _ = rt.Run(context.Background(), l, Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0))
	reqs := byAgent(f, "extract_facts")
	if len(reqs) != 2 || reqs[0].MaxTokens != subagentMaxTokens || !strings.Contains(lastContent(reqs[1]), "cut off at its 8192-token limit") {
		t.Fatalf("%d calls, max tokens %d; repair note %q", len(reqs), reqs[0].MaxTokens, lastContent(reqs[len(reqs)-1]))
	}
}

// A subagent's answer cut off before any text or whole tool call (bench: a critic's, which ended a
// $7.76 run with no deliverable) is a bad answer, not a model failure (G6): one shorter try, and a
// second cut-off fails the tool (ErrToolOutput), never the run (ErrNoContent).
func TestSubagentCutOffEmptyAnswerIsNotAModelFailure(t *testing.T) {
	rt, f := rig(t, map[string][]model.Reply{
		"extract_facts": {{Truncated: true}, {Text: `{"ok":true}`}},
	}, &counter{s: goSpec("retrieve")}, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	res, err := rt.Invoke(context.Background(), lead("extract_facts"), Call{}, "extract_facts", json.RawMessage(q), NewBudget(10, 1, 0), false, 0)
	reqs := byAgent(f, "extract_facts")
	if err != nil || string(res.Output) != `{"ok":true}` || len(reqs) != 2 || !strings.Contains(lastContent(reqs[1]), "cut off at its 8192-token limit before it held any text") {
		t.Fatalf("%s, %v after %d calls", res.Output, err, len(reqs))
	}
	rt, _ = rig(t, map[string][]model.Reply{
		"extract_facts": {{Truncated: true}, {Truncated: true}},
	}, &counter{s: goSpec("retrieve")}, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	_, err = rt.Invoke(context.Background(), lead("extract_facts"), Call{}, "extract_facts", json.RawMessage(q), NewBudget(10, 1, 0), false, 0)
	if !errors.Is(err, ErrToolOutput) || errors.Is(err, model.ErrNoContent) {
		t.Fatalf("two cut-off answers: %v", err)
	}
	rt, _ = rig(t, map[string][]model.Reply{"extract_facts": {{}}}, &counter{s: goSpec("retrieve")}, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	if _, err = rt.Invoke(context.Background(), lead("extract_facts"), Call{}, "extract_facts", json.RawMessage(q), NewBudget(10, 1, 0), false, 0); !errors.Is(err, model.ErrNoContent) {
		t.Fatalf("an empty answer that was not cut off is still a model failure: %v", err)
	}
}
