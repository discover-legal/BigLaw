import type { Task, Template, Health, WorkflowType, SearchResult, AuditEntry, DocumentRef, AgentSummary, AppSettings, LawyerProfile, Me, Client, ClientMatter, ConflictCheckResult, IngestResult, CostSummary, TaskCostResult } from "./types";

type SettingsPatch = {
  presentation?: Partial<AppSettings["presentation"]>;
  dytopo?: Partial<AppSettings["dytopo"]>;
  debate?: Partial<AppSettings["debate"]>;
  docuseal?: Partial<{ enabled: boolean; url: string; apiKey: string }>;
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

  searchDocuments: (query: string) =>
    fetch(`/documents/search?query=${encodeURIComponent(query)}`).then(json<SearchResult[]>),

  recentAudit: (limit = 60) => fetch(`/audit?limit=${limit}`).then(json<AuditEntry[]>),

  getCostSummary: () => fetch("/cost/summary").then(json<CostSummary>),
  getTaskCost: (id: string) => fetch(`/tasks/${id}/cost`).then(json<TaskCostResult>),
  getProfileCost: (id: string) => fetch(`/profiles/${id}/cost`).then(json<{ profileId: string; summary: CostSummary; entries: unknown[] }>),
};

/**
 * Subscribe to the global live audit stream. The server replays recent
 * entries on connect, then pushes new ones as they happen.
 */
export function streamAudit(onEntry: (entry: AuditEntry) => void): () => void {
  const es = new EventSource("/audit/stream");
  es.onmessage = (e) => {
    try { onEntry(JSON.parse(e.data) as AuditEntry); } catch { /* ignore */ }
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
    try { handlers.onSnapshot(JSON.parse((e as MessageEvent).data) as Task); } catch { /* ignore */ }
  });

  for (const evt of ["started", "phase", "round", "complete", "failed"]) {
    es.addEventListener(evt, () => handlers.onPing());
  }

  return () => es.close();
}
