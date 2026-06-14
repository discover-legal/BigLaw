// Mirrors src/types.ts as received over JSON (Date fields arrive as ISO strings).

export type WorkflowType =
  | "counsel" | "roundtable" | "adversarial" | "review" | "tabulate" | "full_bench";

export type TaskStatus =
  | "pending" | "running" | "awaiting_gate" | "complete" | "failed";

export type TaskPhase =
  | "intake" | "research" | "analysis" | "drafting" | "review" | "verification" | "delivery";

export interface Citation {
  source: string;
  quote: string;
  page?: number;
  mechanicallyVerified: boolean;
}

export interface Challenge {
  challengerId: string;
  challengerName: string;
  content: string;
  citations: Citation[];
  resolution?: string;
  resolvedAt?: string;
}

export interface VerificationCheck { name: string; passed: boolean; notes?: string; }

export interface VerificationResult {
  findingId: string;
  checks: VerificationCheck[];
  passed: boolean;
  completedAt: string;
}

export interface Finding {
  id: string;
  agentId: string;
  agentName: string;
  content: string;
  citations: Citation[];
  confidence: number;
  challenged: boolean;
  challenge?: Challenge;
  resolved: boolean;
  verificationResult?: VerificationResult;
  round: number;
  timestamp: string;
}

export interface GateRequest {
  id: string;
  taskId: string;
  findingId: string;
  finding: Finding;
  status: "pending" | "approved" | "rejected";
  reviewerNote?: string;
  /** Remy's client-advocate read on this finding (from the CNTXT advocacy brief). */
  clientVoiceNote?: string;
  createdAt: string;
  reviewedAt?: string;
}

export interface TaskTable {
  columns: string[];
  rows: Array<Record<string, string>>;
  sourceFindingIds: string[];
  generatedAt: string;
}

export interface RoundGoal {
  id: string;
  round: number;
  phase: TaskPhase;
  description: string;
  expectedOutputs: string[];
}

export interface RoundState {
  roundId: string;
  goal: RoundGoal;
  activeAgentIds: string[];
  edges: Array<{ from: string; to: string; similarity: number; offerText: string }>;
  messages: unknown[];
  findings: Finding[];
  status: string;
  startedAt: string;
  completedAt?: string;
}

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

export interface ToneProfile {
  generatedAt: string;
  sourceType: "linkedin_export" | "writing_samples";
  sampleCount: number;
  formality: "formal" | "semi-formal" | "conversational";
  sentenceStyle: "long-complex" | "mixed" | "short-punchy";
  vocabulary: "technical-heavy" | "balanced" | "plain-language";
  rhetoricalStyle: "assertive" | "collaborative" | "hedging" | "analytical";
  signaturePatterns: string[];
  injectionSnippet: string;
}

export interface LawyerProfile {
  id: string;
  name: string;
  email: string;
  role: LawyerRole;
  title?: string;
  color?: string;
  practiceAreas?: string[];
  bio?: string;
  mode?: UserMode;
  linkedinProfileUrl?: string;
  toneProfile?: ToneProfile;
}

export interface ClientMatter {
  matterNumber: string;
  description: string;
  practiceArea?: string;
  openedAt: string;
}

export type OcgRuleCategory =
  | "billing_increments" | "entry_specificity" | "prohibited_tasks"
  | "rate_limits" | "staffing" | "description_format" | "timing" | "other";

export interface OcgRule {
  id: string;
  category: OcgRuleCategory;
  text: string;
  severity: "hard" | "soft";
}

