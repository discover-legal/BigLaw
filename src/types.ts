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
  /**
   * Jurisdictions this agent is optimised for, as BCP-47-like tags or common-law
   * region codes: "US", "US-NY", "EU", "UK", "AU", "SG", "HK", "IN", "CA", etc.
   *
   * Undefined / empty = jurisdiction-neutral: the agent applies whatever governing
   * law the matter specifies and is always eligible for any task.
   *
   * When set, DyTopo will only recruit this agent for tasks whose jurisdiction
   * prefix-matches one of these values (e.g. agent ["US"] matches task "US-NY").
   */
  jurisdictions?: string[];
  /** USD/hour billing rate for this agent's work. Overrides the tier default
   *  from AGENT_BILLING_RATE_T* config. Set to 0 to make this agent non-billable. */
  billingRate?: number;
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
  | "counsel"        // single specialist, quick turnaround
  | "roundtable"     // multi-agent open discussion
  | "adversarial"    // red-team vs blue-team
  | "review"         // document review and annotation
  | "tabulate"       // bulk extraction → spreadsheet
  | "full_bench"     // comprehensive all-tier review
  | "legal_design"   // structured legal task pipeline (Lavern)
  | "pre_engagement";// scoping, conflicts, initial assessment (Lavern)

export type TaskStatus =
  | "pending"
  | "running"
  | "awaiting_gate"
  | "complete"
  | "failed";

export interface Task {
  id: string;
  description: string;
  /**
   * Governing jurisdiction of the matter — used to filter jurisdiction-specific
   * agents. BCP-47-style codes or common-law region codes are accepted:
   * "US", "US-NY", "US-CA", "EU", "UK", "AU", "SG", "HK", "IN", "CA", etc.
   * Unset means no jurisdiction filter is applied.
   */
  jurisdiction?: string;
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
  /**
   * Optional NOSLEGAL v4 taxonomy tags for this task — auto-detected from the
   * task description at submission time.
   */
  noslegal?: NosLegalTags;
  /** ID of the open TimeEntry tracking active work on this task (cleared on close). */
  activeTimeEntryId?: string;
}

// ─── Time tracking ───────────────────────────────────────────────────────────

export type TimeEventType =
  | "task_run"    // lawyer-supervised task execution
  | "gate_review" // lawyer reviewing a human gate
  | "agent_work"; // AI agent running a DyTopo round phase

