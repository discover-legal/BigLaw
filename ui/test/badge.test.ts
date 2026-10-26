// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { Badge, badgeFor } from '../src/components/badge.ts';

const html = (method: string, confidence?: number) => renderToStaticMarkup(createElement(Badge, { method, confidence }));

test('each ladder method renders its badge', () => {
  assert.match(html('exact', 1), /class="badge badge-ok"[^>]*>exact</);
  assert.match(html('tolerant', 0.95), /badge-near[^>]*>tolerant</);
  assert.match(html('exact@transcript', 0.85), /badge-warn[^>]*>exact @transcript</);
  assert.match(html('tolerant@transcript', 0.85), /badge-warn[^>]*>tolerant @transcript</);
  assert.match(html('judge', 0.8), /badge-warn[^>]*>judge</);
  assert.match(html('none', 0), /badge-bad[^>]*>excluded</);
});

test('a transcript badge says to check the original', () => {
  assert.match(badgeFor('exact@transcript', 0.85).title, /check against the original/);
});

test('an unknown method is never shown as verified', () => {
  assert.equal(badgeFor('made-up').label, 'excluded');
  assert.equal(badgeFor('').tone, 'bad');
});
