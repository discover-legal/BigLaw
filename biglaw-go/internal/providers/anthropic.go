// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/discover-legal/biglaw-go/internal/config"
)

type AnthropicProvider struct {
	client *anthropic.Client
	cfg    *config.Config
}

func NewAnthropicProvider(cfg *config.Config) *AnthropicProvider {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.Anthropic.APIKey),
	}
	if cfg.Anthropic.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.Anthropic.BaseURL))
	}
	client := anthropic.NewClient(opts...)
	return &AnthropicProvider{client: &client, cfg: cfg}
}

func (p *AnthropicProvider) Chat(params ChatParams) (*ChatResponse, error) {
	t0 := time.Now()

	// Build system prompt blocks (with optional cache_control).
	var systemBlocks []anthropic.TextBlockParam
	if params.System != "" {
		blk := anthropic.TextBlockParam{Text: params.System}
		if params.CacheSystem {
			blk.CacheControl = anthropic.CacheControlEphemeralParam{}
		}
		systemBlocks = []anthropic.TextBlockParam{blk}
	}

	// Convert internal messages to SDK messages.
	msgs, err := toSDKMessages(params.Messages)
	if err != nil {
		return nil, err
	}

	// Convert tools.
	tools := toSDKTools(params.Tools)

	// Build request params.
	reqParams := anthropic.MessageNewParams{
		Model:     anthropic.Model(params.Model),
		MaxTokens: int64(params.MaxTokens),
		System:    systemBlocks,
		Messages:  msgs,
	}
	if len(tools) > 0 {
		reqParams.Tools = tools
	}

	// Extended thinking.
	if params.Thinking != nil {
		reqParams.Thinking = anthropic.ThinkingConfigParamOfEnabled(int64(params.Thinking.BudgetTokens))
	}

	msg, err := p.client.Messages.New(context.Background(), reqParams)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return fromMessage(msg, time.Since(t0).Milliseconds()), nil
}

// ─── SDK type converters ──────────────────────────────────────────────────────

func toSDKMessages(messages []Message) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, m := range messages {
		blocks, err := toSDKContentBlocks(m.Content)
		if err != nil {
			return nil, err
		}
		switch m.Role {
		case "user":
			out = append(out, anthropic.NewUserMessage(blocks...))
		case "assistant":
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		default:
			out = append(out, anthropic.NewUserMessage(blocks...))
		}
	}
	return out, nil
}

func toSDKContentBlocks(content interface{}) ([]anthropic.ContentBlockParamUnion, error) {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil, nil
		}
		return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(v)}, nil
	case []ContentBlock:
		var blocks []anthropic.ContentBlockParamUnion
		for _, b := range v {
			switch b.Type {
			case BlockText:
				blocks = append(blocks, anthropic.NewTextBlock(b.Text))
			case BlockToolResult:
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolUseID, b.Content, false))
			case BlockToolUse:
				inputJSON, _ := json.Marshal(b.Input)
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, json.RawMessage(inputJSON), b.Name))
			}
		}
		return blocks, nil
	default:
		return nil, nil
	}
}

func toSDKTools(tools []ToolParam) []anthropic.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schemaJSON, _ := json.Marshal(t.InputSchema)
		var schema anthropic.ToolInputSchemaParam
		_ = json.Unmarshal(schemaJSON, &schema)
		out = append(out, anthropic.ToolUnionParamOfTool(schema, t.Name))
	}
	return out
}

func fromMessage(msg *anthropic.Message, durationMs int64) *ChatResponse {
	resp := &ChatResponse{
		DurationMs: durationMs,
		Usage: Usage{
			InputTokens:  int(msg.Usage.InputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
		},
	}

	cacheWrite := int(msg.Usage.CacheCreationInputTokens)
	cacheRead := int(msg.Usage.CacheReadInputTokens)
	if cacheWrite > 0 {
		resp.Usage.CacheWriteTokens = &cacheWrite
	}
	if cacheRead > 0 {
		resp.Usage.CacheReadTokens = &cacheRead
	}

	switch msg.StopReason {
	case anthropic.StopReasonEndTurn:
		resp.StopReason = StopEndTurn
	case anthropic.StopReasonToolUse:
		resp.StopReason = StopToolUse
	case anthropic.StopReasonMaxTokens:
		resp.StopReason = StopMaxTokens
	default:
		resp.StopReason = StopEndTurn
	}

	for _, blk := range msg.Content {
		switch {
		case blk.Type == "text":
			tb := blk.AsText()
			resp.Content = append(resp.Content, ContentBlock{
				Type: BlockText,
				Text: tb.Text,
			})
		case blk.Type == "thinking":
			th := blk.AsThinking()
			resp.Content = append(resp.Content, ContentBlock{
				Type:     BlockThinking,
				Thinking: th.Thinking,
			})
		case blk.Type == "tool_use":
			tu := blk.AsToolUse()
			var input map[string]interface{}
			_ = json.Unmarshal(tu.Input, &input)
			resp.Content = append(resp.Content, ContentBlock{
				Type:  BlockToolUse,
				ID:    tu.ID,
				Name:  tu.Name,
				Input: input,
			})
		}
	}
	return resp
}
