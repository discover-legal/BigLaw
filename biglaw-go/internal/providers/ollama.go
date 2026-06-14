// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Ollama / generic OpenAI-compatible provider.
// Used for LM Studio, Jan, vLLM, llama.cpp, and local inference.

package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
}

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
	return NewOpenAICompatProvider(baseURL, apiKey)
}

// NewOpenAICompatProvider builds a provider against any OpenAI-compatible chat
// completions endpoint — local (Ollama, LM Studio, vLLM, llama.cpp) or hosted
// (DashScope/Qwen, Moonshot/Kimi, Zhipu/GLM, OpenAI, DeepSeek). baseURL may or
// may not include a trailing /v1; Chat() always appends /v1/chat/completions.
func NewOpenAICompatProvider(baseURL, apiKey string) *OllamaProvider {
	// The OpenAI convention (and our .env examples) is a base URL that already
	// ends in /v1 — e.g. http://localhost:11434/v1. Chat() appends the full
	// /v1/chat/completions path, so strip a trailing /v1 to avoid /v1/v1/.
	// DashScope's compatible-mode path ends in /compatible-mode/v1 — the same
	// /v1 strip applies.
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return &OllamaProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		// See openAIChatRequest: OpenAI-hosted models take a different
		// token-cap parameter than local OpenAI-compatible servers.
		useMaxCompletionTokens: strings.Contains(baseURL, "api.openai.com"),
		client:                 &http.Client{Timeout: 120 * time.Second},
	}
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
// and llama-vision all accept the OpenAI multimodal content array.
type openAIMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
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
			Role    string `json:"role"`
			Content string `json:"content"`
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
			// Flatten to text (no tool_use support in all models).
			var sb strings.Builder
			for _, b := range v {
				if b.Type == BlockText {
					sb.WriteString(b.Text)
				} else if b.Type == BlockToolResult {
					sb.WriteString(b.Content)
				}
			}
			msgs = append(msgs, openAIMessage{Role: m.Role, Content: sb.String()})
		}
	}

	bareModel := routing.ResolveModelID(params.Model)
	reqBody := openAIChatRequest{
		Model:    bareModel,
		Messages: msgs,
		Stream:   false,
	}
	if p.useMaxCompletionTokens {
		reqBody.MaxCompletionTokens = params.MaxTokens
	} else {
		reqBody.MaxTokens = params.MaxTokens
	}
	if params.JSONMode {
		reqBody.ResponseFormat = &openAIResponseFmt{Type: "json_object"}
	}
	if params.ReasoningEffort != "" {
		reqBody.ReasoningEffort = params.ReasoningEffort
	}

	body, _ := json.Marshal(reqBody)
	url := p.baseURL + "/v1/chat/completions"
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
		return nil, fmt.Errorf("ollama: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: HTTP %d: %s", resp.StatusCode, string(b))
	}

	var chatResp openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	text := ""
	if len(chatResp.Choices) > 0 {
		text = chatResp.Choices[0].Message.Content
	}

	return &ChatResponse{
		StopReason: StopEndTurn,
		Content:    []ContentBlock{{Type: BlockText, Text: text}},
		Usage: Usage{
			InputTokens:  chatResp.Usage.PromptTokens,
			OutputTokens: chatResp.Usage.CompletionTokens,
		},
		DurationMs: time.Since(t0).Milliseconds(),
	}, nil
}
