// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"context"
	"encoding/json"
	"maps"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

const testDesc = "Does one test thing. It exists for the runtime's tests. It returns what it was given. Do not use it in production.\nExample: {\"q\": \"x\", \"op\": null}"

var testIn = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["q","op"],"properties":{"q":{"type":["string","null"]},"op":{"type":["string","null"]}}}`)
var testOut = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["ok"],"properties":{"ok":{"type":"boolean"}}}`)

// counter is a Go tool that counts its runs and answers {"ok": true}.
type counter struct {
	s    Spec
	mu   sync.Mutex
	runs int
	got  []Call
	use  Usage // charged on every call
	out  json.RawMessage
	err  error
}

func (c *counter) Spec() Spec { return c.s }
func (c *counter) Call(_ context.Context, call Call) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs++
	c.got = append(c.got, call)
	return Result{Output: cmpOr(c.out, json.RawMessage(`{"ok":true}`)), Usage: c.use}, c.err
}

func cmpOr(a, b json.RawMessage) json.RawMessage {
	if a != nil {
		return a
	}
	return b
}

func goSpec(name string) Spec {
	return Spec{Name: name, Kind: "go", Description: testDesc, In: testIn, Out: testOut, Class: "free"}
}

func subSpec(name string, allowed ...string) Spec {
	return Spec{Name: name, Kind: "subagent", Description: testDesc, In: testIn, Out: testOut, Class: "evidence", MaxTurns: 3, Allowed: allowed, Role: model.Specialist}
}

// call is a scripted tool call.
func call(name, args string) model.ToolCall {
	return model.ToolCall{Function: model.Function{Name: name, Arguments: args}}
}

func calls(cs ...model.ToolCall) model.Reply { return model.Reply{ToolCalls: cs} }

// rig is a runtime with a Fake and a registry of the given tools.
func rig(t *testing.T, scripts map[string][]model.Reply, tools ...Tool) (*Runtime, *model.Fake) {
	t.Helper()
	f := model.NewFake(scripts)
	rt := &Runtime{Chat: f, Roles: model.Roles{Lead: "m-lead", Specialist: "m-spec"}, Reg: NewRegistry()}
	for _, tl := range tools {
		if s, ok := tl.(*Subagent); ok {
			s.RT = rt
		}
		if err := rt.Reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	return rt, f
}

func lead(visible ...string) Agent {
	return Agent{Name: "lead", Role: model.Lead, Prompt: "You are the lead.", Visible: visible, MaxTurns: 20}
}

// journal records steps.
type journal struct {
	mu    sync.Mutex
	steps []Step
}

func (j *journal) Record(_ context.Context, s Step) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.steps = append(j.steps, s)
	return nil
}

func (j *journal) final() []Step { // the last record of each step number
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Step
	for _, s := range j.steps {
		if len(out) > 0 && out[len(out)-1].N == s.N {
			out[len(out)-1] = s
		} else {
			out = append(out, s)
		}
	}
	return out
}

// finisher is a Final Go tool that ends the run.
type finisher struct{ counter }

func (f *finisher) Call(ctx context.Context, c Call) (Result, error) {
	r, err := f.counter.Call(ctx, c)
	r.Final = true
	return r, err
}

func newFinish() *finisher {
	s := goSpec("finish")
	s.Final, s.MaxTurns = true, 3
	return &finisher{counter{s: s}}
}

// MemStats is an in-memory Stats.
type MemStats struct {
	mu sync.Mutex
	m  map[string]map[Arm]Stat
}

func (m *MemStats) Load(_ context.Context, sig string) (map[Arm]Stat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.m[sig]), nil // nil reads as empty
}

func (m *MemStats) Add(_ context.Context, sig string, a Arm, d Stat) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]map[Arm]Stat{}
	}
	if m.m[sig] == nil {
		m.m[sig] = map[Arm]Stat{}
	}
	m.m[sig][a] = m.m[sig][a].plus(d)
	return nil
}