export interface OcgDocument {
  id: string;
  clientId: string;
  title: string;
  rules: OcgRule[];
  excerpt: string;
  createdAt: string;
  updatedAt: string;
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

export interface ClientVoiceGuide {
  generatedAt: string;
  sampleCount: number;
  preferredFormality: string;
  communicationStyle: string;
  terminologyPreferences: string;
  reportingPreferences: string;
  signaturePatterns: string[];
  injectionSnippet: string;
}

export interface Client {
  id: string;
  name: string;
  clientNumber: string;
  matters: ClientMatter[];
  adversaries: string[];
  notes?: string;
  ocgId?: string;
  voiceGuide?: ClientVoiceGuide;
  createdAt: string;
  updatedAt: string;
}

export interface ConflictCheckResult {
  hasConflict: boolean;
  conflictingClientId?: string;
  conflictingClientName?: string;
  matchedAdversary?: string;
}

export interface SuggestedLawyer {
  id: string;
  name: string;
  email: string;
}

export interface IngestResult {
  id: string;
  title?: string;
  practiceArea?: string | null;
  detectedClient?: { clientNumber: string; clientName: string } | null;
  suggestedLawyers: SuggestedLawyer[];
  extractionMethod?: string;
  extractionNotes?: string[];
  attachments?: Attachment[];
}

export interface Attachment {
  id: string;
  docId: string;
  filename: string;
  mediaType: string;
  kind: string;
  size: number;
  page?: number;
  createdAt?: string;
}

export type UserMode = "admin" | "full_flavour" | "lite";

export interface ModeCapabilities {
  manageUsers: boolean;
  seeAllMatters: boolean;
  assignMatters: boolean;
  clientRoster: boolean;
  timeTracking: boolean;
  matterAnalytics: boolean;
  fullConnectors: boolean;
  adminSettings: boolean;
}

/** Accent hex per mode — admin keeps gold rather than near-black for readability. */
export const MODE_ACCENT: Record<UserMode, string> = {
  admin:        "#E6B450",   // gold — full parchment-and-gold experience
  full_flavour: "#C8102E",   // scarlet
  lite:         "#C4940F",   // amber-gold
};

export const MODE_LABEL: Record<UserMode, string> = {
  admin:        "Admin",
  full_flavour: "Full Flavour",
  lite:         "Lite",
};

export interface Me {
  user: { profileId: string; name: string; email: string; role: LawyerRole } | null;
  authEnabled: boolean;
  mode?: UserMode;
  modeColor?: string;
  capabilities?: ModeCapabilities;
}

export interface Task {
  id: string;
  description: string;
  clientNumber?: string;
  matterNumber?: string;
  assignedLawyerIds?: string[];
  documentIds: string[];
  workflowType: WorkflowType;
  status: TaskStatus;
  currentPhase: TaskPhase;
  currentRound: number;
  maxRounds: number;
  activeAgentIds: string[];
  rounds: RoundState[];
  findings: Finding[];
  pendingGates: GateRequest[];
  output?: string;
  error?: string;
  createdAt: string;
  updatedAt: string;
  completedAt?: string;
  table?: TaskTable;
}

export interface Template {
  id: string;
  name: string;
  description: string;
}

export interface AppSettings {
  presentation: { mode: "lawyer" | "plain"; firmName: string };
  dytopo: { maxRounds: number; maxAgentsPerRound: number; similarityThreshold: number };
  debate: { verificationPasses: number; gateConfidenceThreshold: number; adversarialEnabled: boolean; citationRequired: boolean };
  docuseal: { enabled: boolean; url: string; apiKeySet: boolean };
  clientVoice: { gateNotes: boolean; matterNotifications: boolean };
}

export interface AgentSummary {
  id: string;
  name: string;
  tier: number;
  type: string;
  domain: string;
  description?: string;
}

export interface DocumentRef {
  id: string;
  title: string;
  content: string;
  source?: string;
  jurisdiction?: string;
  documentType?: string;
  practiceArea?: string;
  detectedClientNumber?: string;
  ingestedAt?: string;
}

export interface SearchResult {
  document: DocumentRef;
  score: number;
  excerpt: string;
}

export interface AuditEntry {
  id: string;
  ts: string;
  event: string;
  actorId?: string;
  prevHash?: string;
  taskId?: string;
  agentId?: string;
  model?: string;
  durationMs?: number;
  data?: Record<string, unknown>;
}

export interface TimeEntry {
  id: string;
  profileId: string;
  profileName: string;
  taskId: string;
  matterNumber?: string;
  clientNumber?: string;
  description: string;
  event: string;
  startedAt: string;
  endedAt?: string;
  durationMs: number;
  billingUnits: number;
  agentId?: string;
  agentName?: string;
  billingRate?: number;
  billingAmountUsd?: number;
  clioSyncedAt?: string;
  ocgSuggestions?: OcgSuggestion[];
  ocgCheckedAt?: string;
}

export type CostContext =
  | "task" | "descriptor" | "synthesis" | "tabulate" | "round_goal"
  | "protocol_debate" | "protocol_verify" | "tone_analysis" | "classification"
  | "ocg_extraction" | "ocg_check" | "voice_analysis";

export interface CostEntry {
  id: string;
  ts: string;
  model: string;
  provider: "anthropic" | "ollama" | "local";
  inputTokens: number;
  outputTokens: number;
  cacheWriteTokens?: number;
  cacheReadTokens?: number;
  costUsd: number | null;
  estimatedWh: number | null;
  estimatedWatts: number | null;
  co2Grams: number | null;
  electricityCostUsd: number | null;
  durationMs: number;
  context: CostContext;
  taskId?: string;
  profileId?: string;
  agentId?: string;
}

export interface CostSummary {
  totalUsd: number;
  totalInputTokens: number;
  totalOutputTokens: number;
  totalCacheWriteTokens: number;
  totalCacheReadTokens: number;
  totalWh: number;
  totalCo2Grams: number;
  totalElectricityCostUsd: number;
  byModel: Record<string, {
    usd: number;
    inputTokens: number;
    outputTokens: number;
    cacheWriteTokens: number;
    cacheReadTokens: number;
    wh: number;
    co2Grams: number;
    electricityCostUsd: number;
    calls: number;
  }>;
  byContext: Record<string, { usd: number; inputTokens: number; outputTokens: number; calls: number }>;
  entryCount: number;
}

export interface TaskCostResult {
  taskId: string;
  summary: CostSummary;
  entries: CostEntry[];
}

export interface Health {
  status: string;
  version: string;
  uptime: number;
  tasks: { total: number; running: number; awaiting_gate: number; complete: number };
}

export const WORKFLOWS: { id: WorkflowType; name: string; desc: string }[] = [
  { id: "counsel",     name: "Counsel",     desc: "Single specialist, quick" },
  { id: "roundtable",  name: "Roundtable",  desc: "Multi-agent discussion" },
  { id: "adversarial", name: "Adversarial", desc: "Red-team vs blue-team" },
  { id: "review",      name: "Review",      desc: "Document review" },
  { id: "tabulate",    name: "Tabulate",    desc: "Bulk → spreadsheet" },
  { id: "full_bench",  name: "Full Bench",  desc: "Comprehensive all-tier" },
];

// ─── Billing: pre-bills, invoice validation, agent billing ──────────────────

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

export interface AgentBillingSummary {
  agentId: string;
  agentName: string;
  entries: number;
  billingUnits: number;
  billingAmountUsd: number;
}

export type InvoiceViolationType =
  | "block_billing" | "vague_description" | "rate_exceeded" | "unauthorized_task"
  | "timing_violation" | "staffing_violation" | "excessive_hours" | "other";

export interface InvoiceViolation {
  lineId: string;
  ruleId?: string;
  ruleText?: string;
  type: InvoiceViolationType;
  severity: "hard" | "soft";
  message: string;
  suggestedAction: "reject" | "reduce" | "request_detail";
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
  disputeLetter?: string;
  validatedAt: string;
}

// ─── Budgets, deadlines & matter health ──────────────────────────────────────

export interface BudgetBurn {
  matterNumber: string;
  budgetUsd: number;
  burnUsd: number;
  burnPct: number;
  remaining: number;
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
  basedOn: string;
}

export interface DeadlineJurisdiction {
  jurisdiction: string;
  name: string;
  id: string;
  ruleCount: number;
}

export interface ComputedDeadline {
  ruleId: string;
  event: string;
  dueDate: string;
  warningDate?: string;
  days: number;
  dayType: "calendar" | "business";
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

export type HealthSignal = "green" | "amber" | "red";

export interface MatterRiskFactor {
  type: string;
  severity: "high" | "medium" | "low";
  message: string;
  suggestedAction?: string;
}

export interface MatterHealthScore {
  matterNumber: string;
  score: number;
  signal: HealthSignal;
  signalLabel: string;
  dimensions: {
    budgetHealth: number;
    deadlineHealth: number;
    activityFreshness: number;
    gateBacklog: number;
    ocgCompliance: number;
  };
  riskFactors: MatterRiskFactor[];
  trend: "improving" | "stable" | "deteriorating";
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

// ─── Watchtower: dockets & regulatory pulse ──────────────────────────────────

export interface WatchedDocket {
  matterNumber: string;
  docketNumber: string;
  court: string;
  caseName?: string;
  addedAt: string;
  lastCheckedAt?: string;
  lastFilingDate?: string;
  totalFilingsSeen: number;
}

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

export interface RegulationAlert {
  id: string;
  matterNumber?: string;
  practiceArea: string;
  jurisdiction: string;
  headline: string;
  url: string;
  summary: string;
  detectedAt: string;
  source: string;
}

// ─── Drafting: playbooks, redline, headnotes, precedents, citations ──────────

export type PlaybookScope = "firm" | "client" | "matter" | "personal";

export interface PlaybookEntry {
  clauseType: string;
  practiceArea: string;
  standardPosition: string;
  fallbackPosition?: string;
  redLines: string[];
  dealPoints: string[];
  sourceDocumentCount: number;
  exampleLanguage?: string[];
  lastUpdated: string;
}

export interface Playbook {
  id: string;
  scope: PlaybookScope;
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

export interface ResolvedClause {
  clauseType: string;
  practiceArea: string;
  effectiveEntry: PlaybookEntry;
  resolvedFrom: PlaybookScope;
  availableTiers: PlaybookScope[];
  personalNote?: string;
}

export interface PlaybookQueryResult {
  clauseType: string;
  practiceArea?: string;
  resolved: ResolvedClause[] | null;
  cascadeSummary?: string;
  message?: string;
  queriedAt?: string;
}

export type RedlineAction = "accept" | "redline" | "escalate" | "delete" | "no_position";

export interface RedlineIssue {
  clauseType: string;
  counterpartyText: string;
  firmPosition: string;
  positionSource: "client" | "matter" | "personal" | "firm" | "none";
  action: RedlineAction;
  proposedText?: string;
  rationale: string;
  isRedLine: boolean;
  severity: "critical" | "high" | "medium" | "low";
}

/** A playbook position absent from the counterparty draft. */
export interface MissingClause {
  clauseType: string;
  firmPosition: string;
  positionSource: string;
  severity: "critical" | "high" | "medium" | "low";
  isRedLine: boolean;
  suggestedText?: string;
  rationale: string;
}

export interface RedlineReport {
  id: string;
  documentId?: string;
  documentTitle?: string;
  practiceArea?: string;
  jurisdiction?: string;
  totalClauses: number;
  acceptCount: number;
  redlineCount: number;
  escalateCount: number;
  deleteCount: number;
  criticalCount: number;
  missingCount?: number;
  issues: RedlineIssue[];
  missingClauses?: MissingClause[];
  executiveSummary: string;
  generatedAt: string;
}

export interface Headnote {
  number: number;
  proposition: string;
  sourceText: string;
  location?: string;
  holdingType: "ratio" | "obiter" | "procedural" | "statutory";
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

export interface PrecedentClause {
  heading: string;
  draftText: string;
  source: "client" | "matter" | "personal" | "firm" | "knowledge_store" | "generated";
  hasRedLine: boolean;
  notes?: string;
  fallback?: string;
}

export interface PrecedentDocument {
  id: string;
  documentType: string;
  title: string;
  practiceArea?: string;
  jurisdiction?: string;
  actingFor?: string;
  sourcePrecedentCount: number;
  playbookPositionCount: number;
  clauses: PrecedentClause[];
  document: string;
  draftingNotes: string[];
  generatedAt: string;
}

export const PRECEDENT_TYPES: { id: string; name: string }[] = [
  { id: "nda",               name: "NDA / Confidentiality" },
  { id: "spa",               name: "Share purchase agreement" },
  { id: "asset_purchase",    name: "Asset purchase agreement" },
  { id: "facility",          name: "Loan / facility agreement" },
  { id: "employment",        name: "Employment contract" },
  { id: "service_agreement", name: "Services agreement / MSA" },
  { id: "supply_agreement",  name: "Supply / distribution agreement" },
  { id: "jv_agreement",      name: "Joint venture agreement" },
  { id: "ip_assignment",     name: "IP assignment" },
  { id: "licence",           name: "IP / technology licence" },
  { id: "settlement",        name: "Settlement agreement" },
  { id: "term_sheet",        name: "Term sheet / heads of terms" },
  { id: "other",             name: "Other (describe)" },
];

export type CitationSignal = "green" | "yellow" | "red" | "blue";

export interface CitationTreatment {
  caseName: string;
  citation?: string;
  treatmentType: string;
  court?: string;
  year?: number;
  url?: string;
}

export interface CitationCheckResult {
  query: string;
  resolvedCitation?: string;
  clusterId?: string;
  caseName?: string;
  court?: string;
  year?: number;
  status: "good_law" | "limited" | "overruled" | "superseded" | "unclear";
  signal: CitationSignal;
  signalLabel: string;
  confidence: number;
  positiveTreatmentCount: number;
  negativeTreatmentCount: number;
  topNegativeTreatments: CitationTreatment[];
  reasoning: string;
  courtListenerUrl?: string;
  checkedAt: string;
}

// ─── Analytics ────────────────────────────────────────────────────────────────

export interface NosLegalBreakdown {
  total: number;
  byAreaOfLaw: Record<string, number>;
  byWorkType: Record<string, number>;
  bySector: Record<string, number>;
  byAssetType: Record<string, number>;
}

// ─── Jobs queue ───────────────────────────────────────────────────────────────

export type JobStatus = "pending" | "running" | "done" | "failed" | "dead_letter";

export interface Job {
  id: string;
  type: string;
  payload: Record<string, unknown>;
  status: JobStatus;
  createdAt: string;
  startedAt?: string;
  completedAt?: string;
  retries: number;
  maxRetries: number;
  error?: string;
}

export interface QueueStats {
  pending: number;
  running: number;
  done: number;
  failed: number;
  dead_letter: number;
}

// Mirror of PHASE_SEQUENCES in src/orchestrator.ts — drives the phase stepper.
export const PHASE_SEQUENCES: Record<WorkflowType, TaskPhase[]> = {
  counsel:     ["intake", "research", "drafting", "delivery"],
  roundtable:  ["intake", "research", "analysis", "drafting", "review", "delivery"],
  adversarial: ["intake", "research", "analysis", "review", "verification", "delivery"],
  review:      ["intake", "analysis", "review", "verification", "delivery"],
  tabulate:    ["intake", "analysis", "delivery"],
  full_bench:  ["intake", "research", "analysis", "drafting", "review", "verification", "delivery"],
};
