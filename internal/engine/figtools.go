// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// The figure reader's own tools (read_figures, figures.go): figure_context reads the document around
// a quote it labels, exact_calc checks its arithmetic. Neither writes anything: the reader's labels
// are its only output, and Go checks them.

// contextBytes is what one figure_context call returns at most.
const contextBytes = 1500

type figureContext struct{ e *Engine }

func (e *Engine) figureContextTool() agent.Tool { return figureContext{e} }

func (figureContext) Spec() agent.Spec { return spec("figure_context") }

// Call returns the document's text right before or after the quote's first exact occurrence in it,
// from offset bytes out, cut at rune boundaries, in the task's scope only.
func (t figureContext) Call(ctx context.Context, c agent.Call) (agent.Result, error) {
	var in struct {
		DocumentID string `json:"documentId"`
		Quote      string
		Side       string
		Offset     *int
	}
	if err := agent.Decode(c, &in); err != nil {
		return agent.Result{}, err
	}
	d, err := t.e.docs.Get(ctx, c.DocumentIDs, in.DocumentID)
	if err != nil {
		return agent.Result{}, agent.Fail("figure_context: " + in.DocumentID + " is not one of this task's documents")
	}
	i := strings.Index(d.Text, in.Quote)
	if i < 0 {
		return agent.Result{}, agent.Fail("figure_context: the quote is not in the document as written: pass the quote exactly as you were given it")
	}
	off := 0
	if in.Offset != nil {
		off = *in.Offset
	}
	var text string
	var next *int
	if in.Side == "before" {
		end := max(0, i-off)
		start := max(0, end-contextBytes)
		for start > 0 && !utf8.RuneStart(d.Text[start]) {
			start++
		}
		text = d.Text[start:end]
		if start > 0 {
			n := off + len(text)
			next = &n
		}
	} else {
		start := min(len(d.Text), i+len(in.Quote)+off)
		end := min(len(d.Text), start+contextBytes)
		for end < len(d.Text) && !utf8.RuneStart(d.Text[end]) {
			end--
		}
		text = d.Text[start:end]
		if end < len(d.Text) {
			n := off + len(text)
			next = &n
		}
	}
	out, _ := json.Marshal(map[string]any{"text": text, "next": next})
	return agent.Result{Output: out}, nil
}

type exactCalc struct{}

func (exactCalc) Spec() agent.Spec { return spec("exact_calc") }

var plainNumber = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$`)

// Call does one exact operation on two plain decimals (big.Rat: no rounding, no floating point).
func (exactCalc) Call(_ context.Context, c agent.Call) (agent.Result, error) {
	var in struct{ Op, A, B string }
	if err := agent.Decode(c, &in); err != nil {
		return agent.Result{}, err
	}
	a, aok := new(big.Rat).SetString(strings.TrimSpace(in.A))
	b, bok := new(big.Rat).SetString(strings.TrimSpace(in.B))
	if !plainNumber.MatchString(strings.TrimSpace(in.A)) || !plainNumber.MatchString(strings.TrimSpace(in.B)) || !aok || !bok {
		return agent.Result{}, agent.Fail("exact_calc: write each number as plain digits with an optional sign and one decimal point, no separators, e.g. \"4217\" or \"-1250.50\"")
	}
	r := new(big.Rat)
	switch in.Op {
	case "add":
		r.Add(a, b)
	case "sub":
		r.Sub(a, b)
	case "mul":
		r.Mul(a, b)
	case "div":
		if b.Sign() == 0 {
			return agent.Result{}, agent.Fail("exact_calc: division by zero")
		}
		r.Quo(a, b)
	default: // cmp
		r.SetInt64(int64(a.Cmp(b)))
	}
	s := r.RatString() // a fraction ("1/3") when no decimal is exact
	for p := 0; p <= 40; p++ {
		if x, _ := new(big.Rat).SetString(r.FloatString(p)); x.Cmp(r) == 0 {
			s = r.FloatString(p)
			break
		}
	}
	out, _ := json.Marshal(map[string]string{"result": s})
	return agent.Result{Output: out}, nil
}
