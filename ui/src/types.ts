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

export interface Task {
  id: string;
  description: string;
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
