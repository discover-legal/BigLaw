// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, version 3.
// See <https://www.gnu.org/licenses/gpl-3.0.html>

import "dotenv/config";

function require(key: string): string {
  const v = process.env[key];
  if (!v) throw new Error(`Missing required env var: ${key}`);
  return v;
}

function optional(key: string, fallback: string): string {
  return process.env[key] ?? fallback;
}

export const Config = {
  anthropic: {
    apiKey: require("ANTHROPIC_API_KEY"),
    model: optional("ANTHROPIC_MODEL", "claude-opus-4-8"),
  },

  embeddings: {
    // Optional when LOCAL_EMBEDDINGS=true
    apiKey: process.env.OPENAI_API_KEY ?? "",
    model: optional("EMBEDDING_MODEL", "text-embedding-3-small"),
    dimensions: parseInt(optional("EMBEDDING_DIMENSIONS", "1536")),
  },

  // Vector DB — Qdrant in dev; swap URL for RuVector (https://github.com/ruvnet/RuVector)
  vectorDb: {
    url: optional("QDRANT_URL", "http://localhost:6333"),
    apiKey: process.env.QDRANT_API_KEY,
    collections: {
      agents: "fac_agents",
      documents: "fac_documents",
      memory: "fac_memory",
    },
  },

  mcp: {
    port: parseInt(optional("MCP_PORT", "3100")),
  },

  api: {
    port: parseInt(optional("API_PORT", "3101")),
  },

  dytopo: {
    similarityThreshold: parseFloat(optional("DYTOPO_SIMILARITY_THRESHOLD", "0.68")),
    maxAgentsPerRound: parseInt(optional("DYTOPO_MAX_AGENTS_PER_ROUND", "12")),
    maxRounds: parseInt(optional("DYTOPO_MAX_ROUNDS", "14")),
  },

  debate: {
    citationRequired: optional("DEBATE_CITATION_REQUIRED", "true") === "true",
    adversarialEnabled: optional("DEBATE_ADVERSARIAL_ENABLED", "true") === "true",
    verificationPasses: parseInt(optional("DEBATE_VERIFICATION_PASSES", "10")),
    gateConfidenceThreshold: parseFloat(optional("DEBATE_GATE_CONFIDENCE_THRESHOLD", "0.80")),
  },

  search: {
    tavilyApiKey: process.env.TAVILY_API_KEY ?? "",
  },

  // Local inference — Ollama (https://ollama.com) for LLM + embeddings
  local: {
    ollamaUrl: optional("OLLAMA_URL", "http://localhost:11434"),
    ollamaEnabled: optional("OLLAMA_ENABLED", "false") === "true",
    ollamaModel: optional("OLLAMA_MODEL", "llama3.2"),
    // Comma-separated agent tiers to route to Ollama, e.g. "3" or "2,3"
    ollamaTiers: optional("OLLAMA_TIERS", "3"),
    localEmbeddings: optional("LOCAL_EMBEDDINGS", "false") === "true",
    // Embedding model served via Ollama — e.g. nomic-embed-text, all-minilm
    localEmbeddingModel: optional("LOCAL_EMBEDDING_MODEL", "nomic-embed-text"),
    // Generic OpenAI-compat server: LM Studio (http://localhost:1234/v1), Jan, vLLM, llama.cpp
    localInferenceUrl: process.env.LOCAL_INFERENCE_URL ?? "",
    localInferenceKey: optional("LOCAL_INFERENCE_KEY", "local"),
    localInferenceModel: optional("LOCAL_INFERENCE_MODEL", "local-model"),
    // "all" routes every tier locally; "1,2,3" routes only those tiers; "" = disabled
    localInferenceTiers: optional("LOCAL_INFERENCE_TIERS", ""),
  },

  // PDF tools — PyMuPDF (generation + extraction) + Camelot (table extraction)
  pdf: {
    pythonBin: optional("PDF_PYTHON_BIN", "python3"),
    outputDir: optional("PDF_OUTPUT_DIR", "./output/documents"),
  },

  persistence: {
    tasksFile: optional("TASKS_FILE", ".tasks.json"),
  },

  logging: {
    level: optional("LOG_LEVEL", "info"),
  },

  // Audit log — append-only JSONL of every inference call, tool call, gate, debate
  audit: {
    enabled: optional("AUDIT_ENABLED", "true") === "true",
    logFile: optional("AUDIT_LOG_FILE", "./audit.jsonl"),
  },
} as const;