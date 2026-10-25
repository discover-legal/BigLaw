// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Script errors; not model failures (HTTPStatus maps them to 500).
var (
	ErrUnscripted      = errors.New("model.Fake: no script for this agent")
	ErrScriptExhausted = errors.New("model.Fake: script exhausted")
)

// Reply is one scripted answer; Err (e.g. &Error{Kind: ErrTimeout}) is returned instead when set.
type Reply struct {
	Text      string
	ToolCalls []ToolCall
	Truncated bool // the output stopped at MaxTokens
	Usage     Usage
	Err       error
}

// Fake is the scripted Chatter for tests: each agent's replies in order; unscripted or exhausted is
// an error. It checks replies as Client does, ids "<agent>-<call>-<index>"; it does not resolve Roles.
type Fake struct {
	// Vectors answers Embed, one input at a time (nil: Embed is unscripted). Set it before use.
	Vectors func(text string) ([]float32, error)
	// Route names the script a request is answered from (nil: its agent's), so a test can answer by
	// what a request holds when calls arrive in any order (a parallel sweep). Set it before use.
	Route func(Request) string
	// Respond answers a request itself when it returns true (a test's stand-in for a model whose answer
	// depends on what the request holds, such as the figure reader's labels); otherwise the scripts do.
	Respond func(Request) (Reply, bool)

	mu      sync.Mutex
	embeds  []EmbedRequest
	scripts map[string][]Reply
	calls   []Request
	n       map[string]int
}

func NewFake(scripts map[string][]Reply) *Fake {
	f := &Fake{scripts: map[string][]Reply{}, n: map[string]int{}}
	for agent, rs := range scripts {
		f.scripts[agent] = append([]Reply(nil), rs...)
	}
	return f
}

// Calls returns every request received, in order.
func (f *Fake) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.calls...)
}

func (f *Fake) Chat(ctx context.Context, req Request) (*Response, error) {
	if err := req.check(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctxErr(ctx)
	}
	key := req.Agent
	if f.Route != nil {
		key = f.Route(req)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	f.n[key]++
	n := f.n[key] // this script's call number
	script, ok := f.scripts[key]
	r, answered := Reply{}, false
	if f.Respond != nil {
		r, answered = f.Respond(req)
	}
	switch {
	case answered:
	case !ok:
		return nil, fmt.Errorf("%w %q", ErrUnscripted, key)
	case n > len(script):
		return nil, fmt.Errorf("%w: script %q got call %d of a %d-reply script", ErrScriptExhausted, key, n, len(script))
	default:
		r = script[n-1]
	}
	if r.Err != nil {
		return nil, r.Err
	}
	if strings.TrimSpace(r.Text) == "" && len(r.ToolCalls) == 0 {
		e := fail(ErrNoContent, "the scripted reply has neither text nor tool calls")
		e.Truncated = r.Truncated
		return nil, e
	}
	resp := &Response{Model: cmp.Or(req.Model, "fake:"+string(req.Role)), Text: r.Text, Truncated: r.Truncated, Usage: r.Usage}
	for i, tc := range r.ToolCalls {
		tc.ID = cmp.Or(tc.ID, key+"-"+strconv.Itoa(n)+"-"+strconv.Itoa(i))
		tc.Type, tc.Function.Arguments = "function", cmp.Or(strings.TrimSpace(tc.Function.Arguments), "{}")
		resp.ToolCalls = append(resp.ToolCalls, tc)
	}
	if e := checkCalls(resp.ToolCalls); e != nil {
		return nil, e
	}
	return resp, nil
}

// EmbedCalls returns every embeddings request received, in order.
func (f *Fake) EmbedCalls() []EmbedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]EmbedRequest(nil), f.embeds...)
}

// Embed answers from f.Vectors, checking the request and vectors as Client does; no usage.
func (f *Fake) Embed(ctx context.Context, req EmbedRequest) (*Embeddings, error) {
	if err := req.check(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctxErr(ctx)
	}
	f.mu.Lock()
	f.embeds = append(f.embeds, req)
	f.mu.Unlock()
	if f.Vectors == nil {
		return nil, fmt.Errorf("%w: Fake.Vectors is unset", ErrUnscripted)
	}
	e := &Embeddings{Model: "fake:embed"}
	for _, text := range req.Inputs {
		v, err := f.Vectors(text)
		if err != nil {
			return nil, err
		}
		e.Vectors = append(e.Vectors, v)
	}
	if !usable(e.Vectors) {
		return nil, fail(ErrUnavailable, "the scripted vectors are not of one length with a norm of 1e-6 to 1e6")
	}
	return e, nil
}
