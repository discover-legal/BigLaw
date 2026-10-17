// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package model is BigLaw's one model client: OpenAI-compatible chat
// completions with native tool calls (Ollama, LM Studio, vLLM, GoFetch,
// OpenRouter, LiteLLM or a cloud vendor), typed failures, bounded retries,
// pricing and watt-hour metering. Tests use Fake; none calls a real model.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Role names which configured model runs a call.
type Role string

const (
	Lead       Role = "lead"
	Specialist Role = "specialist"
	Critic     Role = "critic"
)

// Roles is the model id per role, and the only way role bindings are read
// (PR 3 swaps the three variables for BIGLAW_MODELS behind it).
type Roles struct{ Lead, Specialist, Critic string }

// Model is role's model id; an unset specialist or critic uses the lead's.
func (r Roles) Model(role Role) (string, error) {
	m, ok := map[Role]string{Lead: r.Lead, Specialist: r.Specialist, Critic: r.Critic}[role]
	if !ok {
		return "", fail(ErrNotConfigured, "unknown model role "+strconv.Quote(string(role)))
	}
	if m == "" {
		m = r.Lead
	}
	if m == "" {
		return "", fail(ErrNotConfigured, "no model is set for the "+string(role)+" role (set BIGLAW_MODEL_LEAD)")
	}
	return m, nil
}

// Message is one chat message in the OpenAI shape. Role is system, user,
// assistant or tool. Parts (user only) replaces Text with text and image
// parts; an image must be a base64 data:image/(png|jpeg|webp|gif) URL, so
// the endpoint never fetches anything on BigLaw's behalf.
type Message struct {
	Role       string     `json:"role"`
	Text       string     `json:"-"`
	Parts      []Part     `json:"-"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"` // assistant
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// MarshalJSON writes content as Parts, Text, or null (a tool-calls-only turn).
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	var content any = m.Text
	if len(m.Parts) > 0 {
		content = m.Parts
	} else if m.Text == "" && len(m.ToolCalls) > 0 {
		content = nil
	}
	return json.Marshal(struct {
		plain
		Content any `json:"content"`
	}{plain(m), content})
}

// Part is a text or image content part (TextPart, ImagePart).
type Part struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	ImageURL map[string]string `json:"image_url,omitempty"`
}

func TextPart(s string) Part { return Part{Type: "text", Text: s} }
func ImagePart(dataURL string) Part {
	return Part{Type: "image_url", ImageURL: map[string]string{"url": dataURL}}
}

// ToolCall is one call the model asked for; Function.Arguments is JSON text.
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"` // "function"
	Function Function `json:"function"`
}

type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a function the model may call. Parameters (a JSON Schema object)
// is sent byte for byte, so a fixed tool list keeps the prompt-cache prefix
// stable. Strict asks the endpoint to enforce the schema; Go validation
// stays authoritative, as local endpoints ignore it.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict,omitempty"`
}

// Request is one chat call.
type Request struct {
	Agent             string // "lead" or a subagent tool's name: routes Fake scripts, labels logs
	Role              Role   // which configured model runs it
	Model             string // overrides Role when set
	Messages          []Message
	Tools             []Tool
	ToolChoice        string // "", "auto", "none" or "required"
	ParallelToolCalls *bool
	MaxTokens         int
	Temperature       *float64
}

// Usage is what one call consumed; InputTokens excludes cache reads and
// writes. CostUSD is nil when the model has no price (unknown, never $0); a
// call to a local endpoint costs $0 and carries WattHours.
type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int
	Duration                                                     time.Duration
	CostUSD, WattHours                                           *float64
}

// Response is a successful call: text, tool calls, or both.
type Response struct {
	Model     string
	Text      string
	ToolCalls []ToolCall
	Truncated bool // the output stopped at MaxTokens
	Usage     Usage
}

// Chatter is what the agent loop calls: *Client, or *Fake in tests.
type Chatter interface {
	Chat(ctx context.Context, req Request) (*Response, error)
}

