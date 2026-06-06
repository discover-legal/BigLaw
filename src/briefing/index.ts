// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Client Intelligence Briefing — hub-and-spoke multi-agent swarm.
 *
 * The classic partner-prep problem: a client file is scattered across
 * 10 mailboxes, 3 DMs, 2 call notes, a Clio matter, an iManage workspace,
 * a Slack channel, and a shared Drive folder. Nobody has the whole picture.
 * The partner gets on the call having read one of those sources.
 *
 * This engine solves it with a hub-and-spoke swarm:
 *
 *   Hub (Sonnet manager)
 *     ├─ Clio Spoke       → matter list, billing, activities, notes, contacts
 *     ├─ iManage Spoke    → emails, file notes, draft documents, correspondence
 *     ├─ Slack Spoke      → client/matter channel messages, DMs
 *     ├─ Drive/Box Spoke  → recently touched files
 *     ├─ Knowledge Spoke  → regulatory/industry context from the knowledge store
 *     └─ Internal Spoke   → Big Michael tasks, time entries, matter health
 *
 * Each spoke runs in parallel against its connector. Slow connectors don't
 * block the briefing — they time out at 12 s and the hub synthesises what
 * it has. Results flow up to a shared Chalkboard as typed Intel Items.
 *
 * Each spoke is itself a mini-manager: it queries its connector, parses the
 * response into structured IntelItems, and writes them to the chalkboard.
 * If a connector isn't configured, that spoke returns empty intel silently.
 *
 * The hub synthesises the full chalkboard into a single Markdown briefing —
 * one source of truth assembled from every place the file lives.
 *
 * WHAT IT KILLS:
 *   Clio Grow / CRM — relationship management + pre-call prep
 *   Clio Insights client reports — billing + activity summaries
 *   Manual partner prep (30 min before every call)
 *   Relationship intelligence tools (ContactsLaw, Nexl, Introhive)
 *   The eternal "where is the file status?" Slack DM to the associate
 */

import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import { resolveModelId } from "../providers/index.js";
import { mcpCall } from "../tools/connectors.js";
import { searchGraphMail, searchGmail } from "../email/client.js";
import { searchSharePoint, searchTeamsMessages } from "../integrations/graph.js";
import type { Client, ClientMatter, Task, TimeEntry } from "../types.js";
import type { KnowledgeStore } from "../knowledge/index.js";

const SONNET_MODEL = "claude-sonnet-4-6";
const SPOKE_TIMEOUT_MS = 12_000;

// ─── Chalkboard ───────────────────────────────────────────────────────────────

/**
 * A single piece of intelligence written by a spoke agent.
 * Source-attributed so the hub can weight confidence and cite provenance.
 */
export interface IntelItem {
  /** Which spoke contributed this item */
  source: IntelSource;
  /** Semantic category of the intel */
  category: IntelCategory;
  /** ISO timestamp of the underlying event (not the extraction time) */
  eventAt?: string;
  /** Matter or case reference this relates to */
  matterNumber?: string;
  /** Short headline — one sentence */
  headline: string;
  /** Structured data (connector-specific; varies by spoke) */
  data: Record<string, unknown>;
}

export type IntelSource =
  | "clio"
  | "imanage"
  | "slack"
  | "google_drive"
  | "box"
  | "email_graph"
  | "email_gmail"
  | "sharepoint"
  | "teams_chat"
  | "knowledge_store"
  | "internal_tasks"
  | "internal_time"
  | "internal_health";

export type IntelCategory =
  | "matter_status"
  | "billing"
  | "activity"
  | "correspondence"
  | "email"
  | "document"
  | "relationship"
  | "regulatory"
  | "risk"
  | "deadline"
  | "note";

export class Chalkboard {
  private readonly items: IntelItem[] = [];

  write(item: IntelItem): void {
    this.items.push(item);
  }

  writeMany(items: IntelItem[]): void {
    this.items.push(...items);
  }

  read(): readonly IntelItem[] {
    return this.items;
  }

  bySource(source: IntelSource): IntelItem[] {
    return this.items.filter((i) => i.source === source);
  }

  byCategory(category: IntelCategory): IntelItem[] {
    return this.items.filter((i) => i.category === category);
  }

  get size(): number {
    return this.items.length;
  }
}

// ─── Spoke types ──────────────────────────────────────────────────────────────

interface SpokeResult {
  source: IntelSource;
  items: IntelItem[];
  durationMs: number;
  error?: string;
}

