// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Pending is a call about to run, as the policy sees it.
type Pending struct {
	TaskID string
	Agent  string // "lead" or the calling subagent's name
	Depth  int
	Spec   Spec
	Input  json.RawMessage
	Budget *Budget // the caller's: what remains
}

// Policy is the task's rulebook (engine/policy.go: plan first, class shares, finish preconditions,
// escalation). The runtime's own rules come first and cannot be relaxed by it: the visible list,
// no nesting, calls that must be alone, the input schema and the reserve. A non-nil refusal stops
// the call; it costs the agent that turn and tells it what to do next.
type Policy interface {
	Allow(ctx context.Context, p Pending) *Refusal
}

// Step is one durable step of a lead's run (DESIGN § Long-running durable runs): a lead turn or a
// tool call, logged as started (with its grant) and then as committed, refused or failed.
type Step struct {
	N      int             `json:"step"`
	Kind   string          `json:"kind"` // lead_turn or tool_call
	Tool   string          `json:"tool,omitempty"`
	Status string          `json:"status"` // started, committed, refused or failed
	Input  json.RawMessage `json:"input,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
	Grant  Grant           `json:"grant"`
	Usage  Usage           `json:"usage"`
	Note   string          `json:"note,omitempty"`
	Model  string          `json:"model,omitempty"` // the model the step ran on (a lead turn, a subagent)
	Final  bool            `json:"final,omitempty"` // a Final tool accepted: the run was done here
}

// Journal records the lead's steps (Postgres in engine). A Record error stops the run (ErrJournal).
type Journal interface {
	Record(ctx context.Context, s Step) error
}

// LoadSubagent is one subagent from its spec file's bytes (file names it: <name>.json) and its
// prompt's: the spec decoded strictly (no unknown field), the name the file's and the kind subagent.
func (rt *Runtime) LoadSubagent(file string, spec, prompt []byte) (Tool, error) {
	var s Spec
	d := json.NewDecoder(strings.NewReader(string(spec)))
	d.DisallowUnknownFields()
	err := d.Decode(&s)
	switch {
	case err != nil:
		return nil, fmt.Errorf("agent: spec %s: %v", file, err)
	case strings.TrimSpace(string(prompt)) == "":
		return nil, fmt.Errorf("agent: spec %s: no prompt beside it", file)
	case s.Name+".json" != path.Base(file) || s.Kind != "subagent":
		return nil, fmt.Errorf("agent: spec %s: the name must match the file and the kind be subagent", file)
	}
	var schema bytes.Buffer // the model must see its output schema: the prompt alone can omit a required field
	if err := json.Compact(&schema, s.Out); err != nil {
		return nil, fmt.Errorf("agent: spec %s: output schema: %v", file, err)
	}
	return &Subagent{S: s, RT: rt, Prompt: string(prompt) + "\n\nYour answer is one JSON object valid " +
		"against this JSON Schema. Every property a \"required\" list names must be present, even when empty; use null " +
		"only where the type allows null, [] for an empty array:\n" + schema.String()}, nil
}