var (
	toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	imageURL = regexp.MustCompile(`^data:image/(png|jpeg|webp|gif);base64,[A-Za-z0-9+/]+={0,2}$`)
	msgRoles = map[string]bool{"system": true, "user": true, "assistant": true, "tool": true}
	choices  = map[string]bool{"": true, "auto": true, "none": true, "required": true}
)

// check refuses a malformed request (a programming error, not a model failure).
func (r *Request) check() error {
	bad := func(format string, a ...any) error { return fmt.Errorf("model: invalid request: "+format, a...) }
	if r.Agent == "" || len(r.Messages) == 0 || !choices[r.ToolChoice] {
		return bad("it needs an Agent, messages, and a tool choice of auto, none or required")
	}
	seen := map[string]bool{}
	for _, t := range r.Tools {
		if !toolName.MatchString(t.Name) || seen[t.Name] || !json.Valid(t.Parameters) || !bytes.HasPrefix(bytes.TrimSpace(t.Parameters), []byte("{")) {
			return bad("tool %q: the name is malformed or repeated, or the parameters are not a JSON object", t.Name)
		}
		seen[t.Name] = true
	}
	for i, m := range r.Messages {
		if !msgRoles[m.Role] || len(m.Parts) > 0 && (m.Role != "user" || m.Text != "") ||
			len(m.ToolCalls) > 0 && m.Role != "assistant" || (m.ToolCallID != "") != (m.Role == "tool") {
			return bad("message %d: unknown role, parts outside a user message or beside Text, tool calls outside an assistant message, or a tool call id missing or misplaced", i)
		}
		for _, p := range m.Parts {
			if !(p.Type == "text" && p.Text != "" && p.ImageURL == nil || p.Type == "image_url" && p.Text == "" && imageURL.MatchString(p.ImageURL["url"])) {
				return bad("message %d: a part is non-empty text or one base64 data:image/(png|jpeg|webp|gif) URL", i)
			}
		}
	}
	return nil
}

// Options configures New.
type Options struct {
	URL, Key   string // BIGLAW_MODEL_URL (the API base, e.g. http://localhost:11434/v1) and BIGLAW_MODEL_KEY
	Roles      Roles
	Prices     *Prices // nil: the built-in table
	LocalWatts float64
	Log        *slog.Logger
	Timeout    time.Duration // per HTTP attempt; 0 means 5 minutes
}

// Client calls one OpenAI-compatible endpoint.
type Client struct {
	endpoint, key string
	roles         Roles
	prices        *Prices
	watts         float64
	local, openAI bool
	http          *http.Client
	log           *slog.Logger
	attempts      int           // HTTP attempts per call
	backoff       time.Duration // first retry wait; doubles, plus up to 25% jitter
}

const (
	maxAttempts = 3
	maxWait     = 30 * time.Second // a longer Retry-After ends the call instead
	maxResponse = 32 << 20
)