// ─── Types exported for API surface ──────────────────────────────────────────

export interface BriefingMatterSnapshot {
  matterNumber: string;
  description: string;
  practiceArea?: string;
  status: "active" | "idle" | "complete";
  daysSinceActivity: number;
  openBillingUsd: number;
  totalBilledUsd: number;
  pendingGates: number;
  lastOutput?: string;
}

export interface BriefingBillingSnapshot {
  last90DaysUsd: number;
  wipUsd: number;
  oldestWipDays: number;
  openMatterCount: number;
}

export interface ClientBriefing {
  id: string;
  clientId: string;
  clientName: string;
  clientNumber: string;
  generatedAt: string;
  briefingDate: string;
  executiveSummary: string;
  matters: BriefingMatterSnapshot[];
  billing: BriefingBillingSnapshot;
  openItems: string[];
  relationshipNotes?: string;
  industryContext?: string;
  /** The complete Markdown briefing document */
  document: string;
  /** All intel items gathered by the swarm — chalkboard export */
  chalkboard: IntelItem[];
  /** Per-spoke status: how many items each source contributed */
  spokeSummary: Record<IntelSource, { items: number; durationMs: number; error?: string }>;
}

// ─── BriefingEngine (public façade) ──────────────────────────────────────────

export class BriefingEngine {
  private readonly client: Anthropic;

  constructor() {
    this.client = new Anthropic({
      apiKey: Config.anthropic.apiKey,
      ...(Config.anthropic.baseUrl ? { baseURL: Config.anthropic.baseUrl } : {}),
    });
  }

  /**
   * Launch the hub-and-spoke swarm and synthesise a client briefing.
   *
   * All configured spokes run in parallel. Unconfigured connectors return
   * empty intel silently — the hub works with whatever it gets.
   */
  async generate(
    clientRecord: Client,
    allTasks: Task[],
    timeEntries: TimeEntry[],
    opts: {
      knowledge?: KnowledgeStore;
      taskId?: string;
      briefingDate?: string;
      industryContext?: string;
    } = {},
  ): Promise<ClientBriefing> {
    const start = Date.now();
    const briefingDate = opts.briefingDate ?? new Date().toISOString().slice(0, 10);
    const chalkboard = new Chalkboard();

    // ── Launch all spokes in parallel ──────────────────────────────────────
    const spokePromises: Array<Promise<SpokeResult>> = [
      this.runClioSpoke(clientRecord),
      this.runIManageSpoke(clientRecord),
      this.runSlackSpoke(clientRecord),
      this.runDriveBoxSpoke(clientRecord),
      this.runEmailSpoke(clientRecord),
      this.runSharePointSpoke(clientRecord),
      this.runTeamsChatSpoke(clientRecord),
      this.runInternalSpoke(clientRecord, allTasks, timeEntries),
    ];
    if (opts.knowledge) {
      spokePromises.push(this.runKnowledgeSpoke(clientRecord, opts.knowledge, opts.industryContext));
    }

    const spokeResults = await Promise.allSettled(spokePromises);

    const spokeSummary: Record<string, { items: number; durationMs: number; error?: string }> = {};
    for (const result of spokeResults) {
      if (result.status === "fulfilled") {
        const { source, items, durationMs, error } = result.value;
        chalkboard.writeMany(items);
        spokeSummary[source] = { items: items.length, durationMs, error };
        if (error) logger.warn(`Briefing spoke error: ${source}`, { error });
      } else {
        logger.warn("Briefing spoke rejected", { reason: String(result.reason) });
      }
    }

    // ── Derive structured snapshots from the chalkboard ───────────────────
    const matters = this.buildMatterSnapshots(clientRecord.matters, allTasks, timeEntries, chalkboard);
    const billing = this.buildBillingSnapshot(timeEntries, clientRecord.clientNumber, matters);
    const openItems = this.collectOpenItems(chalkboard, matters);

    // ── Hub synthesis: Sonnet reads the chalkboard and drafts the briefing ─
    const { executiveSummary, document } = await this.synthesize(
      clientRecord, chalkboard, matters, billing, openItems, opts,
    );

    const briefing: ClientBriefing = {
      id: crypto.randomUUID(),
      clientId: clientRecord.id,
      clientName: clientRecord.name,
      clientNumber: clientRecord.clientNumber,
      generatedAt: new Date().toISOString(),
      briefingDate,
      executiveSummary,
      matters,
      billing,
      openItems,
      relationshipNotes: clientRecord.notes,
      industryContext: opts.industryContext,
      document,
      chalkboard: chalkboard.read() as IntelItem[],
      spokeSummary: spokeSummary as ClientBriefing["spokeSummary"],
    };

    logger.info("Client briefing generated via swarm", {
      id: briefing.id,
      client: clientRecord.name,
      chalkboardItems: chalkboard.size,
      spokes: Object.keys(spokeSummary).length,
      durationMs: Date.now() - start,
    });

    return briefing;
  }

