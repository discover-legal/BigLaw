// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

// ─── Agent taxonomy ───────────────────────────────────────────────────────────

export type AgentTier = 0 | 1 | 2 | 3;

export type AgentType =
  | "root"        // tier 0
  | "manager"     // tier 1
  | "specialist"  // tier 2
  | "tool";       // tier 3

export type AgentDomain =
  | "orchestration"
  | "research"
  | "investigation"
  | "drafting"
  | "review"
  | "compliance"
  | "analysis"
  | "tool";

export interface AgentDefinition {
  id: string;
  name: string;
  tier: AgentTier;
  type: AgentType;
  domain: AgentDomain;
  /** Free-text capabilities description — embedded for semantic search */
  description: string;
  systemPrompt: string;
  /** Tool names this agent is permitted to call — principle of least privilege */
  allowedTools: string[];
  skills: string[];
  metadata?: Record<string, unknown>;
}

// ─── DyTopo core ──────────────────────────────────────────────────────────────

export type TaskPhase =
  | "intake"
  | "research"
  | "analysis"
  | "drafting"
  | "review"
  | "verification"
  | "delivery";

export interface RoundGoal {
  id: string;
  round: number;
  phase: TaskPhase;
  description: string;
  /** What outputs the orchestrator expects this round to produce */
  expectedOutputs: string[];
}

export interface NeedDescriptor {
  agentId: string;
  /** Natural language: what context or knowledge this agent currently requires */
  text: string;
  embedding?: number[];
}

export interface OfferDescriptor {
  agentId: string;
  /** Natural language: what knowledge or capability this agent can contribute */
  text: string;
  embedding?: number[];
}

export interface CommunicationEdge {
  /** Agent that offers → sends its offer content as context to the needing agent */
  from: string;
  to: string;
  similarity: number;
  offerText: string;
}

export interface AgentMessage {
  id: string;
  from: string;
  to: string;
  content: string;
  round: number;
  timestamp: Date;
}

export interface RoundState {
  roundId: string;
  goal: RoundGoal;
  activeAgentIds: string[];
  edges: CommunicationEdge[];
  messages: AgentMessage[];
  findings: Finding[];
  status: "running" | "complete" | "awaiting_gate";
  startedAt: Date;
  completedAt?: Date;
}

// ─── Memory ──────────────────────────────────────────────────────────────────

/**
 * Intra-round memory: accumulates within a single round.
 * Cleared at round boundaries.
 */
export interface IntraRoundMemory {
  roundId: string;
  /** Keyed by agentId — messages received this round */
  receivedMessages: Record<string, AgentMessage[]>;
  /** Keyed by agentId — findings produced this round */
  agentFindings: Record<string, Finding[]>;
  sharedContext: string[];
}

/** Alias used in memory module imports */
export type InterRoundMemory = MemoryEntry[];

/**
 * Inter-round memory: persists across rounds, stored in the vector DB.
 * Agents query this to recover context from earlier rounds.
 */
export interface MemoryEntry {
  id: string;
  taskId: string;
  round: number;
  phase: TaskPhase;
  agentId?: string;   // undefined = task-level summary
  /** Natural language content, embedded for retrieval */
  content: string;
  embedding?: number[];
  tags: string[];
  createdAt: Date;
}

// ─── Laverne-style debate protocol ───────────────────────────────────────────

export interface Citation {
  source: string;       // document ID or URL
  quote: string;        // verbatim text cited
  page?: number;
  /** True when mechanical string-match against source passes */
  mechanicallyVerified: boolean;
}

export interface Finding {
  id: string;
  agentId: string;
  agentName: string;
  content: string;
  citations: Citation[];
  /** 0–1 confidence from the producing agent */
  confidence: number;
  challenged: boolean;
  challenge?: Challenge;
  resolved: boolean;
  verificationResult?: VerificationResult;
  round: number;
  timestamp: Date;
}

export interface Challenge {
  challengerId: string;
  challengerName: string;
  content: string;
  citations: Citation[];
  /** Orchestrator's resolution after weighing both sides */
  resolution?: string;
  resolvedAt?: Date;
}

export interface VerificationCheck {
  name: string;
  passed: boolean;
  notes?: string;
}

export interface VerificationResult {
  findingId: string;
  checks: VerificationCheck[];
  passed: boolean;
  completedAt: Date;
}

// ─── Human gates ─────────────────────────────────────────────────────────────

export interface GateRequest {
  id: string;
  taskId: string;
  findingId: string;
  finding: Finding;
  status: "pending" | "approved" | "rejected";
  reviewerNote?: string;
  createdAt: Date;
  reviewedAt?: Date;
}

// ─── Task management ─────────────────────────────────────────────────────────

export type WorkflowType =
  | "counsel"      // single specialist, quick turnaround
  | "roundtable"   // multi-agent open discussion
  | "adversarial"  // red-team vs blue-team
  | "review"       // document review and annotation
  | "tabulate"     // bulk extraction → spreadsheet
  | "full_bench";  // comprehensive all-tier review

