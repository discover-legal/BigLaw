// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, version 3.
// See <https://www.gnu.org/licenses/gpl-3.0.html>

import { AnthropicProvider } from "./anthropic.js";
import { OllamaProvider } from "./ollama.js";
import { Config } from "../config.js";
import type { ModelProvider } from "./types.js";

export * from "./types.js";

export const OLLAMA_PREFIX = "ollama:";
/** Prefix for a generic OpenAI-compat local server (LM Studio, Jan, vLLM, llama.cpp). */
export const LOCAL_PREFIX = "local:";

/** True if this model ID targets an Ollama instance. */
export function isOllamaModel(modelId: string): boolean {
  return modelId.startsWith(OLLAMA_PREFIX);
}

/** True if this model ID targets a generic local OpenAI-compat server. */
export function isLocalModel(modelId: string): boolean {
  return modelId.startsWith(LOCAL_PREFIX);
}

/** Strip the "ollama:" prefix to get the bare Ollama model name. */
export function ollamaModelName(modelId: string): string {
  return modelId.slice(OLLAMA_PREFIX.length);
}

/** Strip the "local:" prefix to get the bare model name. */
export function localModelName(modelId: string): string {
  return modelId.slice(LOCAL_PREFIX.length);
}

// Lazily-created singletons — one client per provider type
let _anthropic: AnthropicProvider | undefined;
let _ollama: OllamaProvider | undefined;
let _local: OllamaProvider | undefined;

/** Return the correct provider for a model ID. */
export function getProvider(modelId: string): ModelProvider {
  if (isOllamaModel(modelId)) {
    _ollama ??= new OllamaProvider(`${Config.local.ollamaUrl}/v1`, "ollama");
    return _ollama;
  }
  if (isLocalModel(modelId)) {
    // Generic OpenAI-compat server — falls back to Ollama URL if LOCAL_INFERENCE_URL not set
    _local ??= new OllamaProvider(
      Config.local.localInferenceUrl || `${Config.local.ollamaUrl}/v1`,
      Config.local.localInferenceKey,
    );
    return _local;
  }
  _anthropic ??= new AnthropicProvider();
  return _anthropic;
}

/** Resolve the bare model name to pass to the provider's chat() call. */
export function resolveModelId(modelId: string): string {
  if (isOllamaModel(modelId)) return ollamaModelName(modelId);
  if (isLocalModel(modelId)) return localModelName(modelId);
  return modelId;
}
