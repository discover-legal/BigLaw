// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

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
    // Bind to loopback by default — the REST API has no auth of its own and can
    // submit tasks, ingest/read documents, and trigger paid LLM calls. Set
    // API_HOST=0.0.0.0 only behind a trusted proxy or with API_KEY set.
    host: optional("API_HOST", "127.0.0.1"),
    // Optional shared secret. If set, every request (except /health) must send
    // it as the `x-api-key` header. Empty = no auth (safe only on loopback).
    apiKey: optional("API_KEY", ""),
  },

  // Authentication, sessions, and access control.
  auth: {
    // OFF by default → local dev runs with no login as a single partner who
    // sees everything. Turn ON (with OAuth creds below) for shared deployments.
    enabled: optional("AUTH_ENABLED", "false") === "true",
    sessionSecret: optional("SESSION_SECRET", "dev-insecure-change-me-in-production-please"),
    // The browser origin(s) allowed by CORS. Defaults to local Vite ports.
    allowedOrigins: optional("CORS_ORIGINS", "http://localhost:5173,http://localhost:5174")
      .split(",").map((s) => s.trim()).filter(Boolean),
    // Public base URL of THIS API (for OAuth redirect URIs).
    baseUrl: optional("PUBLIC_BASE_URL", "http://localhost:3101"),
    // Where to send the browser back after a successful login.
    uiUrl: optional("PUBLIC_UI_URL", "http://localhost:5173"),
    // Emails that become `partner` (admin) on first OAuth login; everyone else
    // is provisioned as `lawyer`. Comma-separated.
    adminEmails: optional("ADMIN_EMAILS", "").split(",").map((s) => s.trim().toLowerCase()).filter(Boolean),
    // OAuth app credentials — register apps with each provider and set these.
    providers: {
      google:    { clientId: optional("GOOGLE_CLIENT_ID", ""),    clientSecret: optional("GOOGLE_CLIENT_SECRET", "") },
      microsoft: { clientId: optional("MICROSOFT_CLIENT_ID", ""), clientSecret: optional("MICROSOFT_CLIENT_SECRET", "") },
      linkedin:  { clientId: optional("LINKEDIN_CLIENT_ID", ""),  clientSecret: optional("LINKEDIN_CLIENT_SECRET", "") },
    },
  },

  // Per-agent agentic loop — how many tool_use iterations an agent may run
  // before it must return a final answer.
  agents: {
    maxToolIterations: parseInt(optional("AGENT_MAX_TOOL_ITERATIONS", "6")),
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
    // Directories the PDF tools are allowed to READ from. Prevents an agent
    // (e.g. via prompt injection) from reading arbitrary files like .env or
    // system files through a tool `path`. Comma-separated; empty = sensible
    // defaults (cwd + OS temp + output dir), resolved in tools/pdf.ts.
    allowedDirs: optional("PDF_ALLOWED_DIRS", "")
      .split(",").map((d) => d.trim()).filter(Boolean),
  },

  persistence: {
    tasksFile: optional("TASKS_FILE", ".tasks.json"),
    settingsFile: optional("SETTINGS_FILE", ".settings.json"),
    profilesFile: optional("PROFILES_FILE", ".profiles.json"),
  },

  logging: {
    level: optional("LOG_LEVEL", "info"),
  },

  // Audit log — append-only JSONL of every inference call, tool call, gate, debate
  audit: {
    enabled: optional("AUDIT_ENABLED", "true") === "true",
    logFile: optional("AUDIT_LOG_FILE", "./audit.jsonl"),
  },

  // DocuSeal — open-source e-signature (https://www.docuseal.com)
  // Self-host: docker run -d -p 3000:3000 docuseal/docuseal
  // API key from: Settings → API in the DocuSeal admin panel
  docuseal: {
    apiKey: process.env.DOCUSEAL_API_KEY ?? "",
    url: optional("DOCUSEAL_URL", "http://localhost:3000"),
    // Whether e-signature is offered. Defaults to on when an API key is present;
    // the admin panel can toggle it and set url/key at runtime.
    enabled: optional("DOCUSEAL_ENABLED", (process.env.DOCUSEAL_API_KEY ? "true" : "false")) === "true",
  },

  // UI/presentation preferences, tunable from the admin panel (persisted).
  presentation: {
    // "lawyer" = full legal terminology + citations; "plain" = plain-language framing for non-lawyers.
    mode: optional("UI_MODE", "lawyer") as "lawyer" | "plain",
    firmName: optional("FIRM_NAME", ""),
  },

  // Infisical — open-source secrets manager (https://infisical.com)
  // Self-host: docker compose up (see https://infisical.com/docs/self-hosting)
  // These values are bootstrap-only; all other secrets are fetched from Infisical at startup.
  infisical: {
    url: optional("INFISICAL_URL", "https://app.infisical.com"),
    clientId: process.env.INFISICAL_CLIENT_ID ?? "",
    clientSecret: process.env.INFISICAL_CLIENT_SECRET ?? "",
    projectId: process.env.INFISICAL_PROJECT_ID ?? "",
    environment: optional("INFISICAL_ENV", "production"),
    path: optional("INFISICAL_PATH", "/"),
  },
} as const;