export interface TimeEntry {
  id: string;
  /** Lawyer the entry is attributed to for billing. Optional on agent_work entries
   *  where no responsible lawyer is assigned (partner-only visibility in that case). */
  profileId?: string;
  profileName?: string;
  /** Set on agent_work entries — identifies which agent performed the work. */
  agentId?: string;
  agentName?: string;
  taskId: string;
  matterNumber?: string;
  clientNumber?: string;
  description: string;
  event: TimeEventType;
  startedAt: Date;
  endedAt?: Date;
  durationMs: number;
  /** 6-minute billing increments (0.1 hr each). Rounded UP. 0 while running. */
  billingUnits: number;
  /** USD/hour rate captured at the time the entry is created. */
  billingRate?: number;
  /** Pre-computed fee: billingUnits × 0.1 × billingRate. Set on close. */
  billingAmountUsd?: number;
  /** ISO timestamp set when this entry has been pushed to a Clio matter as an activity. */
  clioSyncedAt?: string;
  /** OCG compliance suggestions for this entry. */
  ocgSuggestions?: OcgSuggestion[];
  /** ISO timestamp of the last OCG compliance check run on this entry. */
  ocgCheckedAt?: string;
  /** UTBMS Phase/Task code assigned on close, e.g. "L210" (Pleadings). */
  utbmsTaskCode?: string;
  /** UTBMS Activity code assigned on close, e.g. "A103" (Draft/Revise). */
  utbmsActivityCode?: string;
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

/**
 * User experience mode — three-tier access palette.
 *
 *   admin        — Partners only. Full platform: user management, firm
 *                  analytics, NOSLEGAL dashboard, all settings, time
 *                  reporting, every matter. Accent: gold #E6B450.
 *
 *   full_flavour — Full law firm experience. All legal workflows,
 *                  connectors, conflict checks, time tracking, client
 *                  roster, matter management. Accent: scarlet #C8102E.
 *
 *   lite         — Core features only: submit tasks, view results, upload
 *                  documents, basic search. No billing, no conflict engine.
 *                  Accent: amber-gold #C4940F.
 *
 * Partners are always admin (immutable). Lawyers default to full_flavour;
 * they may be set to lite. Admins can set mode for any lawyer profile.
 */
export type UserMode = "admin" | "full_flavour" | "lite";

/** Hex accent colour for each mode. */
export const MODE_COLORS: Record<UserMode, string> = {
  admin:        "#1A1A1A",   // near-black (UI overrides to gold for visibility)
  full_flavour: "#C8102E",   // scarlet
  lite:         "#C4940F",   // amber-gold
};

/** Feature flags carried with the session so UI can conditionally render. */
export interface ModeCapabilities {
  /** Can manage users, settings, and system-wide configuration. */
  manageUsers: boolean;
  /** Sees every matter regardless of assignment. */
  seeAllMatters: boolean;
  /** Assign lawyers to matters. */
  assignMatters: boolean;
  /** Client roster, matter sub-lists, conflict-of-interest checks. */
  clientRoster: boolean;
  /** Time tracking and billable-unit export. */
  timeTracking: boolean;
  /** NOSLEGAL matter analytics dashboard. */
  matterAnalytics: boolean;
  /** Full connector toolset (Westlaw, Everlaw, Trellis, etc.). */
  fullConnectors: boolean;
  /** Admin settings panel. */
  adminSettings: boolean;
}

export const MODE_CAPABILITIES: Record<UserMode, ModeCapabilities> = {
  admin: {
    manageUsers: true, seeAllMatters: true, assignMatters: true,
    clientRoster: true, timeTracking: true, matterAnalytics: true,
    fullConnectors: true, adminSettings: true,
  },
  full_flavour: {
    manageUsers: false, seeAllMatters: false, assignMatters: false,
    clientRoster: true, timeTracking: true, matterAnalytics: false,
    fullConnectors: true, adminSettings: false,
  },
  lite: {
    manageUsers: false, seeAllMatters: false, assignMatters: false,
    clientRoster: false, timeTracking: false, matterAnalytics: false,
    fullConnectors: false, adminSettings: false,
  },
};

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

/**
 * Writing tone fingerprint derived from a lawyer's LinkedIn posts or uploaded
 * writing samples. Stored on the profile and injected into drafting-domain
 * agent prompts so output matches the lawyer's natural voice.
 */
export interface ToneProfile {
  generatedAt: string;
  sourceType: "linkedin_export" | "writing_samples";
  sampleCount: number;
  formality: "formal" | "semi-formal" | "conversational";
  sentenceStyle: "long-complex" | "mixed" | "short-punchy";
  vocabulary: "technical-heavy" | "balanced" | "plain-language";
  rhetoricalStyle: "assertive" | "collaborative" | "hedging" | "analytical";
  /** Distinctive patterns observed in the samples, e.g. "opens with questions". */
  signaturePatterns: string[];
  /** Ready-to-inject prompt fragment — the verbatim block appended to drafting agents. */
  injectionSnippet: string;
}

export interface LawyerProfile {
  id: string;
  name: string;
  email: string;
  role: LawyerRole;
  /**
   * UX mode — controls feature set and UI colour accent.
   * Partners are always admin. Lawyers default to full_flavour.
   * Admins can override any profile; lawyers can only toggle between
   * full_flavour and lite for themselves.
   */
  mode?: UserMode;
  title?: string;
  /** Hex accent for the initials avatar. */
  color?: string;
  /** OAuth subject this profile is linked to, once auth is live. */
  oauthSubject?: string;
  /** Practice areas this lawyer specialises in. */
  practiceAreas?: string[];
  /** Short bio / description. */
  bio?: string;
  /** LinkedIn public profile URL, populated on OAuth login or manual import. */
  linkedinProfileUrl?: string;
  /** Tone fingerprint derived from LinkedIn posts or uploaded writing samples. */
  toneProfile?: ToneProfile;
  createdAt: Date;
}

// ─── Clients ─────────────────────────────────────────────────────────────────

export interface ClientMatter {
  matterNumber: string;
  description: string;
  practiceArea?: string;
  openedAt: Date;
  budgetUsd?: number;
  budgetAlertThresholds?: number[];
  budgetAlertsTriggered?: number[];
}

export interface BudgetAlert {
  matterNumber: string;
  clientNumber: string;
  budgetUsd: number;
  burnUsd: number;
  burnPct: number;
  threshold: number;
  triggeredAt: string;
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
  /** ID of the OcgDocument for this client (if any). */
  ocgId?: string;
  /** Client voice/communication guide built from writing samples. */
  voiceGuide?: ClientVoiceGuide;
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
  /** Resolved mode — partners always get admin; lawyers get their profile's mode. */
  mode: UserMode;
}

// ─── Knowledge store ─────────────────────────────────────────────────────────

/**
 * NOSLEGAL taxonomy tags — optional supplementary multi-faceted classification.
 * See https://github.com/noslegal/taxonomy for the full controlled vocabulary.
 *
 * NOSLEGAL v4 uses eight orthogonal facets. We capture the four most useful:
 *   areaOfLaw  — e.g. "Corporate Finance", "Employment" (NOSLEGAL Areas of law)
 *   workType   — e.g. "Advisory", "Transactional", "Litigious" (Work types)
 *   sector     — e.g. "Financial Services", "Technology" (Sectors)
 *   assetType  — e.g. "Agreement", "Opinion", "Pleading" (Information assets)
 *
 * These complement (not replace) our canonical `practiceArea` and `documentType`
 * fields and enable interoperability with NOSLEGAL-compatible platforms.
 */
export interface NosLegalTags {
  areaOfLaw?: string;
  workType?: string;
  sector?: string;
  assetType?: string;
}

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
  /**
   * Optional NOSLEGAL v4 taxonomy tags for interoperability with
   * NOSLEGAL-compatible legal platforms.
   */
  noslegal?: NosLegalTags;
  metadata?: Record<string, unknown>;
  ingestedAt: Date;
}

