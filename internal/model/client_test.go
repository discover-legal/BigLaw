// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"bytes"
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

// endpoint serves h at /v1/chat/completions and returns a client for it.
func endpoint(t *testing.T, key string, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Options{URL: srv.URL + "/v1/", Key: key, Roles: Roles{Lead: "lead-model"}, LocalWatts: 100, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	c.backoff = time.Millisecond
	return c, srv
}

// hang answers only when the client goes away (the body is read first, so
// the server notices the disconnect).
func hang(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	<-r.Context().Done()
}

func reply(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, body)
}

func userReq(text string) Request {
	return Request{Agent: "lead", Role: Lead, Messages: []Message{{Role: "user", Text: text}}}
}

func TestChatToolCallRoundTrip(t *testing.T) {
	var got map[string]any
	var path, auth string
	c, _ := endpoint(t, "sk-test", func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		reply(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_a","type":"function","function":{"name":"retrieve","arguments":"{\"q\":\"notice period\"}"}},
			{"type":"function","function":{"name":"ledger","arguments":{"op":"plan"}}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":600}}}`)
	})
	par := true
	req := Request{Agent: "lead", Role: Specialist, ToolChoice: "required", ParallelToolCalls: &par, MaxTokens: 512,
		Tools: []Tool{{Name: "retrieve", Description: "Search the matter.", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`), Strict: true},
			{Name: "ledger", Description: "The plan.", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{{Role: "system", Text: "You are the lead."}, {Role: "user", Text: "Draft the memo."},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c0", Type: "function", Function: Function{Name: "retrieve", Arguments: `{"q":"x"}`}}}},
			{Role: "tool", ToolCallID: "c0", Text: "[result r1]"}}}
	resp, err := c.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/chat/completions" || auth != "Bearer sk-test" {
		t.Errorf("path %q, auth %q", path, auth)
	}
	if got["model"] != "lead-model" || got["tool_choice"] != "required" || got["parallel_tool_calls"] != true || got["max_tokens"] != 512.0 || got["stream"] != false {
		t.Errorf("request = %v (the unset specialist role must use the lead's model)", got)
	}
	tools := got["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if tools[0].(map[string]any)["type"] != "function" || fn["name"] != "retrieve" || fn["strict"] != true || tools[1].(map[string]any)["function"].(map[string]any)["strict"] != nil {
		t.Errorf("tools = %v", tools)
	}
	msgs := got["messages"].([]any)
	asst, tool := msgs[2].(map[string]any), msgs[3].(map[string]any)
	call := asst["tool_calls"].([]any)[0].(map[string]any)
	if asst["content"] != nil || call["id"] != "c0" || call["function"].(map[string]any)["arguments"] != `{"q":"x"}` ||
		tool["role"] != "tool" || tool["tool_call_id"] != "c0" || tool["content"] != "[result r1]" {
		t.Errorf("history = %v", msgs)
	}
	want := []ToolCall{{"call_a", "function", Function{"retrieve", `{"q":"notice period"}`}}, {"call_1", "function", Function{"ledger", `{"op":"plan"}`}}}
	if len(resp.ToolCalls) != 2 || resp.ToolCalls[0] != want[0] || resp.ToolCalls[1] != want[1] || resp.Model != "lead-model" {
		t.Fatalf("response = %+v", resp)
	}
	if u := resp.Usage; u.InputTokens != 400 || u.CacheReadTokens != 600 || u.OutputTokens != 50 {
		t.Errorf("usage = %+v (input excludes the 600 cached tokens)", u)
	}
}

func TestImagePartsRoundTrip(t *testing.T) {
	var got struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	c, _ := endpoint(t, "", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		reply(w, `{"choices":[{"message":{"content":"Page 1: NOTICE OF APPLICATION"}}]}`)
	})
	img := "data:image/png;base64,iVBORw0KGgo="
	req := Request{Agent: "transcribe_page", Role: Lead, Messages: []Message{{Role: "user", Parts: []Part{TextPart("Transcribe this page."), ImagePart(img)}}}}
	resp, err := c.Chat(context.Background(), req)
	if err != nil || resp.Text != "Page 1: NOTICE OF APPLICATION" {
		t.Fatalf("%v %v", resp, err)
	}
	parts := got.Messages[0].Content
	if len(parts) != 2 || parts[0]["type"] != "text" || parts[0]["text"] != "Transcribe this page." ||
		parts[1]["type"] != "image_url" || parts[1]["image_url"].(map[string]any)["url"] != img || parts[1]["text"] != nil {
		t.Fatalf("parts = %v", parts)
	}
	// A remote image (the endpoint would fetch it), a non-image type, or a part
	// that is both or neither never leaves BigLaw.
	for _, p := range [][]Part{{ImagePart("https://evil.example/a.png")}, {ImagePart("data:text/html;base64,PGI+")},
		{ImagePart("data:image/png;base64,abc def")}, {{Type: "text"}}, {{Type: "text", Text: "x", ImageURL: map[string]string{"url": img}}}} {
		bad := Request{Agent: "x", Messages: []Message{{Role: "user", Parts: p}}}
		if _, err := c.Chat(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "invalid request") {
			t.Errorf("parts %v: err = %v", p, err)
		}
	}
}

// TestRequestBytesStable: the same request encodes to the same bytes, and a
// tool schema is sent exactly as given, so a static tool list keeps the
// provider's prompt-cache prefix (DESIGN § Revision 5, best practice 15).
func TestRequestBytesStable(t *testing.T) {
	var bodies [][]byte
	c, _ := endpoint(t, "", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		reply(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})
	schema := `{"type":"object","required":["z","a"],"properties":{"z":{"type":"string"},"a":{"type":"string"}}}`
	req := userReq("hi")
	req.Tools = []Tool{{Name: "b_tool", Parameters: json.RawMessage(schema)}, {Name: "a_tool", Parameters: json.RawMessage(`{}`)}}
	for range 2 {
		if _, err := c.Chat(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(bodies[0], bodies[1]) || !bytes.Contains(bodies[0], []byte(schema)) ||
		bytes.Index(bodies[0], []byte(`"b_tool"`)) > bytes.Index(bodies[0], []byte(`"a_tool"`)) {
		t.Fatalf("request bytes not stable or reordered:\n%s\n%s", bodies[0], bodies[1])
	}
}

const secretKey = "sk-SECRETKEY-123"

// TestTypedErrors: each failure is the documented kind and HTTP status, and
// no error text carries the key, the endpoint's query or upstream text.
func TestTypedErrors(t *testing.T) {
	cases := []struct {
		name   string
		h      http.HandlerFunc
		key    string
		ctx    time.Duration
		kind   error
		status int
		typ    string
	}{
		{"429", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":{"message":"`+secretKey+` over quota"}}`, http.StatusTooManyRequests)
		},
			secretKey, 0, ErrRateLimited, 503, "model_rate_limited"},
		{"500", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UPSTREAMTEXT "+secretKey, http.StatusInternalServerError)
		}, secretKey, 0, ErrUnavailable, 502, "model_unavailable"},
		{"400", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "UPSTREAMTEXT", http.StatusBadRequest) }, "", 0, ErrUnavailable, 502, "model_unavailable"},
		{"deadline", hang, "", 50 * time.Millisecond, ErrTimeout, 504, "model_timeout"},
		{"no choices", func(w http.ResponseWriter, r *http.Request) { reply(w, `{"choices":[]}`) }, "", 0, ErrNoContent, 422, "no_content"},
		{"empty choice", func(w http.ResponseWriter, r *http.Request) { reply(w, `{"choices":[{"message":{"content":"  "}}]}`) }, "", 0, ErrNoContent, 422, "no_content"},
		{"truncated empty", func(w http.ResponseWriter, r *http.Request) {
			reply(w, `{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`)
		}, "", 0, ErrNoContent, 422, "no_content"},
		{"not json", func(w http.ResponseWriter, r *http.Request) { reply(w, `<html>UPSTREAMTEXT</html>`) }, "", 0, ErrUnavailable, 502, "model_unavailable"},
		{"content not a string", func(w http.ResponseWriter, r *http.Request) {
			reply(w, `{"choices":[{"message":{"content":[{"type":"text","text":"UPSTREAMTEXT"}]}}]}`)
		}, "", 0, ErrUnavailable, 502, "model_unavailable"},
		{"nameless tool call", func(w http.ResponseWriter, r *http.Request) {
			reply(w, `{"choices":[{"message":{"tool_calls":[{"id":"x","function":{"arguments":"{}"}}]}}]}`)
		}, "", 0, ErrUnavailable, 502, "model_unavailable"},
		{"401 without key", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }, "", 0, ErrNotConfigured, 503, "model_not_configured"},
		{"403 with key", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }, secretKey, 0, ErrNotConfigured, 503, "model_not_configured"},
		{"404", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "", 0, ErrNotConfigured, 503, "model_not_configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, srv := endpoint(t, tc.key, tc.h)
			c.endpoint += "?k=QUERYSECRET"
			ctx := context.Background()
			if tc.ctx > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.ctx)
				defer cancel()
			}
			_, err := c.Chat(ctx, userReq("hi"))
			assertKind(t, err, tc.kind, tc.status, tc.typ)
			for _, leak := range []string{"SECRET", "UPSTREAMTEXT", srv.URL} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error leaks %q: %v", leak, err)
				}
			}
		})
	}

	t.Run("client timeout", func(t *testing.T) {
		c, _ := endpoint(t, "", hang)
		c.http.Timeout = 50 * time.Millisecond
		_, err := c.Chat(context.Background(), userReq("hi"))
		assertKind(t, err, ErrTimeout, 504, "model_timeout")
	})
	t.Run("unreachable", func(t *testing.T) {
		c, srv := endpoint(t, secretKey, nil)
		srv.Close()
		c.endpoint += "?k=QUERYSECRET"
		_, err := c.Chat(context.Background(), userReq("hi"))
		assertKind(t, err, ErrUnavailable, 502, "model_unavailable")
		if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "chat/completions") {
			t.Errorf("error leaks the URL or key: %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		c, _ := endpoint(t, "", hang)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)
		_, err := c.Chat(ctx, userReq("hi"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if s, _, _ := HTTPStatus(err); s != 499 {
			t.Errorf("cancelled → %d", s)
		}
	})
	t.Run("not configured", func(t *testing.T) {
		c, _ := New(Options{Roles: Roles{Lead: "m"}})
		_, err := c.Chat(context.Background(), userReq("hi"))
		assertKind(t, err, ErrNotConfigured, 503, "model_not_configured")
		c, _ = New(Options{URL: "http://127.0.0.1:1/v1"})
		_, err = c.Chat(context.Background(), userReq("hi"))
		assertKind(t, err, ErrNotConfigured, 503, "model_not_configured")
	})
	t.Run("redirect not followed", func(t *testing.T) {
		var hit atomic.Int32
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1) }))
		defer other.Close()
		c, _ := endpoint(t, secretKey, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
		})
		_, err := c.Chat(context.Background(), userReq("hi"))
		assertKind(t, err, ErrUnavailable, 502, "model_unavailable")
		if hit.Load() != 0 {
			t.Error("the redirect was followed: the prompt went to another host")
		}
	})
	if s, typ, _ := HTTPStatus(errors.New("db down")); s != 500 || typ != "" {
		t.Errorf("a non-model error → %d %q", s, typ)
	}
}

