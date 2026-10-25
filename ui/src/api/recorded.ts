// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The recorded engine: replays recorded runs in the browser (README § UI, recorded demo). It makes
// no network request of any kind; the recording is bundled. Provenance: recording.json's header.
import recordingJson from '../recorded/recording.json' with { type: 'json' };
import manageJson from '../recorded/manage.json' with { type: 'json' };
import { nearest, rank } from './match.ts';
import { ApiError, type Transport } from './transport.ts';
import type {
  Client, Doc, Finding, GateDecision, Hit, ManageDetail, OutlineEntry, Playbook, Profile, RecordedSearch, Run, RunEvent, Setting, Step, Task, TaskCost, TimeEntry,
} from './types.ts';

/** The site the demo is published on (discover.legal/demo/biglaw/): the banner's way back. A link, not a request. */
const HOME = 'https://discover.legal/';
/** The one original file the recording bundles: the scanned exhibit, to check its transcript against. */
const SCAN = new URL('../recorded/06-affidavit-of-service-scan.jpg', import.meta.url).href;

export interface RecStep extends Step { at: number }
export interface RecFinding extends Finding { atStep: number }
export interface RecTask {
  key: string; matterNumber: string; title: string; prompt: string; keywords: string[];
  workflowType: string; jurisdiction: string; documentIds: string[];
  steps: RecStep[]; findings: RecFinding[]; output: string; durationMs: number;
}
export interface RecDoc extends Doc {
  outline: OutlineEntry[];
  sectionText: Record<string, string>;
  chunkList: { sectionId: string; title: string; text: string; search?: string; context?: string; transcribed?: boolean }[];
}
export interface Recording {
  provenance: string; clients: Client[]; documents: RecDoc[]; tasks: RecTask[]; searches: RecordedSearch[];
  profiles: Profile[]; playbooks: Playbook[]; timeEntries: TimeEntry[]; settings: Setting[];
}

export interface Clock {
  now(): number;
  every(ms: number, fn: () => void): () => void;
}
const realClock: Clock = {
  now: () => Date.now(),
  every(ms, fn) { const h = setInterval(fn, ms); return () => clearInterval(h); },
};

interface Live { id: string; rec: RecTask; description: string; started: number; createdAt: string; cut?: number; decision?: GateDecision }

/** The recording names its scripted vision reader by its Go name; visitors read plain words. */
const READER = 'a scripted stand-in for the vision reader';
export const plain = (s: string) => s.replace('by model.Fake (scripted) (', `by ${READER} (demo only; `)
  .replace('(model.Fake (scripted))', `(${READER}, demo only)`).replaceAll('model.Fake (scripted)', `${READER} (demo only)`);

const BUDGET_CALLS = 150; // BIGLAW_BUDGET_CALLS' default; the recording spends none

/** The Manage screen's recording (eval/demo/manage_test.go): this build's built-in catalogue, read-only. */
export interface ManageRecording { provenance: string; items: ManageDetail[] }
const READ_ONLY = 'The demo is read-only: it shows the built-in catalogue, and nothing can be saved.';

