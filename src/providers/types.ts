// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Normalized provider interface — abstracts Anthropic and Ollama (local) APIs
 * behind a common message format so the agent loop works identically for both.
 */

// ─── Content blocks ───────────────────────────────────────────────────────────

export interface ProviderTextBlock {
  type: "text";
  text: string;
}

export interface ProviderToolUseBlock {
  type: "tool_use";
  id: string;
  name: string;
  input: Record<string, unknown>;
}

export interface ProviderToolResultBlock {
  type: "tool_result";
  tool_use_id: string;
  content: string;
}

export type ProviderContentBlock =
  | ProviderTextBlock
  | ProviderToolUseBlock
  | ProviderToolResultBlock
  | ProviderThinkingBlock;

// ─── Messages ─────────────────────────────────────────────────────────────────

export interface ProviderMessage {
  role: "user" | "assistant";
  /** String shorthand for simple text messages; content block array otherwise */
  content: string | ProviderContentBlock[];
}

// ─── Tools ────────────────────────────────────────────────────────────────────

export interface ProviderTool {
  name: string;
  description?: string;
  input_schema: {
    type: "object";
    properties?: unknown;
    required?: string[];
    [key: string]: unknown;
  };
}

// ─── Chat params / response ───────────────────────────────────────────────────

export interface ChatParams {
  model: string;
  system: string;
  messages: ProviderMessage[];
  tools?: ProviderTool[];
  maxTokens: number;
  /** Cache the system prompt block (Anthropic only; silently ignored by other providers). */
  cacheSystem?: boolean;
  /** Enable extended thinking (Anthropic Opus/Sonnet only; requires maxTokens > budgetTokens). */
  thinking?: { budgetTokens: number };
}

export interface ProviderThinkingBlock {
  type: "thinking";
  thinking: string;
}

export interface ChatResponse {
  stopReason: "end_turn" | "tool_use" | "max_tokens";
  content: ProviderContentBlock[];
}

// ─── Provider interface ───────────────────────────────────────────────────────

export interface ModelProvider {
  chat(params: ChatParams): Promise<ChatResponse>;
}