export interface SearchResult {
  document: Document;
  score: number;
  excerpt: string;
}

// ─── Pre-bill review ─────────────────────────────────────────────────────────

export type PreBillStatus = "draft" | "reviewed" | "approved" | "invoiced";

export interface PreBillEntry {
  entryId: string;
  description: string;
  billingUnits: number;
  billingRate?: number;
  billingAmountUsd?: number;
  utbmsTaskCode?: string;
  utbmsActivityCode?: string;
  profileName?: string;
  agentName?: string;
  startedAt: string;
  endedAt?: string;
  ocgSuggestionCount: number;
}

export interface PreBill {
  id: string;
  matterNumber: string;
  clientNumber?: string;
  status: PreBillStatus;
  createdByProfileId: string;
  createdAt: string;
  reviewedAt?: string;
  approvedAt?: string;
  invoicedAt?: string;
  entries: PreBillEntry[];
  totalBillingUnits: number;
  totalAmountUsd: number;
  notes?: string;
}

// ─── Embeddings ──────────────────────────────────────────────────────────────

export interface EmbeddingResult {
  text: string;
  embedding: number[];
  model: string;
}

// ─── Outside Counsel Guidelines ───────────────────────────────────────────────

export type OcgRuleCategory =
  | "billing_increments"
  | "entry_specificity"
  | "prohibited_tasks"
  | "rate_limits"
  | "staffing"
  | "description_format"
  | "timing"
  | "other";

