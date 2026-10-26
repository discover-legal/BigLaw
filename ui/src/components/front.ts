// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Manage's structured editors read and write two file shapes: a pack's or skill's Markdown with its
// front matter (key: value lines, a key with no value then "- item" lines as a list), the Go parser's
// (engine/specset.go frontMatter) rules; and a spec's JSON, written back in its file's own indent.

export interface Front { keys: string[]; fields: Record<string, string>; lists: Record<string, string[]>; body: string }

export function parseFront(text: string): Front | null {
  const m = /^---\n([\s\S]*?)\n---\n([\s\S]*)$/.exec(text);
  if (!m) return null;
  const f: Front = { keys: [], fields: {}, lists: {}, body: m[2].trim() };
  let list = '';
  for (const ln of m[1].split('\n')) {
    if (ln.startsWith('- ') && list) { f.lists[list].push(ln.slice(2).trim()); continue; }
    const i = ln.indexOf(':');
    if (i <= 0) return null;
    const k = ln.slice(0, i).trim(), v = ln.slice(i + 1).trim();
    f.keys.push(k);
    f.fields[k] = v;
    list = v === '' ? k : '';
    if (list) f.lists[k] = [];
  }
  return f;
}

export function writeFront(f: Front): string {
  const lines = f.keys.map((k) => (f.lists[k]?.length ? `${k}:\n${f.lists[k].map((x) => `- ${x}`).join('\n')}` : `${k}:${f.fields[k] ? ` ${f.fields[k]}` : ''}`));
  return `---\n${lines.join('\n')}\n---\n${f.body}\n`;
}

/** setField sets a key (appending it when the file lacks it); a list replaces the key's items. */
export function setField(f: Front, k: string, v: string | string[]): Front {
  const keys = f.keys.includes(k) ? f.keys : [...f.keys, k];
  return Array.isArray(v)
    ? { ...f, keys, fields: { ...f.fields, [k]: '' }, lists: { ...f.lists, [k]: v.map((x) => x.trim()).filter(Boolean) } }
    : { ...f, keys, fields: { ...f.fields, [k]: v.replace(/\s+/g, ' ').trim() }, lists: { ...f.lists, [k]: [] } };
}

/** A part of the file a field edits: a front-matter value, a list (one item a line) or the body. */
export type Part = 'field' | 'list' | 'body';
const stored = (f: Front, part: Part, k: string) => (part === 'body' ? f.body : part === 'list' ? (f.lists[k] ?? []).join('\n') : f.fields[k] ?? '');
const put = (f: Front, part: Part, k: string, v: string) => (part === 'body' ? { ...f, body: v.trim() } : setField(f, k, part === 'list' ? v.split('\n') : v));

/**
 * typing is what each field shows and the file a keystroke writes. The file is written trimmed, as it
 * is saved (setField, the body trimmed); a field shows what was typed while that still writes as the
 * file's value, so a trailing space or a new empty line survives the keystroke, else the file's value.
 */
export function typing(text: string, typed: Record<string, string>) {
  const f = parseFront(text) as Front;
  return {
    f,
    value(part: Part, k = '') {
      const t = typed[`${part}:${k}`], now = stored(f, part, k);
      return t !== undefined && stored(put(f, part, k, t), part, k) === now ? t : now;
    },
    write: (part: Part, k: string, v: string) => ({ typed: { ...typed, [`${part}:${k}`]: v }, text: writeFront(put(f, part, k, v)) }),
  };
}

/** writeJSON is v in text's own indent (its second line's), so an edit changes only what it edits. */
export function writeJSON(text: string, v: unknown): string {
  const indent = /^\{\n( +)/.exec(text)?.[1].length ?? 1;
  return `${JSON.stringify(v, null, indent)}\n`;
}

/** isSpec: a tool's or subagent's JSON spec (the other editable files are Markdown). */
export const isSpec = (path: string) => path.endsWith('.json') && path.includes('/specs/');
