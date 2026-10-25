// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The REST contract's JSON shapes as the UI reads them (README § REST API; the shapes the
// contract left open are written down in DESIGN § UI contract, PR 11). Readers ignore unknown fields.

type Rung = 'exact' | 'tolerant' | 'judge' | 'ensemble';
/** A ladder method (docs.VerifyQuote): a rung, the rung on a machine transcript, or none (excluded). */
export type Method = Rung | `${Rung}@transcript` | 'none';

export interface Doc {
  id: string;
  name: string;
  contentType: string;
  fileId: string; // the original upload; "" for a document posted as text
  method: string; // text | text_layer | vision
  noText: boolean;
  status: string; // ready | needs_transcription | failed (+ transcribing: contract v1, PR 8)
  transcript?: { model: string; agreement: number; lowConfidence: boolean; pages: { page: number; agreement: number }[] };
  pages: number;
  notes: string[];
  sections: number;
  chunks: number;
  createdAt: string;
  matterNumber?: string;
}

export interface OutlineEntry {
  sectionId: string;
  number: string;
  title: string;
  level: number;
  tokens: number;
}

export interface SectionPage {
  sectionId: string;
  number: string;
  title: string;
  text: string;
  offset: number;
  total: number;
  truncated: boolean;
  next?: number;
}

export interface Hit {
  documentId: string;
  sectionId: string;
  title: string;
  text: string;
  context?: string;
  rank: number;
  transcribed?: boolean;
}

export interface Matter {
  number: string;
  title: string;
  documentIds: string[];
}

export interface Client {
  id: string;
  name: string;
  matters: Matter[];
}

export interface Citation {
  documentId: string;
  quote: string;
  locator: string;
  sectionId: string;
  method: Method;
  confidence: number;
  match: string; // the verbatim source text the ladder matched ("" when unverified)
  note?: string;
}

export interface Finding {
  fid: string;
  issueId: string;
  question: string;
  claim: string;
  severity: string;
  status: 'verified' | 'quarantined';
  citations: Citation[];
  valueNotes?: string[]; // typed values admission dropped, and why (absent in the recording)
}

export interface Step {
  step: number;
  kind: 'lead_turn' | 'tool_call' | 'note'; // note: a lawyer's gate decision the lead reads when the run resumes
  tool: string;
  status: 'started' | 'committed' | 'refused' | 'failed';
  summary: string;
  input: unknown;
  output: unknown;
  source?: 'computed' | 'scripted'; // recorded engine only
}

// rejected: a lawyer rejected the deliverable (with a reason). Live (PR 4b) the lead resumes with that reason;
// the recorded engine has no revised run, so its replay ends there.
export type TaskStatus = 'queued' | 'running' | 'awaiting_gate' | 'complete' | 'failed' | 'cancelled' | 'rejected';
/** Why the run stopped (README § Stop conditions); a rejection is a gate decision, not a stop reason. */
export type StopReason = 'finished' | 'budget_exhausted' | 'max_steps' | 'awaiting_gate' | 'cancelled' | 'failed';

export interface Run {
  status: TaskStatus;
  step: number;
  stopReason: StopReason | null;
  budget: { calls: number; usd: number | null; remaining: number };
  openFindings: number;
}

export interface Gate {
  gateId: string;
  /** hook: a configurable hook holds a call until a lawyer decides (hook, subject, detail say which). */
  kind: 'deliverable' | 'finding' | 'hook';
  findingId?: string;
  hook?: string;
  subject?: string;
  detail?: string;
}

/** A closed gate: who decided is PR 8's (auth); a rejection always carries its (non-empty) reason. */
export interface GateDecision extends Gate {
  decision: 'approved' | 'rejected';
  reason?: string;
  decidedAt: string;
}

export interface Task {
  id: string;
  description: string;
  workflowType: string;
  jurisdiction: string;
  matterNumber: string;
  documentIds: string[];
  status: TaskStatus;
  createdAt: string;
  run: Run;
  pendingGates: Gate[];
  decidedGates: GateDecision[];
  findings: Finding[];
  output: string | null;
  groundingAlert: string | null;
  replay?: { key: string; title: string; prompt: string }; // recorded engine only
}

