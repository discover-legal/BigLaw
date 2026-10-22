// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package agent is BigLaw's agent runtime: the Tool interface and its single registry, JSON-schema
// validation of every tool input and output, one bounded loop for the lead (depth 0) and its
// subagents (depth 1), the budget ledger, and the tool bandit (AgensFlow) behind the policy hook.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/discover-legal/BigLaw/internal/model"
)

// Spec describes a tool. It is fixed at registration: the same bytes reach the model on every turn.
type Spec struct {
	Name        string          `json:"name"`
	Kind        string          `json:"kind"` // "go", "subagent", or "fanout" (a Go tool that runs subagents)
	Description string          `json:"description"`
	In          json.RawMessage `json:"in"`                // JSON Schema of the input (schema.go's subset, strict-shaped)
	Out         json.RawMessage `json:"out"`               // JSON Schema of the output (a subagent's answer)
	Class       string          `json:"class"`             // budget class: plan, evidence, draft, committee or free
	MaxTurns    int             `json:"maxTurns"`          // model calls one call may make (a subagent's turn cap)
	Allowed     []string        `json:"allowed,omitempty"` // subagents: the tools they may call
	Role        model.Role      `json:"role,omitempty"`    // subagents: specialist or critic
	Solo        []string        `json:"solo,omitempty"`    // ops ("*" = any) that must be the only call in their turn
	Final       bool            `json:"final,omitempty"`   // may spend the run's reserve (finish)
}

// Call is one invocation: the matter scope (G1), the input, and what it may spend.
type Call struct {
	TaskID      string          `json:"taskId"`
	DocumentIDs []string        `json:"documentIds"`
	Input       json.RawMessage `json:"input"`
	Budget      Grant           `json:"budget"`
	Depth       int             `json:"depth"` // 0: the lead called it; 1: a subagent did
}

// Grant is what one call may spend: model calls and USD (priced calls only).
type Grant struct {
	Calls int     `json:"calls"`
	USD   float64 `json:"usd"`
}

// Result is a tool's answer. Usage is charged to the task ledger by the caller, also on an error.
type Result struct {
	Output json.RawMessage `json:"output"`
	Usage  Usage           `json:"usage"`
	Model  string          `json:"model,omitempty"`  // the model that ran a subagent (the bandit's arm)
	Reward *float64        `json:"reward,omitempty"` // the bandit's reward 0–1; nil scores a success 1
	Final  bool            `json:"final,omitempty"`  // the run is done (finish accepted)
}

// Usage is what calls spent. USD counts priced calls; Unpriced counts calls whose cost is unknown.
type Usage struct {
	Calls        int     `json:"calls"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	USD          float64 `json:"usd"`
	Unpriced     int     `json:"unpriced"`
}

func (u *Usage) Add(v Usage) {
	u.Calls, u.InputTokens, u.OutputTokens = u.Calls+v.Calls, u.InputTokens+v.InputTokens, u.OutputTokens+v.OutputTokens
	u.USD, u.Unpriced = u.USD+v.USD, u.Unpriced+v.Unpriced
}

// ModelUsage is one model call's usage, from its answer or its failure.
func ModelUsage(err error, r *model.Response) Usage {
	var mu *model.Usage
	var me *model.Error
	switch {
	case r != nil:
		mu = &r.Usage
	case errors.As(err, &me) && me.Usage != nil:
		mu = me.Usage
	case errors.As(err, &me) && me.Status == 200: // an answer arrived, but its usage is unreadable: cost unknown
		return Usage{Calls: 1, Unpriced: 1}
	case errors.As(err, &me): // the call was made and failed: no billed answer
		return Usage{Calls: 1}
	default: // nothing was sent (a malformed request, a cancellation before the call)
		return Usage{}
	}
	u := Usage{Calls: 1, InputTokens: mu.InputTokens + mu.CacheReadTokens + mu.CacheWriteTokens, OutputTokens: mu.OutputTokens}
	if mu.CostUSD == nil {
		u.Unpriced = 1
	} else {
		u.USD = *mu.CostUSD
	}
	return u
}

// Tool is anything the registry can run: a Go function, a subagent, or a remote adapter.
type Tool interface {
	Spec() Spec
	Call(ctx context.Context, c Call) (Result, error)
}

// Errors a tool call can end in besides the model's (G6) kinds. A Refusal wraps one of the first five.
var (
	ErrToolInput  = errors.New("tool input does not match its schema")
	ErrNotOffered = errors.New("tool not offered to this agent")
	ErrNesting    = errors.New("subagents cannot call subagents")
	ErrAlone      = errors.New("tool must be called alone")
	ErrBudget     = errors.New("the budget cannot fund this call")
	ErrPolicy     = errors.New("refused by the task policy")
	ErrToolOutput = errors.New("tool output does not match its schema")
	ErrTurnCap    = errors.New("turn cap reached without an answer")
)

// Refusal is a call that did not run: why, and what the agent should do next (README: actionable).
type Refusal struct {
	Kind         error
	Reason, Next string
}

func (r *Refusal) Error() string { return "refused: " + r.Reason + ". Next: " + r.Next }
func (r *Refusal) Unwrap() error { return r.Kind }

func refuse(kind error, next string, reason ...string) *Refusal {
	return &Refusal{Kind: kind, Reason: strings.Join(reason, ""), Next: next}
}

// Failure is a tool error whose text is safe to show the model (BigLaw's own words, no internals).
type Failure struct{ Msg string }

func (f *Failure) Error() string { return f.Msg }

// Fail returns a Failure; any other non-model error reaches the model only as "internal error".
func Fail(msg string) error { return &Failure{msg} }

// GoTool adapts a function to Tool. fn gets the validated input and returns the output value.
type GoTool struct {
	S  Spec
	Fn func(ctx context.Context, c Call) (any, Usage, error)
}

func (g GoTool) Spec() Spec { return g.S }

func (g GoTool) Call(ctx context.Context, c Call) (Result, error) {
	v, u, err := g.Fn(ctx, c)
	if err != nil {
		return Result{Usage: u}, err
	}
	out, err := json.Marshal(v)
	return Result{Output: out, Usage: u}, err
}

// Decode unmarshals a call's (already validated) input into v.
func Decode(c Call, v any) error {
	if err := json.Unmarshal(c.Input, v); err != nil {
		return refuse(ErrToolInput, "fix the input and call again", "the input does not decode: ", err.Error())
	}
	return nil
}
