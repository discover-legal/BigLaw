// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func call(agent string) Request {
	return Request{Agent: agent, Role: Lead, Messages: []Message{{Role: "user", Text: "go"}}}
}

func TestFakeScriptExhaustedIsError(t *testing.T) {
	f := NewFake(map[string][]Reply{"lead": {{Text: "one"}, {Text: "two"}}})
	for _, want := range []string{"one", "two"} {
		if r, err := f.Chat(context.Background(), call("lead")); err != nil || r.Text != want {
			t.Fatalf("got %+v, %v; want %q", r, err, want)
		}
	}
	r, err := f.Chat(context.Background(), call("lead"))
	if !errors.Is(err, ErrScriptExhausted) || r != nil {
		t.Fatalf("third call: %+v, %v; want ErrScriptExhausted", r, err)
	}
	if s, _, _ := HTTPStatus(err); s != 500 {
		t.Errorf("a script error maps to %d, want 500 (it is a test bug, not a model failure)", s)
	}
	// A reply with nothing in it is a model failure, as from the real client.
	f = NewFake(map[string][]Reply{"lead": {{Text: " "}, {Err: &Error{Kind: ErrTimeout, Detail: "scripted"}}}})
	if _, err := f.Chat(context.Background(), call("lead")); !errors.Is(err, ErrNoContent) {
		t.Errorf("empty reply: %v", err)
	}
	if _, err := f.Chat(context.Background(), call("lead")); !errors.Is(err, ErrTimeout) {
		t.Errorf("scripted failure: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewFake(map[string][]Reply{"lead": {{Text: "x"}}}).Chat(ctx, call("lead")); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestFakeIsDeterministic(t *testing.T) {
	scripts := map[string][]Reply{
		"lead":          {{ToolCalls: []ToolCall{{Function: Function{Name: "extract_facts"}}, {Function: Function{Name: "retrieve", Arguments: `{"q":"x"}`}}}}, {Text: "done"}},
		"extract_facts": {{Text: `{"facts":[]}`, Usage: Usage{InputTokens: 10}}},
	}
	run := func() []*Response {
		f := NewFake(scripts)
		var out []*Response
		for _, a := range []string{"lead", "extract_facts", "lead"} {
			r, err := f.Chat(context.Background(), call(a))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	a, b := run(), run()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two runs differ:\n%+v\n%+v", a, b)
	}
	tc := a[0].ToolCalls
	if tc[0].ID != "lead-1-0" || tc[1].ID != "lead-1-1" || tc[0].Function.Arguments != "{}" || tc[0].Type != "function" {
		t.Errorf("tool calls = %+v", tc)
	}
	// Changing the caller's map after NewFake does not change the script.
	f := NewFake(scripts)
	scripts["lead"][0] = Reply{Text: "mutated"}
	scripts["lead"] = nil
	if r, _ := f.Chat(context.Background(), call("lead")); r == nil || r.Text != "" || len(r.ToolCalls) != 2 {
		t.Errorf("the fake shares the caller's script: %+v", r)
	}
}

func TestFakeRoutesByAgentName(t *testing.T) {
	f := NewFake(map[string][]Reply{
		"lead":          {{Text: "lead 1"}, {Text: "lead 2"}},
		"analyze_issue": {{Text: "issue 1"}},
	})
	// Interleaved calls each take their own agent's next reply.
	for _, step := range []struct{ agent, want string }{{"lead", "lead 1"}, {"analyze_issue", "issue 1"}, {"lead", "lead 2"}} {
		if r, err := f.Chat(context.Background(), call(step.agent)); err != nil || r.Text != step.want {
			t.Fatalf("%s: %+v, %v; want %q", step.agent, r, err, step.want)
		}
	}
	// A subagent with no script is an error, not a default reply, and it
	// does not consume another agent's script.
	if r, err := f.Chat(context.Background(), call("draft_section")); !errors.Is(err, ErrUnscripted) || r != nil {
		t.Fatalf("unscripted agent: %+v, %v", r, err)
	}
	if _, err := f.Chat(context.Background(), call("analyze_issue")); !errors.Is(err, ErrScriptExhausted) {
		t.Errorf("analyze_issue past its script: %v", err)
	}
	if got := len(f.Calls()); got != 5 {
		t.Errorf("Calls() = %d, want 5", got)
	}
	var _ Chatter = f // the fake and the client are interchangeable
	var _ Chatter = (*Client)(nil)
}