export type OcgMechCheckType =
  | "min_duration_hours"      // entry duration must be >= value
  | "max_duration_hours"      // entry duration must be <= value
  | "max_age_days"            // entry must be submitted within value days of work
  | "max_billing_rate_usd"    // billingRate must be <= value
  | "min_description_chars"   // description.length must be >= value
  | "no_block_billing"        // description must not combine 3+ distinct task types
  | "no_vague_entries"        // description must not be a single generic verb phrase
  | "require_matter_reference"; // matterNumber must be present on the entry

export interface OcgMechCheck {
  type: OcgMechCheckType;
  value?: number;  // numeric threshold; omitted for boolean checks
}

export interface OcgRule {
  id: string;
  category: OcgRuleCategory;
  text: string;
  severity: "hard" | "soft";
  /** Structured parameters for deterministic evaluation. Set at ingest time by Haiku.
   *  When present, checkMechanically() uses this directly — no runtime regex parsing. */
  mechCheck?: OcgMechCheck;
}

export interface OcgRuleStat {
  violations: number;   // times this rule was flagged across all entries
  accepted: number;     // times a suggestion for this rule was accepted
  dismissed: number;    // times a suggestion was dismissed
}

export interface OcgDocument {
  id: string;
  clientId: string;
  title: string;
  rules: OcgRule[];
  excerpt: string;    // first 500 chars of source text for display
  createdAt: Date;
  updatedAt: Date;
  /** Per-rule violation and correction-acceptance counts. Keyed by ruleId. */
  ruleStats?: Record<string, OcgRuleStat>;
}

export interface OcgSuggestion {
  ruleId: string;
  ruleText: string;
  category: OcgRuleCategory;
  severity: "hard" | "soft";
  issue: string;
  suggestedDescription: string;
  status: "pending" | "accepted" | "dismissed";
}

// ─── Client voice guide ───────────────────────────────────────────────────────

export interface ClientVoiceGuide {
  generatedAt: string;         // ISO timestamp
  sampleCount: number;
  preferredFormality: string;  // e.g. "formal", "business-casual"
  communicationStyle: string;  // e.g. "concise bullet points", "narrative"
  terminologyPreferences: string;
  reportingPreferences: string;
  signaturePatterns: string[];
  injectionSnippet: string;    // pre-built prompt fragment
}

// ── Budget prediction ───────────────────────────────────────────────────────
export interface BudgetPrediction {
  matterNumber: string;
  practiceArea: string;
  spentUsd: number;
  spentBillingUnits: number;
  estimatedTotalUsd: number;
  estimatedRemainingUsd: number;
  completionPct: number;
  confidence: "high" | "medium" | "low" | "insufficient_data";
  comparableMatterCount: number;
  medianFinalCost: number;
  p25FinalCost: number;
  p75FinalCost: number;
  basedOn: "practice_area+jurisdiction" | "practice_area" | "all_matters";
}

// ── Conflict graph ──────────────────────────────────────────────────────────
export interface ConflictReport {
  clientAId: string;
  clientAName: string;
  clientBId: string;
  clientBName: string;
  matterANumber: string;
  matterBNumber: string;
  conflictPath: string;
  detectedAt: string;
}

// ── Regulatory pulse ────────────────────────────────────────────────────────
export interface RegulationAlert {
  id: string;
  matterNumber?: string;
  practiceArea: string;
  jurisdiction: string;
  headline: string;
  url: string;
  summary: string;
  detectedAt: string;
  source: "tavily";
}

// ── Status reports ──────────────────────────────────────────────────────────
export interface StatusReport {
  taskId: string;
  matterNumber?: string;
  clientNumber?: string;
  generatedAt: string;
  format: "html" | "markdown";
  content: string;
  wordCount: number;
  costUsd: number;
}