  // ─── Spoke: Clio ──────────────────────────────────────────────────────────

  private async runClioSpoke(client: Client): Promise<SpokeResult> {
    const start = Date.now();
    const items: IntelItem[] = [];

    if (!Config.clio?.clientId) {
      return { source: "clio", items: [], durationMs: 0 };
    }

    try {
      const withTimeout = <T>(p: Promise<T>): Promise<T> =>
        Promise.race([p, new Promise<never>((_, r) => setTimeout(() => r(new Error("timeout")), SPOKE_TIMEOUT_MS))]);

      // Use the Clio API via mcpCall (Clio has a REST connector via the clio tools)
      // Endpoint is the Clio API base URL per region
      const clioBase = {
        us: "https://app.clio.com", eu: "https://eu.app.clio.com",
        ca: "https://ca.app.clio.com", au: "https://au.app.clio.com",
      }[Config.clio.region] ?? "https://app.clio.com";

      const matters = await withTimeout(
        mcpCall(`${clioBase}/api/v4`, Config.clio.clientSecret,
          "list_matters", { clientNumber: client.clientNumber }),
      ) as Record<string, unknown>;
      if (Array.isArray(matters?.data)) {
        for (const m of (matters.data as Array<Record<string, unknown>>).slice(0, 10)) {
          items.push({
            source: "clio", category: "matter_status",
            matterNumber: String(m["display_number"] ?? m["id"] ?? ""),
            headline: `Clio matter: ${m["description"] ?? m["display_number"] ?? "—"}`,
            data: m,
          });
        }
      }

      const activities = await withTimeout(
        mcpCall(`${clioBase}/api/v4`, Config.clio.clientSecret,
          "list_activities", { clientNumber: client.clientNumber, limit: 20 }),
      ) as Record<string, unknown>;
      if (Array.isArray(activities?.data)) {
        for (const a of (activities.data as Array<Record<string, unknown>>).slice(0, 20)) {
          items.push({
            source: "clio", category: "activity",
            eventAt: String(a["date"] ?? ""),
            matterNumber: String((a["matter"] as Record<string, unknown>)?.["display_number"] ?? ""),
            headline: String(a["note"] ?? a["description"] ?? "Clio activity"),
            data: a,
          });
        }
      }

    } catch (err) {
      return { source: "clio", items, durationMs: Date.now() - start, error: (err as Error).message };
    }

    return { source: "clio", items, durationMs: Date.now() - start };
  }

  // ─── Spoke: iManage ───────────────────────────────────────────────────────

  private async runIManageSpoke(client: Client): Promise<SpokeResult> {
    const start = Date.now();
    const items: IntelItem[] = [];

    if (!Config.connectors?.imanage?.apiKey) {
      return { source: "imanage", items: [], durationMs: 0 };
    }

    try {
      const withTimeout = <T>(p: Promise<T>): Promise<T> =>
        Promise.race([p, new Promise<never>((_, r) => setTimeout(() => r(new Error("timeout")), SPOKE_TIMEOUT_MS))]);

      // Search for documents related to this client
      const docs = await withTimeout(
        mcpCall(Config.connectors.imanage.endpoint, Config.connectors.imanage.apiKey,
          "imanage_search", { query: client.name, limit: 20 }),
      ) as Record<string, unknown>;

      if (Array.isArray(docs?.results)) {
        for (const d of (docs.results as Array<Record<string, unknown>>).slice(0, 15)) {
          items.push({
            source: "imanage", category: "document",
            eventAt: String(d["edit_date"] ?? d["create_date"] ?? ""),
            headline: String(d["name"] ?? d["doc_num"] ?? "iManage document"),
            data: d,
          });
        }
      }

    } catch (err) {
      return { source: "imanage", items, durationMs: Date.now() - start, error: (err as Error).message };
    }

    return { source: "imanage", items, durationMs: Date.now() - start };
  }

