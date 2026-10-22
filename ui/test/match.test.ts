// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { nearest, rank, terms } from '../src/api/match.ts';

const tasks = [
  { key: 'a', title: 'Deadlines', prompt: 'Compute every deadline under the rules', keywords: ['answer', 'due'] },
  { key: 'b', title: 'Chronology', prompt: 'Build a dated chronology of events', keywords: ['timeline'] },
];

test('terms drops stop words and plurals', () => {
  assert.deepEqual(terms('When are the Deadlines due?'), ['deadline', 'due']);
});

test('a prompt is matched to the recorded task covering most of its words', () => {
  assert.equal(nearest('when is our answer due', tasks)?.task.key, 'a');
  assert.equal(nearest('give me a timeline of events', tasks)?.task.key, 'b');
  assert.equal(nearest('when is our answer due', tasks)?.onScript, true);
});

test('matching is deterministic and ties go to the earlier task', () => {
  const r = Array.from({ length: 5 }, () => nearest('deadline timeline', tasks)?.task.key);
  assert.deepEqual(r, ['a', 'a', 'a', 'a', 'a']);
});

test('an off-script prompt says so and still names the nearest', () => {
  const m = nearest('write a poem about cats', tasks);
  assert.equal(m?.onScript, false);
  assert.equal(m?.score, 0);
  assert.equal(nearest('', tasks)?.onScript, false);
  assert.equal(nearest('anything', []), null);
});

test('search ranks rare words higher and returns only chunks that match', () => {
  const chunks = [
    { documentId: 'd', sectionId: 's0', title: 'Fees', text: 'the fee is payable and the fee is due' },
    { documentId: 'd', sectionId: 's1', title: 'Shortfall', text: 'a shortfall fee of thirty-five percent' },
    { documentId: 'd', sectionId: 's2', title: 'Other', text: 'nothing relevant here' },
  ];
  const hits = rank('shortfall fee', chunks);
  assert.deepEqual(hits.map((h) => h.sectionId), ['s1', 's0']);
  assert.equal(rank('the and of', chunks).length, 0);
});
