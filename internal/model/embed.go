// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxEmbedInputs = 256      // texts per embeddings call
	MaxEmbedInput  = 16 << 10 // bytes per text (a search chunk is at most about 5.6 KB)
)

// EmbedRequest is one embeddings call on the embed role's model.
type EmbedRequest struct {
	Agent  string   // labels logs, as Request.Agent does
	Inputs []string // 1 to MaxEmbedInputs non-blank UTF-8 texts (sent at the model's own length)
}

// Embeddings is a successful call: one vector per input, in order, all the same length.
type Embeddings struct {
	Model   string
	Vectors [][]float32
	Usage   Usage // priced like every call: InputTokens at the model's input rate
}

// Embedder is what docs calls: *Client, or *Fake in tests.
type Embedder interface {
	Embed(ctx context.Context, req EmbedRequest) (*Embeddings, error)
}

func (r *EmbedRequest) check() error {
	ok := r.Agent != "" && len(r.Inputs) > 0 && len(r.Inputs) <= MaxEmbedInputs
	for _, s := range r.Inputs {
		ok = ok && strings.TrimSpace(s) != "" && len(s) <= MaxEmbedInput && utf8.ValidString(s)
	}
	if !ok {
		return fmt.Errorf("model: invalid embeddings request: it needs an Agent, 1 to %d non-blank UTF-8 inputs of at most %d bytes", MaxEmbedInputs, MaxEmbedInput)
	}
	return nil
}

// Embed runs one OpenAI-compatible /embeddings call, with Chat's retries, typed failures and
// pricing. A malformed answer is ErrUnavailable; one the provider billed carries its Usage.
func (c *Client) Embed(ctx context.Context, req EmbedRequest) (*Embeddings, error) {
	if err := req.check(); err != nil {
		return nil, err
	}
	model, err := c.roles.Model(Embed)
	if err != nil {
		return nil, err
	}
	if c.embeddings == "" {
		return nil, fail(ErrNotConfigured, "BIGLAW_MODEL_URL is not set")
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": req.Inputs, "encoding_format": "float"})
	if err != nil {
		return nil, fmt.Errorf("model: encode request: %w", err)
	}
	raw, took, err := c.send(ctx, req.Agent, c.embeddings, body)
	if err != nil {
		return nil, err
	}
	var a struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			Prompt *int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return nil, fail(ErrUnavailable, "the answer is not OpenAI embeddings JSON")
	}
	e := &Embeddings{Model: model, Vectors: make([][]float32, len(req.Inputs)), Usage: Usage{Duration: took}}
	if p := a.Usage.Prompt; p != nil { // a token is at least a byte: a count past the request's size is capped there
		e.Usage.InputTokens = min(len(body), max(0, *p))
	}
	if c.prices.charge(model, &e.Usage, c.watts, c.log); a.Usage.Prompt == nil {
		e.Usage.CostUSD = nil // no usage: the cost is unknown, never $0
	}
	ok := len(a.Data) == len(req.Inputs)
	for _, d := range a.Data {
		if ok = ok && d.Index >= 0 && d.Index < len(e.Vectors) && e.Vectors[d.Index] == nil; ok {
			e.Vectors[d.Index] = d.Embedding
		}
	}
	if !ok || !usable(e.Vectors) { // billed: the usage goes with the error
		x := fail(ErrUnavailable, "the answer does not hold one vector per input, of one length (at most 16,000) and a norm of 1e-6 to 1e6")
		if len(a.Data) == 0 {
			x = fail(ErrNoContent, "the answer has no vectors")
		}
		x.Usage = &e.Usage
		return nil, x
	}
	return e, nil
}

// usable reports whether cosine distance can use vs: one length, at most pgvector's 16,000, each
// norm 1e-6 to 1e6 (zero, subnormal or huge values make pgvector's float32 distances NaN).
func usable(vs [][]float32) bool {
	for _, v := range vs {
		var sq float64
		for _, f := range v {
			sq += float64(f) * float64(f)
		}
		if len(v) != len(vs[0]) || len(v) == 0 || len(v) > 16000 || !(sq >= 1e-12 && sq <= 1e12) {
			return false
		}
	}
	return true
}