  // ─── Spoke: Slack ─────────────────────────────────────────────────────────

  private async runSlackSpoke(client: Client): Promise<SpokeResult> {
    const start = Date.now();
    const items: IntelItem[] = [];

    if (!Config.connectors?.slack?.apiKey) {
      return { source: "slack", items: [], durationMs: 0 };
    }

    try {
      const withTimeout = <T>(p: Promise<T>): Promise<T> =>
        Promise.race([p, new Promise<never>((_, r) => setTimeout(() => r(new Error("timeout")), SPOKE_TIMEOUT_MS))]);

      // Search Slack for client name
      const messages = await withTimeout(
        mcpCall(Config.connectors.slack.endpoint, Config.connectors.slack.apiKey,
          "slack_search", { query: client.name, count: 15 }),
      ) as Record<string, unknown>;

      const matches = (messages as Record<string, unknown>)?.["messages"]
        ?? (messages as Record<string, unknown>)?.["results"];
      if (Array.isArray(matches)) {
        for (const m of (matches as Array<Record<string, unknown>>).slice(0, 15)) {
          items.push({
            source: "slack", category: "correspondence",
            eventAt: String(m["ts"] ?? ""),
            headline: String(m["text"] ?? "Slack message").slice(0, 120),
            data: { channel: m["channel"], user: m["user"], text: m["text"], ts: m["ts"] },
          });
        }
      }

    } catch (err) {
      return { source: "slack", items, durationMs: Date.now() - start, error: (err as Error).message };
    }

    return { source: "slack", items, durationMs: Date.now() - start };
  }

  // ─── Spoke: Google Drive + Box ────────────────────────────────────────────

  private async runDriveBoxSpoke(client: Client): Promise<SpokeResult> {
    const start = Date.now();
    const items: IntelItem[] = [];

    const driveKey = Config.connectors?.googleDrive?.apiKey;
    const boxKey = Config.connectors?.box?.apiKey;

    if (!driveKey && !boxKey) return { source: "google_drive", items: [], durationMs: 0 };

    try {
      const withTimeout = <T>(p: Promise<T>): Promise<T> =>
        Promise.race([p, new Promise<never>((_, r) => setTimeout(() => r(new Error("timeout")), SPOKE_TIMEOUT_MS))]);

      if (driveKey) {
        const driveRes = await withTimeout(
          mcpCall(Config.connectors.googleDrive.endpoint, driveKey,
            "google_drive_search", { query: client.name, pageSize: 10 }),
        ) as Record<string, unknown>;
        const files = (driveRes as Record<string, unknown>)?.["files"] ?? [];
        for (const f of (Array.isArray(files) ? files as Array<Record<string, unknown>> : []).slice(0, 8)) {
          items.push({
            source: "google_drive", category: "document",
            eventAt: String(f["modifiedTime"] ?? ""),
            headline: String(f["name"] ?? "Drive file"),
            data: { id: f["id"], name: f["name"], mimeType: f["mimeType"], modifiedTime: f["modifiedTime"] },
          });
        }
      }

      if (boxKey) {
        const boxRes = await withTimeout(
          mcpCall(Config.connectors.box.endpoint, boxKey,
            "box_search", { query: client.name, limit: 10 }),
        ) as Record<string, unknown>;
        const entries = (boxRes as Record<string, unknown>)?.["entries"] ?? [];
        for (const f of (Array.isArray(entries) ? entries as Array<Record<string, unknown>> : []).slice(0, 8)) {
          items.push({
            source: "box", category: "document",
            headline: String(f["name"] ?? "Box file"),
            data: { id: f["id"], name: f["name"], type: f["type"] },
          });
        }
      }

    } catch (err) {
      return { source: "google_drive", items, durationMs: Date.now() - start, error: (err as Error).message };
    }

    return { source: "google_drive", items, durationMs: Date.now() - start };
  }

  // ─── Spoke: Email (Graph + Gmail) ────────────────────────────────────────

