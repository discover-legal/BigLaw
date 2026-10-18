// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// embedClient serves h at /v1/embeddings for embed model m and returns a client for it.
func embedClient(t *testing.T, m string, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Options{URL: srv.URL + "/v1", Key: "sk-EMBEDKEY", Roles: Roles{Lead: "lead-model", Embed: m}, LocalWatts: 50, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	c.backoff = time.Millisecond
	return c
}

func embedReq(inputs ...string) EmbedRequest { return EmbedRequest{Agent: "ingest", Inputs: inputs} }

// TestEmbeddingsPriced (owner decision 8): an embeddings call goes to <URL>/embeddings on the embed
// role's model, returns one vector per input in input order (whatever order the answer lists them
// in), and is priced from the table like every call (text-embedding-3-small: $0.02 per 1M input
// tokens; ada-002 $0.10), with watt-hours. A model with no price, or an answer with no usage, has
// an unknown cost (nil), never $0; a token count past the inputs' size is capped.
func TestEmbeddingsPriced(t *testing.T) {
	var got map[string]any
	var path, auth string
	c := embedClient(t, "text-embedding-3-small", func(w http.ResponseWriter, r *http.Request) {
		path, auth, got = r.URL.Path, r.Header.Get("Authorization"), map[string]any{}
		json.NewDecoder(r.Body).Decode(&got)
		reply(w, `{"object":"list","data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[0.5,-0.25]}],"usage":{"prompt_tokens":10000,"total_tokens":10000}}`)
	})
	e, err := c.Embed(context.Background(), EmbedRequest{Agent: "ingest", Inputs: []string{strings.Repeat("first ", 2500), "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/embeddings" || auth != "Bearer sk-EMBEDKEY" || got["model"] != "text-embedding-3-small" || got["encoding_format"] != "float" ||
		got["dimensions"] != nil || len(got["input"].([]any)) != 2 || got["input"].([]any)[1] != "second" {
		t.Fatalf("request %s %q %v", path, auth, got)
	}
	if e.Model != "text-embedding-3-small" || len(e.Vectors) != 2 || e.Vectors[0][0] != 0.5 || e.Vectors[1][1] != 1 {
		t.Fatalf("answer %+v (vectors must follow the inputs' order)", e)
	}
	if e.Usage.InputTokens != 10000 || e.Usage.CostUSD == nil || *e.Usage.CostUSD != 10000*0.02/1e6 || e.Usage.WattHours == nil || e.Usage.Duration <= 0 {
		t.Fatalf("usage %+v, cost %v: want 10,000 tokens at $0.02/M = $0.0002, with watt-hours", e.Usage, e.Usage.CostUSD)
	}
	// The same answer for two one-byte inputs: 10,000 tokens is capped at the request's size.
	if e, err := c.Embed(context.Background(), embedReq("a", "b")); err != nil || e.Usage.InputTokens != len(`{"encoding_format":"float","input":["a","b"],"model":"text-embedding-3-small"}`) {
		t.Fatalf("absurd token count: %+v, %v", e, err)
	}
	ada := embedClient(t, "text-embedding-ada-002", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[{"index":0,"embedding":[1]}],"usage":{"prompt_tokens":10}}`)
	})
	if e, err := ada.Embed(context.Background(), embedReq("ten bytes!")); err != nil || e.Usage.CostUSD == nil || *e.Usage.CostUSD != 10*0.10/1e6 {
		t.Fatalf("ada-002: %+v, %v", e, err)
	}
	// No usage in the answer: the cost is unknown, even for a priced model.
	noUsage := embedClient(t, "text-embedding-3-small", func(w http.ResponseWriter, r *http.Request) { reply(w, `{"data":[{"index":0,"embedding":[1]}]}`) })
	if e, err := noUsage.Embed(context.Background(), embedReq("x")); err != nil || e.Usage.CostUSD != nil || e.Usage.InputTokens != 0 {
		t.Fatalf("no usage: %+v, %v", e, err)
	}
	// An unpriced model (no row): the cost is unknown, not $0.
	local := embedClient(t, "nomic-embed-text", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[{"index":0,"embedding":[1,2,3]}],"usage":{"prompt_tokens":7}}`)
	})
	if e, err := local.Embed(context.Background(), embedReq("x")); err != nil || e.Usage.CostUSD != nil || e.Usage.InputTokens != 7 {
		t.Fatalf("unpriced: %+v, %v", e, err)
	}
	// Priced at zero only when the operator says so.
	local.prices, _ = LoadPrices(`{"nomic-embed-text": {"in": 0, "out": 0}}`)
	if e, err := local.Embed(context.Background(), embedReq("x")); err != nil || e.Usage.CostUSD == nil || *e.Usage.CostUSD != 0 {
		t.Fatalf("operator-zeroed: %+v, %v", e, err)
	}
}

// TestEmbedTypedErrors: no embed role, no URL and every malformed answer are typed failures with
// fixed text (never the key, the URL or the answer's bytes); a refused 200 carries its usage; 429
// and 5xx are retried like a chat call; a malformed request is refused before sending.
func TestEmbedTypedErrors(t *testing.T) {
	noRole, _ := New(Options{URL: "http://127.0.0.1:1/v1", Roles: Roles{Lead: "lead-model"}})
	if _, err := noRole.Embed(context.Background(), embedReq("x")); !errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "embed=<model id>") {
		t.Errorf("no embed role: %v", err)
	}
	noURL, _ := New(Options{Roles: Roles{Embed: "m"}})
	if _, err := noURL.Embed(context.Background(), embedReq("x")); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no URL: %v", err)
	}
	for name, tc := range map[string]struct {
		body string
		kind error
	}{
		"not JSON":         {`ECHO sk-EMBEDKEY <html>`, ErrUnavailable},
		"base64 vectors":   {`{"data":[{"index":0,"embedding":"AAAA"}]}`, ErrUnavailable},
		"too few":          {`{"data":[{"index":0,"embedding":[1]}],"usage":{"prompt_tokens":3}}`, ErrUnavailable},
		"repeated index":   {`{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}],"usage":{"prompt_tokens":3}}`, ErrUnavailable},
		"index past end":   {`{"data":[{"index":0,"embedding":[1]},{"index":2,"embedding":[2]}],"usage":{"prompt_tokens":3}}`, ErrUnavailable},
		"lengths differ":   {`{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[2]}],"usage":{"prompt_tokens":3}}`, ErrUnavailable},
		"zero vector":      {`{"data":[{"index":0,"embedding":[0,0]},{"index":1,"embedding":[2,1]}],"usage":{"prompt_tokens":3}}`, ErrUnavailable},
		"empty vector":     {`{"data":[{"index":0,"embedding":[]},{"index":1,"embedding":[]}],"usage":{"prompt_tokens":3}}`, ErrUnavailable},
		"no vectors":       {`{"data":[],"usage":{"prompt_tokens":3}}`, ErrNoContent},
		"float32 overflow": {`{"data":[{"index":0,"embedding":[1e39]},{"index":1,"embedding":[1]}]}`, ErrUnavailable},
		"subnormal":        {`{"data":[{"index":0,"embedding":[1e-45,1e-45]},{"index":1,"embedding":[2,1]}],"usage":{"prompt_tokens":3}}`, ErrUnavailable},
		"huge":             {`{"data":[{"index":0,"embedding":[1,1]},{"index":1,"embedding":[3e38,3e38]}],"usage":{"prompt_tokens":3}}`, ErrUnavailable},
	} {
		c := embedClient(t, "text-embedding-3-small", func(w http.ResponseWriter, r *http.Request) { reply(w, tc.body) })
		_, err := c.Embed(context.Background(), embedReq("a", "b"))
		var me *Error
		if !errors.Is(err, tc.kind) || !errors.As(err, &me) || strings.Contains(err.Error(), "EMBEDKEY") || strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("%s: %v, want %v with fixed text", name, err, tc.kind)
			continue
		}
		if strings.Contains(tc.body, "prompt_tokens") && (me.Usage == nil || me.Usage.InputTokens != 3 || me.Usage.CostUSD == nil) {
			t.Errorf("%s: a billed but refused answer lost its usage: %+v", name, me.Usage)
		}
	}
	// More than pgvector's 16,000 dimensions is refused (a hostile endpoint inside the 32 MiB cap).
	huge := `{"data":[{"index":0,"embedding":[` + strings.Repeat("1,", 16000) + `1]}]}`
	c0 := embedClient(t, "m", func(w http.ResponseWriter, r *http.Request) { reply(w, huge) })
	if _, err := c0.Embed(context.Background(), embedReq("x")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("16,001 dimensions: %v", err)
	}
	// Retries: two 503s then an answer is one successful call of three attempts.
	var n atomic.Int32
	c := embedClient(t, "m", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		reply(w, `{"data":[{"index":0,"embedding":[1]}]}`)
	})
	if _, err := c.Embed(context.Background(), embedReq("x")); err != nil || n.Load() != 3 {
		t.Errorf("retried embed: %v after %d attempts", err, n.Load())
	}
	c = embedClient(t, "m", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401); io.WriteString(w, "sk-EMBEDKEY") })
	if _, err := c.Embed(context.Background(), embedReq("x")); !errors.Is(err, ErrNotConfigured) || strings.Contains(err.Error(), "EMBEDKEY") {
		t.Errorf("401: %v", err)
	}
	// Malformed requests never reach the endpoint.
	var sent atomic.Bool
	c = embedClient(t, "m", func(w http.ResponseWriter, r *http.Request) { sent.Store(true) })
	many := make([]string, MaxEmbedInputs+1)
	for i := range many {
		many[i] = "x"
	}
	for name, req := range map[string]EmbedRequest{
		"no agent": {Inputs: []string{"x"}}, "no inputs": {Agent: "a"}, "blank input": {Agent: "a", Inputs: []string{" \n"}},
		"too many": {Agent: "a", Inputs: many}, "too long": {Agent: "a", Inputs: []string{strings.Repeat("x", MaxEmbedInput+1)}},
		"bad UTF-8": {Agent: "a", Inputs: []string{"\xff"}},
	} {
		if _, err := c.Embed(context.Background(), req); err == nil || errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v, want a request error", name, err)
		}
	}
	if sent.Load() {
		t.Error("a malformed request was sent")
	}
}

