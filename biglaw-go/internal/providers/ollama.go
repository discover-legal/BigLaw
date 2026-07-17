// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Ollama / generic OpenAI-compatible provider.
// Used for LM Studio, Jan, vLLM, llama.cpp, and local inference.

package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/routing"
)

type OllamaProvider struct {
	baseURL                string
	apiKey                 string
	useMaxCompletionTokens bool
	client                 *http.Client
	// limiter throttles the model-call path to a configured per-minute ceiling
	// (PROVIDER_MAX_CALLS_PER_MIN). nil = unlimited (the default). Shared globally.
	limiter *rateLimiter
}

const maxProviderResponseBytes int64 = 32 << 20

func NewOllamaProvider(cfg *config.Config) *OllamaProvider {
	baseURL := cfg.Local.OllamaURL
	apiKey := "ollama"
	if !cfg.Local.OllamaEnabled && cfg.Local.LocalInferenceURL != "" {
		apiKey = cfg.Local.LocalInferenceKey
	}
	if cfg.Local.LocalInferenceURL != "" {
		baseURL = cfg.Local.LocalInferenceURL
		apiKey = cfg.Local.LocalInferenceKey
	}
	p := NewOpenAICompatProvider(baseURL, apiKey)
	// A model spilling to CPU (e.g. 14B on an 8GB GPU) can take longer than the default 300s
	// for a long-form generation; raise the per-call timeout for local inference when set.
	if cfg.Local.RequestTimeoutSec > 0 {
		p.client.Timeout = time.Duration(cfg.Local.RequestTimeoutSec) * time.Second
	}
	return p
}

// NewOpenAICompatProvider builds a provider against any OpenAI-compatible chat
// completions endpoint — local (Ollama, LM Studio, vLLM, llama.cpp) or hosted
// (DashScope/Qwen, Moonshot/Kimi, Zhipu/GLM, OpenAI, DeepSeek). baseURL may or
// may not include a version suffix; Chat() appends the completions path.
func NewOpenAICompatProvider(baseURL, apiKey string) *OllamaProvider {
	// The OpenAI convention (and our .env examples) is a base URL that already
	// ends in /v1 — e.g. http://localhost:11434/v1. Chat() appends the full
	// /v1/chat/completions path, so strip a trailing /v1 to avoid /v1/v1/.
	// DashScope's compatible-mode path ends in /compatible-mode/v1 — the same
	// /v1 strip applies. Zhipu/Z.ai bases end in /v4 (api.z.ai/api/paas/v4):
	// a non-/v1 version suffix is KEPT and Chat() appends only
	// /chat/completions — appending /v1 there 404s every call.
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return &OllamaProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		// See openAIChatRequest: OpenAI-hosted models take a different
		// token-cap parameter than local OpenAI-compatible servers.
		useMaxCompletionTokens: strings.Contains(baseURL, "api.openai.com"),
		client:                 &http.Client{Timeout: 300 * time.Second},
		limiter:                globalRateLimiter(),
	}
}

// reVersionedBase matches a base URL whose last path segment is already an
// API version other than v1 (e.g. Zhipu/Z.ai's .../api/paas/v4).
var reVersionedBase = regexp.MustCompile(`/v[2-9][0-9]*$`)

// rejectsTemperature reports whether the target model refuses a sampling
// temperature override. OpenAI-hosted gpt-5.x and o-series reasoning models
// return HTTP 400 for any temperature other than the default, so the
// override is dropped rather than failing the call. Local OpenAI-compatible
// servers accept temperature for every model.
func rejectsTemperature(openAIHosted bool, model string) bool {
	if !openAIHosted {
		return false
	}
	return strings.HasPrefix(model, "gpt-5") ||
		strings.HasPrefix(model, "o1") ||
		strings.HasPrefix(model, "o3") ||
		strings.HasPrefix(model, "o4")
}

// openAIChatRequest matches the OpenAI/Ollama chat completions format.
// Exactly one of MaxTokens / MaxCompletionTokens is set per request:
// OpenAI-hosted models (gpt-5.x, o-series) reject max_tokens outright and
// require max_completion_tokens, while local OpenAI-compatible servers
// (Ollama, LM Studio, vLLM, llama.cpp) speak the original max_tokens.
type openAIChatRequest struct {
	Model               string             `json:"model"`
	Messages            []openAIMessage    `json:"messages"`
	Tools               []openAITool       `json:"tools,omitempty"`
	MaxTokens           int                `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                `json:"max_completion_tokens,omitempty"`
	Stream              bool               `json:"stream"`
	ResponseFormat      *openAIResponseFmt `json:"response_format,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
	Temperature         *float64           `json:"temperature,omitempty"`
	Thinking            *openAIThinking    `json:"thinking,omitempty"`
}

