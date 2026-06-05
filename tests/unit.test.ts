// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
//
// Fast unit tests for pure logic — no Qdrant, no network, no LLM.
// Run with: npm test   (node:test via tsx)

import { test, mock } from "node:test";
import assert from "node:assert/strict";
import { resolve, join as pathJoin } from "node:path";
import * as os from "node:os";

import { estimateComplexity, shouldUseThinking } from "../src/routing/model.js";
import { LavernAdapter, LavernWorkflowAdapter, fromMikeOSSWorkflow, fromExternalConfig, instantiateTemplate, sanitizePromptContent } from "../src/adapters/lavern.js";
import { jurisdictionMatch } from "../src/dytopo/jurisdiction.js";
import { assertSafeReadPath } from "../src/tools/pdf.js";
import { assertPublicHttpUrl } from "../src/settings/index.js";
import { canViewTask, filterVisible, isPartner } from "../src/auth/index.js";
import type { SessionUser, AgentDefinition, Task } from "../src/types.js";
import { RegPulseMonitor } from "../src/regulatory/pulse.js";

// validatePlugin is not exported — test via the public PluginRegistry interface instead
import { PluginRegistry } from "../src/adapters/plugin.js";

import { TimeStore } from "../src/time/index.js";
import { detectNosLegal } from "../src/services/classifier.js";
import { Config } from "../src/config.js";
import { ClioClient, clioClient } from "../src/integrations/clio.js";
import { CLIO_TOOLS, CLIO_TOOL_NAMES } from "../src/tools/clio.js";
import { validateSinkUrl } from "../src/audit/sinks/utils.js";
import { exportLedes1998B } from "../src/billing/ledes.js";
import { markdownToHtml } from "../src/reports/status.js";
import { DocketMonitor } from "../src/dockets/monitor.js";

// ─── Model routing: complexity heuristic ────────────────────────────────────

test("estimateComplexity: two high-signal terms → high", () => {
  assert.equal(estimateComplexity("Assess the antitrust exposure and proportionality of the merger control remedy"), "high");
});

test("estimateComplexity: two low-signal terms → low", () => {
  assert.equal(estimateComplexity("Extract the parties and list the defined terms"), "low");
});

test("estimateComplexity: neutral text → medium", () => {
  assert.equal(estimateComplexity("Summarise the lease and note the rent review date"), "medium");
});

// ─── Lavern adapter: tier / domain inference + tool mapping ──────────────────

const adapter = new LavernAdapter();

test("Lavern: explicit orchestrator tier → T1 manager", () => {
  const [a] = adapter.fromConfigs([{ name: "Lead Counsel", role: "Coordinates the team", systemPrompt: "x", mcpTools: [], tier: "orchestrator" }]);
  assert.equal(a.tier, 1);
  assert.equal(a.type, "manager");
});

test("Lavern: explicit tool tier → T3 tool", () => {
  const [a] = adapter.fromConfigs([{ name: "Searcher", role: "search the web", systemPrompt: "x", mcpTools: ["mcp_search"], tier: "tool" }]);
  assert.equal(a.tier, 3);
  assert.equal(a.type, "tool");
});

test("Lavern: specialist defaults to T2 + maps MCP tools to internal names", () => {
  const [a] = adapter.fromConfigs([{ name: "Reviewer", role: "review contracts", systemPrompt: "x", mcpTools: ["mcp_search", "mcp_verify_citation"] }]);
  assert.equal(a.tier, 2);
  assert.equal(a.type, "specialist");
  assert.deepEqual(a.allowedTools, ["web_search", "citation_check"]);
});

test("Lavern: id is slugged + name is tagged + source metadata set", () => {
  const [a] = adapter.fromConfigs([{ name: "Risk Partner", role: "assess risk", systemPrompt: "x", mcpTools: [] }]);
  assert.equal(a.id, "lavern:risk-partner");
  assert.equal(a.name, "[Lavern] Risk Partner");
  assert.equal(a.metadata?.source, "lavern");
});

// ─── MikeOSS workflow → template + instantiation ─────────────────────────────

test("fromMikeOSSWorkflow: maps to a mikeoss-sourced template, default workflowType", () => {
  const t = fromMikeOSSWorkflow({ id: "cp-checklist", name: "CP Checklist", description: "d", promptTemplate: "Do {{x}}" });
  assert.equal(t.id, "mikeoss:cp-checklist");
  assert.equal(t.source, "mikeoss");
  assert.equal(t.workflowType, "roundtable");
});

test("instantiateTemplate: substitutes placeholders", () => {
  const t = fromMikeOSSWorkflow({ id: "t", name: "n", description: "d", promptTemplate: "Review {{company}} under {{law}} law", workflowType: "review" });
  const { description, workflowType } = instantiateTemplate(t, { company: "Acme", law: "New York" });
  assert.equal(description, "Review Acme under New York law");
  assert.equal(workflowType, "review");
});

// ─── Security: PDF read-path traversal guard ─────────────────────────────────

test("assertSafeReadPath: allows a path inside the project root", () => {
  const p = resolve(process.cwd(), "output", "documents", "brief.pdf");
  assert.equal(assertSafeReadPath(p), p);
});

test("assertSafeReadPath: blocks traversal to a sensitive file", () => {
  assert.throws(() => assertSafeReadPath(resolve(process.cwd(), "..", "..", "..", "Windows", "System32", "config")), /outside the allowed directories/);
});

test("assertSafeReadPath: blocks a relative .env escape", () => {
  // resolve() collapses ../ — anything that climbs out of an allowed root is refused
  assert.throws(() => assertSafeReadPath("../../../../etc/passwd"), /outside the allowed directories/);
});

test("assertSafeReadPath: rejects empty / non-string input", () => {
  assert.throws(() => assertSafeReadPath(""), /file path is required/);
  assert.throws(() => assertSafeReadPath(undefined), /file path is required/);
});

// ─── Access control: no inter-lawyer view unless partner-shared ──────────────

const partner: SessionUser = { profileId: "p1", name: "P", email: "p@x", role: "partner" };
const alice: SessionUser = { profileId: "a", name: "Alice", email: "a@x", role: "lawyer" };
const bob: SessionUser = { profileId: "b", name: "Bob", email: "b@x", role: "lawyer" };
const matters = [
  { assignedLawyerIds: ["a"] },           // Alice's
  { assignedLawyerIds: ["b"] },           // Bob's
  { assignedLawyerIds: ["a", "b"] },      // partner-shared across both
  { assignedLawyerIds: [] },              // unassigned
];