// TestFakeEmbed: model.Fake answers embeddings from its Vectors function, checks requests like the
// client, records them, and is unscripted (an error) without Vectors.
func TestFakeEmbed(t *testing.T) {
	f := NewFake(nil)
	if _, err := f.Embed(context.Background(), embedReq("x")); !errors.Is(err, ErrUnscripted) {
		t.Errorf("unscripted: %v", err)
	}
	f.Vectors = func(s string) ([]float32, error) { return []float32{float32(len(s))}, nil }
	e, err := f.Embed(context.Background(), embedReq("ab", "abc"))
	if err != nil || e.Model != "fake:embed" || e.Vectors[0][0] != 2 || e.Vectors[1][0] != 3 {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := f.Embed(context.Background(), embedReq("")); err == nil {
		t.Error("a blank input passed the fake")
	}
	if calls := f.EmbedCalls(); len(calls) != 2 || calls[1].Inputs[1] != "abc" { // the refused request is not recorded
		t.Errorf("calls %+v", calls)
	}
	// Vectors Client would refuse (mixed lengths, zero) are refused by the fake too.
	for _, vs := range []func(string) ([]float32, error){
		func(s string) ([]float32, error) { return make([]float32, len(s)), nil },
		func(s string) ([]float32, error) { return []float32{0}, nil },
	} {
		f.Vectors = vs
		if _, err := f.Embed(context.Background(), embedReq("ab", "abc")); !errors.Is(err, ErrUnavailable) {
			t.Errorf("fake accepted unusable vectors: %v", err)
		}
	}
}
