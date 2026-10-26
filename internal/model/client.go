// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package model is BigLaw's one model client (OpenAI-compatible chat completions with tool calls),
// with typed failures, bounded retries, pricing and watt-hour metering. Tests use Fake.
package model

import (
	"bytes"
	"cmp"
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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Role names which configured model runs a call.
type Role string

const (
	Lead       Role = "lead"
	Specialist Role = "specialist"
	Critic     Role = "critic"
	Vision     Role = "vision" // images and scanned pages (PR 3c)
	Embed      Role = "embed"  // embeddings for hybrid search (Client.Embed)
)

// Roles is the model id per role (BIGLAW_MODELS), and the only way role bindings are read.
type Roles struct{ Lead, Specialist, Critic, Vision, Embed string }

// Model is role's model id. An unset specialist or critic uses the lead's; vision and embed have
// no fallback (a text model is never sent images, and an unset embed role means lexical search).
func (r Roles) Model(role Role) (string, error) {
	m, ok := map[Role]string{Lead: r.Lead, Specialist: r.Specialist, Critic: r.Critic, Vision: r.Vision, Embed: r.Embed}[role]
	if !ok {
		return "", fail(ErrNotConfigured, "unknown model role "+strconv.Quote(string(role)))
	}
	if role == Specialist || role == Critic {
		m = cmp.Or(m, r.Lead)
	}
	if m == "" {
		return "", fail(ErrNotConfigured, "no model is set for the "+string(role)+" role (set "+string(role)+"=<model id> in BIGLAW_MODELS)")
	}
	return m, nil
}

// Message is one chat message (role system, user, assistant or tool). Content is a string, nil (sent
// as "", or null on a tool-calls-only assistant turn) or, for a user, a non-empty []Part whose images are base64 data URLs, never remote.
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"` // assistant
	ToolCallID string     `json:"tool_call_id,omitempty"`
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

// ToolCall is one call the model asked for; Function.Arguments is a JSON object as text.
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"` // "function"
	Function Function `json:"function"`
}

type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a function the model may call; Parameters is a JSON Schema object. Strict asks the
// endpoint to enforce it (Go validation stays authoritative: local endpoints ignore it).
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

// Usage is what one call consumed; InputTokens excludes cache reads and writes. CostUSD is nil
// when the model has no price (unknown, never $0); WattHours is set when BIGLAW_LOCAL_WATTS is.
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
		parts, isParts := m.Content.([]Part)
		if _, isText := m.Content.(string); !msgRoles[m.Role] || !isText && !isParts && m.Content != nil || isParts && (m.Role != "user" || len(parts) == 0) ||
			len(m.ToolCalls) > 0 && m.Role != "assistant" || (m.ToolCallID != "") != (m.Role == "tool") {
			return bad("message %d: unknown role, content not a string, nil or a non-empty user []Part, tool calls outside an assistant message, or a tool call id missing or misplaced", i)
		}
		for _, p := range parts {
			if !(p.Type == "text" && p.Text != "" && p.ImageURL == nil || p.Type == "image_url" && p.Text == "" && imageURL.MatchString(p.ImageURL["url"])) {
				return bad("message %d: a part is non-empty text or one base64 data:image/(png|jpeg|webp|gif) URL", i)
			}
		}
	}
	return nil
}

// checkCalls refuses tool calls the agent loop cannot answer: a malformed name, a repeated id, or
// arguments that are not a UTF-8 JSON object. Client and Fake both apply it.
func checkCalls(calls []ToolCall) *Error {
	seen := map[string]bool{}
	for _, tc := range calls {
		if a := strings.TrimSpace(tc.Function.Arguments); !toolName.MatchString(tc.Function.Name) || seen[tc.ID] ||
			!json.Valid([]byte(a)) || !strings.HasPrefix(a, "{") || !utf8.ValidString(a) {
			return fail(ErrUnavailable, "the answer has a tool call with a malformed name, a repeated id, or arguments that are not a JSON object")
		}
		seen[tc.ID] = true
	}
	return nil
}

// Options configures New.
type Options struct {
	URL, Key   string // BIGLAW_MODEL_URL (the API base, e.g. http://localhost:11434/v1) and BIGLAW_MODEL_KEY
	Roles      Roles
	Prices     *Prices // nil: the built-in table
	LocalWatts float64 // BIGLAW_LOCAL_WATTS: > 0 records watt-hours on every call (pricing is unaffected)
	Log        *slog.Logger
	Timeout    time.Duration // per HTTP attempt; 0 means 5 minutes
}