// openAIThinking is Zhipu/Z.ai's hybrid-reasoning toggle on the OpenAI-compat
// endpoint: {"thinking":{"type":"enabled"|"disabled"}}. GLM 4.5+ / 5.x default
// to enabled; disabling trades reasoning depth for large speed/cost wins.
// Driven by MODEL_THINKING (empty = omit the field, provider default applies).
type openAIThinking struct {
	Type string `json:"type"`
}

// openAIResponseFmt requests JSON-constrained decoding. Ollama and LM Studio
// honor {"type":"json_object"} on the OpenAI-compatible endpoint, guaranteeing
// a single valid JSON value with no prose preamble or markdown fences.
type openAIResponseFmt struct {
	Type string `json:"type"`
}

// openAIMessage carries either a plain string in Content (the common case) or
// a multimodal parts array (text + image_url) for vision requests. Content is
// typed as interface{} so a single struct serves both shapes — Qwen-VL, GPT-4o,
// and llama-vision all accept the OpenAI multimodal content array. For tool
// calling it also carries an assistant turn's tool_calls and links a tool
// result back to its call via tool_call_id.
type openAIMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// openAIToolCall is the OpenAI/Ollama function-call shape. Arguments is a JSON
// string (the model's serialized arguments), not a nested object.
type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openAIContentPart is one element of a multimodal content array.
type openAIContentPart struct {
	Type     string             `json:"type"` // "text" | "image_url"
	Text     string             `json:"text,omitempty"`
	ImageURL *openAIImageURLRef `json:"image_url,omitempty"`
}