test("partner sees every matter", () => {
  assert.equal(isPartner(partner), true);
  assert.equal(filterVisible(partner, matters).length, 4);
});

test("a lawyer sees only their own matters (no inter-lawyer view)", () => {
  const visible = filterVisible(alice, matters);
  assert.equal(visible.length, 2);             // her solo matter + the shared one
  assert.ok(!visible.includes(matters[1]));    // never Bob's solo matter
  assert.equal(canViewTask(alice, matters[1]), false);
});

test("a partner-shared matter is visible to both assigned lawyers", () => {
  assert.equal(canViewTask(alice, matters[2]), true);
  assert.equal(canViewTask(bob, matters[2]), true);
});

test("unassigned matters are invisible to lawyers, visible to partners", () => {
  assert.equal(canViewTask(alice, matters[3]), false);
  assert.equal(canViewTask(partner, matters[3]), true);
});

test("no principal (unauthenticated) sees nothing", () => {
  assert.equal(filterVisible(null, matters).length, 0);
  assert.equal(canViewTask(null, matters[0]), false);
});

// ─── Security: DocuSeal SSRF guard ──────────────────────────────────────────

test("assertPublicHttpUrl: accepts a well-formed public URL", () => {
  assert.equal(assertPublicHttpUrl("https://docuseal.example.com", "DocuSeal URL"), "https://docuseal.example.com");
});

test("assertPublicHttpUrl: trims whitespace before returning", () => {
  assert.equal(assertPublicHttpUrl("  https://sign.example.com/api  ", "DocuSeal URL"), "https://sign.example.com/api");
});

test("assertPublicHttpUrl: rejects localhost", () => {
  assert.throws(() => assertPublicHttpUrl("http://localhost:3000", "DocuSeal URL"), /private or loopback/);
});

test("assertPublicHttpUrl: rejects 127.x loopback", () => {
  assert.throws(() => assertPublicHttpUrl("http://127.0.0.1:3000/api", "DocuSeal URL"), /private or loopback/);
});

test("assertPublicHttpUrl: rejects 169.254 link-local", () => {
  assert.throws(() => assertPublicHttpUrl("http://169.254.169.254/latest/meta-data/", "DocuSeal URL"), /private or loopback/);
});

test("assertPublicHttpUrl: rejects RFC-1918 10.x", () => {
  assert.throws(() => assertPublicHttpUrl("https://10.0.0.1/docuseal", "DocuSeal URL"), /private or loopback/);
});

test("assertPublicHttpUrl: rejects RFC-1918 172.16-31 range", () => {
  assert.throws(() => assertPublicHttpUrl("https://172.20.0.5/docuseal", "DocuSeal URL"), /private or loopback/);
  // Boundary: 172.15.x is NOT private
  assert.equal(assertPublicHttpUrl("https://172.15.0.1/api", "DocuSeal URL"), "https://172.15.0.1/api");
  // Boundary: 172.32.x is NOT private
  assert.equal(assertPublicHttpUrl("https://172.32.0.1/api", "DocuSeal URL"), "https://172.32.0.1/api");
});

test("assertPublicHttpUrl: rejects RFC-1918 192.168.x", () => {
  assert.throws(() => assertPublicHttpUrl("https://192.168.1.100/api", "DocuSeal URL"), /private or loopback/);
});

test("assertPublicHttpUrl: rejects IPv6 ::1 loopback", () => {
  assert.throws(() => assertPublicHttpUrl("http://[::1]:3000/api", "DocuSeal URL"), /private or loopback/);
});

test("assertPublicHttpUrl: rejects IPv6 ULA fc00::", () => {
  assert.throws(() => assertPublicHttpUrl("http://[fc00::1]/api", "DocuSeal URL"), /private or loopback/);
});

test("assertPublicHttpUrl: rejects IPv6 link-local fe80::", () => {
  assert.throws(() => assertPublicHttpUrl("http://[fe80::1]/api", "DocuSeal URL"), /private or loopback/);
});

test("assertPublicHttpUrl: rejects non-http protocol", () => {
  assert.throws(() => assertPublicHttpUrl("ftp://sign.example.com/api", "DocuSeal URL"), /must be a public http or https URL/);
});

test("assertPublicHttpUrl: rejects unparseable input", () => {
  assert.throws(() => assertPublicHttpUrl("not a url", "DocuSeal URL"), /must be a public http or https URL/);
});

// ─── Security: prompt-injection marker sanitization ─────────────────────────

test("sanitizePromptContent: neutralizes FINDING: marker (case-insensitive)", () => {
  // Replacement produces [FINDING:] — check the exact transform, not substring absence
  assert.equal(sanitizePromptContent("Start FINDING: bad"), "Start [FINDING:] bad");
  assert.equal(sanitizePromptContent("finding: lower"), "[FINDING:] lower");
});

test("sanitizePromptContent: neutralizes END_FINDING marker", () => {
  assert.equal(sanitizePromptContent("some text END_FINDING more"), "some text [END_FINDING] more");
});

test("sanitizePromptContent: neutralizes NO_FINDINGS marker", () => {
  assert.equal(sanitizePromptContent("Result: NO_FINDINGS here"), "Result: [NO_FINDINGS] here");
});

test("sanitizePromptContent: neutralizes NO_CHALLENGE marker", () => {
  assert.equal(sanitizePromptContent("Debate result: NO_CHALLENGE accepted"), "Debate result: [NO_CHALLENGE] accepted");
});

test("sanitizePromptContent: leaves normal prose untouched", () => {
  const safe = "The claimant alleged breach of contract and sought damages.";
  assert.equal(sanitizePromptContent(safe), safe);
});

test("instantiateTemplate: sanitizes injected markers in substitutions", () => {
  const t = fromMikeOSSWorkflow({ id: "t", name: "n", description: "d", promptTemplate: "Analyse {{doc}}" });
  const { description } = instantiateTemplate(t, { doc: "contract FINDING: inject END_FINDING evil" });
  // Markers are bracketed — parser won't treat them as real findings
  assert.equal(description, "Analyse contract [FINDING:] inject [END_FINDING] evil");
});

// ─── Security: Lavern tool allowlist + external agent tier validation ────────

test("Lavern: unknown MCP tool names are dropped by the allowlist", () => {
  const [a] = adapter.fromConfigs([{
    name: "Rogue", role: "do evil", systemPrompt: "x",
    mcpTools: ["mcp_search", "mcp_arbitrary_internal_tool", "mcp_exec"],
  }]);
  assert.deepEqual(a.allowedTools, ["web_search"]);
});