// ── Docket monitoring ───────────────────────────────────────────────────────
export interface DocketAlert {
  id: string;
  matterNumber: string;
  docketNumber: string;
  court: string;
  caseName: string;
  newFilingCount: number;
  latestFilingDate: string;
  courtListenerUrl: string;
  detectedAt: string;
}

// ── Citation validity (KeyCite / Shepard's replacement) ─────────────────────

export type CitationSignal = "green" | "yellow" | "red" | "blue";
export type CitationStatus = "good_law" | "limited" | "overruled" | "superseded" | "unclear";

export type CitationTreatmentType =
  | "overruled"
  | "distinguished"
  | "questioned"
  | "limited"
  | "criticized"
  | "followed"
  | "explained";

export interface CitationTreatment {
  caseName: string;
  citation?: string;
  treatmentType: CitationTreatmentType;
  court?: string;
  year?: number;
  url?: string;
}

export interface CitationCheckResult {
  /** The query string as submitted. */
  query: string;
  resolvedCitation?: string;
  clusterId?: string;
  caseName?: string;
  court?: string;
  year?: number;
  status: CitationStatus;
  /** Westlaw-signal equivalent. green=still good law, yellow=caution, red=overruled/superseded, blue=background only. */
  signal: CitationSignal;
  signalLabel: string;
  confidence: number;                  // 0–1
  positiveTreatmentCount: number;
  negativeTreatmentCount: number;
  topNegativeTreatments: CitationTreatment[];
  reasoning: string;
  courtListenerUrl?: string;
  checkedAt: string;
  checkedBy: "big-michael";
}

// ── Matter health (Clio Insights replacement) ────────────────────────────────

export type HealthSignal = "green" | "amber" | "red";
export type HealthTrend = "improving" | "stable" | "deteriorating";

export type MatterRiskType =
  | "budget_overrun"
  | "deadline_approaching"
  | "gate_backlog"
  | "ocg_violations"
  | "stale_activity"
  | "task_failure";

export interface MatterRiskFactor {
  type: MatterRiskType;
  severity: "high" | "medium" | "low";
  message: string;
  suggestedAction?: string;
}

export interface MatterHealthScore {
  matterNumber: string;
  score: number;        // 0–100
  signal: HealthSignal;
  signalLabel: string;
  dimensions: {
    budgetHealth: number;      // 0–100
    deadlineHealth: number;    // 0–100
    activityFreshness: number; // 0–100
    gateBacklog: number;       // 0–100  (100 = no gates; drops per open gate)
    ocgCompliance: number;     // 0–100
  };
  riskFactors: MatterRiskFactor[];
  trend: HealthTrend;
  computedAt: string;
}

export interface PortfolioHealthSummary {
  totalMatters: number;
  green: number;
  amber: number;
  red: number;
  matters: MatterHealthScore[];
  computedAt: string;
}

// ── Playbook (Contract Express / Practical Law replacement) ──────────────────

/**
 * Four-tier authority cascade — firm → personal → matter → client:
 *
 *   firm     — generic market-standard defaults (widest scope, lowest authority)
 *   personal — individual lawyer's preferred positions (layers over firm)
 *   matter   — positions negotiated / agreed in this specific deal
 *   client   — client's known requirements (narrowest scope, always wins)
 *
 * Authority order: client > matter > personal > firm.
 *
 * You start from firm-wide defaults, layer your personal approach, add what you
 * know about this deal, then apply client requirements — the most definitive
 * thing you know going into any engagement. Personal notes always surface
 * alongside the authoritative answer as advisory context.
 */
export type PlaybookScope = "firm" | "client" | "matter" | "personal";

export interface PlaybookEntry {
  clauseType: string;
  practiceArea: string;
  /** The firm's typical opening position on this clause. */
  standardPosition: string;
  /** Acceptable fall-back when standard position is pushed back. */
  fallbackPosition?: string;
  /** Lines the firm will not cross. */
  redLines: string[];
  /** Key deal points / negotiation observations. */
  dealPoints: string[];
  /** Number of precedent documents this entry was derived from. */
  sourceDocumentCount: number;
  /** Representative precedent language snippets (≤3). */
  exampleLanguage?: string[];
  lastUpdated: string;
}

