// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
import assert from 'node:assert/strict';
import { mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
// @ts-expect-error: a plain .mjs script, no types
import { NETWORK, scan } from '../scripts/offline-check.mjs';

const root = fileURLToPath(new URL('..', import.meta.url));

async function buildTo(engine: string) {
  const out = mkdtempSync(join(tmpdir(), 'biglaw-ui-'));
  process.env.VITE_ENGINE = engine;
  const { build } = await import('vite');
  await build({ root, configFile: join(root, 'vite.config.ts'), logLevel: 'silent', build: { outDir: out, emptyOutDir: true } });
  delete process.env.VITE_ENGINE;
  return out;
}

test('the recorded bundle has no fetch, XHR, EventSource or WebSocket', async () => {
  const out = await buildTo('recorded');
  try { assert.deepEqual(scan(out), []); } finally { rmSync(out, { recursive: true, force: true }); }
});

test('the check catches the live bundle, which does use the network', async () => {
  const out = await buildTo('live');
  try { assert.ok(scan(out).length > 0); } finally { rmSync(out, { recursive: true, force: true }); }
});

test('the check allows only the two displayed strings, exactly', () => {
  for (const ok of ['`https://discover.legal/`', '"http://127.0.0.1:3101"']) assert.doesNotMatch(ok, NETWORK, ok);
  for (const bad of ['"https://discover.legal/api"', '"https://discover.legal.evil/"', '"http://127.0.0.1:3101/tasks"', 'fetch(x)']) assert.match(bad, NETWORK, bad);
});

test('the way back to discover.legal is in the recorded bundle only', async () => {
  const live = await buildTo('live');
  const recorded = await buildTo('recorded');
  const text = (dir: string) => readdirSync(join(dir, 'assets')).map((f) => readFileSync(join(dir, 'assets', f), 'latin1')).join('');
  try {
    assert.doesNotMatch(text(live), /https:\/\/discover\.legal/);
    assert.match(text(recorded), /https:\/\/discover\.legal\//);
  } finally { for (const d of [live, recorded]) rmSync(d, { recursive: true, force: true }); }
});
