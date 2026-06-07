// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Ollama provider — uses the OpenAI-compatible REST API exposed by Ollama
 * (http://localhost:11434/v1 by default).
 *
 * Supports any model available in Ollama that supports tool_use
 * (e.g. llama3.1, llama3.2, mistral-nemo, qwen2.5, etc.).
 *
 * Tool-less models (e.g. phi3, tinyllama) will still work for non-tool agents —
 * the provider detects the tool_calls finish_reason and falls back gracefully.
 */

import OpenAI from "openai";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { assertPublicHttpUrl } from "../settings/index.js";
import type {
  ModelProvider,
  ChatParams,
  ChatResponse,
  ProviderContentBlock,
  ProviderMessage,
  ProviderTool,
} from "./types.js";

export class OllamaProvider implements ModelProvider {
  private readonly client: OpenAI;

  /**
   * @param baseUrl Full base URL including /v1 suffix, e.g. "http://localhost:11434/v1".
   *                Defaults to OLLAMA_URL config.
   * @param apiKey  API key string. Defaults to "ollama" (Ollama ignores this; LM Studio may use it).
   */
  constructor(baseUrl?: string, apiKey?: string) {
    const resolvedBaseUrl = baseUrl ?? `${Config.local.ollamaUrl}/v1`;
    if (resolvedBaseUrl &&
        !resolvedBaseUrl.startsWith("http://localhost") &&
        !resolvedBaseUrl.startsWith("http://127.")) {
      try {
        assertPublicHttpUrl(resolvedBaseUrl, "OLLAMA_URL/LOCAL_INFERENCE_URL");
      } catch (err) {
        logger.warn("Local inference URL may be unsafe", { error: (err as Error).message });
      }
    }
    if (resolvedBaseUrl?.startsWith("http://") &&
        !resolvedBaseUrl.startsWith("http://localhost") &&
        !resolvedBaseUrl.startsWith("http://127.")) {
      logger.warn("Local inference URL uses HTTP on non-loopback — traffic is not encrypted", { baseUrl: resolvedBaseUrl });
    }
    this.client = new OpenAI({
      baseURL: resolvedBaseUrl,
      apiKey: apiKey ?? "ollama",
    });
  }

  async chat(params: ChatParams): Promise<ChatResponse> {
    const messages = toOpenAIMessages(params.system, params.messages);
    const tools = params.tools?.length ? toOpenAITools(params.tools) : undefined;

    const t0 = Date.now();
    const completion = await this.client.chat.completions.create({
      model: params.model,
      max_tokens: params.maxTokens,
      messages,
      ...(tools ? { tools, tool_choice: "auto" } : {}),
    });
    const durationMs = Date.now() - t0;

    const choice = completion.choices[0];
    if (!choice) throw new Error("Ollama returned empty choices");

    const usage = {
      inputTokens:  completion.usage?.prompt_tokens     ?? 0,
      outputTokens: completion.usage?.completion_tokens ?? 0,
    };

    return { ...fromOpenAIChoice(choice), usage, durationMs };
  }
}

// ─── Conversion helpers ───────────────────────────────────────────────────────

function toOpenAIMessages(
  system: string,
  messages: ProviderMessage[],
): OpenAI.ChatCompletionMessageParam[] {
  const result: OpenAI.ChatCompletionMessageParam[] = [
    { role: "system", content: system },
  ];

  for (const m of messages) {
    if (typeof m.content === "string") {
      result.push({ role: m.role, content: m.content });
      continue;
    }

    if (m.role === "user") {
      // Tool results arrive as user-role blocks; split into separate tool messages
      const toolResults = m.content.filter((b) => b.type === "tool_result");
      const textBlocks = m.content.filter((b) => b.type === "text");

      for (const tr of toolResults) {
        if (tr.type !== "tool_result") continue;
        result.push({
          role: "tool",
          tool_call_id: tr.tool_use_id,
          content: tr.content,
        });
      }
      if (textBlocks.length) {
        result.push({
          role: "user",
          content: textBlocks.map((b) => (b.type === "text" ? b.text : "")).join("\n"),
        });
      }
    } else {
      // Assistant turn — may contain text + tool_use blocks
      const textBlocks = m.content.filter((b) => b.type === "text");
      const toolUseBlocks = m.content.filter((b) => b.type === "tool_use");

      const toolCalls: OpenAI.ChatCompletionMessageToolCall[] = toolUseBlocks
        .filter((b) => b.type === "tool_use")
        .map((b) => {
          if (b.type !== "tool_use") throw new Error("unreachable");
          return {
            id: b.id,
            type: "function" as const,
            function: { name: b.name, arguments: JSON.stringify(b.input) },
          };
        });

      result.push({
        role: "assistant",
        content: textBlocks.map((b) => (b.type === "text" ? b.text : "")).join("\n") || null,
        ...(toolCalls.length ? { tool_calls: toolCalls } : {}),
      });
    }
  }

  return result;
}

function toOpenAITools(tools: ProviderTool[]): OpenAI.ChatCompletionTool[] {
  return tools.map((t) => ({
    type: "function" as const,
    function: {
      name: t.name,
      ...(t.description ? { description: t.description } : {}),
      parameters: t.input_schema,
    },
  }));
}

function fromOpenAIChoice(
  choice: OpenAI.ChatCompletion["choices"][number],
): ChatResponse {
  const content: ProviderContentBlock[] = [];
  // Reasoning models (gemma, qwen-thinking, deepseek-r1, …) served over an
  // OpenAI-compatible endpoint put the final answer in `content` and the
  // chain-of-thought in a separate `reasoning_content` field. If the budget
  // is consumed by reasoning, `content` comes back empty — fall back to the
  // reasoning text so the pipeline gets usable output instead of throwing.
  const msg = choice.message as typeof choice.message & { reasoning_content?: string | null };

  const answer =
    msg.content && msg.content.trim()
      ? msg.content
      : typeof msg.reasoning_content === "string" && msg.reasoning_content.trim()
        ? msg.reasoning_content
        : "";
  if (answer) {
    content.push({ type: "text", text: answer });
  }

  if (msg.tool_calls?.length) {
    for (const tc of msg.tool_calls) {
      let input: Record<string, unknown> = {};
      try {
        input = JSON.parse(tc.function.arguments) as Record<string, unknown>;
      } catch (err) {
        logger.warn("Ollama returned unparseable tool arguments", { tool: tc.function.name, args: tc.function.arguments?.slice(0, 200) });
        // Surface the parse error so the model sees a structured error rather
        // than the tool silently executing with an empty argument set.
        input = { _parse_error: (err as Error).message };
      }
      content.push({
        type: "tool_use",
        id: tc.id,
        name: tc.function.name,
        input,
      });
    }
  }

  let stopReason: "end_turn" | "tool_use" | "max_tokens" = "end_turn";
  if (choice.finish_reason === "tool_calls") stopReason = "tool_use";
  else if (choice.finish_reason === "length") stopReason = "max_tokens";

  return { stopReason, content };
}