  private async runEmailSpoke(client: Client): Promise<SpokeResult> {
    const start = Date.now();
    const items: IntelItem[] = [];

    const graphEnabled = Config.email.graph.enabled;
    const gmailEnabled = Config.email.gmail.enabled;

    if (!graphEnabled && !gmailEnabled) {
      return { source: "email_graph", items: [], durationMs: 0 };
    }

    // Both providers fire in parallel; partial failure is fine
    const [graphResults, gmailResults] = await Promise.allSettled([
      graphEnabled ? searchGraphMail(client.name, { maxResults: 20, daysBack: 90 }) : Promise.resolve([]),
      gmailEnabled ? searchGmail(client.name, { maxResults: 20, daysBack: 90 }) : Promise.resolve([]),
    ]);

    const toItems = (msgs: import("../email/client.js").EmailMessage[], source: IntelSource): IntelItem[] =>
      msgs.map((m): IntelItem => ({
        source,
        category: "email",
        eventAt: m.receivedAt,
        matterNumber: m.matterRef,
        headline: `${m.subject} — from ${m.from}`,
        data: {
          id: m.id,
          subject: m.subject,
          from: m.from,
          receivedAt: m.receivedAt,
          snippet: m.snippet,
          hasAttachments: m.hasAttachments,
          matterRef: m.matterRef,
        },
      }));

    if (graphResults.status === "fulfilled") {
      items.push(...toItems(graphResults.value, "email_graph"));
    } else {
      logger.warn("Email Graph spoke failed", { error: String(graphResults.reason) });
    }

    if (gmailResults.status === "fulfilled") {
      items.push(...toItems(gmailResults.value, "email_gmail"));
    } else {
      logger.warn("Email Gmail spoke failed", { error: String(gmailResults.reason) });
    }

    // Sort all email items by date descending
    items.sort((a, b) => {
      const ta = a.eventAt ? new Date(a.eventAt).getTime() : 0;
      const tb = b.eventAt ? new Date(b.eventAt).getTime() : 0;
      return tb - ta;
    });

    const error = [
      graphResults.status === "rejected" ? `Graph: ${String(graphResults.reason)}` : "",
      gmailResults.status === "rejected" ? `Gmail: ${String(gmailResults.reason)}` : "",
    ].filter(Boolean).join("; ") || undefined;

    return {
      source: "email_graph",
      items,
      durationMs: Date.now() - start,
      error,
    };
  }

  // ─── Spoke: SharePoint ───────────────────────────────────────────────────

  private async runSharePointSpoke(client: Client): Promise<SpokeResult> {
    const start = Date.now();
    const items: IntelItem[] = [];
    if (!Config.email.graph.enabled) return { source: "sharepoint", items: [], durationMs: 0 };

    try {
      const files = await searchSharePoint(client.name, { maxResults: 15, daysBack: 90 });
      for (const f of files) {
        items.push({
          source: "sharepoint", category: "document",
          eventAt: f.lastModified,
          matterNumber: f.matterRef,
          headline: `${f.name}${f.siteName ? ` (${f.siteName})` : ""}`,
          data: { id: f.id, name: f.name, webUrl: f.webUrl, lastModified: f.lastModified, size: f.size, siteId: f.siteId },
        });
      }
    } catch (err) {
      return { source: "sharepoint", items, durationMs: Date.now() - start, error: (err as Error).message };
    }
    return { source: "sharepoint", items, durationMs: Date.now() - start };
  }

  // ─── Spoke: Teams chat ────────────────────────────────────────────────────

  private async runTeamsChatSpoke(client: Client): Promise<SpokeResult> {
    const start = Date.now();
    const items: IntelItem[] = [];
    if (!Config.email.graph.enabled) return { source: "teams_chat", items: [], durationMs: 0 };

    try {
      const messages = await searchTeamsMessages(client.name, { maxResults: 15, daysBack: 60 });
      for (const m of messages) {
        items.push({
          source: "teams_chat", category: "correspondence",
          eventAt: m.createdAt,
          matterNumber: m.matterRef,
          headline: `${m.from}: ${m.body.slice(0, 100)}`,
          data: { id: m.id, from: m.from, body: m.body, createdAt: m.createdAt, webUrl: m.webUrl, channelId: m.channelId },
        });
      }
    } catch (err) {
      return { source: "teams_chat", items, durationMs: Date.now() - start, error: (err as Error).message };
    }
    return { source: "teams_chat", items, durationMs: Date.now() - start };
  }

  // ─── Spoke: Knowledge store ───────────────────────────────────────────────

