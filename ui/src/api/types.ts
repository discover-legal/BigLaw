// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The REST contract's JSON shapes as the UI reads them (README § REST API; the shapes the
// contract left open are written down in DESIGN § UI contract, PR 11). Readers ignore unknown fields.

export type Method = 'exact' | 'tolerant' | 'judge' | 'ensemble' | 'none' | `${'exact' | 'tolerant'}@transcript`;

export interface Doc {
  id: string;
  name: string;
  contentType: string;
  method: string; // text | text_layer | vision
  status: string; // ready | transcribing | needs_transcription | failed
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
}

export interface Step {
  step: number;
  kind: 'lead_turn' | 'tool_call';
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

export interface Run {
  status: TaskStatus;
  step: number;
  stopReason: string | null;
  budget: { calls: number; usd: number | null; remaining: number };
  openFindings: number;
}

export interface Gate {
  gateId: string;
  kind: 'deliverable' | 'finding';
  findingId?: string;
}

/** A closed gate: who decided is PR 8's (auth); a rejection always carries its reason. */
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
  decidedGates?: GateDecision[];
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
export interface Playbook { id: string; name: string; scope: string; clauses: number }
export interface TimeEntry { id: string; date: string; matterNumber: string; hours: number; narrative: string; timekeeper: string }