export interface CostEntry {
  step: number;
  tool: string;
  model: string;
  inputTokens: number;
  outputTokens: number;
  usd: number | null;
}

export interface TaskCost {
  taskId: string;
  summary: { calls: number; inputTokens: number; outputTokens: number; usd: number | null; unpricedCalls: number };
  entries: CostEntry[];
}

export interface NewTask {
  description: string;
  workflowType: 'memo' | 'diligence' | 'contract_review';
  jurisdiction: string;
  matterNumber: string;
  documentIds: string[];
}

// SSE on GET /tasks/{id}/events: `step` (data: Step), `task` (data: Run), `done` (data: Run).
export type RunEvent = { type: 'step'; step: Step } | { type: 'task' | 'done'; run: Run };

export interface Profile { id: string; name: string; role: string }
/** One row of the configuration table (cmd/biglaw/config.go): read-only, as `biglaw version --config` prints it. */
export interface Setting { name: string; default: string; pr: number; secret?: boolean }
/** A search the recorded engine offers, with the hits v2's own Search returned for it. */
export interface RecordedSearch { matterNumber: string; query: string; hits: Hit[] }
export interface Playbook { id: string; name: string; scope: 'client' | 'matter' | 'personal' | 'firm'; clauses: number }
export interface TimeEntry { id: string; date: string; matterNumber: string; hours: number; narrative: string; timekeeper: string }

// Manage (README § Manage; GET /manage/…): what a run uses, by section, with admins' versioned overrides.
export type ManageSection = 'agents' | 'tools' | 'packs' | 'skills' | 'hooks';
export interface ItemUsage { tasks: number; steps: number; calls: number; usd: number; unpriced: number }
export interface ManageItem {
  id: string; section: ManageSection; kind: string; name: string; summary: string;
  editable: boolean; builtin: boolean; version: number; latest: number; deleted: boolean; status?: string;
  files: string[]; uses: string[]; usedBy: string[]; usage: ItemUsage | null;
}
export interface HookSource { file: string; func: string; text: string }
export interface Hook { name: string; wraps: string; what: string; source: HookSource[] }
export interface ManageDetail extends ManageItem {
  content: Record<string, string>; original: Record<string, string> | null; hook?: Hook;
  callers: string[]; // what a skill's tools list may name
}
export interface SpecVersion {
  item: string; version: number; action: 'save' | 'revert' | 'create' | 'delete' | 'secret set' | 'secret deleted' | 'test'; files?: Record<string, string>;
  builtin: boolean; deleted: boolean; from: number; restored: number | null; author: string; note: string; createdAt: string;
  /** A change log entry that is not a version (a secret set or deleted, a Test call). */
  event?: boolean;
}
export interface SaveResult { version: SpecVersion; notes: string[] }
export interface Problem { file: string; message: string }
/** GET /me: who is asking; until sign-in (PR 8) the local user, an admin. */
export interface Me { id: string; name: string; admin: boolean; authEnabled: boolean; adminWithoutAuth?: boolean }
/** A secret as the API shows it: never its value. */
export interface SecretView { name: string; origin: string; updatedBy: string; updatedAt: string; usedBy: string[] }
/** One tool a server lists: its description is the server's (third-party) text; item and previous: already imported, and the text an admin accepted. */
export interface RemoteTool { name: string; description: string; in?: unknown; problem?: string; item?: string; previous?: string; previousIn?: unknown }
export interface Discovery { server: string; serverName: string; protocol: string; tools: RemoteTool[] }
export interface ImportResult { remote: string; item?: string; action?: 'created' | 'updated' | 'unchanged'; previous: string; text: string; previousIn?: unknown; in?: unknown; problem?: string }
export interface TestResult {
  taskId: string; output: unknown; error: string; millis: number;
  usage: { calls: number; inputTokens: number; outputTokens: number; usd: number; unpriced: number };
}