  private async runKnowledgeSpoke(
    client: Client,
    knowledge: KnowledgeStore,
    industryContext?: string,
  ): Promise<SpokeResult> {
    const start = Date.now();
    const items: IntelItem[] = [];

    try {
      const query = industryContext
        ? `${client.name} ${industryContext} regulatory`
        : `${client.name} industry regulation compliance`;
      const results = await knowledge.search(query, { topK: 5 });
      const arr = Array.isArray(results)
        ? results as unknown as Array<Record<string, unknown>>
        : [];
      for (const r of arr.slice(0, 5)) {
        items.push({
          source: "knowledge_store", category: "regulatory",
          headline: String(r["title"] ?? r["documentTitle"] ?? "Knowledge item").slice(0, 100),
          data: { title: r["title"], content: String(r["content"] ?? r["text"] ?? "").slice(0, 500) },
        });
      }
    } catch (err) {
      return { source: "knowledge_store", items, durationMs: Date.now() - start, error: (err as Error).message };
    }

    return { source: "knowledge_store", items, durationMs: Date.now() - start };
  }

  // ─── Spoke: Internal (tasks + time entries) ───────────────────────────────

  private async runInternalSpoke(
    client: Client,
    allTasks: Task[],
    timeEntries: TimeEntry[],
  ): Promise<SpokeResult> {
    const start = Date.now();
    const items: IntelItem[] = [];
    const now = new Date();

    // Filter to client
    const clientTasks = allTasks.filter(
      (t) => t.clientNumber === client.clientNumber || t.clientNumber === client.id,
    );
    const clientEntries = timeEntries.filter((e) => e.clientNumber === client.clientNumber);

    // Task status items
    for (const t of clientTasks.slice(0, 20)) {
      items.push({
        source: "internal_tasks", category: "matter_status",
        eventAt: t.updatedAt.toISOString(),
        matterNumber: t.matterNumber,
        headline: `Task ${t.status}: ${t.description.slice(0, 80)}`,
        data: {
          taskId: t.id, status: t.status, phase: t.currentPhase,
          pendingGates: t.pendingGates?.length ?? 0,
          outputSnippet: t.output?.slice(0, 300),
        },
      });
    }

    // Billing items (recent WIP)
    const openEntries = clientEntries.filter((e) => !e.endedAt).slice(0, 10);
    for (const e of openEntries) {
      items.push({
        source: "internal_time", category: "billing",
        eventAt: e.startedAt.toISOString(),
        matterNumber: e.matterNumber,
        headline: `WIP: ${e.description.slice(0, 80)} ($${(e.billingAmountUsd ?? 0).toFixed(0)} unbilled)`,
        data: { entryId: e.id, description: e.description, billingAmountUsd: e.billingAmountUsd },
      });
    }

    return { source: "internal_tasks", items, durationMs: Date.now() - start };
  }

  // ─── Structured snapshots from chalkboard ────────────────────────────────

  private buildMatterSnapshots(
    clientMatters: ClientMatter[],
    tasks: Task[],
    entries: TimeEntry[],
    chalkboard: Chalkboard,
  ): BriefingMatterSnapshot[] {
    const now = new Date();
    return clientMatters.map((m): BriefingMatterSnapshot => {
      const matterTasks = tasks.filter((t) => t.matterNumber === m.matterNumber);
      const matterEntries = entries.filter((e) => e.matterNumber === m.matterNumber);

      const lastActivity = matterTasks
        .map((t) => t.updatedAt)
        .sort((a, b) => b.getTime() - a.getTime())[0];
      const daysSinceActivity = lastActivity
        ? Math.floor((now.getTime() - lastActivity.getTime()) / 86_400_000)
        : 999;

      const closed = matterEntries.filter((e) => e.endedAt && e.billingAmountUsd != null);
      const open = matterEntries.filter((e) => !e.endedAt);
      const openBillingUsd = open.reduce((s, e) => s + (e.billingAmountUsd ?? 0), 0);
      const totalBilledUsd = closed.reduce((s, e) => s + (e.billingAmountUsd ?? 0), 0);
      const pendingGates = matterTasks.reduce((s, t) => s + (t.pendingGates?.length ?? 0), 0);

      const latestTask = [...matterTasks].sort(
        (a, b) => b.updatedAt.getTime() - a.updatedAt.getTime(),
      )[0];
      const status: BriefingMatterSnapshot["status"] =
        latestTask?.status === "running" ? "active" :
        latestTask?.status === "complete" ? "complete" : "idle";

      return {
        matterNumber: m.matterNumber,
        description: m.description,
        practiceArea: m.practiceArea,
        status,
        daysSinceActivity,
        openBillingUsd,
        totalBilledUsd,
        pendingGates,
        lastOutput: latestTask?.output?.slice(0, 300),
      };
    });
  }

