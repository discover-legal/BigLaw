// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
import assert from 'node:assert/strict';
import { test } from 'node:test';
import recordingJson from '../src/recorded/recording.json' with { type: 'json' };
import { createTransport as recorded, type Clock, type Recording } from '../src/api/recorded.ts';
import { createTransport as http } from '../src/api/http.ts';
import type { RunEvent } from '../src/api/types.ts';

const rec = recordingJson as unknown as Recording;

function fakeClock() {
  let t = 1_000_000;
  const fns = new Set<() => void>();
  const clock: Clock = { now: () => t, every: (_ms, fn) => { fns.add(fn); return () => fns.delete(fn); } };
  return { clock, advance(ms: number) { t += ms; for (const f of [...fns]) f(); } };
}

const lit = rec.clients.flatMap((c) => c.matters).find((m) => m.number === 'DEMO-LIT-1')!;
const newTask = (description: string) => ({ description, workflowType: 'memo' as const, jurisdiction: 'US', matterNumber: lit.number, documentIds: lit.documentIds });

test('both transports implement the same seam', () => {
  const required = ['clients', 'documents', 'outline', 'section', 'search', 'tasks', 'task', 'match', 'submit', 'cancel', 'decideGate', 'cost', 'events', 'playbooks', 'timeEntries', 'profiles'];
  for (const t of [http(), recorded(rec)]) for (const k of required) assert.equal(typeof (t as unknown as Record<string, unknown>)[k], 'function', k);
  assert.equal(http().mode, 'live');
  assert.equal(recorded(rec).mode, 'recorded');
  assert.match(recorded(rec).provenance ?? '', /not live/);
});

test('the http transport calls the contract routes on the same origin', async () => {
  const calls: string[] = [];
  const orig = globalThis.fetch;
  globalThis.fetch = (async (url: string, init?: RequestInit) => { calls.push(`${init?.method} ${url}`); return new Response('[]', { status: 200 }); }) as typeof fetch;
  try {
    const t = http();
    await t.tasks(); await t.search('fee cap', ['a', 'b']); await t.section('d1', 's3'); await t.cost('t1');
    await t.decideGate('t1', 'g1', true); await t.submit(newTask('x'));
  } finally { globalThis.fetch = orig; }
  assert.deepEqual(calls, ['GET /tasks', 'GET /documents/search?q=fee%20cap&ids=a,b', 'GET /documents/d1/sections/s3?offset=0',
    'GET /tasks/t1/cost', 'POST /tasks/t1/gates/g1/approve', 'POST /tasks']);
});

test('a recorded run replays on the clock, then waits at the deliverable gate', async () => {
  const { clock, advance } = fakeClock();
  const t = recorded(rec, clock);
  const m = t.match('When is our answer due?', lit.number);
  assert.equal(m?.key, 'lit-deadlines');
  const task = await t.submit(newTask('When is our answer due?'), m!.key);
  assert.equal(task.status, 'running');
  assert.equal(task.findings.length, 0);
  assert.equal(task.output, null);
  const events: RunEvent[] = [];
  t.events(task.id, (e) => events.push(e));
  const r = rec.tasks.find((x) => x.key === 'lit-deadlines')!;
  advance(r.steps[2].at - (clock.now() - 1_000_000));
  assert.equal(events.filter((e) => e.type === 'step').length, 3);
  advance(r.durationMs);
  const done = await t.task(task.id);
  assert.equal(done.status, 'awaiting_gate');
  assert.equal(done.run.stopReason, 'finished');
  assert.equal(done.findings.length, r.findings.length);
  assert.match(done.output ?? '', /## Evidence Note/);
  assert.deepEqual(events.filter((e) => e.type === 'step').map((e) => (e as { step: { step: number } }).step.step), r.steps.map((s) => s.step));
  assert.equal(events.at(-1)?.type, 'done');
  await assert.rejects(t.decideGate(task.id, done.pendingGates[0].gateId, false), /no rejected branch/);
  assert.equal((await t.decideGate(task.id, done.pendingGates[0].gateId, true)).status, 'complete');
  assert.equal((await t.cost(task.id)).summary.calls, 0);
});

test('an off-script prompt is refused, not answered', async () => {
  const t = recorded(rec, fakeClock().clock);
  assert.equal(t.match('write me a sonnet about lighthouses', lit.number)?.onScript, false);
  await assert.rejects(t.submit(newTask('write me a sonnet about lighthouses')), /No recorded run answers that prompt/);
});

test('cancel stops the replay where it is', async () => {
  const { clock, advance } = fakeClock();
  const t = recorded(rec, clock);
  const task = await t.submit(newTask('chronology of events'));
  advance(3000);
  const c = await t.cancel(task.id);
  advance(1e7);
  const after = await t.task(task.id);
  assert.equal(after.status, 'cancelled');
  assert.equal(after.run.step, c.run.step);
  assert.equal(after.output, null);
});

test('every verified citation highlights inside its section, and the memo cites only verified findings', () => {
  let cites = 0;
  for (const task of rec.tasks) {
    const ok = new Set(task.findings.filter((f) => f.status === 'verified').map((f) => f.fid));
    for (const [, fid] of task.output.matchAll(/\[(F\d+)\]/g)) assert.ok(ok.has(fid), `${task.key} cites ${fid}`);
    for (const f of task.findings) for (const c of f.citations) {
      if (c.method === 'none') { assert.equal(c.match, ''); continue; }
      const doc = rec.documents.find((d) => d.id === c.documentId)!;
      assert.ok(doc.sectionText[c.sectionId].includes(c.match), `${task.key} ${f.fid}: match not in ${c.sectionId}`);
      cites++;
    }
  }
  assert.ok(cites > 50);
});

test('recorded search runs over the bundled chunks', async () => {
  const hits = await recorded(rec).search('shortfall fee thirty-five percent', lit.documentIds);
  assert.ok(hits.length > 0);
  assert.ok(hits.every((h) => lit.documentIds.includes(h.documentId)));
  assert.match(hits[0].text, /shortfall/i);
});
