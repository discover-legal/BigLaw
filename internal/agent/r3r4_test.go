// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// TestV4EscapedResultsNeverPanic (lab-r3 r4 verifier H1, the three repros; panicked on 89229c9): a
// tool result json.Marshal wrote (HTML-escaped, "&" as a 6-byte escape) is shown whole when it fits and
// otherwise refused as typed, valid JSON; nothing is cut, so nothing can index past a string's end.
func TestV4EscapedResultsNeverPanic(t *testing.T) {
	for _, c := range []struct {
		name string
		v    any
		n    int
	}{
		{"string field, 16 KB retry", map[string]any{"text": strings.Repeat("Smith & Jones LLP ", 700), "next": 3}, ShrunkResult},
		{"one-item array, 16 KB retry", map[string]any{"passages": []string{strings.Repeat("From: A <a@x.com> ", 700)}}, ShrunkResult},
		{"string field, 100 KB transcript cap", map[string]any{"text": strings.Repeat("Procter & Gamble <PG> ", 4300)}, maxResult},
		{"fits", map[string]any{"text": "Smith & Jones <x>"}, maxResult},
	} {
		raw, _ := json.Marshal(c.v) // as tools write their output
		got := content(toolOut{"Result of retrieve (step 4):\n", "retrieve", raw}, c.n)
		body := strings.TrimPrefix(got, "Result of retrieve (step 4):\n")
		switch {
		case len(got) > c.n:
			t.Errorf("%s: %d bytes > %d", c.name, len(got), c.n)
		case len(raw)+29 <= c.n && body != string(raw):
			t.Errorf("%s: a result that fits was changed", c.name)
		case len(raw)+29 > c.n && ErrorSchema.Validate(json.RawMessage(body)) != nil:
			t.Errorf("%s: the refusal does not match the error schema: %q", c.name, body)
		}
	}
}

// TestPanickingToolFailsOnlyItsRun (lab-r3 r4 H1): a tool that panics ends its run with ErrPanic (the
// engine fails that task as internal); the process and another run on the same runtime go on.
func TestPanickingToolFailsOnlyItsRun(t *testing.T) {
	boom := GoTool{S: goSpec("boom"), Fn: func(context.Context, Call) (any, Usage, error) { panic("index out of range") }}
	rt, _ := rig(t, map[string][]model.Reply{"lead": {calls(call("boom", q)), calls(call("finish", q))}}, boom, newFinish())
	if _, err := rt.Run(context.Background(), lead("boom", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); !errors.Is(err, ErrPanic) {
		t.Fatalf("a panicking tool's run ended with %v, want ErrPanic", err)
	}
	if res, err := rt.Run(context.Background(), lead("boom", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil || !res.Final {
		t.Fatalf("the next run on the same runtime: %v", err)
	}
}

// TestCompactionRelaxesAfterRetry (lab-r3 r3 verifier L5): after an over-long retry the older
// results are compacted for the next turn too, then the compaction relaxes a turn at a time (keep
// stayed at 1 for the rest of the run).
func TestCompactionRelaxesAfterRetry(t *testing.T) {
	var script []model.Reply
	for range 5 {
		script = append(script, calls(call("echo", q)))
	}
	script = append(script, model.Reply{Err: &model.Error{Kind: model.ErrUnavailable, Detail: "HTTP 400", Status: 400}})
	for range 6 {
		script = append(script, calls(call("echo", q)))
	}
	script = append(script, calls(call("finish", q)))
	rt, f := rig(t, map[string][]model.Reply{"lead": script}, &counter{s: goSpec("echo")}, newFinish())
	if _, err := rt.Run(context.Background(), lead("echo", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatal(err)
	}
	full := func(i int) int {
		n := 0
		for _, m := range f.Calls()[i].Messages {
			if c, ok := m.Content.(string); ok && m.Role == "tool" && !strings.HasPrefix(c, "[step ") {
				n++
			}
		}
		return n
	}
	if a, b := full(7), full(len(f.Calls())-1); a != 1 || b != keepTurns {
		t.Errorf("results in full: %d the turn after the retry (want 1), %d at the end (want %d)", a, b, keepTurns)
	}
}

// TestPanickedStepKeepsItsUsage (lab-r3 r5 verifier L6): a tool that made a model call and then
// panicked is journaled failed with that call's usage, never 0, and its grant is charged for it.
func TestPanickedStepKeepsItsUsage(t *testing.T) {
	var rt *Runtime
	s := goSpec("boom")
	s.MaxTurns = 2 // a grant of model calls
	boom := GoTool{S: s, Fn: func(ctx context.Context, c Call) (any, Usage, error) {
		if _, _, err := rt.ChatWithin(ctx, model.Request{Agent: "boom", Role: model.Lead, MaxTokens: 10, Messages: []model.Message{{Role: "user", Content: "x"}}}, NewBudget(c.Budget.Calls, c.Budget.USD, 0)); err != nil {
			t.Errorf("the tool's call: %v", err)
		}
		panic("after a call")
	}}
	rt, _ = rig(t, map[string][]model.Reply{"lead": {calls(call("boom", q))}, "boom": {{Text: "x"}}}, boom, newFinish())
	j := &journal{}
	rt.Journal = j
	b := NewBudget(50, 1, 0)
	if _, err := rt.Run(context.Background(), lead("boom", "finish"), Call{Input: json.RawMessage(`"go"`)}, b); !errors.Is(err, ErrPanic) {
		t.Fatalf("run: %v", err)
	}
	for _, s := range j.final() {
		if s.Tool == "boom" && (s.Status != "failed" || s.Usage.Calls != 1) {
			t.Errorf("the panicked step was journaled %s with usage %+v", s.Status, s.Usage)
		}
	}
	if b.Spent().Calls != 2 { // the lead's turn and the tool's call
		t.Errorf("the budget was charged %d calls, want 2", b.Spent().Calls)
	}
}