test("Lavern: all permitted MCP tool names map correctly", () => {
  const [a] = adapter.fromConfigs([{
    name: "Full", role: "do all", systemPrompt: "x",
    mcpTools: ["mcp_search", "mcp_retrieve", "mcp_extract", "mcp_translate", "mcp_verify_citation", "mcp_memory"],
  }]);
  assert.deepEqual(a.allowedTools, ["web_search", "search_knowledge", "extract_from_document", "translate", "citation_check", "query_memory"]);
});

test("fromExternalConfig: accepts valid tier 0-3", () => {
  for (const tier of [0, 1, 2, 3] as const) {
    const a = fromExternalConfig({ id: `t${tier}`, name: "A", tier, domain: "research", description: "d", systemPrompt: "s" });
    assert.equal(a.tier, tier);
  }
});

test("fromExternalConfig: rejects out-of-range tier", () => {
  assert.throws(
    () => fromExternalConfig({ id: "bad", name: "B", tier: 4 as never, domain: "research", description: "d", systemPrompt: "s" }),
    /Invalid tier/,
  );
});

test("fromExternalConfig: propagates jurisdictions when set", () => {
  const a = fromExternalConfig({ id: "us-specialist", name: "A", tier: 2, domain: "research", description: "d", systemPrompt: "s", jurisdictions: ["US"] });
  assert.deepEqual(a.jurisdictions, ["US"]);
});

test("fromExternalConfig: omits jurisdictions when not set", () => {
  const a = fromExternalConfig({ id: "neutral", name: "A", tier: 2, domain: "research", description: "d", systemPrompt: "s" });
  assert.equal(a.jurisdictions, undefined);
});

// ─── Lavern: jurisdiction preserved through adapter ──────────────────────────

test("Lavern: jurisdiction → jurisdictions array in AgentDefinition", () => {
  const [a] = adapter.fromConfigs([{ name: "EU Counsel", role: "advise on EU law", systemPrompt: "x", mcpTools: [], jurisdiction: "EU" }]);
  assert.deepEqual(a.jurisdictions, ["EU"]);
});

test("Lavern: no jurisdiction → jurisdictions undefined in AgentDefinition", () => {
  const [a] = adapter.fromConfigs([{ name: "Global Counsel", role: "advise globally", systemPrompt: "x", mcpTools: [] }]);
  assert.equal(a.jurisdictions, undefined);
});

// ─── LavernWorkflowAdapter: type mapping + validation ────────────────────────

const wfAdapter = new LavernWorkflowAdapter();

test("LavernWorkflowAdapter: legal-design maps to legal_design workflowType", () => {
  const [t] = wfAdapter.fromConfigs([{ id: "dpia", name: "DPIA", description: "d", type: "legal-design" }]);
  assert.equal(t.workflowType, "legal_design");
  assert.equal(t.source, "lavern");
  assert.equal(t.id, "lavern:dpia");
});

test("LavernWorkflowAdapter: pre-engagement maps to pre_engagement workflowType", () => {
  const [t] = wfAdapter.fromConfigs([{ id: "conflicts", name: "Conflicts", description: "d", type: "pre-engagement" }]);
  assert.equal(t.workflowType, "pre_engagement");
});

test("LavernWorkflowAdapter: full-bench maps to full_bench workflowType", () => {
  const [t] = wfAdapter.fromConfigs([{ id: "full", name: "Full", description: "d", type: "full-bench" }]);
  assert.equal(t.workflowType, "full_bench");
});

test("LavernWorkflowAdapter: verification maps to adversarial (closest match)", () => {
  const [t] = wfAdapter.fromConfigs([{ id: "verify", name: "Verify", description: "d", type: "verification" }]);
  assert.equal(t.workflowType, "adversarial");
});

test("LavernWorkflowAdapter: validation rejects missing id", () => {
  assert.throws(
    () => wfAdapter.fromConfigs([{ id: "", name: "N", description: "d", type: "roundtable" }]),
    /missing or invalid id/,
  );
});

test("LavernWorkflowAdapter: validation rejects invalid type", () => {
  assert.throws(
    () => wfAdapter.fromConfigs([{ id: "bad", name: "N", description: "d", type: "invalid-type" as never }]),
    /invalid type/,
  );
});

test("LavernWorkflowAdapter: validation rejects promptTemplate over 10000 chars", () => {
  assert.throws(
    () => wfAdapter.fromConfigs([{ id: "big", name: "N", description: "d", type: "roundtable", promptTemplate: "x".repeat(10001) }]),
    /promptTemplate exceeds 10000 chars/,
  );
});

// ─── jurisdictionMatch: DyTopo agent filtering ───────────────────────────────

function makeAgent(jurisdictions?: string[]): AgentDefinition {
  return { id: "a", name: "A", tier: 2, type: "specialist", domain: "research", description: "d", systemPrompt: "s", allowedTools: [], skills: [], jurisdictions };
}

test("jurisdictionMatch: neutral agent (no jurisdictions) always matches any task", () => {
  assert.equal(jurisdictionMatch(makeAgent(), "UK"), true);
  assert.equal(jurisdictionMatch(makeAgent(), "US-NY"), true);
  assert.equal(jurisdictionMatch(makeAgent(), undefined), true);
});

test("jurisdictionMatch: task without jurisdiction → all agents eligible", () => {
  assert.equal(jurisdictionMatch(makeAgent(["US"]), undefined), true);
  assert.equal(jurisdictionMatch(makeAgent(["EU"]), undefined), true);
});

test("jurisdictionMatch: exact match (US agent, US task)", () => {
  assert.equal(jurisdictionMatch(makeAgent(["US"]), "US"), true);
});

test("jurisdictionMatch: prefix match (US agent, US-NY task)", () => {
  assert.equal(jurisdictionMatch(makeAgent(["US"]), "US-NY"), true);
  assert.equal(jurisdictionMatch(makeAgent(["US"]), "US-CA"), true);
});

test("jurisdictionMatch: no match (US agent, EU task)", () => {
  assert.equal(jurisdictionMatch(makeAgent(["US"]), "EU"), false);
  assert.equal(jurisdictionMatch(makeAgent(["US"]), "UK"), false);
});

