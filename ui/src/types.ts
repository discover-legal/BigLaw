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

export interface Client {
  id: string;
  name: string;
  clientNumber: string;
  matters: ClientMatter[];
  adversaries: string[];
  notes?: string;
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
  taskId?: string;
  agentId?: string;
  model?: string;
  durationMs?: number;
  data?: Record<string, unknown>;
}

export type CostContext =
  | "task" | "descriptor" | "synthesis" | "tabulate" | "round_goal"
  | "protocol_debate" | "protocol_verify" | "tone_analysis" | "classification";

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

// Mirror of PHASE_SEQUENCES in src/orchestrator.ts — drives the phase stepper.
export const PHASE_SEQUENCES: Record<WorkflowType, TaskPhase[]> = {
  counsel:     ["intake", "research", "drafting", "delivery"],
  roundtable:  ["intake", "research", "analysis", "drafting", "review", "delivery"],
  adversarial: ["intake", "research", "analysis", "review", "verification", "delivery"],
  review:      ["intake", "analysis", "review", "verification", "delivery"],
  tabulate:    ["intake", "analysis", "delivery"],
  full_bench:  ["intake", "research", "analysis", "drafting", "review", "verification", "delivery"],
};
