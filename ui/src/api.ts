import type {
  Task, Template, Health, WorkflowType, SearchResult, AuditEntry, DocumentRef, AgentSummary,
  AppSettings, LawyerProfile, ToneProfile, Me, Client, ClientMatter, ConflictCheckResult,
  IngestResult, CostSummary, TaskCostResult, TimeEntry, OcgDocument, ClientVoiceGuide,
  PreBill, PreBillStatus, AgentBillingSummary, InvoiceValidationResult,
  BudgetBurn, BudgetPrediction, DeadlineJurisdiction, DeadlineResult,
  MatterHealthScore, PortfolioHealthSummary,
  WatchedDocket, RegulationAlert,
  Playbook, PlaybookScope, PlaybookQueryResult, RedlineReport, HeadnoteReport,
  PrecedentDocument, CitationCheckResult,
  NosLegalBreakdown, Job, JobStatus, QueueStats, Attachment,
  ReviewRecord, RedtimeTimeline,
} from "./types";

type SettingsPatch = {
  presentation?: Partial<AppSettings["presentation"]>;
  dytopo?: Partial<AppSettings["dytopo"]>;
  debate?: Partial<AppSettings["debate"]>;
  docuseal?: Partial<{ enabled: boolean; url: string; apiKey: string }>;
  clientVoice?: Partial<AppSettings["clientVoice"]>;
  models?: Partial<AppSettings["models"]>;
  drafting?: Partial<AppSettings["drafting"]>;
};

async function json<T>(res: Response): Promise<T> {
  if (!res.ok) {
    const detail = await res.text().catch(() => "");
    throw new Error(`${res.status} ${res.statusText}${detail ? ` — ${detail}` : ""}`);
  }
  return res.json() as Promise<T>;
}

const POST = (body: unknown): RequestInit => ({
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
});

