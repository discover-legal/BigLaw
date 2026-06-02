// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import type {
  ModelProvider,
  ChatParams,
  ChatResponse,
  ProviderContentBlock,
  ProviderMessage,
} from "./types.js";

export class AnthropicProvider implements ModelProvider {
  private readonly client: Anthropic;

  constructor() {
    this.client = new Anthropic({
      apiKey: Config.anthropic.apiKey,
      ...(Config.anthropic.baseUrl ? { baseURL: Config.anthropic.baseUrl } : {}),
    });
  }

  async chat(params: ChatParams): Promise<ChatResponse> {
    // System prompt — wrap in a cacheable block when requested.
    // Anthropic caches blocks ≥ 1024 tokens; shorter prompts are silently uncached.
    const system: string | Anthropic.TextBlockParam[] = params.cacheSystem
      ? [{ type: "text", text: params.system, cache_control: { type: "ephemeral" } }]
      : params.system;

    if (params.thinking) {
      // Extended thinking requires the beta client and budget_tokens < max_tokens.
      const betaMsg = await this.client.beta.messages.create({
        betas: ["interleaved-thinking-2025-05-14"],
        model: params.model,
        max_tokens: params.maxTokens,
        system,
        tools: params.tools as Anthropic.Tool[] | undefined,
        messages: params.messages.map(toAnthropicMessage),
        thinking: { type: "enabled", budget_tokens: params.thinking.budgetTokens },
      });
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const content = (betaMsg.content as any[]).map(fromAnyBlock);
      const stopReason = fromAnthropicStopReason(betaMsg.stop_reason);
      return { stopReason, content };
    }

    const msg = await this.client.messages.create({
      model: params.model,
      max_tokens: params.maxTokens,
      system,
      tools: params.tools as Anthropic.Tool[] | undefined,
      messages: params.messages.map(toAnthropicMessage),
    });

    const content = msg.content.map(fromAnthropicBlock);
    const stopReason = fromAnthropicStopReason(msg.stop_reason);
    return { stopReason, content };
  }
}

// ─── Conversion helpers ───────────────────────────────────────────────────────

function toAnthropicMessage(m: ProviderMessage): Anthropic.MessageParam {
  if (typeof m.content === "string") {
    return { role: m.role, content: m.content };
  }
  return {
    role: m.role,
    // Anthropic accepts the same block shapes we use internally — cast is safe
    content: m.content as Anthropic.ContentBlock[],
  };
}

function fromAnthropicBlock(b: Anthropic.ContentBlock): ProviderContentBlock {
  if (b.type === "text") return { type: "text", text: b.text };
  if (b.type === "tool_use") {
    return {
      type: "tool_use",
      id: b.id,
      name: b.name,
      input: b.input as Record<string, unknown>,
    };
  }
  return { type: "text", text: JSON.stringify(b) };
}

// Handles beta response blocks which include thinking blocks alongside standard ones.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function fromAnyBlock(b: any): ProviderContentBlock {
  if (b.type === "thinking") return { type: "thinking", thinking: b.thinking ?? "" };
  return fromAnthropicBlock(b as Anthropic.ContentBlock);
}

function fromAnthropicStopReason(
  reason: string | null,
): "end_turn" | "tool_use" | "max_tokens" {
  if (reason === "tool_use") return "tool_use";
  if (reason === "max_tokens") return "max_tokens";
  return "end_turn";
}