export type TaskStatus =
  | "pending"
  | "running"
  | "awaiting_gate"
  | "complete"
  | "failed";

export interface Task {
  id: string;
  description: string;
  /** Law-firm client number (the client this matter belongs to). Optional. */
  clientNumber?: string;
  /** Law-firm matter number (the file/matter reference). Optional. */
  matterNumber?: string;
  /** Lawyer profile ids this matter is assigned to. A lawyer sees a matter only
   *  if their id is here; partners (admins) see all and control assignment.
   *  Multiple ids = a partner has shared the case across lawyers. */
  assignedLawyerIds?: string[];
  /** Document IDs ingested into the knowledge store for this task */
  documentIds: string[];
  /**
   * ProfileId of the user who submitted this task — used to scope agent tool
   * access so lawyers' agents cannot discover or read documents owned by other users.
   * Undefined for partner-submitted tasks (partners see the full knowledge store).
   */
  createdByProfileId?: string;
  workflowType: WorkflowType;
  status: TaskStatus;
  currentPhase: TaskPhase;
  currentRound: number;
  maxRounds: number;
  activeAgentIds: string[];
  rounds: RoundState[];
  findings: Finding[];
  pendingGates: GateRequest[];
  /** Final synthesised output from the root orchestrator */
  output?: string;
  error?: string;
  createdAt: Date;
  updatedAt: Date;
  completedAt?: Date;
  /** Structured tabular output — populated for the `tabulate` workflow. */
  table?: TaskTable;
}

/** Structured spreadsheet-style output for the `tabulate` workflow. */
export interface TaskTable {
  /** Display column headers, in order. */
  columns: string[];
  /**
   * One object per row. Keys include every column name plus an internal
   * `_findingId` linking the row back to its source finding.
   */
  rows: Array<Record<string, string>>;
  /** Unique finding IDs the rows were derived from. */
  sourceFindingIds: string[];
  generatedAt: Date;
}

// ─── Lawyers, roles, sessions ────────────────────────────────────────────────

/** partner = admin (sees all matters, controls assignment); lawyer = sees own matters. */
export type LawyerRole = "lawyer" | "partner";

export const PRACTICE_AREAS = [
  "Corporate & M&A",
  "Competition & Antitrust",
  "Employment & Labour",
  "Intellectual Property",
  "Real Estate",
  "Banking & Finance",
  "Litigation & Dispute Resolution",
  "Tax",
  "Regulatory & Compliance",
  "Data Privacy & Cybersecurity",
  "Immigration",
  "Insolvency & Restructuring",
  "Capital Markets",
  "Insurance",
  "Environmental & Climate",
] as const;

export type PracticeArea = typeof PRACTICE_AREAS[number];

export interface LawyerProfile {
  id: string;
  name: string;
  email: string;
  role: LawyerRole;
  title?: string;
  /** Hex accent for the initials avatar. */
  color?: string;
  /** OAuth subject this profile is linked to, once auth is live. */
  oauthSubject?: string;
  /** Practice areas this lawyer specialises in. */
  practiceAreas?: string[];
  /** Short bio / description. */
  bio?: string;
  createdAt: Date;
}

// ─── Clients ─────────────────────────────────────────────────────────────────

export interface ClientMatter {
  matterNumber: string;
  description: string;
  practiceArea?: string;
  openedAt: Date;
}

export interface Client {
  id: string;
  name: string;
  /** Unique firm-assigned client reference number. */
  clientNumber: string;
  matters: ClientMatter[];
  /** Names of opposing/adverse parties — used for conflict-of-interest checks. */
  adversaries: string[];
  notes?: string;
  createdAt: Date;
  updatedAt: Date;
}

/** Result of a conflict-of-interest check. */
export interface ConflictCheckResult {
  hasConflict: boolean;
  /** Which existing client's adversary list triggered the conflict. */
  conflictingClientId?: string;
  conflictingClientName?: string;
  matchedAdversary?: string;
}

/** The authenticated principal for a request (or the local-dev partner when auth is off). */
export interface SessionUser {
  profileId: string;
  name: string;
  email: string;
  role: LawyerRole;
}

// ─── Knowledge store ─────────────────────────────────────────────────────────

export interface Document {
  id: string;
  title: string;
  content: string;
  source?: string;
  jurisdiction?: string;
  documentType?: string;
  /** Lawyer profile id that uploaded/ingested this doc (for access scoping). */
  ownerId?: string;
  /** Auto-detected or manually set practice area. */
  practiceArea?: string;
  /** Auto-detected client number. */
  detectedClientNumber?: string;
  metadata?: Record<string, unknown>;
  ingestedAt: Date;
}

export interface SearchResult {
  document: Document;
  score: number;
  excerpt: string;
}

// ─── Embeddings ──────────────────────────────────────────────────────────────

export interface EmbeddingResult {
  text: string;
  embedding: number[];
  model: string;
}