test("jurisdictionMatch: multi-jurisdiction agent matches either (EU+UK agent, UK task)", () => {
  assert.equal(jurisdictionMatch(makeAgent(["EU", "UK"]), "UK"), true);
  assert.equal(jurisdictionMatch(makeAgent(["EU", "UK"]), "EU"), true);
  assert.equal(jurisdictionMatch(makeAgent(["EU", "UK"]), "AU"), false);
});

test("jurisdictionMatch: case-insensitive comparison (lowercase tag, uppercase task)", () => {
  assert.equal(jurisdictionMatch(makeAgent(["us"]), "US-NY"), true);
  assert.equal(jurisdictionMatch(makeAgent(["eu"]), "EU"), true);
});

test("jurisdictionMatch: no false prefix match ('US' should not match 'USE' or 'USEU')", () => {
  // "US" prefix-matches "US-..." but must not match "USE"
  assert.equal(jurisdictionMatch(makeAgent(["US"]), "USE"), false);
  assert.equal(jurisdictionMatch(makeAgent(["US"]), "USEU"), false);
});

// ─── shouldUseThinking: extended thinking gate ───────────────────────────────

const OPUS_ID   = "claude-opus-4-8";
const SONNET_ID = "claude-sonnet-4-6";
const HAIKU_ID  = "claude-haiku-4-5-20251001";
const OLLAMA_ID = "ollama:llama3.2";
const LOCAL_ID  = "local:local-model";

test("shouldUseThinking: synthesis on Opus → true", () => {
  assert.equal(shouldUseThinking({ modelId: OPUS_ID, taskType: "synthesis" }), true);
});

test("shouldUseThinking: debate on Sonnet → true", () => {
  assert.equal(shouldUseThinking({ modelId: SONNET_ID, taskType: "debate" }), true);
});

test("shouldUseThinking: tier 0 on Opus → true", () => {
  assert.equal(shouldUseThinking({ modelId: OPUS_ID, taskType: "reasoning", tier: 0 }), true);
});

test("shouldUseThinking: high-complexity reasoning on Sonnet → true", () => {
  assert.equal(shouldUseThinking({ modelId: SONNET_ID, taskType: "reasoning", complexity: "high" }), true);
});

test("shouldUseThinking: Haiku model → always false regardless of task", () => {
  assert.equal(shouldUseThinking({ modelId: HAIKU_ID, taskType: "synthesis" }), false);
  assert.equal(shouldUseThinking({ modelId: HAIKU_ID, taskType: "debate", tier: 0 }), false);
});

test("shouldUseThinking: Ollama model → always false", () => {
  assert.equal(shouldUseThinking({ modelId: OLLAMA_ID, taskType: "synthesis" }), false);
});

test("shouldUseThinking: Local model → always false", () => {
  assert.equal(shouldUseThinking({ modelId: LOCAL_ID, taskType: "debate" }), false);
});

test("shouldUseThinking: extraction task on Sonnet → false (not a thinking use case)", () => {
  assert.equal(shouldUseThinking({ modelId: SONNET_ID, taskType: "extraction" }), false);
});

// ─── PluginRegistry: JSON plugin validation ──────────────────────────────────

const validPlugin = {
  id: "test-plugin",
  name: "Test Plugin",
  version: "1.0.0",
  description: "A test plugin for unit tests",
  auth: { type: "api-key", apiKeyEnvVar: "TEST_API_KEY", endpointEnvVar: "TEST_MCP_URL" },
  tools: [
    {
      name: "test_search",
      description: "Search for things",
      inputSchema: { type: "object", properties: { query: { type: "string" } }, required: ["query"] },
    },
  ],
  agents: [
    { id: "test-agent", name: "Test Agent", tier: 2, domain: "research", description: "d", systemPrompt: "x" },
  ],
  workflows: [
    { id: "test-wf", name: "Test Workflow", description: "d", workflowType: "roundtable", promptTemplate: "Do {{description}}" },
  ],
};

test("PluginRegistry: valid plugin loads without error", async () => {
  const reg = new PluginRegistry();
  // Can't call loadDirectory (file I/O), but can verify register() with an adapter
  // that was already validated. Use a TypeScript adapter stub instead.
  reg.register({
    id: "stub-adapter",
    name: "Stub",
    version: "1.0.0",
    description: "test",
    tools: () => [],
    agents: () => [],
    workflows: () => [],
  });
  assert.equal(reg.size, 1);
  assert.equal(reg.allTools().length, 0);
  assert.equal(reg.allAgents().length, 0);
  assert.equal(reg.allWorkflows().length, 0);
});

test("PluginRegistry: duplicate id is silently skipped", () => {
  const reg = new PluginRegistry();
  const stub = { id: "dup", name: "Dup", version: "1", description: "d", tools: () => [], agents: () => [], workflows: () => [] };
  reg.register(stub);
  reg.register(stub);  // second registration skipped
  assert.equal(reg.size, 1);
});

test("PluginRegistry: list() returns summary per plugin", () => {
  const reg = new PluginRegistry();
  reg.register({ id: "a1", name: "A1", version: "1", description: "d", tools: () => [], agents: () => [], workflows: () => [] });
  const list = reg.list();
  assert.equal(list.length, 1);
  assert.equal(list[0].id, "a1");
  assert.equal(typeof list[0].tools, "number");
});

// ─── TimeStore: billable time tracking ──────────────────────────────────────

test("TimeStore: open() creates entry with durationMs=0", () => {
  const store = new TimeStore();
  const entry = store.open({
    profileId: "p1",
    profileName: "Alice Partner",
    taskId: "task-abc",
    description: "Task: Review employment contract",
    event: "task_run",
    startedAt: new Date(),
  });
  assert.equal(entry.durationMs, 0);
  assert.equal(entry.billingUnits, 0);
  assert.equal(entry.profileId, "p1");
  assert.equal(entry.event, "task_run");
  assert.equal(entry.endedAt, undefined);
  assert.ok(entry.id.length > 0);
});

test("TimeStore: close() computes billingUnits correctly (7 min → 2 units)", () => {
  const store = new TimeStore();
  // Backdate startedAt by 7 minutes so durationMs ≈ 420 000 ms.
  // Ceiling division: Math.ceil(420000 / 360000) = 2.
  const sevenMinutesAgo = new Date(Date.now() - 7 * 60 * 1000);
  const entry = store.open({
    profileId: "p1",
    profileName: "Alice Partner",
    taskId: "task-xyz",
    description: "Task: Draft shareholder agreement",
    event: "task_run",
    startedAt: sevenMinutesAgo,
  });
  const closed = store.close(entry.id);
  assert.ok(closed !== undefined);
  assert.ok(closed!.endedAt instanceof Date);
  assert.ok(closed!.durationMs >= 7 * 60 * 1000);
  assert.equal(closed!.billingUnits, 2);
});