// New builds a client. With no URL every call fails with ErrNotConfigured,
// so the server still runs without a model.
func New(o Options) (*Client, error) {
	c := &Client{key: o.Key, roles: o.Roles, prices: o.Prices, watts: o.LocalWatts, log: o.Log, attempts: maxAttempts, backoff: time.Second}
	if c.prices == nil {
		c.prices, _ = LoadPrices("")
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	// Redirects are not followed: prompts carry matter data and go only where configured.
	c.http = &http.Client{Timeout: o.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if o.URL != "" {
		u, err := url.Parse(o.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return nil, errors.New("model: the endpoint must be an http(s) URL without credentials")
		}
		c.local, c.openAI = isLocalHost(u.Hostname()), strings.EqualFold(u.Hostname(), "api.openai.com")
		u.Path, u.RawPath = strings.TrimRight(u.Path, "/")+"/chat/completions", ""
		c.endpoint = u.String()
	}
	return c, nil
}

// Chat runs one call. A failure matches one Err* kind (or context.Canceled)
// under errors.Is; a malformed request is a plain error.
func (c *Client) Chat(ctx context.Context, req Request) (*Response, error) {
	if err := req.check(); err != nil {
		return nil, err
	}
	model, err := req.Model, error(nil)
	if model == "" {
		if model, err = c.roles.Model(req.Role); err != nil {
			return nil, err
		}
	}
	if c.endpoint == "" {
		return nil, fail(ErrNotConfigured, "BIGLAW_MODEL_URL is not set")
	}
	w := map[string]any{"model": model, "messages": req.Messages, "stream": false}
	set := func(k string, ok bool, v any) {
		if ok {
			w[k] = v
		}
	}
	tools := make([]map[string]any, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = map[string]any{"type": "function", "function": t}
	}
	set("tools", len(tools) > 0, tools)
	set("tool_choice", req.ToolChoice != "", req.ToolChoice)
	set("parallel_tool_calls", req.ParallelToolCalls != nil, req.ParallelToolCalls)
	set("temperature", req.Temperature != nil, req.Temperature)
	set(map[bool]string{true: "max_completion_tokens", false: "max_tokens"}[c.openAI], req.MaxTokens > 0, req.MaxTokens) // api.openai.com refuses max_tokens for reasoning models
	body, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("model: encode request: %w", err)
	}
	raw, took, err := c.send(ctx, req.Agent, body)
	if err != nil {
		return nil, err
	}
	resp, err := parse(raw)
	if err != nil {
		return nil, err
	}
	resp.Model, resp.Usage.Duration = model, took
	c.prices.charge(model, &resp.Usage, c.local, c.watts, c.log)
	return resp, nil
}

// send POSTs body, retrying 429 and 500/502/503/504/529 after a backoff or
// the provider's Retry-After (up to maxWait), at most c.attempts attempts in
// all. It returns a 200's body and how long that attempt took.
func (c *Client) send(ctx context.Context, agent string, body []byte) ([]byte, time.Duration, error) {
	for attempt := 1; ; attempt++ {
		t0 := time.Now()
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, 0, errors.New("model: cannot build the HTTP request")
		}
		hreq.Header.Set("Content-Type", "application/json")
		if c.key != "" {
			hreq.Header.Set("Authorization", "Bearer "+c.key)
		}
		hresp, err := c.http.Do(hreq)
		if err != nil {
			return nil, 0, c.transportErr(ctx, err)
		}
		data, retry, err := c.answer(ctx, hresp)
		if err == nil {
			return data, time.Since(t0), nil
		}
		wait := RetryAfter(err)
		if wait == 0 {
			wait = c.backoff << (attempt - 1)
			wait += time.Duration(rand.Int64N(int64(wait)/4 + 1))
		}
		dl, hasDL := ctx.Deadline()
		if !retry || attempt >= c.attempts || wait > maxWait || hasDL && time.Until(dl) < wait {
			return nil, 0, err
		}
		c.log.Warn("model call failed; retrying", "agent", agent, "attempt", attempt, "of", c.attempts, "wait", wait, "error", err.Error())
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, 0, ctxErr(ctx)
		}
	}
}

