// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import { AnthropicProvider } from "./anthropic.js";
import { OllamaProvider } from "./ollama.js";
import { Config } from "../config.js";
import type { ModelProvider } from "./types.js";
import { assertPublicHttpUrl } from "../settings/index.js";
import { logger } from "../logger.js";

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
    if (!_ollama) {
      const ollamaUrl = `${Config.local.ollamaUrl}/v1`;
      if (!ollamaUrl.startsWith("http://localhost") && !ollamaUrl.startsWith("http://127.")) {
        try {
          assertPublicHttpUrl(ollamaUrl, "OLLAMA_URL");
        } catch (err) {
          logger.warn("OLLAMA_URL validation warning", { error: (err as Error).message });
        }
      }
      _ollama = new OllamaProvider(ollamaUrl, "ollama");
    }
    return _ollama;
  }
  if (isLocalModel(modelId)) {
    if (!_local) {
      // Generic OpenAI-compat server — falls back to Ollama URL if LOCAL_INFERENCE_URL not set
      const localUrl = Config.local.localInferenceUrl || `${Config.local.ollamaUrl}/v1`;
      if (!localUrl.startsWith("http://localhost") && !localUrl.startsWith("http://127.")) {
        try {
          assertPublicHttpUrl(localUrl, "LOCAL_INFERENCE_URL");
        } catch (err) {
          logger.warn("LOCAL_INFERENCE_URL validation warning", { error: (err as Error).message });
        }
      }
      _local = new OllamaProvider(localUrl, Config.local.localInferenceKey);
    }
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
