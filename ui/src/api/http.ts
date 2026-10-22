// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The live transport: the Go API on the same origin (routes: README § REST API).
import { ApiError, type Transport } from './transport.ts';
import type { RunEvent, Step, Run } from './types.ts';

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const j = (await res.json()) as { error?: string };
      if (j.error) msg = j.error;
    } catch { /* not JSON */ }
    throw new ApiError(res.status, msg);
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
    search: (q, ids) => call('GET', `/documents/search?q=${enc(q)}&ids=${ids.map(enc).join(',')}`),
    tasks: () => call('GET', '/tasks'),
    task: (id) => call('GET', `/tasks/${enc(id)}`),
    match: () => null,
    submit: (t) => call('POST', '/tasks', t),
    cancel: (id) => call('POST', `/tasks/${enc(id)}/cancel`),
    decideGate: (id, g, approve) => call('POST', `/tasks/${enc(id)}/gates/${enc(g)}/${approve ? 'approve' : 'reject'}`),
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
  };
}