type openAIImageURLRef struct {
	URL string `json:"url"` // data:<media-type>;base64,<data> or an https URL
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
			// ReasoningContent carries a reasoning-capable model's chain-of-thought
			// when it is billed separately from the visible answer (Z.ai/GLM
			// "reasoning_content", DeepSeek-R1 style). A response with empty Content
			// but non-empty ReasoningContent means the model spent its whole
			// completion budget thinking — the signal to retry with thinking off.
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (p *OllamaProvider) Chat(params ChatParams) (*ChatResponse, error) {
	t0 := time.Now()

	// Client-side throttle: block until the per-minute budget admits this call.
	// No-op unless PROVIDER_MAX_CALLS_PER_MIN is set.
	p.limiter.acquire()

	// Build messages — Ollama uses a flat OpenAI-style format.
	// Prepend system message if present.
	var msgs []openAIMessage
	if params.System != "" {
		msgs = append(msgs, openAIMessage{Role: "system", Content: params.System})
	}
	for _, m := range params.Messages {
		switch v := m.Content.(type) {
		case string:
			msgs = append(msgs, openAIMessage{Role: m.Role, Content: v})
		case []ContentBlock:
			// If the block list carries any image, emit an OpenAI multimodal
			// parts array (text + image_url); otherwise flatten to a plain
			// string (not all text-only models accept the array form).
			hasImage := false
			for _, b := range v {
				if b.Type == BlockImage && b.Data != "" {
					hasImage = true
					break
				}
			}
			if hasImage {
				var parts []openAIContentPart
				for _, b := range v {
					switch b.Type {
					case BlockText:
						if b.Text != "" {
							parts = append(parts, openAIContentPart{Type: "text", Text: b.Text})
						}
					case BlockToolResult:
						if b.Content != "" {
							parts = append(parts, openAIContentPart{Type: "text", Text: b.Content})
						}
					case BlockImage:
						if b.Data != "" {
							mt := b.MediaType
							if mt == "" {
								mt = "image/png"
							}
							parts = append(parts, openAIContentPart{
								Type:     "image_url",
								ImageURL: &openAIImageURLRef{URL: "data:" + mt + ";base64," + b.Data},
							})
						}
					}
				}
				msgs = append(msgs, openAIMessage{Role: m.Role, Content: parts})
				continue
			}
			// Tool-calling flow. An assistant turn carries text + tool_use
			// blocks → one message with tool_calls; a user turn carries
			// tool_result blocks → one OpenAI "tool" message per result (linked
			// by tool_call_id). This is what makes multi-turn agentic tool use
			// work on the OpenAI-compatible path (Ollama/qwen, DashScope, …).
			var sb strings.Builder
			var toolCalls []openAIToolCall
			for _, b := range v {
				switch b.Type {
				case BlockText:
					sb.WriteString(b.Text)
				case BlockToolUse:
					args, _ := json.Marshal(b.Input)
					if len(args) == 0 || string(args) == "null" {
						args = []byte("{}")
					}
					tc := openAIToolCall{ID: b.ID, Type: "function"}
					tc.Function.Name = b.Name
					tc.Function.Arguments = string(args)
					toolCalls = append(toolCalls, tc)
				case BlockToolResult:
					msgs = append(msgs, openAIMessage{
						Role:       "tool",
						ToolCallID: b.ToolUseID,
						Content:    b.Content,
					})
				}
			}
			if len(toolCalls) > 0 {
				// content may be null on an assistant tool-call turn.
				var content interface{}
				if sb.Len() > 0 {
					content = sb.String()
				}
				msgs = append(msgs, openAIMessage{Role: m.Role, Content: content, ToolCalls: toolCalls})
			} else if sb.Len() > 0 {
				msgs = append(msgs, openAIMessage{Role: m.Role, Content: sb.String()})
			}
		}
	}

	bareModel := routing.ResolveModelID(params.Model)
	reqBody := openAIChatRequest{
		Model:    bareModel,
		Messages: msgs,
		Stream:   false,
	}
	// Thinking-aware output budget: on a versioned Zhipu-style endpoint (…/v4) with
	// thinking enabled-or-default, reasoning tokens are billed into the completion and
	// emitted BEFORE the visible answer — so a structured output at its raw cap can be
	// truncated to nothing. Inflate the cap by MODEL_THINKING_TOKENS_FACTOR so the
	// answer survives. Only for Zhipu-style bases (reVersionedBase); local/OpenAI
	// endpoints are untouched.
	maxTok := params.MaxTokens
	if maxTok > 0 && thinkingNotDisabled() && reVersionedBase.MatchString(p.baseURL) {
		maxTok *= thinkingTokensFactor()
	}
	if p.useMaxCompletionTokens {
		reqBody.MaxCompletionTokens = maxTok
	} else {
		reqBody.MaxTokens = maxTok
	}
	for _, t := range params.Tools {
		reqBody.Tools = append(reqBody.Tools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	if params.JSONMode {
		reqBody.ResponseFormat = &openAIResponseFmt{Type: "json_object"}
	}
	if params.ReasoningEffort != "" {
		reqBody.ReasoningEffort = params.ReasoningEffort
	}
	if params.Temperature != nil && !rejectsTemperature(p.useMaxCompletionTokens, bareModel) {
		reqBody.Temperature = params.Temperature
	}
	if v := os.Getenv("MODEL_THINKING"); v == "enabled" || v == "disabled" {
		reqBody.Thinking = &openAIThinking{Type: v}
	}

	url := p.baseURL + "/v1/chat/completions"
	// A base that already carries a non-/v1 version segment (Zhipu/Z.ai's
	// /api/paas/v4) gets only the completions path — /v4/v1/... 404s.
	if reVersionedBase.MatchString(p.baseURL) {
		url = p.baseURL + "/chat/completions"
	}

	body, _ := json.Marshal(reqBody)
	chatResp, err := p.sendChat(body, url)
	if err != nil {
		return nil, err
	}

	// Thinking-aware retry: a response that came back with EMPTY content but spent its
	// budget on reasoning (reasoning_content present, or finish_reason=length) burned
	// the whole cap thinking. When thinking is enabled-or-default, retry ONCE with
	// thinking explicitly disabled so the model produces visible text.
	if p.shouldRetryThinkingDisabled(reqBody, chatResp) {
		slog.Warn("provider returned empty content with reasoning/length finish; retrying once with thinking disabled",
			"model", bareModel, "finishReason", firstFinishReason(chatResp))
		retryBody := reqBody
		retryBody.Thinking = &openAIThinking{Type: "disabled"}
		if b2, mErr := json.Marshal(retryBody); mErr == nil {
			if r2, err2 := p.sendChat(b2, url); err2 == nil {
				chatResp = r2
			}
		}
	}

	stop := StopEndTurn
	var content []ContentBlock
	if len(chatResp.Choices) > 0 {
		ch := chatResp.Choices[0]
		if ch.Message.Content != "" {
			content = append(content, ContentBlock{Type: BlockText, Text: ch.Message.Content})
		}
		if len(ch.Message.ToolCalls) > 0 {
			// The model asked to call tools — surface them as tool_use blocks so
			// the agentic loop executes them and feeds results back next turn.
			stop = StopToolUse
			for i, tc := range ch.Message.ToolCalls {
				var input map[string]interface{}
				if tc.Function.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
				}
				if input == nil {
					input = map[string]interface{}{}
				}
				id := tc.ID
				if id == "" { // some local servers omit the id; synthesize a stable one
					id = fmt.Sprintf("call_%d", i)
				}
				content = append(content, ContentBlock{
					Type:  BlockToolUse,
					ID:    id,
					Name:  tc.Function.Name,
					Input: input,
				})
			}
		} else if ch.FinishReason == "length" {
			stop = StopMaxTokens
		}
	}
	if len(content) == 0 {
		content = []ContentBlock{{Type: BlockText, Text: ""}}
	}

	return &ChatResponse{
		StopReason: stop,
		Content:    content,
		Usage: Usage{
			InputTokens:  chatResp.Usage.PromptTokens,
			OutputTokens: chatResp.Usage.CompletionTokens,
		},
		DurationMs: time.Since(t0).Milliseconds(),
	}, nil
}

// sendChat POSTs one chat request and decodes the reply, retrying transient HTTP
// failures (429/5xx/529) with Retry-After honored when present, else exponential
// backoff with jitter — bounded by maxProviderRetries and the client timeout budget.
// Each retry is logged at WARN with the attempt count. A network error or a
// non-retryable status returns immediately.
func (p *OllamaProvider) sendChat(body []byte, url string) (*openAIChatResponse, error) {
	start := time.Now()
	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("ollama: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.apiKey)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			// Transport error (timeout, connection refused): not an HTTP status —
			// surface it rather than spin (the client already applied its timeout).
			return nil, fmt.Errorf("ollama: do request: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			responseBody, rerr := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes+1))
			resp.Body.Close()
			if rerr != nil {
				return nil, fmt.Errorf("ollama: read response: %w", rerr)
			}
			if int64(len(responseBody)) > maxProviderResponseBytes {
				return nil, fmt.Errorf("ollama: response exceeds %d MiB limit", maxProviderResponseBytes>>20)
			}
			var chatResp openAIChatResponse
			derr := json.Unmarshal(responseBody, &chatResp)
			if derr != nil {
				return nil, fmt.Errorf("ollama: decode response: %w", derr)
			}
			return &chatResp, nil
		}

		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		retryAfter := resp.Header.Get("Retry-After")
		resp.Body.Close()
		lastErr = fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, string(b))

		if !retryableStatus(resp.StatusCode) || attempt >= maxProviderRetries {
			return nil, lastErr
		}

		wait, honored := parseRetryAfter(retryAfter)
		if !honored {
			wait = backoffDuration(attempt)
		}
		if wait > maxBackoff {
			wait = maxBackoff
		}
		// Respect the client timeout budget: don't sleep past what the caller allotted.
		if p.client.Timeout > 0 && time.Since(start)+wait > p.client.Timeout {
			return nil, lastErr
		}
		slog.Warn("provider call failed; backing off and retrying",
			"status", resp.StatusCode, "attempt", attempt+1, "maxAttempts", maxProviderRetries+1,
			"waitMs", wait.Milliseconds(), "retryAfterHonored", honored)
		time.Sleep(wait)
	}
}

// shouldRetryThinkingDisabled reports whether an empty-content response is the
// thinking-burned-the-budget case worth one disabled-thinking retry: thinking is
// enabled-or-default, the request didn't already disable it, and the first choice
// has no content and no tool calls but carries reasoning or a length finish_reason.
func (p *OllamaProvider) shouldRetryThinkingDisabled(req openAIChatRequest, resp *openAIChatResponse) bool {
	if !thinkingNotDisabled() {
		return false
	}
	if req.Thinking != nil && req.Thinking.Type == "disabled" {
		return false // already disabled — nothing to fall back to
	}
	if resp == nil || len(resp.Choices) == 0 {
		return false
	}
	ch := resp.Choices[0]
	if strings.TrimSpace(ch.Message.Content) != "" || len(ch.Message.ToolCalls) > 0 {
		return false // a real answer (text or a tool call) — leave it
	}
	return strings.TrimSpace(ch.Message.ReasoningContent) != "" || ch.FinishReason == "length"
}

// firstFinishReason is a small logging helper.
func firstFinishReason(resp *openAIChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].FinishReason
}
