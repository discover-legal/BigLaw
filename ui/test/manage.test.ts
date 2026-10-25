// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import manageJson from '../src/recorded/manage.json' with { type: 'json' };
import { createTransport as recorded, type ManageRecording } from '../src/api/recorded.ts';
import { createTransport as http } from '../src/api/http.ts';
import { ApiError } from '../src/api/transport.ts';
import { DiffView, diffLines, hunks } from '../src/components/diff.ts';
import { isSpec, parseFront, setField, writeFront, writeJSON } from '../src/components/front.ts';

const manage = manageJson as unknown as ManageRecording;
const files = manage.items.flatMap((d) => Object.entries(d.content));

test('the line diff is a longest common subsequence, removals before additions', () => {
  assert.deepEqual(diffLines('a\nb\nc\n', 'a\nx\nc\n'), [{ op: ' ', text: 'a' }, { op: '-', text: 'b' }, { op: '+', text: 'x' }, { op: ' ', text: 'c' }]);
  assert.deepEqual(diffLines('', 'new\n'), [{ op: '+', text: 'new' }]);
  assert.deepEqual(diffLines('same', 'same'), [{ op: ' ', text: 'same' }]);
  const long = Array.from({ length: 20 }, (_, i) => `l${i}`).join('\n');
  const h = hunks(diffLines(long, long.replace('l10', 'L10')));
  assert.deepEqual(h[0], { gap: 7 });
  assert.equal(h.filter((l) => 'op' in l && l.op !== ' ').length, 2);
  assert.deepEqual(h.at(-1), { gap: 6 });
});

test('the diff view shows changed files only, marked', () => {
  const html = renderToStaticMarkup(createElement(DiffView, { files: [{ path: 'p.md', a: 'one\n', b: 'two\n' }, { path: 'q.md', a: 'x', b: 'x' }] }));
  assert.match(html, /<code>p\.md<\/code>/);
  assert.doesNotMatch(html, /q\.md/);
  assert.match(html, /class="d-del">- one/);
  assert.match(html, /class="d-add">\+ two/);
  assert.match(renderToStaticMarkup(createElement(DiffView, { files: [], empty: 'Nothing.' })), /Nothing\./);
});

test('every built-in pack and skill round-trips through the structured editor unchanged', () => {
  const fronted = files.filter(([p]) => p.endsWith('.md') && (p.includes('/packs/') || p.endsWith('/SKILL.md')));
  assert.ok(fronted.length >= 7, `${fronted.length} pack and skill files`);
  for (const [p, text] of fronted) {
    const f = parseFront(text);
    assert.ok(f, p);
    assert.equal(writeFront(f), text, p);
  }
  const us = parseFront(files.find(([p]) => p.endsWith('jurisdiction/us.md'))![1])!;
  const edited = writeFront(setField(setField(us, 'status', 'reviewed'), 'reviewedBy', 'A. Lawyer'));
  assert.match(edited, /\nstatus: reviewed\nreviewedBy: A\. Lawyer\nchecklist:\n- /);
  assert.equal(parseFront('no front matter'), null);
});

test('a spec edited by field keeps its file\'s indent and changes only what changed', () => {
  const specs = files.filter(([p]) => isSpec(p));
  assert.ok(specs.length >= 20);
  for (const [p, text] of specs) {
    const v = JSON.parse(text) as Record<string, unknown>;
    const out = writeJSON(text, { ...v, maxTurns: 7 });
    assert.equal((JSON.parse(out) as { maxTurns: number }).maxTurns, 7, p);
    assert.equal(out.split('\n')[1].match(/^ +/)?.[0], text.split('\n')[1].match(/^ +/)?.[0], p);
  }
});

test('the recorded demo shows the catalogue read-only', async () => {
  const t = recorded();
  assert.equal((await t.me()).admin, false);
  const items = await t.manageItems();
  for (const s of ['agents', 'tools', 'packs', 'skills', 'hooks']) assert.ok(items.some((i) => i.section === s), s);
  assert.ok(items.every((i) => !('content' in i)));
  const lead = await t.manageItem('prompt.lead');
  assert.deepEqual(lead.original, lead.content);
  const hook = await t.manageItem('hook.draft_section');
  assert.ok(hook.hook && hook.hook.source.every((s) => s.text.includes('func ')));
  assert.equal(t.exportUrl('patch'), null);
  assert.deepEqual(await t.manageChanges(), []);
  for (const write of [() => t.manageSave('prompt.lead', 0, {}, ''), () => t.manageRevert('prompt.lead', 0, 0, ''), () => t.manageDelete('skill.deadlines', 0, '')]) {
    await assert.rejects(write(), (e: ApiError) => e.status === 403 && /read-only/.test(e.message));
  }
  await assert.rejects(t.manageItem('nope'), /not found/);
});

test('the http transport calls Manage\'s routes and keeps a refusal\'s problems', async () => {
  const calls: string[] = [];
  const orig = globalThis.fetch;
  globalThis.fetch = (async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method} ${url}${init?.body ? ` ${init.body}` : ''}`);
    if (String(url).endsWith('/versions')) return new Response(JSON.stringify({ error: 'bad', errorType: 'invalid', problems: [{ file: 'f', message: 'm' }] }), { status: 422 });
    return new Response('[]', { status: 200 });
  }) as typeof fetch;
  try {
    const t = http();
    await t.me(); await t.manageItems(); await t.manageItem('agent.read_figures'); await t.manageHistory('pack.jurisdiction.us'); await t.manageChanges();
    await t.manageRevert('prompt.lead', 2, 3, 'n'); await t.manageDelete('skill.x', 1, '');
    await assert.rejects(t.manageSave('prompt.lead', 0, { a: 'b' }, ''), (e: ApiError) => e.status === 422 && e.problems[0].message === 'm');
  } finally { globalThis.fetch = orig; }
  assert.deepEqual(calls, ['GET /me', 'GET /manage/items', 'GET /manage/items/agent.read_figures', 'GET /manage/items/pack.jurisdiction.us/history', 'GET /manage/changes',
    'POST /manage/items/prompt.lead/revert {"to":2,"base":3,"note":"n"}', 'POST /manage/items/skill.x/delete {"base":1,"note":""}',
    'POST /manage/items/prompt.lead/versions {"base":0,"files":{"a":"b"},"note":""}']);
  assert.equal(http().exportUrl('zip', 'skill.a b'), '/manage/export?format=zip&item=skill.a%20b');
});