export function createTransport(rec: Recording = recordingJson as unknown as Recording, clock: Clock = realClock,
  manage: ManageRecording = manageJson as unknown as ManageRecording): Transport {
  const items = manage.items.map((d) => ({ ...d, original: d.original ?? d.content })); // a built-in's original is its content
  const refuse = async (): Promise<never> => { throw new ApiError(403, READ_ONLY); };
  const runs = new Map<string, Live>();
  let seq = 0;
  const docById = new Map(rec.documents.map((d) => [d.id, d]));
  const notFound = (what: string) => new ApiError(404, `${what} not found`);

  const done = (l: Live) => (l.cut ?? l.rec.steps.filter((s) => s.at <= clock.now() - l.started).length);
  const finished = (l: Live) => l.cut === undefined && done(l) >= l.rec.steps.length;
  function runOf(l: Live): Run {
    const n = done(l);
    const status = l.cut !== undefined && n < l.rec.steps.length ? 'cancelled'
      : !finished(l) ? 'running' : l.decision?.decision === 'rejected' ? 'rejected' : l.decision ? 'complete' : 'awaiting_gate';
    return {
      status, step: n,
      stopReason: status === 'cancelled' ? 'cancelled' : finished(l) ? 'finished' : null,
      budget: { calls: 0, usd: 0, remaining: BUDGET_CALLS },
      openFindings: 0,
    };
  }
  function snapshot(l: Live): Task {
    const run = runOf(l);
    const strip = ({ atStep: _a, ...f }: RecFinding): Finding => ({ ...f, citations: f.citations.map((c) => (c.note ? { ...c, note: plain(c.note) } : c)) });
    return {
      id: l.id, description: l.description, workflowType: l.rec.workflowType, jurisdiction: l.rec.jurisdiction,
      matterNumber: l.rec.matterNumber, documentIds: l.rec.documentIds, status: run.status, createdAt: l.createdAt, run,
      pendingGates: run.status === 'awaiting_gate' ? [{ gateId: `${l.id}-deliverable`, kind: 'deliverable' }] : [],
      decidedGates: l.decision ? [l.decision] : [],
      findings: l.rec.findings.filter((f) => f.atStep <= run.step).map(strip),
      output: finished(l) ? l.rec.output : null,
      groundingAlert: null,
      replay: { key: l.rec.key, title: l.rec.title, prompt: l.rec.prompt },
    };
  }
  const get = (id: string) => { const l = runs.get(id); if (!l) throw notFound('task'); return l; };
  const stepOf = ({ at: _a, ...s }: RecStep): Step => s;

  return {
    mode: 'recorded',
    provenance: rec.provenance,
    home: HOME,
    clients: async () => rec.clients,
    documents: async (m) => rec.documents
      .filter((d) => !m || d.matterNumber === m)
      .map(({ outline: _o, sectionText: _s, chunkList: _c, ...d }) => ({
        ...d, notes: d.notes.map(plain), ...(d.transcript ? { transcript: { ...d.transcript, model: plain(d.transcript.model) } } : {}),
      })),
    async outline(id) { const d = docById.get(id); if (!d) throw notFound('document'); return d.outline; },
    async section(id, sid) {
      const d = docById.get(id), o = d?.outline.find((x) => x.sectionId === sid);
      if (!d || !o) throw notFound('section');
      const text = d.sectionText[sid] ?? '';
      return { sectionId: sid, number: o.number, title: o.title, text, offset: 0, total: [...text].length, truncated: false };
    },
    fileUrl: (id) => (docById.get(id)?.contentType.startsWith('image/') ? SCAN : null),
    async search(q, ids) {
      const scope = new Set(ids);
      const replay = rec.searches.find((s) => s.query === q.trim() && s.hits.every((h) => scope.has(h.documentId)));
      if (replay) return replay.hits;
      const chunks = ids.flatMap((id) => (docById.get(id)?.chunkList ?? []).map((c) => ({ documentId: id, ...c })));
      return rank(q, chunks).map(({ search: _s, ...h }) => h as Hit);
    },
    tasks: async () => [...runs.values()].reverse().map(snapshot),
    task: async (id) => snapshot(get(id)),
    match(description, matter) {
      const m = nearest(description, rec.tasks.filter((t) => t.matterNumber === matter));
      return m && { key: m.task.key, title: m.task.title, prompt: m.task.prompt, score: m.score, onScript: m.onScript, jurisdiction: m.task.jurisdiction };
    },
    async submit(t, replayKey) {
      let r = rec.tasks.find((x) => x.key === replayKey && x.matterNumber === t.matterNumber);
      if (!r) {
        const m = nearest(t.description, rec.tasks.filter((x) => x.matterNumber === t.matterNumber));
        if (!m) throw new ApiError(404, 'this matter has no recorded runs');
        if (!m.onScript) throw new ApiError(422, `No recorded run answers that prompt. This demo only replays recorded runs; the nearest is "${m.task.title}".`);
        r = m.task;
      }
      const l: Live = { id: `rec-${++seq}-${r.key}`, rec: r, description: t.description.trim() || r.prompt, started: clock.now(), createdAt: new Date(clock.now()).toISOString() };
      runs.set(l.id, l);
      return snapshot(l);
    },
    async cancel(id) { const l = get(id); if (!finished(l) && l.cut === undefined) l.cut = done(l); return snapshot(l); },
    async decideGate(id, gateId, approve, reason) {
      const l = get(id);
      if (snapshot(l).pendingGates[0]?.gateId !== gateId) throw new ApiError(409, 'That gate is not open.');
      const why = reason?.trim() ?? '';
      if (!approve && !why) throw new ApiError(400, 'Say why you reject the deliverable: the reason is what the lead is asked to fix.');
      l.decision = { gateId, kind: 'deliverable', decision: approve ? 'approved' : 'rejected', ...(approve ? {} : { reason: why }), decidedAt: new Date(clock.now()).toISOString() };
      return snapshot(l);
    },
    async cost(id): Promise<TaskCost> {
      get(id);
      return { taskId: id, summary: { calls: 0, inputTokens: 0, outputTokens: 0, usd: 0, unpricedCalls: 0 }, entries: [] };
    },
    events(id, on) {
      const l = runs.get(id);
      if (!l) return () => {}; // task(id) reports the 404
      let sent = 0, stop = () => {};
      const tick = () => {
        const n = done(l);
        for (; sent < n; sent++) on({ type: 'step', step: stepOf(l.rec.steps[sent]) });
        const run = runOf(l);
        if (run.status !== 'running') { on({ type: 'done', run }); stop(); } else on({ type: 'task', run } as RunEvent);
      };
      stop = clock.every(200, tick);
      tick();
      return () => stop();
    },
    suggestions: (matter) => rec.tasks.filter((t) => t.matterNumber === matter)
      .map((t) => ({ key: t.key, title: t.title, prompt: t.prompt, score: 1, onScript: true, jurisdiction: t.jurisdiction })),
    recordedSearches: (matter) => rec.searches.filter((s) => s.matterNumber === matter),
    fastForward(id) { const l = get(id); if (l.cut === undefined) l.started = clock.now() - l.rec.durationMs; },
    playbooks: async () => rec.playbooks,
    timeEntries: async () => rec.timeEntries,
    profiles: async () => rec.profiles,
    settings: async () => rec.settings,
    me: async () => ({ id: 'demo', name: 'Demo visitor', admin: false, authEnabled: false }),
    manageItems: async () => items.map(({ content: _c, original: _o, hook: _h, callers: _k, ...i }) => i),
    async manageItem(id) { const d = items.find((x) => x.id === id); if (!d) throw notFound('item'); return d; },
    manageHistory: async () => [],
    manageChanges: async () => [],
    manageSave: refuse, manageRevert: refuse, manageDelete: refuse,
    manageSecrets: async () => [],
    manageSetSecret: refuse, manageDeleteSecret: refuse, manageDiscover: refuse, manageImport: refuse, manageTest: refuse,
    exportUrl: () => null,
  };
}