func assertKind(t *testing.T, err, kind error, status int, typ string) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("err = %v, want %v", err, kind)
	}
	for _, other := range []error{ErrNotConfigured, ErrRateLimited, ErrUnavailable, ErrTimeout, ErrNoContent} {
		if other != kind && errors.Is(err, other) {
			t.Errorf("err %v also matches %v", err, other)
		}
	}
	if s, ty, msg := HTTPStatus(err); s != status || ty != typ || msg == "" {
		t.Errorf("HTTPStatus = %d %q %q, want %d %q", s, ty, msg, status, typ)
	}
}

func TestRetryBackoffBounded(t *testing.T) {
	counting := func(status []int, header string) (*Client, *atomic.Int32) {
		var n atomic.Int32
		c, _ := endpoint(t, "", func(w http.ResponseWriter, r *http.Request) {
			i := int(n.Add(1)) - 1
			if i < len(status) && status[i] != 200 {
				if header != "" {
					w.Header().Set("Retry-After", header)
				}
				w.WriteHeader(status[i])
				return
			}
			reply(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		})
		return c, &n
	}
	for _, tc := range []struct {
		name     string
		status   []int
		header   string
		attempts int32
		kind     error
	}{
		{"always 503", []int{503, 503, 503, 503, 503}, "", 3, ErrUnavailable},
		{"always 429", []int{429, 429, 429, 429}, "", 3, ErrRateLimited},
		{"529 then 502 then ok", []int{529, 502, 200}, "", 3, nil},
		{"400 is not retried", []int{400, 200}, "", 1, ErrUnavailable},
		{"404 is not retried", []int{404, 200}, "", 1, ErrNotConfigured},
		{"Retry-After over 30s ends the call", []int{429, 200}, "120", 1, ErrRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, n := counting(tc.status, tc.header)
			_, err := c.Chat(context.Background(), userReq("hi"))
			if n.Load() != tc.attempts || (tc.kind == nil) != (err == nil) || tc.kind != nil && !errors.Is(err, tc.kind) {
				t.Fatalf("%d attempts, err %v; want %d, %v", n.Load(), err, tc.attempts, tc.kind)
			}
		})
	}
	// The provider's Retry-After reaches the caller for the 503's header.
	c, _ := counting([]int{429, 200}, "120")
	if _, err := c.Chat(context.Background(), userReq("hi")); RetryAfter(err) != 120*time.Second {
		t.Errorf("RetryAfter = %v", RetryAfter(err))
	}
	// A backoff that would outlast the caller's deadline is not slept.
	c, n := counting([]int{503, 503, 503}, "")
	c.backoff = 5 * time.Second // under maxWait, over the deadline
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if _, err := c.Chat(ctx, userReq("hi")); !errors.Is(err, ErrUnavailable) || n.Load() != 1 || time.Since(start) > 500*time.Millisecond {
		t.Errorf("err %v after %d attempts in %v", err, n.Load(), time.Since(start))
	}
}

