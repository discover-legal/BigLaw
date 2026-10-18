// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	MaxEmbedInputs = 256      // texts per embeddings call
	MaxEmbedInput  = 16 << 10 // bytes per text (a search chunk is at most about 5.6 KB)
)

// EmbedRequest is one embeddings call on the embed role's model.
type EmbedRequest struct {
	Agent      string   // labels logs, as Request.Agent does
	Inputs     []string // 1 to MaxEmbedInputs non-blank UTF-8 texts
	Dimensions int      // asks for this many dimensions (text-embedding-3 models); 0: the model's own
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
	ok := r.Agent != "" && len(r.Inputs) > 0 && len(r.Inputs) <= MaxEmbedInputs && r.Dimensions >= 0 && r.Dimensions <= 16000
	for _, s := range r.Inputs {
		ok = ok && strings.TrimSpace(s) != "" && len(s) <= MaxEmbedInput && utf8.ValidString(s)
	}
	if !ok {
		return fmt.Errorf("model: invalid embeddings request: it needs an Agent, 1 to %d non-blank UTF-8 inputs of at most %d bytes, and 0 to 16000 dimensions", MaxEmbedInputs, MaxEmbedInput)
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
	w := map[string]any{"model": model, "input": req.Inputs, "encoding_format": "float"}
	if req.Dimensions > 0 {
		w["dimensions"] = req.Dimensions
	}
	body, err := json.Marshal(w)
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
			Prompt int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return nil, fail(ErrUnavailable, "the answer is not OpenAI embeddings JSON")
	}
	e := &Embeddings{Model: model, Vectors: make([][]float32, len(req.Inputs)), Usage: Usage{InputTokens: max(0, a.Usage.Prompt), Duration: took}}
	c.prices.charge(model, &e.Usage, c.watts, c.log)
	kind, detail := error(nil), ""
	if len(a.Data) == 0 {
		kind, detail = ErrNoContent, "the answer has no vectors"
	}
	for _, d := range a.Data {
		if len(a.Data) != len(req.Inputs) || d.Index < 0 || d.Index >= len(e.Vectors) || e.Vectors[d.Index] != nil ||
			len(d.Embedding) != len(a.Data[0].Embedding) || len(d.Embedding) > 16000 || req.Dimensions > 0 && len(d.Embedding) != req.Dimensions ||
			!slices.ContainsFunc(d.Embedding, func(f float32) bool { return f != 0 }) { // a zero vector has no cosine distance
			kind, detail = ErrUnavailable, "the answer does not hold one non-zero vector of one length (at most 16,000) per input"
			break
		}
		e.Vectors[d.Index] = d.Embedding
	}
	if kind != nil { // billed: the usage goes with the error
		x := fail(kind, detail)
		x.Usage = &e.Usage
		return nil, x
	}
	return e, nil
}
