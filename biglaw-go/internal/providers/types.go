// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package providers

// ─── Provider abstraction ─────────────────────────────────────────────────────

type ContentBlockType string

const (
	BlockText       ContentBlockType = "text"
	BlockToolUse    ContentBlockType = "tool_use"
	BlockToolResult ContentBlockType = "tool_result"
	BlockThinking   ContentBlockType = "thinking"
	BlockImage      ContentBlockType = "image"
)

type ContentBlock struct {
	Type      ContentBlockType       `json:"type"`
	Text      string                 `json:"text,omitempty"`
	Thinking  string                 `json:"thinking,omitempty"`
	ID        string                 `json:"id,omitempty"`          // tool_use
	Name      string                 `json:"name,omitempty"`        // tool_use
	Input     map[string]interface{} `json:"input,omitempty"`       // tool_use
	ToolUseID string                 `json:"tool_use_id,omitempty"` // tool_result
	Content   string                 `json:"content,omitempty"`     // tool_result
	// Image blocks (vision). MediaType is an IANA image type
	// ("image/png", "image/jpeg", "image/webp", "image/gif"); Data is the
	// raw bytes base64-encoded (no data: prefix). Honored by the Anthropic
	// provider (image source block) and the OpenAI-compatible provider
	// (image_url data URL) so Qwen-VL and Claude both receive vision input.
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

// ImageBlock builds a vision content block from base64-encoded image bytes.
func ImageBlock(mediaType, base64Data string) ContentBlock {
	return ContentBlock{Type: BlockImage, MediaType: mediaType, Data: base64Data}
}

type Message struct {
	Role    string      `json:"role"`    // "user" | "assistant"
	Content interface{} `json:"content"` // string | []ContentBlock
}

type ToolParam struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type ChatParams struct {
	Model     string
	MaxTokens int
	System    string
	Messages  []Message
	Tools     []ToolParam
	// CacheSystem hints that the system prompt is cacheable. Provider-agnostic;
	// the OpenAI-compatible backend ignores it.
	CacheSystem bool
	// ReasoningEffort, when non-empty ("low"/"medium"/"high"), asks a
	// reasoning-capable model to think harder. Sent as the OpenAI-standard
	// reasoning_effort field (honored by o-series, OpenRouter, DeepSeek-R1, and
	// many compatible servers); endpoints that don't support it ignore it.
	ReasoningEffort string
	// JSONMode constrains the decoder to emit a single valid JSON value — no
	// prose preamble, no markdown fences. Honored by the OpenAI-compatible
	// provider (Ollama/LM Studio/DashScope) via response_format. Set it on
	// structured-extraction calls.
	JSONMode bool
	// Temperature, when non-nil, overrides the model's sampling temperature.
	// Lower values make the model more deterministic and far more likely to
	// copy source text verbatim instead of paraphrasing — important for
	// citation quotes, which must match the source to be machine-verifiable.
	// nil leaves the server default (Ollama defaults qwen to ~0.8).
	Temperature *float64
}

type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheWriteTokens *int
	CacheReadTokens  *int
}

type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
)

type ChatResponse struct {
	StopReason StopReason
	Content    []ContentBlock
	Usage      Usage
	DurationMs int64
}

type Provider interface {
	Chat(params ChatParams) (*ChatResponse, error)
}