func TestRolesModel(t *testing.T) {
	r := Roles{Lead: "big", Critic: "other"}
	for role, want := range map[Role]string{Lead: "big", Specialist: "big", Critic: "other"} {
		if got, err := r.Model(role); got != want || err != nil {
			t.Errorf("%s → %q %v", role, got, err)
		}
	}
	if _, err := r.Model("vision"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("unknown role: %v", err)
	}
	if _, err := (Roles{Critic: "x"}).Model(Critic); err != nil {
		t.Errorf("a set critic without a lead: %v", err)
	}
	if _, err := (Roles{}).Model(Specialist); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no lead: %v", err)
	}
}

func TestRequestCheck(t *testing.T) {
	ok := json.RawMessage(`{"type":"object"}`)
	for name, req := range map[string]Request{
		"no agent":          {Messages: []Message{{Role: "user", Text: "x"}}},
		"no messages":       {Agent: "lead"},
		"bad choice":        {Agent: "lead", ToolChoice: "any", Messages: []Message{{Role: "user", Text: "x"}}},
		"bad role":          {Agent: "lead", Messages: []Message{{Role: "developer", Text: "x"}}},
		"tool name":         {Agent: "lead", Tools: []Tool{{Name: "a b", Parameters: ok}}, Messages: []Message{{Role: "user", Text: "x"}}},
		"duplicate tool":    {Agent: "lead", Tools: []Tool{{Name: "a", Parameters: ok}, {Name: "a", Parameters: ok}}, Messages: []Message{{Role: "user", Text: "x"}}},
		"schema not object": {Agent: "lead", Tools: []Tool{{Name: "a", Parameters: json.RawMessage(`[]`)}}, Messages: []Message{{Role: "user", Text: "x"}}},
		"tool msg no id":    {Agent: "lead", Messages: []Message{{Role: "tool", Text: "x"}}},
		"id on user msg":    {Agent: "lead", Messages: []Message{{Role: "user", Text: "x", ToolCallID: "c"}}},
		"calls on user msg": {Agent: "lead", Messages: []Message{{Role: "user", Text: "x", ToolCalls: []ToolCall{{ID: "c"}}}}},
		"parts beside text": {Agent: "lead", Messages: []Message{{Role: "user", Text: "x", Parts: []Part{TextPart("y")}}}},
		"parts on system":   {Agent: "lead", Messages: []Message{{Role: "system", Parts: []Part{TextPart("y")}}}},
	} {
		if err := req.check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := NewFake(map[string][]Reply{"lead": {{Text: "x"}}}).Chat(context.Background(), req); err == nil {
			t.Errorf("%s: Fake accepted it", name)
		}
	}
	for _, u := range []string{"ftp://h/v1", "http://u:p@h/v1", "/v1", "http:///v1"} {
		if _, err := New(Options{URL: u}); err == nil {
			t.Errorf("New(%q) accepted", u)
		}
	}
}