  private buildBillingSnapshot(
    entries: TimeEntry[],
    clientNumber: string,
    matters: BriefingMatterSnapshot[],
  ): BriefingBillingSnapshot {
    const now = new Date();
    const ninety = new Date(now.getTime() - 90 * 86_400_000);
    const clientEntries = entries.filter((e) => e.clientNumber === clientNumber);
    const closed = clientEntries.filter(
      (e) => e.endedAt && new Date(e.endedAt) >= ninety && e.billingAmountUsd != null,
    );
    const open = clientEntries.filter((e) => !e.endedAt);
    const last90DaysUsd = closed.reduce((s, e) => s + (e.billingAmountUsd ?? 0), 0);
    const wipUsd = matters.reduce((s, m) => s + m.openBillingUsd, 0);
    const oldest = open.map((e) =>
      Math.floor((now.getTime() - e.startedAt.getTime()) / 86_400_000)
    ).sort((a, b) => b - a)[0] ?? 0;
    return {
      last90DaysUsd,
      wipUsd,
      oldestWipDays: oldest,
      openMatterCount: matters.filter((m) => m.status !== "complete").length,
    };
  }

  private collectOpenItems(chalkboard: Chalkboard, matters: BriefingMatterSnapshot[]): string[] {
    const items: string[] = [];

    for (const m of matters) {
      if (m.pendingGates > 0) items.push(`${m.matterNumber}: ${m.pendingGates} gate(s) await partner approval`);
      if (m.openBillingUsd > 0) items.push(`${m.matterNumber}: $${m.openBillingUsd.toFixed(0)} WIP unbilled`);
      if (m.status === "idle" && m.daysSinceActivity > 30) {
        items.push(`${m.matterNumber}: no activity for ${m.daysSinceActivity} days — confirm status with client`);
      }
    }

    // Surface high-signal items from the chalkboard
    const correspondenceItems = chalkboard.byCategory("correspondence").slice(0, 3);
    for (const i of correspondenceItems) items.push(`Correspondence: ${i.headline}`);

    // Surface recent email threads — most recent 3, trimmed
    const emailItems = [
      ...chalkboard.bySource("email_graph"),
      ...chalkboard.bySource("email_gmail"),
    ].sort((a, b) => {
      const ta = a.eventAt ? new Date(a.eventAt).getTime() : 0;
      const tb = b.eventAt ? new Date(b.eventAt).getTime() : 0;
      return tb - ta;
    }).slice(0, 3);
    for (const e of emailItems) {
      items.push(`Email [${e.eventAt?.slice(0, 10) ?? "?"}]: ${e.headline.slice(0, 100)}`);
    }

    return items.slice(0, 15);
  }

  // ─── Hub synthesis ────────────────────────────────────────────────────────