// answer reads a response: a 200's body, or the classified failure and
// whether to retry. No upstream text reaches the error (it may quote the
// request or the key).
func (c *Client) answer(ctx context.Context, hresp *http.Response) (data []byte, retry bool, err error) {
	defer hresp.Body.Close()
	code := hresp.StatusCode
	if code == http.StatusOK {
		if data, err = io.ReadAll(io.LimitReader(hresp.Body, maxResponse+1)); err != nil {
			return nil, false, c.transportErr(ctx, err)
		}
		if len(data) > maxResponse {
			return nil, false, fail(ErrUnavailable, "the answer is over 32 MiB")
		}
		return data, false, nil
	}
	detail := "HTTP " + strconv.Itoa(code)
	switch {
	case code == http.StatusTooManyRequests:
		e := fail(ErrRateLimited, detail)
		if s, err := strconv.Atoi(strings.TrimSpace(hresp.Header.Get("Retry-After"))); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
		return nil, true, e
	case code == 401 || code == 403:
		if c.key == "" {
			return nil, false, fail(ErrNotConfigured, detail+": the endpoint wants a key and BIGLAW_MODEL_KEY is not set")
		}
		return nil, false, fail(ErrNotConfigured, detail+": the endpoint refused BIGLAW_MODEL_KEY")
	case code == 404:
		return nil, false, fail(ErrNotConfigured, detail+": check that BIGLAW_MODEL_URL is the API base (usually ending in /v1) and the model id exists there")
	case code >= 300 && code < 400:
		return nil, false, fail(ErrUnavailable, detail+": redirects are not followed; set BIGLAW_MODEL_URL to the final address")
	}
	return nil, code == 500 || code == 502 || code == 503 || code == 504 || code == 529, fail(ErrUnavailable, detail)
}

func ctxErr(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("model: %w", context.Canceled)
	}
	return fail(ErrTimeout, "the caller's deadline passed before the model answered")
}

// transportErr classifies a failure to connect or to read the answer. The
// URL is cut out (url.Error carries it, and a query may hold a key), and
// the key is masked should anything echo it.
func (c *Client) transportErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctxErr(ctx)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fail(ErrTimeout, "no answer within the client timeout")
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := strings.ReplaceAll(err.Error(), c.endpoint, "[endpoint]")
	if c.key != "" {
		msg = strings.ReplaceAll(msg, c.key, "[key]")
	}
	return fail(ErrUnavailable, "could not reach the endpoint: "+msg)
}

// parse turns a 200 body into a Response. No choice, or a choice with
// neither text nor tool calls, is ErrNoContent; anything that is not the
// chat-completions shape is ErrUnavailable.
func parse(raw []byte) (*Response, error) {
	var w struct {
		Choices []struct {
			Message struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt   int `json:"prompt_tokens"`
			Complete int `json:"completion_tokens"`
			Details  struct {
				Cached int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CacheWrite int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &w) != nil {
		return nil, fail(ErrUnavailable, "the answer is not OpenAI chat-completions JSON")
	}
	if len(w.Choices) == 0 {
		return nil, fail(ErrNoContent, "the answer has no choices")
	}
	ch := w.Choices[0]
	r := &Response{Truncated: ch.FinishReason == "length"}
	if ch.Message.Content != nil {
		r.Text = *ch.Message.Content
	}
	for i, tc := range ch.Message.ToolCalls {
		if tc.Function.Name == "" {
			return nil, fail(ErrUnavailable, "the answer has a tool call without a function name")
		}
		args := string(tc.Function.Arguments) // a JSON string per the protocol; an object from some servers
		var s string
		if json.Unmarshal(tc.Function.Arguments, &s) == nil {
			args = s
		}
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		if tc.ID == "" {
			tc.ID = "call_" + strconv.Itoa(i) // some local servers omit it
		}
		r.ToolCalls = append(r.ToolCalls, ToolCall{tc.ID, "function", Function{tc.Function.Name, args}})
	}
	if strings.TrimSpace(r.Text) == "" && len(r.ToolCalls) == 0 {
		return nil, fail(ErrNoContent, "the answer has neither text nor tool calls"+map[bool]string{true: " (cut off at max_tokens)"}[r.Truncated])
	}
	u := w.Usage
	r.Usage = Usage{InputTokens: max(0, u.Prompt-max(0, u.Details.Cached)-max(0, u.CacheWrite)), OutputTokens: max(0, u.Complete),
		CacheReadTokens: max(0, u.Details.Cached), CacheWriteTokens: max(0, u.CacheWrite)}
	return r, nil
}