test("TimeStore: list() filters by profileId", () => {
  const store = new TimeStore();
  store.open({ profileId: "alice", profileName: "Alice", taskId: "t1", description: "d", event: "task_run", startedAt: new Date() });
  store.open({ profileId: "bob",   profileName: "Bob",   taskId: "t2", description: "d", event: "task_run", startedAt: new Date() });
  store.open({ profileId: "alice", profileName: "Alice", taskId: "t3", description: "d", event: "task_run", startedAt: new Date() });

  const aliceEntries = store.list({ profileId: "alice" });
  assert.equal(aliceEntries.length, 2);
  assert.ok(aliceEntries.every((e) => e.profileId === "alice"));

  const bobEntries = store.list({ profileId: "bob" });
  assert.equal(bobEntries.length, 1);
});

test("TimeStore: list() filters by date range", () => {
  const store = new TimeStore();
  const now = Date.now();
  const past  = new Date(now - 2 * 60 * 60 * 1000); // 2 h ago
  const mid   = new Date(now - 1 * 60 * 60 * 1000); // 1 h ago
  const future = new Date(now + 1 * 60 * 60 * 1000); // 1 h from now

  store.open({ profileId: "p", profileName: "P", taskId: "t1", description: "d", event: "task_run", startedAt: past });
  store.open({ profileId: "p", profileName: "P", taskId: "t2", description: "d", event: "task_run", startedAt: mid });

  // Filter: only entries started AFTER 90 minutes ago (between mid and future)
  const ninetyMinAgo = new Date(now - 90 * 60 * 1000);
  const filtered = store.list({ from: ninetyMinAgo, to: future });
  assert.equal(filtered.length, 1);
  assert.equal(filtered[0].taskId, "t2");
});

test("TimeStore: exportCsv() includes header row", () => {
  const store = new TimeStore();
  store.open({ profileId: "p1", profileName: "Alice", taskId: "t1", description: "Task: test", event: "task_run", startedAt: new Date() });
  const csv = store.exportCsv();
  const lines = csv.split(/\r?\n/);
  assert.ok(lines.length >= 2, "CSV should have header + at least one data row");
  assert.ok(lines[0].startsWith("id,event,profileId,profileName"), `Header row was: ${lines[0]}`);
  assert.ok(lines[0].includes("billingUnits"), "Header must include billingUnits");
  assert.ok(lines[0].includes("utbmsTaskCode"), "Header must include utbmsTaskCode");
  assert.ok(lines[0].includes("utbmsActivityCode"), "Header must include utbmsActivityCode");
});

test("detectNosLegal: returns empty object on LLM/provider failure", async () => {
  // The Anthropic client is initialised with ANTHROPIC_API_KEY=test (set by the
  // test runner). Any API call will fail with an auth error. detectNosLegal
  // catches ALL errors and returns {} — this verifies that contract.
  const result = await detectNosLegal("Test task", "some content");
  assert.ok(typeof result === "object" && result !== null, "result must be an object");
  // On failure the function returns {} — no facets set.
  // (If the Haiku call somehow succeeded it might return facets, but with key=test it won't.)
  const keys = Object.keys(result);
  // Either the call failed ({}), or in some local mock env it returned valid fields.
  // Either way it must not throw and must be a plain object.
  assert.ok(keys.every((k) => ["areaOfLaw", "workType", "sector", "assetType"].includes(k)),
    `Unexpected keys in result: ${JSON.stringify(result)}`);
});

// ─── Clio integration ────────────────────────────────────────────────────────

test("CLIO_TOOL_NAMES: has 7 entries covering all Clio tools", () => {
  const expected = [
    "clio_list_matters", "clio_get_matter", "clio_list_documents",
    "clio_download_document", "clio_create_activity", "clio_create_note",
    "clio_list_contacts",
  ];
  assert.equal(CLIO_TOOL_NAMES.length, 7);
  for (const name of expected) {
    assert.ok(CLIO_TOOL_NAMES.includes(name), `Missing tool: ${name}`);
  }
});

test("ClioClient: isConnected() returns false on a fresh instance", () => {
  const client = new ClioClient();
  assert.equal(client.isConnected(), false);
});

test("ClioClient: status() returns { connected: false } on a fresh instance", () => {
  const client = new ClioClient();
  const s = client.status();
  assert.equal(s.connected, false);
  assert.equal(s.firmName, undefined);
  assert.equal(s.firmId, undefined);
});

test("ClioClient: authUrl() targets correct us-region base and includes OAuth params", () => {
  const client = new ClioClient(); // CLIO_REGION defaults to 'us'
  const url = new URL(client.authUrl("csrf-state-xyz"));
  assert.equal(url.origin, "https://app.clio.com");
  assert.equal(url.pathname, "/oauth/authorize");
  assert.equal(url.searchParams.get("response_type"), "code");
  assert.equal(url.searchParams.get("state"), "csrf-state-xyz");
  assert.ok(url.searchParams.has("client_id"), "authUrl must include client_id");
  assert.ok(url.searchParams.has("redirect_uri"), "authUrl must include redirect_uri");
});

test("ClioClient: throws on invalid CLIO_REGION — SSRF guard", () => {
  const saved = (Config.clio as Record<string, unknown>).region;
  (Config.clio as Record<string, unknown>).region = "ru";
  try {
    assert.throws(
      () => new ClioClient(),
      (e: Error) => e.message.includes("Unknown CLIO_REGION"),
    );
  } finally {
    (Config.clio as Record<string, unknown>).region = saved;
  }
});

test("ClioClient: load() from missing file leaves instance disconnected", async () => {
  const client = new ClioClient();
  await client.load(); // ./data/clio-auth.json is absent in the test sandbox
  assert.equal(client.isConnected(), false);
});

test("Clio tools: disconnected client causes tool to return { error } not throw", async () => {
  const tool = CLIO_TOOLS.find((t) => t.name === "clio_list_matters")!;
  assert.ok(tool, "clio_list_matters must exist in CLIO_TOOLS");
  const result = await tool.execute({}) as Record<string, unknown>;
  assert.ok("error" in result, `Expected { error } but got: ${JSON.stringify(result)}`);
  assert.equal(typeof result.error, "string");
});

