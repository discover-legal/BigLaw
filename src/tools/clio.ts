// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Clio tool definitions — 7 tools exposing Clio practice management to agents.
 *
 * All tools follow the same pattern as connectors.ts: return a result object
 * or { error: string } — never throw.
 */

import { clioClient } from "../integrations/clio.js";
import { extractWritingSamples } from "../services/writingSamples.js";
import type { ToolImpl } from "./index.js";

// ─── clio_list_matters ────────────────────────────────────────────────────────

const clioListMatters: ToolImpl = {
  name: "clio_list_matters",
  schema: {
    name: "clio_list_matters",
    description: "List open matters from Clio. Returns id, display_number, description, status, client name, practice area.",
    input_schema: {
      type: "object" as const,
      properties: {
        status: { type: "string", description: "Filter by status: open (default), pending, closed, or all" },
        limit: { type: "number", description: "Maximum results (default 50, max 200)" },
        page: { type: "number", description: "Page number (default 1)" },
      },
    },
  },
  async execute(input) {
    try {
      const limit = Math.min((input.limit as number | undefined) ?? 50, 200);
      return await clioClient.listMatters({
        status: (input.status as string | undefined) ?? "open",
        limit,
        page: (input.page as number | undefined) ?? 1,
      });
    } catch (e) {
      return { error: (e as Error).message };
    }
  },
};

// ─── clio_get_matter ──────────────────────────────────────────────────────────

const clioGetMatter: ToolImpl = {
  name: "clio_get_matter",
  schema: {
    name: "clio_get_matter",
    description: "Get full details of a Clio matter by ID, including client, practice area, responsible attorney, and custom fields.",
    input_schema: {
      type: "object" as const,
      properties: {
        matter_id: { type: "number", description: "Clio matter ID" },
      },
      required: ["matter_id"],
    },
  },
  async execute(input) {
    try {
      return await clioClient.getMatter(input.matter_id as number);
    } catch (e) {
      return { error: (e as Error).message };
    }
  },
};

// ─── clio_list_documents ──────────────────────────────────────────────────────

const clioListDocuments: ToolImpl = {
  name: "clio_list_documents",
  schema: {
    name: "clio_list_documents",
    description: "List documents attached to a Clio matter.",
    input_schema: {
      type: "object" as const,
      properties: {
        matter_id: { type: "number", description: "Clio matter ID" },
        limit: { type: "number", description: "Maximum results (default 50)" },
      },
      required: ["matter_id"],
    },
  },
  async execute(input) {
    try {
      return await clioClient.listDocuments(
        input.matter_id as number,
        (input.limit as number | undefined) ?? 50,
      );
    } catch (e) {
      return { error: (e as Error).message };
    }
  },
};

// ─── clio_download_document ───────────────────────────────────────────────────

const clioDownloadDocument: ToolImpl = {
  name: "clio_download_document",
  schema: {
    name: "clio_download_document",
    description: "Download a Clio document and return its text content for analysis. Supports PDF and DOCX (text is extracted).",
    input_schema: {
      type: "object" as const,
      properties: {
        document_id: { type: "number", description: "Clio document ID" },
        filename: { type: "string", description: "Filename including extension (e.g. contract.pdf) — used to determine extraction method" },
      },
      required: ["document_id", "filename"],
    },
  },
  async execute(input) {
    try {
      const buf = await clioClient.downloadDocument(input.document_id as number);
      const filename = input.filename as string;
      const { samples } = await extractWritingSamples(buf, filename);
      const text = samples.join("\n\n").slice(0, 50_000);
      const truncated = samples.join("\n\n").length > 50_000;
      return { text, truncated };
    } catch (e) {
      return { error: (e as Error).message };
    }
  },
};

// ─── clio_create_activity ─────────────────────────────────────────────────────

const clioCreateActivity: ToolImpl = {
  name: "clio_create_activity",
  schema: {
    name: "clio_create_activity",
    description: "Create a time entry (activity) on a Clio matter. Use this to log billable time from Big Michael back to Clio.",
    input_schema: {
      type: "object" as const,
      properties: {
        matter_id: { type: "number", description: "Clio matter ID" },
        description: { type: "string", description: "Description of the work performed" },
        date: { type: "string", description: "ISO date (YYYY-MM-DD)" },
        duration_hours: { type: "number", description: "Duration in hours (e.g. 0.5 for 30 min)" },
      },
      required: ["matter_id", "description", "date", "duration_hours"],
    },
  },
  async execute(input) {
    try {
      return await clioClient.createActivity(input.matter_id as number, {
        description: input.description as string,
        dateOn: input.date as string,
        durationHours: input.duration_hours as number,
      });
    } catch (e) {
      return { error: (e as Error).message };
    }
  },
};

// ─── clio_create_note ─────────────────────────────────────────────────────────

const clioCreateNote: ToolImpl = {
  name: "clio_create_note",
  schema: {
    name: "clio_create_note",
    description: "Post a note to a Clio matter. Use this to save synthesis output, findings, or research memos back into the client file.",
    input_schema: {
      type: "object" as const,
      properties: {
        matter_id: { type: "number", description: "Clio matter ID" },
        subject: { type: "string", description: "Note subject" },
        content: { type: "string", description: "Note body" },
      },
      required: ["matter_id", "subject", "content"],
    },
  },
  async execute(input) {
    try {
      return await clioClient.createNote(
        input.matter_id as number,
        input.subject as string,
        input.content as string,
      );
    } catch (e) {
      return { error: (e as Error).message };
    }
  },
};

// ─── clio_list_contacts ───────────────────────────────────────────────────────

const clioListContacts: ToolImpl = {
  name: "clio_list_contacts",
  schema: {
    name: "clio_list_contacts",
    description: "List contacts from Clio (clients, companies, people).",
    input_schema: {
      type: "object" as const,
      properties: {
        type: { type: "string", description: "Filter by type: Person or Company" },
        limit: { type: "number", description: "Maximum results (default 50)" },
      },
    },
  },
  async execute(input) {
    try {
      return await clioClient.listContacts({
        type: input.type as string | undefined,
        limit: (input.limit as number | undefined) ?? 50,
      });
    } catch (e) {
      return { error: (e as Error).message };
    }
  },
};

// ─── Exports ──────────────────────────────────────────────────────────────────

export const CLIO_TOOLS: ToolImpl[] = [
  clioListMatters,
  clioGetMatter,
  clioListDocuments,
  clioDownloadDocument,
  clioCreateActivity,
  clioCreateNote,
  clioListContacts,
];

export const CLIO_TOOL_NAMES = CLIO_TOOLS.map((t) => t.name);
