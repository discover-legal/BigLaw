// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { follow } from '../src/api/follow.ts';
import { createTransport as http } from '../src/api/http.ts';
import type { RunEvent } from '../src/api/types.ts';

/** An EventSource the test drives as the server does (internal/api events). */
class FakeSource {
  static opened: FakeSource[] = [];
  readonly on = new Map<string, ((e: { data: string }) => void)[]>();
  closed = false;
  readonly url: string;
  constructor(url: string) { this.url = url; FakeSource.opened.push(this); }
  addEventListener(type: string, fn: (e: { data: string }) => void) { this.on.set(type, [...(this.on.get(type) ?? []), fn]); }
  close() { this.closed = true; }
  /** One stream as the server sends it: every finished step from the first, the run, and done when it stopped. */
  serve(steps: number, status: string) {
    const send = (type: string, v: unknown) => { if (!this.closed) for (const fn of this.on.get(type) ?? []) fn({ data: JSON.stringify(v) }); };
    for (let n = 1; n <= steps; n++) send('step', { step: n, tool: 'lead', status: 'committed', summary: '' });
    const run = { status, step: steps, stopReason: null, budget: { calls: steps, usd: 0, remaining: 150 - steps }, openFindings: 0 };
    send('task', run);
    if (status !== 'running' && status !== 'queued') send('done', run);
  }
}

test('the Task screen\'s stream opens again when a decided gate resumes the run', () => {
  const orig = globalThis.EventSource;
  globalThis.EventSource = FakeSource as unknown as typeof EventSource;
  FakeSource.opened = [];
  const seen: number[] = [], runs: string[] = [];
  try {
    const s = follow(http(), 't 1', (e: RunEvent) => {
      if (e.type === 'step') { if (!seen.includes(e.step.step)) seen.push(e.step.step); } else runs.push(`${e.type}:${e.run.status}`);
    });
    assert.equal(FakeSource.opened.length, 1);
    assert.equal(FakeSource.opened[0].url, '/tasks/t%201/events');
    FakeSource.opened[0].serve(4, 'running');
    s.status('running'); // the screen read the task while its stream is open: nothing to reopen
    assert.equal(FakeSource.opened.length, 1);
    FakeSource.opened[0].serve(6, 'awaiting_gate'); // the run pauses at a gate: the server ends the stream
    assert.ok(FakeSource.opened[0].closed);
    s.status('awaiting_gate');
    assert.equal(FakeSource.opened.length, 1, 'a paused run is not streamed');
    s.status('running'); // Approve or Reject: the run resumes
    assert.equal(FakeSource.opened.length, 2);
    assert.equal(FakeSource.opened[1].url, '/tasks/t%201/events');
    FakeSource.opened[1].serve(8, 'awaiting_gate'); // the steps from the first again, then the new ones
    s.status('queued'); // the next gate decided; queued is live too, as the server's stream treats it
    assert.equal(FakeSource.opened.length, 3);
    FakeSource.opened[2].serve(9, 'complete');
    s.status('complete');
    assert.deepEqual(seen, [1, 2, 3, 4, 5, 6, 7, 8, 9]);
    assert.deepEqual(runs, ['task:running', 'task:awaiting_gate', 'done:awaiting_gate', 'task:awaiting_gate', 'done:awaiting_gate', 'task:complete', 'done:complete']);
    s.close();
    s.status('running');
    assert.equal(FakeSource.opened.length, 3, 'a closed screen opens nothing');
  } finally { globalThis.EventSource = orig; }
});

test('a stream the screen closed delivers nothing more', () => {
  const orig = globalThis.EventSource;
  globalThis.EventSource = FakeSource as unknown as typeof EventSource;
  FakeSource.opened = [];
  let n = 0;
  try {
    const s = follow(http(), 't1', () => { n++; });
    s.close();
    assert.ok(FakeSource.opened[0].closed);
    assert.equal(n, 0);
  } finally { globalThis.EventSource = orig; }
});