  private async synthesize(
    client: Client,
    chalkboard: Chalkboard,
    matters: BriefingMatterSnapshot[],
    billing: BriefingBillingSnapshot,
    openItems: string[],
    opts: { taskId?: string; briefingDate?: string; industryContext?: string },
  ): Promise<{ executiveSummary: string; document: string }> {
    const start = Date.now();

    // Organise chalkboard items for the prompt
    const bySource = (src: IntelSource, limit = 8) =>
      chalkboard.bySource(src).slice(0, limit)
        .map((i) => `  - [${i.category}${i.eventAt ? ` ${i.eventAt.slice(0, 10)}` : ""}] ${i.headline}`)
        .join("\n") || "  (none)";

    // Email is merged across Graph + Gmail, sorted by date
    const emailItems = [
      ...chalkboard.bySource("email_graph"),
      ...chalkboard.bySource("email_gmail"),
    ].sort((a, b) => {
      const ta = a.eventAt ? new Date(a.eventAt).getTime() : 0;
      const tb = b.eventAt ? new Date(b.eventAt).getTime() : 0;
      return tb - ta;
    }).slice(0, 12);
    const emailBlock = emailItems.length > 0
      ? emailItems.map((i) => `  - [${i.eventAt?.slice(0, 10) ?? "?"}] ${i.headline}`).join("\n")
      : "  (not configured or no results)";

    const matterLines = matters
      .map((m) =>
        `• ${m.matterNumber} [${m.status.toUpperCase()}] — ${m.description}` +
        (m.practiceArea ? ` (${m.practiceArea})` : "") +
        ` | $${m.totalBilledUsd.toFixed(0)} billed | ${m.daysSinceActivity}d idle` +
        (m.pendingGates > 0 ? ` | ⚠ ${m.pendingGates} gate(s)` : ""),
      )
      .join("\n") || "(no matters)";

    const prompt = `You are writing a pre-call partner briefing synthesised from multiple connected systems.

CLIENT: ${client.name} (${client.clientNumber})
BRIEFING DATE: ${opts.briefingDate ?? new Date().toISOString().slice(0, 10)}

MATTER STATUS (from internal systems):
${matterLines}

BILLING:
  Last 90 days: $${billing.last90DaysUsd.toFixed(0)}
  WIP unbilled: $${billing.wipUsd.toFixed(0)}
  Oldest open entry: ${billing.oldestWipDays}d
  Open matters: ${billing.openMatterCount}

OPEN ITEMS:
${openItems.map((i) => `- ${i}`).join("\n") || "- None"}

INTELLIGENCE FROM CONNECTED SYSTEMS:
Clio:
${bySource("clio")}
iManage:
${bySource("imanage")}
Email (most recent first):
${emailBlock}
Slack:
${bySource("slack")}
Documents (Drive/Box):
${bySource("google_drive", 5)}
SharePoint:
${bySource("sharepoint", 5)}
Teams Conversations:
${bySource("teams_chat", 5)}
Knowledge Store:
${bySource("knowledge_store")}

${client.notes ? `RELATIONSHIP NOTES:\n${client.notes}\n` : ""}
${opts.industryContext ? `INDUSTRY CONTEXT:\n${opts.industryContext}\n` : ""}

Write:
1. A 2-sentence EXECUTIVE SUMMARY — the single most important thing the partner needs to know.
2. A full BRIEFING DOCUMENT in Markdown:
   ## ${client.name} — Partner Briefing (${opts.briefingDate ?? "today"})
   ### Executive Summary
   ### Matter Status
   ### Billing Posture
   ### Recent Email Threads              ← synthesise Graph + Gmail, highlight open threads
   ### Correspondence & Activity         ← synthesise Clio + iManage + Slack + Teams
   ### Documents in Play                 ← synthesise Drive/Box/iManage/SharePoint docs
   ### Regulatory / Industry Context     ← from knowledge store
   ### Open Items & Actions Required
   ### Relationship Notes

Return JSON:
{"executiveSummary":"...","document":"..."}`;

    try {
      const response = await this.client.messages.create({
        model: SONNET_MODEL, max_tokens: 2000,
        messages: [{ role: "user", content: prompt }],
      });

      const usage = response.usage;
      costStore.record({
        model: resolveModelId(SONNET_MODEL), provider: "anthropic",
        inputTokens: usage.input_tokens, outputTokens: usage.output_tokens,
        cacheWriteTokens: (usage as Record<string, unknown>)["cache_creation_input_tokens"] as number | undefined,
        cacheReadTokens: (usage as Record<string, unknown>)["cache_read_input_tokens"] as number | undefined,
        costUsd: calcCostUsd(resolveModelId(SONNET_MODEL), usage.input_tokens, usage.output_tokens),
        estimatedWh: null, estimatedWatts: null,
        durationMs: Date.now() - start, context: "client_briefing", taskId: opts.taskId,
      });

      const raw = response.content[0]?.type === "text" ? response.content[0].text : "{}";
      const s = raw.indexOf("{"), e = raw.lastIndexOf("}");
      if (s === -1 || e <= s) return this.fallback(client, matters, billing, openItems);
      return JSON.parse(raw.slice(s, e + 1)) as { executiveSummary: string; document: string };
    } catch {
      return this.fallback(client, matters, billing, openItems);
    }
  }

  private fallback(
    client: Client,
    matters: BriefingMatterSnapshot[],
    billing: BriefingBillingSnapshot,
    openItems: string[],
  ): { executiveSummary: string; document: string } {
    const summary = `${client.name} has ${matters.length} matter(s) on record. ` +
      `$${billing.wipUsd.toFixed(0)} WIP outstanding; ${openItems.length} open item(s) require attention.`;
    return {
      executiveSummary: summary,
      document: `## ${client.name} — Partner Briefing\n\n` +
        `**Matters:** ${matters.length} | **WIP:** $${billing.wipUsd.toFixed(0)} | **Open:** ${openItems.length}\n\n` +
        openItems.map((i) => `- ${i}`).join("\n"),
    };
  }
}

export const briefingEngine = new BriefingEngine();