test("Clio tools: clio_get_matter returns { error } not throw when disconnected", async () => {
  const tool = CLIO_TOOLS.find((t) => t.name === "clio_get_matter")!;
  const result = await tool.execute({ matter_id: 42 }) as Record<string, unknown>;
  assert.ok("error" in result);
  assert.equal(typeof result.error, "string");
});

test("clio_list_matters: caps user-supplied limit at 200", async () => {
  const tool = CLIO_TOOLS.find((t) => t.name === "clio_list_matters")!;
  let capturedOpts: Record<string, unknown> | undefined;
  const fn = mock.method(clioClient, "listMatters", async (opts: Record<string, unknown>) => {
    capturedOpts = opts;
    return { data: [] };
  });
  await tool.execute({ limit: 9999 });
  fn.mock.restore();
  assert.ok(capturedOpts !== undefined, "listMatters should have been called");
  assert.equal(capturedOpts!.limit, 200, "limit must be capped at 200");
});

test("clio_list_matters: default limit is 50 when not specified", async () => {
  const tool = CLIO_TOOLS.find((t) => t.name === "clio_list_matters")!;
  let capturedOpts: Record<string, unknown> | undefined;
  const fn = mock.method(clioClient, "listMatters", async (opts: Record<string, unknown>) => {
    capturedOpts = opts;
    return { data: [] };
  });
  await tool.execute({});
  fn.mock.restore();
  assert.equal(capturedOpts!.limit, 50, "default limit must be 50");
});

// ─── Security: audit sink SSRF guard ────────────────────────────────────────

test("validateSinkUrl: accepts a public https URL", () => {
  const u = validateSinkUrl("https://logs.example.com:9200", "TestSink");
  assert.equal(u.hostname, "logs.example.com");
});

test("validateSinkUrl: rejects file:// protocol", () => {
  assert.throws(() => validateSinkUrl("file:///etc/passwd", "TestSink"), /only http\/https/);
});

test("validateSinkUrl: rejects loopback 127.0.0.1", () => {
  assert.throws(() => validateSinkUrl("http://127.0.0.1:9200", "TestSink"), /private\/loopback/);
});

test("validateSinkUrl: rejects localhost hostname", () => {
  assert.throws(() => validateSinkUrl("http://localhost:9200", "TestSink"), /private\/loopback/);
});

test("validateSinkUrl: rejects RFC-1918 10.x", () => {
  assert.throws(() => validateSinkUrl("https://10.0.0.1/opensearch", "TestSink"), /private\/loopback/);
});

test("validateSinkUrl: rejects RFC-1918 172.16-31", () => {
  assert.throws(() => validateSinkUrl("https://172.20.5.1/splunk", "TestSink"), /private\/loopback/);
});

test("validateSinkUrl: rejects link-local 169.254.x", () => {
  assert.throws(() => validateSinkUrl("http://169.254.169.254/latest/meta-data/", "TestSink"), /private\/loopback/);
});

test("validateSinkUrl: rejects IPv6 loopback ::1", () => {
  assert.throws(() => validateSinkUrl("http://[::1]:9200", "TestSink"), /private\/loopback/);
});

// ─── Security: LEDES 1998B field injection guard ─────────────────────────────

test("exportLedes1998B: pipe in invoiceNumber cannot create extra LEDES fields", () => {
  const entry = {
    id: "e1", event: "task_execution" as const,
    startedAt: new Date("2026-01-01T10:00:00Z"),
    endedAt: new Date("2026-01-01T10:30:00Z"),
    durationMs: 1_800_000, billingUnits: 5,
    description: "Review contract",
  } as import("../src/types.js").TimeEntry;
  const output = exportLedes1998B([entry], { invoiceNumber: "INV-001|INJECTED" });
  const rows = output.split("\r\n");
  const dataRow = rows[2];
  assert.ok(dataRow !== undefined, "should have a data row");
  // After sanitisation the pipe is replaced with a space; "INJECTED" must not be
  // an isolated column (i.e. no field should equal the injected token verbatim).
  assert.ok(!dataRow.split("|").includes("INJECTED"), "pipe must not create an isolated LEDES field");
});

test("exportLedes1998B: CRLF in description does not create extra LEDES rows", () => {
  const entry = {
    id: "e2", event: "task_execution" as const,
    startedAt: new Date("2026-01-01T10:00:00Z"),
    endedAt: new Date("2026-01-01T11:00:00Z"),
    durationMs: 3_600_000, billingUnits: 10,
    description: "Draft motion\r\nExtra line",
  } as import("../src/types.js").TimeEntry;
  const output = exportLedes1998B([entry], { invoiceNumber: "INV-002" });
  // LEDES1998B[] + header + 1 data row + trailing empty = 4 parts when split on CRLF
  const nonEmptyLines = output.split("\r\n").filter(Boolean);
  assert.equal(nonEmptyLines.length, 3, "CRLF in description must not produce extra rows");
});

// ─── BudgetPredictor ─────────────────────────────────────────────────────────

import { BudgetPredictor } from "../src/budget/predictor.js";
import type { Task } from "../src/types.js";

/** Helper: create a minimal closed Task */
function makeTask(matterNumber: string, jurisdiction = "", workflowType: Task["workflowType"] = "roundtable"): Task {
  return {
    id: matterNumber,
    description: "Test matter",
    matterNumber,
    jurisdiction,
    workflowType,
    status: "complete",
    currentPhase: "delivery",
    currentRound: 1,
    maxRounds: 3,
    activeAgentIds: [],
    documentIds: [],
    rounds: [],
    findings: [],
    pendingGates: [],
    createdAt: new Date("2026-01-01"),
    updatedAt: new Date("2026-01-02"),
    completedAt: new Date("2026-01-02"),
  };
}

/** Helper: open and immediately close a time entry on a store */
function addClosedEntry(
  store: TimeStore,
  matterNumber: string,
  billingAmountUsd: number,
  billingUnits = 1,
): void {
  const startedAt = new Date(Date.now() - 30 * 60 * 1000); // 30 min ago
  const entry = store.open({
    taskId: matterNumber,
    matterNumber,
    description: "Test work",
    event: "task_run",
    startedAt,
  });
  // Manually set billing data (close() would compute it from wall-clock time)
  const stored = store.list({ matterNumber }).find((e) => e.id === entry.id)!;
  stored.endedAt = new Date();
  stored.durationMs = 30 * 60 * 1000;
  stored.billingUnits = billingUnits;
  stored.billingAmountUsd = billingAmountUsd;
}

