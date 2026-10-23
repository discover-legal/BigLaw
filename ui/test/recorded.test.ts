// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
import assert from 'node:assert/strict';
import { test } from 'node:test';
import recordingJson from '../src/recorded/recording.json' with { type: 'json' };
import { createTransport as recorded, type Clock, type Recording } from '../src/api/recorded.ts';
import { createTransport as http } from '../src/api/http.ts';
import { rank } from '../src/api/match.ts';
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
  const required = ['clients', 'documents', 'outline', 'section', 'search', 'tasks', 'task', 'match', 'submit', 'cancel', 'decideGate', 'cost', 'events', 'playbooks', 'timeEntries', 'profiles', 'fileUrl'];
  for (const t of [http(), recorded(rec)]) for (const k of required) assert.equal(typeof (t as unknown as Record<string, unknown>)[k], 'function', k);
  assert.equal(http().mode, 'live');
  assert.equal(recorded(rec).mode, 'recorded');
  assert.match(recorded(rec).provenance ?? '', /not live/);
  assert.equal(recorded(rec).home, 'https://discover.legal/');
  assert.equal(http().home, undefined);
});

test('the http transport calls the contract routes on the same origin', async () => {
  const calls: string[] = [];
  const orig = globalThis.fetch;
  globalThis.fetch = (async (url: string, init?: RequestInit) => { calls.push(`${init?.method} ${url}${init?.method === 'POST' && init.body && !String(url).endsWith('/tasks') ? ` ${init.body}` : ''}`); return new Response('[]', { status: 200 }); }) as typeof fetch;
  try {
    const t = http();
    await t.tasks(); await t.search('fee cap', ['a', 'b']); await t.section('d1', 's3'); await t.cost('t1');
    await t.decideGate('t1', 'g1', true); await t.decideGate('t1', 'g2', false, 'cite the order'); await t.submit(newTask('x'));
  } finally { globalThis.fetch = orig; }
  assert.deepEqual(calls, ['GET /tasks', 'GET /documents/search?q=fee%20cap&ids=a,b', 'GET /documents/d1/sections/s3?offset=0',
    'GET /tasks/t1/cost', 'POST /tasks/t1/gates/g1/approve', 'POST /tasks/t1/gates/g2/reject {"reason":"cite the order"}', 'POST /tasks']);
  assert.equal(http().fileUrl('d 1'), '/documents/d%201/file');
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
  assert.equal((await t.decideGate(task.id, done.pendingGates[0].gateId, true)).status, 'complete');
  await assert.rejects(t.decideGate(task.id, done.pendingGates[0].gateId, true), /not open/);
  assert.equal((await t.cost(task.id)).summary.calls, 0);
});

test('rejecting the deliverable needs a reason, closes the gate and ends the replay as rejected', async () => {
  const { clock, advance } = fakeClock();
  const t = recorded(rec, clock);
  const task = await t.submit(newTask('When is our answer due?'));
  advance(1e7);
  const gate = (await t.task(task.id)).pendingGates[0];
  await assert.rejects(t.decideGate(task.id, gate.gateId, false, '  '), /Say why/);
  const r = await t.decideGate(task.id, gate.gateId, false, 'Add the Rule 26(f) deadline.');
  assert.equal(r.status, 'rejected');
  assert.equal(r.run.stopReason, 'finished'); // the run finished; the rejection is the gate's decision
  assert.deepEqual(r.pendingGates, []);
  assert.equal(r.decidedGates[0].decision, 'rejected');
  assert.equal(r.decidedGates[0].reason, 'Add the Rule 26(f) deadline.');
  assert.match(r.output ?? '', /## Evidence Note/);
  await assert.rejects(t.decideGate(task.id, gate.gateId, true), /not open/);
});

test('the recording shows every rung it can: exact, tolerant, @transcript and an excluded quote', () => {
  const methods = new Set(rec.tasks.flatMap((t) => t.findings.flatMap((f) => f.citations.map((c) => c.method))));
  for (const m of ['exact', 'tolerant', 'exact@transcript', 'tolerant@transcript', 'none']) assert.ok(methods.has(m as never), m);
  const lit1 = rec.tasks.find((x) => x.key === 'lit-deadlines')!;
  const q = lit1.findings.filter((f) => f.status === 'quarantined');
  assert.equal(q.length, 1);
  assert.doesNotMatch(lit1.output, new RegExp(`\\[${q[0].fid}\\]`));
  assert.match(lit1.output, new RegExp(`1 finding quarantined: ${q[0].fid}`));
  for (const c of rec.tasks.flatMap((t) => t.findings.flatMap((f) => f.citations)).filter((c) => c.method.endsWith('@transcript'))) {
    assert.equal(c.confidence, 0.85);
    assert.match(c.note ?? '', /scripted/);
    assert.ok(recorded(rec).fileUrl(c.documentId));
  }
});

test('recorded searches replay v2\'s hits; other queries rank like the server at the top', async () => {
  const t = recorded(rec);
  for (const s of rec.searches) {
    const ids = rec.clients.flatMap((c) => c.matters).find((m) => m.number === s.matterNumber)!.documentIds;
    assert.deepEqual(await t.search(s.query, ids), s.hits);
    const chunks = ids.flatMap((id) => rec.documents.find((d) => d.id === id)!.chunkList.map((c) => ({ documentId: id, ...c })));
    const top = rank(s.query, chunks)[0];
    assert.deepEqual([top.documentId, top.sectionId], [s.hits[0].documentId, s.hits[0].sectionId], s.query);
  }
});

test('the other screens have sample records', async () => {
  const t = recorded(rec);
  assert.ok((await t.playbooks()).length > 0);
  assert.ok((await t.profiles()).length > 0);
  assert.ok((await t.settings!()).some((s) => s.name === 'BIGLAW_BUDGET_CALLS' && s.default === '400'));
  const matters = new Set(rec.clients.flatMap((c) => c.matters.map((m) => m.number)));
  for (const e of await t.timeEntries()) assert.ok(matters.has(e.matterNumber), e.id);
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

test('visitors never see the scripted reader\'s Go name', async () => {
  const t = recorded(rec);
  const docs = await t.documents();
  const notes = docs.flatMap((d) => [...d.notes, d.transcript?.model ?? '']);
  const task = await t.submit(newTask(''), 'lit-deadlines');
  t.fastForward!(task.id);
  const cites = (await t.task(task.id)).findings.flatMap((f) => f.citations.map((c) => c.note ?? ''));
  for (const s of [...notes, ...cites]) assert.doesNotMatch(s, /model\.Fake|\.go\b/, s);
  assert.ok(notes.some((n) => n.includes('a scripted stand-in for the vision reader (demo only; ')), notes.join(' / '));
});