export const api = {
  listTasks: () => fetch("/tasks").then(json<Task[]>),
  getTask: (id: string) => fetch(`/tasks/${id}`).then(json<Task>),
  health: () => fetch("/health").then(json<Health>),
  listTemplates: () => fetch("/templates").then(json<Template[]>),
  listAgents: () => fetch("/agents").then(json<AgentSummary[]>),
  getSettings: () => fetch("/settings").then(json<AppSettings>),
  updateSettings: (patch: SettingsPatch) =>
    fetch("/settings", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) }).then(json<AppSettings>),

  submitTask: (body: { description: string; workflowType: WorkflowType; documentIds?: string[]; clientNumber?: string; matterNumber?: string }) =>
    fetch("/tasks", POST(body)).then(json<Task>),

  fromTemplate: (body: { templateId: string; substitutions?: Record<string, string>; documentIds?: string[]; clientNumber?: string; matterNumber?: string }) =>
    fetch("/tasks/from-template", POST(body)).then(json<Task>),

  deleteTask: (id: string) =>
    fetch(`/tasks/${id}`, { method: "DELETE" }).then((r) => json<{ ok: true }>(r)),

  assignLawyers: (taskId: string, lawyerIds: string[]) =>
    fetch(`/tasks/${taskId}/assign`, POST({ lawyerIds })).then(json<Task>),

  me: () => fetch("/me").then(json<Me>),
  authProviders: () => fetch("/auth/providers").then(json<{ google: boolean; microsoft: boolean; linkedin: boolean }>),
  logout: () => fetch("/auth/logout", { method: "POST" }).then((r) => json<{ ok: true }>(r)),
  listProfiles: () => fetch("/profiles").then(json<LawyerProfile[]>),
  getProfile: (id: string) => fetch(`/profiles/${id}`).then(json<LawyerProfile>),
  createProfile: (body: { name: string; email: string; role?: string; title?: string; practiceAreas?: string[]; bio?: string }) =>
    fetch("/profiles", POST(body)).then(json<LawyerProfile>),
  updateProfile: (id: string, patch: Partial<Pick<LawyerProfile, "name" | "title" | "color" | "role" | "practiceAreas" | "bio" | "mode">>) =>
    fetch(`/profiles/${id}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) }).then(json<LawyerProfile>),
  deleteProfile: (id: string) =>
    fetch(`/profiles/${id}`, { method: "DELETE" }).then((r) => json<{ ok: true }>(r)),

  listClients: () => fetch("/clients").then(json<Client[]>),
  createClient: (body: { name: string; clientNumber: string; adversaries?: string[]; notes?: string }) =>
    fetch("/clients", POST(body)).then(json<Client & { conflict: ConflictCheckResult }>),
  updateClient: (id: string, patch: Partial<Pick<Client, "name" | "adversaries" | "notes">>) =>
    fetch(`/clients/${id}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) }).then(json<Client>),
  deleteClient: (id: string) =>
    fetch(`/clients/${id}`, { method: "DELETE" }).then((r) => json<{ ok: true }>(r)),
  addMatter: (clientId: string, body: { matterNumber: string; description: string; practiceArea?: string }) =>
    fetch(`/clients/${clientId}/matters`, POST(body)).then(json<ClientMatter>),
  removeMatter: (clientId: string, matterNumber: string) =>
    fetch(`/clients/${clientId}/matters/${encodeURIComponent(matterNumber)}`, { method: "DELETE" }).then((r) => json<{ ok: true }>(r)),
  checkConflict: (name: string) =>
    fetch("/clients/check-conflict", POST({ name })).then(json<ConflictCheckResult>),

  approveGate: (taskId: string, gateId: string, note?: string) =>
    fetch(`/tasks/${taskId}/gates/${gateId}/approve`, POST({ note })).then((r) => json<{ ok: true }>(r)),

  rejectGate: (taskId: string, gateId: string, reason: string) =>
    fetch(`/tasks/${taskId}/gates/${gateId}/reject`, POST({ reason })).then((r) => json<{ ok: true }>(r)),

  tableCsvUrl: (id: string) => `/tasks/${id}/table.csv`,

  listDocuments: () => fetch("/documents").then(json<DocumentRef[]>),

  ingestDocument: (body: { title: string; content: string; source?: string; jurisdiction?: string; documentType?: string; practiceArea?: string }) =>
    fetch("/documents", POST(body)).then(json<IngestResult>),

  uploadDocument: (file: File) => {
    const fd = new FormData();
    fd.append("file", file);
    return fetch("/documents/upload", { method: "POST", body: fd }).then(json<IngestResult>);
  },

  searchDocuments: (query: string, topK?: number) =>
    fetch(`/documents/search?q=${encodeURIComponent(query)}${topK ? `&topK=${topK}` : ""}`).then(json<SearchResult[]>),

  // ── Tabular reviews (due-diligence grid) ────────────────────────────────────
  getReview: (id: string) =>
    fetch(`/reviews/${encodeURIComponent(id)}`).then(json<ReviewRecord>),
  reviewCsvUrl: (id: string) => `/reviews/${encodeURIComponent(id)}/table.csv`,

  // ── Redtime (document version lineage timeline) ─────────────────────────────
  getTimeline: (id: string, params?: { matterNumber?: string; clientNumber?: string; profileId?: string; practiceArea?: string }) => {
    const qs = new URLSearchParams();
    if (params?.matterNumber) qs.set("matterNumber", params.matterNumber);
    if (params?.clientNumber) qs.set("clientNumber", params.clientNumber);
    if (params?.profileId) qs.set("profileId", params.profileId);
    if (params?.practiceArea) qs.set("practiceArea", params.practiceArea);
    const q = qs.toString();
    return fetch(`/documents/${encodeURIComponent(id)}/timeline${q ? `?${q}` : ""}`).then(json<RedtimeTimeline>);
  },

  listAttachments: (docId: string) =>
    fetch(`/documents/attachments/${docId}`).then(json<Attachment[]>),
  attachmentUrl: (docId: string, attId: string) => `/documents/attachments/${docId}/${attId}`,
  exportDocumentUrl: (docId: string) => `/documents/export/${docId}`,

  recentAudit: (limit = 60, opts?: { actorId?: string; event?: string; taskId?: string }) => {
    const q = new URLSearchParams({ limit: String(limit) });
    if (opts?.actorId) q.set("actorId", opts.actorId);
    if (opts?.event) q.set("event", opts.event);
    if (opts?.taskId) q.set("taskId", opts.taskId);
    return fetch(`/audit?${q.toString()}`).then(json<AuditEntry[]>);
  },

  toneImport: (profileId: string, file: File) => {
    const fd = new FormData();
    fd.append("file", file);
    return fetch(`/profiles/${profileId}/tone/import`, { method: "POST", body: fd })
      .then(json<{ toneProfile: ToneProfile; samplesAnalysed: number; sourceType: string }>);
  },

  clearTone: (profileId: string) =>
    fetch(`/profiles/${profileId}/tone`, { method: "DELETE" }).then(json<LawyerProfile>),

  getCostSummary: () => fetch("/cost/summary").then(json<CostSummary>),
  getTaskCost: (id: string) => fetch(`/tasks/${id}/cost`).then(json<TaskCostResult>),
  getProfileCost: (id: string) => fetch(`/profiles/${id}/cost`).then(json<{ profileId: string; summary: CostSummary; entries: unknown[] }>),

  // OCG
  getClientOcg: (clientId: string) => fetch(`/clients/${clientId}/ocg`).then(json<OcgDocument>),
  ingestClientOcg: (clientId: string, body: { title: string; text: string }) =>
    fetch(`/clients/${clientId}/ocg`, POST(body)).then(json<{ ocg: OcgDocument; ruleCount: number }>),
  uploadClientOcg: (clientId: string, title: string, file: File) => {
    const fd = new FormData(); fd.append("file", file); fd.append("title", title);
    return fetch(`/clients/${clientId}/ocg`, { method: "POST", body: fd }).then(json<{ ocg: OcgDocument; ruleCount: number }>);
  },
  deleteClientOcg: (clientId: string) =>
    fetch(`/clients/${clientId}/ocg`, { method: "DELETE" }).then(json<{ ok: true }>),
  importClientVoice: (clientId: string, file: File) => {
    const fd = new FormData(); fd.append("file", file);
    return fetch(`/clients/${clientId}/voice-guide/import`, { method: "POST", body: fd })
      .then(json<{ voiceGuide: ClientVoiceGuide; samplesAnalysed: number }>);
  },
  deleteClientVoice: (clientId: string) =>
    fetch(`/clients/${clientId}/voice-guide`, { method: "DELETE" }).then(json<{ ok: true }>),

  // Time entry suggestions
  listTimeEntrySuggestions: (params?: { clientNumber?: string; matterNumber?: string; profileId?: string }) => {
    const qs = new URLSearchParams();
    if (params?.clientNumber) qs.set("clientNumber", params.clientNumber);
    if (params?.matterNumber) qs.set("matterNumber", params.matterNumber);
    if (params?.profileId) qs.set("profileId", params.profileId);
    return fetch(`/time-entries/suggestions?${qs}`).then(json<TimeEntry[]>);
  },
  listTimeEntries: (params?: { clientNumber?: string; matterNumber?: string; profileId?: string }) => {
    const qs = new URLSearchParams();
    if (params?.clientNumber) qs.set("clientNumber", params.clientNumber);
    if (params?.matterNumber) qs.set("matterNumber", params.matterNumber);
    if (params?.profileId) qs.set("profileId", params.profileId);
    return fetch(`/time-entries?${qs}`).then(json<TimeEntry[]>);
  },
  runOcgCheck: (body: { clientNumber?: string; matterNumber?: string; limit?: number }) =>
    fetch("/time-entries/run-ocg-check", POST(body)).then(json<{ checked: number; withSuggestions: number }>),
  acceptSuggestion: (entryId: string, ruleId: string) =>
    fetch(`/time-entries/${entryId}/suggestions/accept`, POST({ ruleId })).then(json<TimeEntry>),
  dismissSuggestion: (entryId: string, ruleId: string) =>
    fetch(`/time-entries/${entryId}/suggestions/dismiss`, POST({ ruleId })).then(json<TimeEntry>),

  // ── Billing: pre-bills, invoice validation, exports ────────────────────────
  listPreBills: (matterNumber?: string) =>
    fetch(`/pre-bills${matterNumber ? `?matterNumber=${encodeURIComponent(matterNumber)}` : ""}`).then(json<PreBill[]>),
  createPreBill: (body: { matterNumber: string; clientNumber?: string; from?: string; to?: string }) =>
    fetch("/pre-bills", POST(body)).then(json<PreBill>),
  getPreBill: (id: string) => fetch(`/pre-bills/${id}`).then(json<PreBill>),
  patchPreBill: (id: string, body: { status?: PreBillStatus; notes?: string; entryEdit?: { entryId: string; description: string } }) =>
    fetch(`/pre-bills/${id}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }).then(json<PreBill>),

  validateInvoice: (body: {
    invoiceText: string; clientId?: string; submittedByFirm?: string;
    matterNumber?: string; generateDisputeLetter?: boolean;
  }) => fetch("/invoices/validate", POST(body)).then(json<InvoiceValidationResult>),

  agentBillingSummary: (params?: { taskId?: string; matterNumber?: string; clientNumber?: string }) => {
    const qs = new URLSearchParams();
    if (params?.taskId) qs.set("taskId", params.taskId);
    if (params?.matterNumber) qs.set("matterNumber", params.matterNumber);
    if (params?.clientNumber) qs.set("clientNumber", params.clientNumber);
    return fetch(`/time-entries/agent-summary?${qs}`).then(json<AgentBillingSummary[]>);
  },

  timeExportCsvUrl: () => "/time-entries/export.csv",
  timeExportJsonUrl: () => "/time-entries/export.json",
  timeExportLedesUrl: (matterNumber: string) =>
    `/time-entries/export.ledes?matterNumber=${encodeURIComponent(matterNumber)}`,

  // ── Budgets, deadlines & matter health ─────────────────────────────────────
  setMatterBudget: (clientId: string, matterNumber: string, body: { budgetUsd: number; thresholds?: number[] }) =>
    fetch(`/clients/${clientId}/matters/${encodeURIComponent(matterNumber)}/budget`,
      { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }).then(json<ClientMatter>),
  getMatterBudget: (clientId: string, matterNumber: string) =>
    fetch(`/clients/${clientId}/matters/${encodeURIComponent(matterNumber)}/budget`).then(json<BudgetBurn>),
  checkMatterBudget: (clientId: string, matterNumber: string) =>
    fetch(`/clients/${clientId}/matters/${encodeURIComponent(matterNumber)}/budget/check`, POST({})).then(json<{ ok: true }>),
  budgetPrediction: (matterNumber: string) =>
    fetch(`/matters/${encodeURIComponent(matterNumber)}/budget-prediction`).then(json<BudgetPrediction>),

  deadlineRules: () => fetch("/deadlines/rules").then(json<DeadlineJurisdiction[]>),
  computeDeadlines: (body: { jurisdiction: string; triggerEvent: string; triggerDate: string }) =>
    fetch("/deadlines/compute", POST(body)).then(json<DeadlineResult>),

  matterHealth: (matterNumber: string) =>
    fetch(`/matters/${encodeURIComponent(matterNumber)}/health`).then(json<MatterHealthScore>),
  portfolioHealth: () => fetch("/analytics/portfolio-health").then(json<PortfolioHealthSummary>),

  // ── Watchtower: dockets & regulatory ───────────────────────────────────────
  listDockets: () => fetch("/dockets").then(json<WatchedDocket[]>),
  watchDocket: (body: { matterNumber: string; docketNumber: string; court: string; caseName?: string }) =>
    fetch("/dockets/watch", POST(body)).then(json<WatchedDocket>),
  unwatchDocket: (matterNumber: string) =>
    fetch(`/dockets/watch/${encodeURIComponent(matterNumber)}`, { method: "DELETE" }).then((r) => json<{ ok: true }>(r)),
  docketCheckNow: () => fetch("/dockets/check-now", POST({})).then(json<{ ok: true; watching: number }>),
  regulatoryCheckNow: () =>
    fetch("/regulatory/check-now", POST({})).then(json<{ checked: number; alerts: RegulationAlert[] }>),

  // ── Drafting: playbooks, redline, headnotes, precedents, citations ─────────
  listPlaybooks: (params?: { scope?: PlaybookScope; practiceArea?: string }) => {
    const qs = new URLSearchParams();
    if (params?.scope) qs.set("scope", params.scope);
    if (params?.practiceArea) qs.set("practiceArea", params.practiceArea);
    return fetch(`/playbooks?${qs}`).then(json<Playbook[]>);
  },
  getPlaybook: (id: string) => fetch(`/playbooks/${id}`).then(json<Playbook>),
  buildPlaybook: (body: {
    scope?: PlaybookScope; ownerId?: string; ownerName?: string; practiceArea: string;
    jurisdiction?: string; name: string; description?: string; clauseTypes?: string[];
  }) => fetch("/playbooks/build", POST(body)).then(json<Playbook>),
  resolvePlaybook: (clauseType: string, params?: { practiceArea?: string; matterNumber?: string; clientId?: string; profileId?: string }) => {
    const qs = new URLSearchParams();
    if (params?.practiceArea) qs.set("practiceArea", params.practiceArea);
    if (params?.matterNumber) qs.set("matterNumber", params.matterNumber);
    if (params?.clientId) qs.set("clientId", params.clientId);
    if (params?.profileId) qs.set("profileId", params.profileId);
    return fetch(`/playbooks/resolve/${encodeURIComponent(clauseType)}?${qs}`).then(json<PlaybookQueryResult>);
  },
  deletePlaybook: (id: string) =>
    fetch(`/playbooks/${id}`, { method: "DELETE" }).then((r) => json<{ deleted: true }>(r)),

  redline: (body: {
    documentText: string; practiceArea?: string; jurisdiction?: string;
    matterNumber?: string; clientId?: string; documentTitle?: string;
  }) => fetch("/redline", POST(body)).then(json<RedlineReport>),

  generateHeadnotes: (body: {
    opinionText: string; caseName?: string; citation?: string; court?: string;
    dateFiled?: string; jurisdiction?: string;
  }) => fetch("/headnotes/generate", POST(body)).then(json<HeadnoteReport>),

  generatePrecedent: (body: {
    documentType: string; practiceArea?: string; jurisdiction?: string; actingFor?: string;
    matterNumber?: string; clientId?: string; specialInstructions?: string;
  }) => fetch("/precedents/generate", POST(body)).then(json<PrecedentDocument>),

  checkCitation: (q: string) =>
    fetch(`/citations/check?q=${encodeURIComponent(q)}`).then(json<CitationCheckResult>),

  // ── Analytics ───────────────────────────────────────────────────────────────
  noslegalAnalytics: () => fetch("/analytics/noslegal").then(json<NosLegalBreakdown>),

  // ── Jobs queue ──────────────────────────────────────────────────────────────
  listJobs: (params?: { status?: JobStatus; limit?: number; offset?: number }) => {
    const qs = new URLSearchParams();
    if (params?.status) qs.set("status", params.status);
    if (params?.limit != null) qs.set("limit", String(params.limit));
    if (params?.offset != null) qs.set("offset", String(params.offset));
    return fetch(`/jobs?${qs}`).then(json<Job[]>);
  },
  jobStats: () => fetch("/jobs/stats").then(json<QueueStats>),
  retryJob: (id: string) => fetch(`/jobs/${id}/retry`, POST({})).then(json<{ ok: true; job: Job }>),
};

/**
 * Generic SSE subscription for alert streams (`data:`-only events).
 * `onDown` fires if the stream closes for good (e.g. 403/503 from the server) —
 * EventSource does not retry after a non-200 response.
 */
export function streamAlerts<T>(
  url: string,
  onEvent: (event: T) => void,
  onDown?: () => void,
): () => void {
  const es = new EventSource(url);
  es.onmessage = (e) => {
    // A malformed event is dropped, not fatal — but log it so a server-side
    // serialization bug shows up in the console instead of vanishing.
    try { onEvent(JSON.parse(e.data) as T); }
    catch (err) { console.error(`SSE ${url}: unparseable event`, err, e.data); }
  };
  es.onerror = () => {
    if (es.readyState === EventSource.CLOSED) onDown?.();
  };
  return () => es.close();
}

/**
 * Subscribe to the global live audit stream. The server replays recent
 * entries on connect, then pushes new ones as they happen.
 */
export function streamAudit(
  onEntry: (entry: AuditEntry) => void,
  opts?: { actorId?: string; taskId?: string },
): () => void {
  const q = new URLSearchParams();
  if (opts?.actorId) q.set("actorId", opts.actorId);
  if (opts?.taskId) q.set("taskId", opts.taskId);
  const qs = q.toString();
  const es = new EventSource(`/audit/stream${qs ? `?${qs}` : ""}`);
  es.onmessage = (e) => {
    try { onEntry(JSON.parse(e.data) as AuditEntry); }
    catch (err) { console.error("SSE /audit/stream: unparseable event", err, e.data); }
  };
  return () => es.close();
}

/**
 * Subscribe to a task's live progress stream. The server emits a full-task
 * `snapshot` immediately, then lightweight progress events — we treat every
 * event as a cue to call `onPing` (which refetches the authoritative state).
 */
export function streamTask(
  id: string,
  handlers: { onSnapshot: (task: Task) => void; onPing: () => void },
): () => void {
  const es = new EventSource(`/tasks/${id}/stream`);

  es.addEventListener("snapshot", (e) => {
    try { handlers.onSnapshot(JSON.parse((e as MessageEvent).data) as Task); }
    catch (err) { console.error(`SSE /tasks/${id}/stream: unparseable snapshot`, err); }
  });

  for (const evt of ["started", "phase", "round", "complete", "failed"]) {
    es.addEventListener(evt, () => handlers.onPing());
  }

  return () => es.close();
}
