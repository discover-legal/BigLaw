// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import OpenAI from "openai";
import { Config } from "./config.js";
import { logger } from "./logger.js";
import type { EmbeddingResult } from "./types.js";

// ─── Providers ────────────────────────────────────────────────────────────────

// OpenAI embeddings (cloud) — used when LOCAL_EMBEDDINGS=false
let _openaiClient: OpenAI | undefined;
function openaiClient(): OpenAI {
  _openaiClient ??= new OpenAI({ apiKey: Config.embeddings.apiKey });
  return _openaiClient;
}

// ─── Ollama local embedding via /api/embed ─────────────────────────────────

interface OllamaEmbedResponse {
  embeddings: number[][];
  model: string;
}

async function ollamaEmbed(texts: string[]): Promise<number[][]> {
  const url = `${Config.local.ollamaUrl}/api/embed`;
  const model = Config.local.localEmbeddingModel;

  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ model, input: texts }),
  });

  if (!res.ok) {
    const err = await res.text();
    throw new Error(`Ollama embed failed (${res.status}): ${err}`);
  }

  const data = (await res.json()) as OllamaEmbedResponse;
  if (!data.embeddings?.length) {
    throw new Error("Ollama returned empty embeddings");
  }
  return data.embeddings;
}

// ─── Public API ───────────────────────────────────────────────────────────────

export async function embed(text: string): Promise<EmbeddingResult> {
  if (Config.local.localEmbeddings) {
    const [vec] = await ollamaEmbed([text]);
    return { text, embedding: vec, model: Config.local.localEmbeddingModel };
  }

  const response = await openaiClient().embeddings.create({
    model: Config.embeddings.model,
    input: text,
    dimensions: Config.embeddings.dimensions,
  });
  if (!response.data[0]) {
    throw new Error("OpenAI embeddings API returned an empty data array");
  }
  return { text, embedding: response.data[0].embedding, model: Config.embeddings.model };
}

export async function embedBatch(texts: string[]): Promise<EmbeddingResult[]> {
  if (texts.length === 0) return [];

  if (Config.local.localEmbeddings) {
    // Ollama's /api/embed accepts an array input
    const vectors = await ollamaEmbed(texts);
    if (vectors.length !== texts.length) {
      throw new Error(`Ollama embedding count mismatch: got ${vectors.length}, expected ${texts.length}`);
    }
    const model = Config.local.localEmbeddingModel;
    return texts.map((text, i) => ({ text, embedding: vectors[i], model }));
  }

  const response = await openaiClient().embeddings.create({
    model: Config.embeddings.model,
    input: texts,
    dimensions: Config.embeddings.dimensions,
  });
  return response.data.map((item, i) => ({
    text: texts[i],
    embedding: item.embedding,
    model: Config.embeddings.model,
  }));
}

export function cosineSimilarity(a: number[], b: number[]): number {
  if (a.length !== b.length) {
    throw new Error(`cosineSimilarity: vector length mismatch (${a.length} vs ${b.length})`);
  }
  let dot = 0, magA = 0, magB = 0;
  for (let i = 0; i < a.length; i++) {
    dot += a[i] * b[i];
    magA += a[i] * a[i];
    magB += b[i] * b[i];
  }
  const denom = Math.sqrt(magA) * Math.sqrt(magB);
  return denom === 0 ? 0 : dot / denom;
}