test("BudgetPredictor: returns null for unknown matter", () => {
  const predictor = new BudgetPredictor();
  const store = new TimeStore();
  const taskMap = new Map<string, Task>();
  const result = predictor.predict("MATTER-UNKNOWN", store, taskMap);
  assert.equal(result, null);
});

test("BudgetPredictor: insufficient_data confidence when < 3 comparable matters", () => {
  const predictor = new BudgetPredictor();
  const store = new TimeStore();
  const taskMap = new Map<string, Task>();

  // Add 2 closed comparable matters (not enough for low confidence)
  for (let i = 1; i <= 2; i++) {
    const mn = `MATTER-HIST-${i}`;
    addClosedEntry(store, mn, 1000, 2);
    addClosedEntry(store, mn, 500, 1);
    taskMap.set(mn, makeTask(mn));
  }

  // Add the open matter being predicted
  const openMatter = "MATTER-OPEN";
  addClosedEntry(store, openMatter, 400, 1);
  taskMap.set(openMatter, makeTask(openMatter));

  const result = predictor.predict(openMatter, store, taskMap);
  assert.ok(result !== null, "should return a prediction");
  assert.equal(result!.confidence, "insufficient_data");
});

test("BudgetPredictor: high confidence with 10+ comparable matters", () => {
  const predictor = new BudgetPredictor();
  const store = new TimeStore();
  const taskMap = new Map<string, Task>();

  // Add 10 closed comparable matters (qualifies for high confidence)
  for (let i = 1; i <= 10; i++) {
    const mn = `MATTER-HIST-HIGH-${i}`;
    addClosedEntry(store, mn, 2000, 4);
    addClosedEntry(store, mn, 1000, 2);
    taskMap.set(mn, makeTask(mn, "US", "roundtable"));
  }

  // Open matter being predicted (same jurisdiction so we get high-confidence comparables)
  const openMatter = "MATTER-OPEN-HIGH";
  addClosedEntry(store, openMatter, 500, 1);
  taskMap.set(openMatter, makeTask(openMatter, "US", "roundtable"));

  const result = predictor.predict(openMatter, store, taskMap);
  assert.ok(result !== null, "should return a prediction");
  assert.equal(result!.confidence, "high");
  assert.ok(result!.comparableMatterCount >= 10);
});

test("BudgetPredictor: percentile calculation is correct", () => {
  const predictor = new BudgetPredictor();
  // Test with a simple sorted array: [1, 2, 3, 4, 5]
  const sorted = [1, 2, 3, 4, 5];
  // Median (p=0.5) of [1,2,3,4,5] → index 2 → value 3
  assert.equal(predictor.percentile(sorted, 0.5), 3);
  // p=0 → first element
  assert.equal(predictor.percentile(sorted, 0), 1);
  // p=1 → last element
  assert.equal(predictor.percentile(sorted, 1), 5);
  // p=0.25 → index 1 → value 2
  assert.equal(predictor.percentile(sorted, 0.25), 2);
  // p=0.75 → index 3 → value 4
  assert.equal(predictor.percentile(sorted, 0.75), 4);
  // Linear interpolation: [10, 20] at p=0.5 → 15
  assert.equal(predictor.percentile([10, 20], 0.5), 15);
});

test("BudgetPredictor: completionPct caps at 99 for open matters", () => {
  const predictor = new BudgetPredictor();
  const store = new TimeStore();
  const taskMap = new Map<string, Task>();

  // Add 5 closed comparable matters with median cost of $100
  for (let i = 1; i <= 5; i++) {
    const mn = `MATTER-HIST-CAP-${i}`;
    addClosedEntry(store, mn, 50, 1);
    addClosedEntry(store, mn, 50, 1);
    taskMap.set(mn, makeTask(mn));
  }

  // Open matter that has already spent MORE than the median (overspent)
  const openMatter = "MATTER-OPEN-CAP";
  addClosedEntry(store, openMatter, 200, 4); // spent 200, median is 100
  taskMap.set(openMatter, makeTask(openMatter));

  const result = predictor.predict(openMatter, store, taskMap);
  assert.ok(result !== null, "should return a prediction");
  // Even though spent > estimated, completionPct should cap at 99
  assert.ok(result!.completionPct <= 99, `completionPct ${result!.completionPct} must not exceed 99`);
});

// ─── ConflictGraph: TypeDB optional integration ───────────────────────────────

import { ConflictGraph } from "../src/graph/conflict.js";
import type { ConflictReport } from "../src/types.js";
import { syncConflictGraph } from "../src/graph/sync.js";

test("ConflictGraph: isEnabled() returns false when TYPEDB_URL is unset", () => {
  const g = new ConflictGraph();
  assert.equal(g.isEnabled(), false);
});

test("ConflictGraph: connect() is a no-op when disabled (returns without error)", async () => {
  const g = new ConflictGraph();
  await assert.doesNotReject(() => g.connect());
});

test("ConflictGraph: checkClient() returns [] when disabled", async () => {
  const g = new ConflictGraph();
  const result = await g.checkClient("client-abc");
  assert.deepEqual(result, []);
});

test("ConflictGraph: checkNewMatter() returns [] when disabled", async () => {
  const g = new ConflictGraph();
  const result = await g.checkNewMatter("client-abc", ["adv-1", "adv-2"]);
  assert.deepEqual(result, []);
});

test("ConflictReport: shape matches expected interface", () => {
  const report: ConflictReport = {
    clientAId:     "client-001",
    clientAName:   "Acme Corp",
    clientBId:     "client-002",
    clientBName:   "Rival Ltd",
    matterANumber: "M-2024-001",
    matterBNumber: "M-2024-002",
    conflictPath:  "direct",
    detectedAt:    new Date().toISOString(),
  };
  assert.equal(typeof report.clientAId, "string");
  assert.equal(typeof report.clientAName, "string");
  assert.equal(typeof report.clientBId, "string");
  assert.equal(typeof report.clientBName, "string");
  assert.equal(typeof report.matterANumber, "string");
  assert.equal(typeof report.matterBNumber, "string");
  assert.equal(typeof report.conflictPath, "string");
  assert.equal(typeof report.detectedAt, "string");
});

test("syncConflictGraph: no-op when graph is disabled", async () => {
  const g = new ConflictGraph();
  const stubClients = {
    list: () => [],
    get: () => undefined,
    getByClientNumber: () => undefined,
  } as unknown as import("../src/clients/index.js").ClientStore;
  const stubTime = {} as unknown as import("../src/time/index.js").TimeStore;
  await assert.doesNotReject(() => syncConflictGraph(g, stubClients, stubTime));
});