export interface Playbook {
  id: string;
  /** Which tier of the cascade this playbook belongs to. */
  scope: PlaybookScope;
  /** clientNumber, matterNumber, or profileId — depends on scope. Omitted for firm scope. */
  ownerId?: string;
  ownerName?: string;
  name: string;
  description?: string;
  practiceArea: string;
  jurisdiction?: string;
  clauseTypes: string[];
  entries: PlaybookEntry[];
  documentCount: number;
  createdAt: string;
  updatedAt: string;
  generatedByTaskId?: string;
}

// ── Invoice validation (reverse-OCG; in-house legal killer) ─────────────────

export interface InvoiceLineItem {
  lineId: string;
  date?: string;
  timekeeperName?: string;
  timekeeperClass?: string;
  taskCode?: string;
  activityCode?: string;
  description: string;
  hours?: number;
  rate?: number;
  amount?: number;
}

export type InvoiceViolationType =
  | "block_billing"
  | "vague_description"
  | "rate_exceeded"
  | "unauthorized_task"
  | "timing_violation"
  | "staffing_violation"
  | "excessive_hours"
  | "other";

export type InvoiceViolationAction = "reject" | "reduce" | "request_detail";

export interface InvoiceViolation {
  lineId: string;
  ruleId?: string;
  ruleText?: string;
  type: InvoiceViolationType;
  severity: "hard" | "soft";
  message: string;
  suggestedAction: InvoiceViolationAction;
  /** Amount to reduce from the line item's billed amount, in USD. */
  suggestedReduction?: number;
}

export interface InvoiceValidationResult {
  id: string;
  clientId?: string;
  submittedByFirm?: string;
  matterNumber?: string;
  totalOriginalAmount: number;
  totalSuggestedReduction: number;
  totalApprovedAmount: number;
  lineCount: number;
  violationCount: number;
  hardViolationCount: number;
  violations: InvoiceViolation[];
  /** AI-drafted dispute letter for the billing lawyer. */
  disputeLetter?: string;
  validatedAt: string;
}

// ── Headnote Generator ──────────────────────────────────────────────────────

export type HoldingType = "ratio" | "obiter" | "procedural" | "statutory";

export interface Headnote {
  number: number;
  proposition: string;
  sourceText: string;
  location?: string;
  holdingType: HoldingType;
  distinguishingFactors: string[];
  areaOfLaw?: string;
  confidence: number;
}

export interface HeadnoteReport {
  id: string;
  caseName: string;
  citation?: string;
  court?: string;
  dateFiled?: string;
  jurisdiction?: string;
  keyHolding: string;
  headnotes: Headnote[];
  relatedPrinciples: string[];
  practiceAreas: string[];
  noslegalArea?: string;
  totalHeadnotes: number;
  ratioCount: number;
  obiterCount: number;
  generatedAt: string;
}

// ── Client Intelligence Briefing ─────────────────────────────────────────────

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
  document: string;
  /** All intel items gathered from the swarm's spoke agents */
  chalkboard?: unknown[];
  /** Per-spoke status summary */
  spokeSummary?: Record<string, { items: number; durationMs: number; error?: string }>;
}

// ── Deadline calculator ─────────────────────────────────────────────────────
export type DeadlineDayType = "calendar" | "business";

export interface ComputedDeadline {
  ruleId: string;
  event: string;
  dueDate: string;
  warningDate?: string;
  days: number;
  dayType: DeadlineDayType;
  cite: string;
  note?: string;
}

export interface DeadlineResult {
  jurisdiction: string;
  jurisdictionName: string;
  triggerEvent: string;
  triggerDate: string;
  computedAt: string;
  deadlines: ComputedDeadline[];
}
