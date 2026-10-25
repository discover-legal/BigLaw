// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The live transport: the Go API on the same origin (routes: README § REST API).
import { ApiError, type Transport } from './transport.ts';
import type { Problem, RunEvent, Step, Run } from './types.ts';

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    let problems: Problem[] = [];
    try {
      const j = (await res.json()) as { error?: string; problems?: Problem[] };
      if (j.error) msg = j.error;
      problems = j.problems ?? [];
    } catch { /* not JSON */ }
    throw new ApiError(res.status, msg, problems);
  }
  return (await res.json()) as T;
}

const enc = encodeURIComponent;

export function createTransport(): Transport {
  return {
    mode: 'live',
    provenance: null,
    clients: () => call('GET', '/clients'),
    documents: (m) => call('GET', m ? `/documents?matter=${enc(m)}` : '/documents'),
    outline: (id) => call('GET', `/documents/${enc(id)}/outline`),
    section: (id, sid, offset = 0) => call('GET', `/documents/${enc(id)}/sections/${enc(sid)}?offset=${offset}`),
    fileUrl: (id) => `/documents/${enc(id)}/file`,
    search: (q, ids) => call('GET', `/documents/search?q=${enc(q)}&ids=${ids.map(enc).join(',')}`),
    tasks: () => call('GET', '/tasks'),
    task: (id) => call('GET', `/tasks/${enc(id)}`),
    match: () => null,
    submit: (t) => call('POST', '/tasks', t),
    cancel: (id) => call('POST', `/tasks/${enc(id)}/cancel`),
    decideGate: (id, g, approve, reason) => call('POST', `/tasks/${enc(id)}/gates/${enc(g)}/${approve ? 'approve' : 'reject'}`, approve ? undefined : { reason }),
    cost: (id) => call('GET', `/tasks/${enc(id)}/cost`),
    events(id, on) {
      const es = new EventSource(`/tasks/${enc(id)}/events`);
      es.addEventListener('step', (e) => on({ type: 'step', step: JSON.parse((e as MessageEvent).data) as Step }));
      for (const type of ['task', 'done'] as const) {
        es.addEventListener(type, (e) => {
          const ev: RunEvent = { type, run: JSON.parse((e as MessageEvent).data) as Run };
          on(ev);
          if (type === 'done') es.close();
        });
      }
      return () => es.close();
    },
    playbooks: () => call('GET', '/playbooks'),
    timeEntries: () => call('GET', '/time-entries'),
    profiles: () => call('GET', '/profiles'),
    me: () => call('GET', '/me'),
    manageItems: () => call('GET', '/manage/items'),
    manageItem: (id) => call('GET', `/manage/items/${enc(id)}`),
    manageHistory: (id) => call('GET', `/manage/items/${enc(id)}/history`),
    manageChanges: () => call('GET', '/manage/changes'),
    manageSave: (id, base, files, note) => call('POST', `/manage/items/${enc(id)}/versions`, { base, files, note }),
    manageRevert: (id, to, base, note) => call('POST', `/manage/items/${enc(id)}/revert`, { to, base, note }),
    manageDelete: (id, base, note) => call('POST', `/manage/items/${enc(id)}/delete`, { base, note }),
    manageSecrets: () => call('GET', '/manage/secrets'),
    manageSetSecret: async (name, value) => { await call('POST', '/manage/secrets', { name, value }); },
    manageDeleteSecret: async (name) => { await call('POST', `/manage/secrets/${enc(name)}/delete`); },
    manageDiscover: (id) => call('POST', `/manage/items/${enc(id)}/discover`),
    manageImport: (id, tools, offerToLead) => call('POST', `/manage/items/${enc(id)}/import`, { tools, offerToLead }),
    manageTest: (id, documentIds, input) => call('POST', `/manage/items/${enc(id)}/test`, { documentIds, input }),
    exportUrl: (format, item) => `/manage/export?format=${format}${item ? `&item=${enc(item)}` : ''}`,
  };
}
