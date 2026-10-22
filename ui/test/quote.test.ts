// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
import assert from 'node:assert/strict';
import { test } from 'node:test';
import recordingJson from '../src/recorded/recording.json' with { type: 'json' };
import type { Recording } from '../src/api/recorded.ts';
import { plainText, quoteCells } from '../src/components/quote.ts';

test('emphasis markers are dropped for display', () => {
  assert.equal(plainText('**Date:** September 25, 2026'), 'Date: September 25, 2026');
  assert.equal(plainText('__Re:__ notice'), 'Re: notice');
  assert.equal(plainText('Section 9.2* applies'), 'Section 9.2* applies');
});

test('a quoted table row shows as its cells', () => {
  assert.deepEqual(quoteCells('| Veterans Day | Wednesday, November 11, 2026 |'), ['Veterans Day', 'Wednesday, November 11, 2026']);
  assert.deepEqual(quoteCells('served on 10/21/2026 via its registered agent'), ['served on 10/21/2026 via its registered agent']);
});

test('no recorded quote shows markup', () => {
  const rec = recordingJson as unknown as Recording;
  for (const c of rec.tasks.flatMap((t) => t.findings.flatMap((f) => f.citations))) {
    for (const cell of quoteCells(c.quote)) assert.doesNotMatch(cell, /\*\*|__|\|/, c.quote);
  }
});
