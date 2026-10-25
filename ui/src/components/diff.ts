// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// A line diff (longest common subsequence) for Manage's history and unsaved edits, and its view.
// Plain createElement so node:test renders it. The server's export patch is Go's (engine/diff.go).
import { createElement as h } from 'react';

export interface DiffLine { op: ' ' | '-' | '+'; text: string }

const MAX_CELLS = 4_000_000; // past it: the whole of a replaced by the whole of b

export function diffLines(a: string, b: string): DiffLine[] {
  const x = a === '' ? [] : a.replace(/\n$/, '').split('\n'), y = b === '' ? [] : b.replace(/\n$/, '').split('\n');
  const n = x.length, m = y.length;
  if (n * m > MAX_CELLS) return [...x.map((text) => ({ op: '-' as const, text })), ...y.map((text) => ({ op: '+' as const, text }))];
  const t = Array.from({ length: n + 1 }, () => new Int32Array(m + 1)); // t[i][j]: the LCS of x[i:] and y[j:]
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) t[i][j] = x[i] === y[j] ? t[i + 1][j + 1] + 1 : Math.max(t[i + 1][j], t[i][j + 1]);
  const out: DiffLine[] = [];
  let i = 0, j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && x[i] === y[j]) out.push({ op: ' ', text: x[i++] }), j++;
    else if (i < n && (j === m || t[i + 1][j] >= t[i][j + 1])) out.push({ op: '-', text: x[i++] });
    else out.push({ op: '+', text: y[j++] });
  }
  return out;
}

/** hunks keeps changed lines with `context` lines around them; a longer unchanged run is one gap line. */
export function hunks(lines: DiffLine[], context = 3): (DiffLine | { gap: number })[] {
  const near = lines.map((_, k) => lines.slice(Math.max(0, k - context), k + context + 1).some((l) => l.op !== ' '));
  const out: (DiffLine | { gap: number })[] = [];
  for (let k = 0; k < lines.length; k++) {
    if (near[k]) { out.push(lines[k]); continue; }
    const last = out.at(-1);
    if (last && 'gap' in last) last.gap++; else out.push({ gap: 1 });
  }
  return out;
}

export interface FilePair { path: string; a: string; b: string }

/** DiffView shows each changed file's hunks; files that are the same are left out. */
export function DiffView(props: { files: FilePair[]; empty?: string }) {
  const changed = props.files.filter((f) => f.a !== f.b);
  if (changed.length === 0) return h('p', { className: 'muted small' }, props.empty ?? 'No differences.');
  return h('div', { className: 'diffs' }, changed.map((f) => h('figure', { key: f.path, className: 'diff' },
    h('figcaption', null, h('code', null, f.path), f.a === '' ? ' (new)' : f.b === '' ? ' (deleted)' : ''),
    h('pre', null, hunks(diffLines(f.a, f.b)).map((l, k) => ('gap' in l
      ? h('span', { key: k, className: 'd-gap' }, `… ${l.gap} unchanged ${l.gap === 1 ? 'line' : 'lines'}\n`)
      : h('span', { key: k, className: l.op === '+' ? 'd-add' : l.op === '-' ? 'd-del' : undefined }, `${l.op} ${l.text}\n`)))))));
}