// Client calls one OpenAI-compatible endpoint.
type Client struct {
	endpoint, key string
	embeddings    string // the /embeddings URL
	roles         Roles
	prices        *Prices
	watts         float64
	openAI        bool
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
	c := &Client{key: o.Key, roles: o.Roles, prices: cmp.Or(o.Prices, &Prices{builtin: builtinPrices}), watts: o.LocalWatts,
		log: cmp.Or(o.Log, slog.Default()), attempts: maxAttempts, backoff: time.Second}
	// Redirects are not followed: prompts carry matter data and go only where configured.
	c.http = &http.Client{Timeout: cmp.Or(o.Timeout, 5*time.Minute), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if o.URL != "" {
		u, err := url.Parse(o.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return nil, errors.New("model: the endpoint must be an http(s) URL without credentials")
		}
		c.openAI = strings.EqualFold(u.Hostname(), "api.openai.com")
		base := strings.TrimRight(u.Path, "/")
		u.Path, u.RawPath = base+"/chat/completions", ""
		c.endpoint = u.String()
		u.Path = base + "/embeddings"
		c.embeddings = u.String()
	}
	return c, nil
}

// Chat runs one call. A failure matches one Err* kind (or context.Canceled) under errors.Is;
// a malformed request is a plain error.
func (c *Client) Chat(ctx context.Context, req Request) (*Response, error) {
	if err := req.check(); err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		var err error
		if model, err = c.roles.Model(req.Role); err != nil {
			return nil, err
		}
	}
	if c.endpoint == "" {
		return nil, fail(ErrNotConfigured, "BIGLAW_MODEL_URL is not set")
	}
	msgs := slices.Clone(req.Messages) // a nil Content is sent as "", except on a tool-calls-only assistant turn
	for i, m := range msgs {
		if m.Content == nil && len(m.ToolCalls) == 0 {
			msgs[i].Content = ""
		}
	}
	w := map[string]any{"model": model, "messages": msgs, "stream": false}
	set := func(k string, ok bool, v any) {
		if ok {
			w[k] = v
		}
	}
	tools := make([]map[string]any, len(req.Tools))
	for i, t := range req.Tools {
		t.Strict = t.Strict && c.openAI // strict mode only where it is known to be supported; Go validates everywhere
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
	raw, took, err := c.send(ctx, req.Agent, c.endpoint, body)
	if err != nil {
		return nil, err
	}
	resp, err := parse(raw) // a billed answer BigLaw refuses still reports its usage, on the *Error
	if resp == nil {
		return nil, err
	}
	resp.Model, resp.Usage.Duration = model, took
	c.prices.charge(model, &resp.Usage, c.watts, c.log)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// send POSTs body, retrying 429 and 500/502/503/504/529 (never a network error: the call may
// have run) at most c.attempts times. It returns a 200's body and the attempts' total time.
func (c *Client) send(ctx context.Context, agent, endpoint string, body []byte) ([]byte, time.Duration, error) {
	var took time.Duration
	for attempt := 1; ; attempt++ {
		t0 := time.Now()
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, 0, errors.New("model: cannot build the HTTP request")
		}
		hreq.Header.Set("Content-Type", "application/json")
		if c.key != "" {
			hreq.Header.Set("Authorization", "Bearer "+c.key)
		}
		hresp, err := c.http.Do(hreq)
		if err != nil {
			return nil, 0, transportErr(ctx, err)
		}
		data, retry, err := c.answer(ctx, hresp)
		if took += time.Since(t0); err == nil {
			return data, took, nil
		}
		if !retry || attempt >= c.attempts {
			return nil, 0, err
		}
		backoff := c.backoff << (attempt - 1) // plus up to 25% jitter; a retried failure is always an *Error
		wait := cmp.Or(err.(*Error).RetryAfter, backoff+time.Duration(rand.Int64N(int64(backoff)/4+1)))
		if dl, hasDL := ctx.Deadline(); wait > maxWait || hasDL && time.Until(dl) < wait {
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

// answer reads a response: a 200's body, or the classified *Error and whether to retry. Of an error
// body only the provider's error type and message reach the error (providerError: bounded, the key
// and anything key-, token- or URL-shaped redacted), never the rest (it may quote the request).
func (c *Client) answer(ctx context.Context, hresp *http.Response) (data []byte, retry bool, err error) {
	defer hresp.Body.Close()
	code := hresp.StatusCode
	if code == http.StatusOK {
		if data, err = io.ReadAll(io.LimitReader(hresp.Body, maxResponse+1)); err != nil {
			return nil, false, transportErr(ctx, err)
		}
		if len(data) > maxResponse {
			return nil, false, fail(ErrUnavailable, "the answer is over 32 MiB")
		}
		return data, false, nil
	}
	detail := "HTTP " + strconv.Itoa(code)
	if why := providerError(hresp.Body, c.key); why != "" { // the provider's own error type and message, bounded and redacted (bench7: a 400 "credit balance is too low" showed only as "HTTP 400")
		detail += " (" + why + ")"
	}
	switch {
	case code == http.StatusTooManyRequests:
		e := fail(ErrRateLimited, detail)
		if s, _ := strconv.ParseInt(strings.TrimSpace(hresp.Header.Get("Retry-After")), 10, 64); s > 0 { // overflow parses as MaxInt64
			e.RetryAfter = time.Duration(min(s, 86400)) * time.Second
		}
		return nil, true, e
	case code == 401 || code == 403 || code == 404:
		return nil, false, fail(ErrNotConfigured, detail+": check BIGLAW_MODEL_KEY, that BIGLAW_MODEL_URL is the API base (usually ending in /v1), and the model id")
	}
	return nil, code == 500 || code == 502 || code == 503 || code == 504 || code == 529, &Error{Kind: ErrUnavailable, Detail: detail, Status: code}
}

func ctxErr(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("model: %w", context.Canceled)
	}
	return fail(ErrTimeout, "the caller's deadline passed before the model answered")
}

// transportErr classifies a failure to connect or to read the answer into a fixed phrase: Go's
// error text can quote the host, port, URL or bytes the endpoint sent (an echoed key or prompt).
func transportErr(ctx context.Context, err error) error {
	var ne net.Error
	var dns *net.DNSError
	switch {
	case ctx.Err() != nil:
		return ctxErr(ctx)
	case errors.As(err, &ne) && ne.Timeout():
		return fail(ErrTimeout, "no answer within the client timeout")
	case errors.As(err, &dns):
		return fail(ErrUnavailable, "could not reach the endpoint: DNS lookup failed")
	case errors.Is(err, syscall.ECONNREFUSED):
		return fail(ErrUnavailable, "could not reach the endpoint: connection refused")
	}
	return fail(ErrUnavailable, "the connection failed or the answer is not valid HTTP")
}

// parse turns a 200 body into a Response. No usable choice is ErrNoContent; anything else that is
// not a chat completion is ErrUnavailable. Once usage is read, an error comes with the Response.
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
	if json.Unmarshal(raw, &w) != nil { // Status 200: an answer arrived (and may be billed), its usage unreadable
		return nil, &Error{Kind: ErrUnavailable, Detail: "the answer is not OpenAI chat-completions JSON", Status: 200}
	}
	u := w.Usage
	r := &Response{Usage: Usage{InputTokens: max(0, u.Prompt-max(0, u.Details.Cached)-max(0, u.CacheWrite)), OutputTokens: max(0, u.Complete),
		CacheReadTokens: max(0, u.Details.Cached), CacheWriteTokens: max(0, u.CacheWrite)}}
	bad := func(e *Error) (*Response, error) { e.Usage = &r.Usage; return r, e }
	if len(w.Choices) == 0 {
		return bad(fail(ErrNoContent, "the answer has no choices"))
	}
	ch := w.Choices[0]
	r.Truncated = ch.FinishReason == "length"
	if ch.Message.Content != nil {
		r.Text = *ch.Message.Content
	}
	for i, tc := range ch.Message.ToolCalls {
		args := string(tc.Function.Arguments) // a JSON string per the protocol; an object from some servers
		var s string
		if json.Unmarshal(tc.Function.Arguments, &s) == nil {
			args = s
		}
		id := cmp.Or(tc.ID, "call_"+strconv.Itoa(i)) // some local servers omit it
		r.ToolCalls = append(r.ToolCalls, ToolCall{id, "function", Function{tc.Function.Name, cmp.Or(strings.TrimSpace(args), "{}")}})
	}
	if e := checkCalls(r.ToolCalls); e != nil {
		return bad(e)
	}
	if strings.TrimSpace(r.Text) == "" && len(r.ToolCalls) == 0 {
		e := fail(ErrNoContent, "the answer has neither text nor tool calls"+map[bool]string{true: " (cut off at max_tokens)"}[r.Truncated])
		e.Truncated = r.Truncated
		return bad(e)
	}
	return r, nil
}