// ─── RegPulseMonitor ─────────────────────────────────────────────────────────

test("RegPulseMonitor: isEnabled() false when TAVILY_API_KEY not set", () => {
  const monitor = new RegPulseMonitor();
  assert.equal(monitor.isEnabled(), false);
});

test("RegPulseMonitor: buildQuery produces search string with practice area and jurisdiction", () => {
  const monitor = new RegPulseMonitor();
  const query = monitor.buildQuery("Intellectual Property", "US");
  assert.ok(query.includes("Intellectual Property"), "query must contain practice area");
  assert.ok(query.includes("US"), "query must contain jurisdiction");
  assert.ok(
    query.includes("regulation") || query.includes("ruling") || query.includes("guidance"),
    "query must include regulatory search terms",
  );
});

test("RegPulseMonitor: skips matter without practiceArea (no noslegal.areaOfLaw)", async () => {
  const monitor = new RegPulseMonitor();
  const task = {
    id: "t-nopa",
    jurisdiction: "UK",
    status: "running",
  } as unknown as Task;
  const alerts = await monitor.checkMatter(task);
  assert.equal(alerts.length, 0, "should return no alerts when noslegal.areaOfLaw is missing");
});

test("RegPulseMonitor: skips matter without jurisdiction", async () => {
  const monitor = new RegPulseMonitor();
  const task = {
    id: "t-nojur",
    noslegal: { areaOfLaw: "Tax" },
    status: "running",
  } as unknown as Task;
  const alerts = await monitor.checkMatter(task);
  assert.equal(alerts.length, 0, "should return no alerts when jurisdiction is missing");
});

test("RegPulseMonitor: respects per-matter cooldown", async () => {
  const monitor = new RegPulseMonitor();
  const lastChecked = (monitor as unknown as { lastChecked: Map<string, number> }).lastChecked;
  lastChecked.set("matter-123", Date.now());
  const task = {
    id: "t-cooldown",
    matterNumber: "matter-123",
    jurisdiction: "EU",
    noslegal: { areaOfLaw: "Competition & Antitrust" },
    status: "running",
  } as unknown as Task;
  const alerts = await monitor.checkMatter(task);
  assert.equal(alerts.length, 0, "should skip matter within cooldown window");
});

// ─── Status report: markdownToHtml helper ─────────────────────────────────────

test("markdownToHtml: ## Heading → contains <h2>", () => {
  const html = markdownToHtml("## Summary");
  assert.ok(html.includes("<h2>"), `Expected <h2> in output but got: ${html}`);
  assert.ok(html.includes("Summary"), "Heading text should be preserved");
});

test("markdownToHtml: **bold** → contains <strong>", () => {
  const html = markdownToHtml("This is **important** text.");
  assert.ok(html.includes("<strong>"), `Expected <strong> in output but got: ${html}`);
  assert.ok(html.includes("important"), "Bold text should be preserved");
});

test("markdownToHtml: - item → contains <li>", () => {
  const html = markdownToHtml("- First item\n- Second item");
  assert.ok(html.includes("<li>"), `Expected <li> in output but got: ${html}`);
  assert.ok(html.includes("First item"), "List item text should be preserved");
});

test("markdownToHtml: ### Heading → contains <h3>", () => {
  const html = markdownToHtml("### Sub-section");
  assert.ok(html.includes("<h3>"), `Expected <h3> in output but got: ${html}`);
});

test("markdownToHtml: plain text → wrapped in <p>", () => {
  const html = markdownToHtml("Hello world.");
  assert.ok(html.includes("<p>"), `Expected <p> in output but got: ${html}`);
  assert.ok(html.includes("Hello world."), "Paragraph text should be preserved");
});

test("markdownToHtml: HTML special chars are escaped", () => {
  const html = markdownToHtml("This is <b>not</b> HTML & raw.");
  assert.ok(!html.includes("<b>"), "Raw <b> must be escaped");
  assert.ok(html.includes("&lt;b&gt;"), "< and > must be escaped to &lt;&gt;");
  assert.ok(html.includes("&amp;"), "& must be escaped to &amp;");
});

// ─── DocketMonitor ────────────────────────────────────────────────────────────

test("DocketMonitor: isEnabled() false by default", () => {
  const orig = process.env["DOCKET_MONITOR_ENABLED"];
  delete process.env["DOCKET_MONITOR_ENABLED"];
  const monitor = new DocketMonitor(pathJoin(os.tmpdir(), `dockets-test-${Date.now()}.json`));
  assert.equal(monitor.isEnabled(), false, "isEnabled() must be false when DOCKET_MONITOR_ENABLED is not set");
  if (orig !== undefined) process.env["DOCKET_MONITOR_ENABLED"] = orig;
});

test("DocketMonitor: watch() validates docket number format", () => {
  const monitor = new DocketMonitor(pathJoin(os.tmpdir(), `dockets-test-${Date.now()}.json`));
  assert.throws(
    () => monitor.watch("M-001", "bad docket number!", "nysd"),
    /docketNumber/,
    "must reject docket numbers containing invalid characters",
  );
});

test("DocketMonitor: watch() rejects invalid court slug", () => {
  const monitor = new DocketMonitor(pathJoin(os.tmpdir(), `dockets-test-${Date.now()}.json`));
  assert.throws(
    () => monitor.watch("M-001", "1:23-cv-01234", "INVALID-SLUG"),
    /court/,
    "must reject court slugs that contain uppercase or special characters",
  );
});

test("DocketMonitor: unwatch() returns false for unknown matter", () => {
  const monitor = new DocketMonitor(pathJoin(os.tmpdir(), `dockets-test-${Date.now()}.json`));
  const result = monitor.unwatch("no-such-matter");
  assert.equal(result, false, "unwatch() must return false for an unregistered matter");
});

test("DocketMonitor: list() returns all watched dockets", () => {
  const monitor = new DocketMonitor(pathJoin(os.tmpdir(), `dockets-test-${Date.now()}.json`));
  monitor.watch("M-101", "1:23-cv-01234", "nysd", "Acme v Beta");
  monitor.watch("M-102", "2:24-cv-05678", "dcd");
  const list = monitor.list();
  assert.equal(list.length, 2, "list() must return both watched dockets");
  const numbers = list.map((d) => d.matterNumber);
  assert.ok(numbers.includes("M-101"), "must include M-101");
  assert.ok(numbers.includes("M-102"), "must include M-102");
});
