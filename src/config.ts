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
    // Optional: point at a custom Anthropic-compatible endpoint (enterprise routing, proxies).
    baseUrl: process.env.ANTHROPIC_BASE_URL ?? "",
    // Token budget for extended thinking on synthesis/debate Opus calls.
    // Must be < maxTokens on those calls (synthesis uses 16 000 total).
    thinkingBudgetTokens: parseInt(optional("THINKING_BUDGET_TOKENS", "10000")),
  },

  embeddings: {
    // Optional when LOCAL_EMBEDDINGS=true
    apiKey: process.env.OPENAI_API_KEY ?? "",
    model: optional("EMBEDDING_MODEL", "text-embedding-3-small"),
    dimensions: parseInt(optional("EMBEDDING_DIMENSIONS", "1536")),
  },

  // Vector DB — RuVector native in-process HNSW (no external service required).
  // Three persistent stores are written to dataDir on first run and reloaded on restart.
  vectorDb: {
    /** Directory for the three on-disk RuVector stores (agents / memory / knowledge). */
    dataDir: optional("RUVECTOR_DATA_DIR", "./data"),
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
    sessionSecret: (() => {
      const secret = optional("SESSION_SECRET", "dev-insecure-change-me-in-production-please");
      if (optional("AUTH_ENABLED", "false") === "true") {
        if (secret === "dev-insecure-change-me-in-production-please") {
          throw new Error("SESSION_SECRET must be set to a strong random value when AUTH_ENABLED=true");
        }
        if (secret.length < 32) {
          throw new Error("SESSION_SECRET must be at least 32 characters when AUTH_ENABLED=true");
        }
      }
      return secret;
    })(),
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
    clientsFile: optional("CLIENTS_FILE", ".clients.json"),
    timeFile: optional("TIME_FILE", ".time-entries.json"),
    /** Persisted Q-table for agent recruitment learning (RuVector LearningEngine). */
    learningFile: optional("LEARNING_FILE", ".qtable.json"),
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

  // ─── Legal data connectors ─────────────────────────────────────────────────
  // Each connector is enabled when its API key is set; endpoint defaults to the
  // public vendor MCP URL. The connector tools return a "not configured" error
  // when the key is absent, so they are always safe to register.
  connectors: {
    courtListener: {
      // Public REST API — works without a key (key unlocks higher rate limits).
      apiKey: process.env.COURT_LISTENER_API_KEY ?? "",
      endpoint: optional("COURT_LISTENER_API_URL", "https://www.courtlistener.com/api/rest/v4"),
    },
    ironclad: {
      apiKey: process.env.IRONCLAD_API_KEY ?? "",
      endpoint: optional("IRONCLAD_MCP_URL", "https://mcp.na1.ironcladapp.com/mcp"),
      enabled: Boolean(process.env.IRONCLAD_API_KEY),
    },
    imanage: {
      apiKey: process.env.IMANAGE_API_KEY ?? "",
      endpoint: optional("IMANAGE_MCP_URL", "https://cloudimanage.com/mcp/work"),
      enabled: Boolean(process.env.IMANAGE_API_KEY),
    },
    definely: {
      apiKey: process.env.DEFINELY_API_KEY ?? "",
      endpoint: optional("DEFINELY_MCP_URL", "https://mcp.uk.definely.com/api/proxy/core-mcp"),
      enabled: Boolean(process.env.DEFINELY_API_KEY),
    },
    westlaw: {
      apiKey: process.env.WESTLAW_API_KEY ?? "",
      endpoint: optional("WESTLAW_MCP_URL", "https://legal-mcp.thomsonreuters.com/mcp"),
      enabled: Boolean(process.env.WESTLAW_API_KEY),
    },
    everlaw: {
      apiKey: process.env.EVERLAW_API_KEY ?? "",
      endpoint: optional("EVERLAW_MCP_URL", "https://api.everlaw.com/v1/mcp"),
      enabled: Boolean(process.env.EVERLAW_API_KEY),
    },
    trellis: {
      apiKey: process.env.TRELLIS_API_KEY ?? "",
      endpoint: optional("TRELLIS_MCP_URL", "https://mcp.trellis.law/anthropic"),
      enabled: Boolean(process.env.TRELLIS_API_KEY),
    },
    descrybe: {
      apiKey: process.env.DESCRYBE_API_KEY ?? "",
      endpoint: optional("DESCRYBE_MCP_URL", "https://mcp.descrybe.com/mcp"),
      enabled: Boolean(process.env.DESCRYBE_API_KEY),
    },
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