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
    apiKey: require("OPENAI_API_KEY"),
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

  persistence: {
    tasksFile: optional("TASKS_FILE", ".tasks.json"),
  },

  logging: {
    level: optional("LOG_LEVEL", "info"),
  },
} as const;