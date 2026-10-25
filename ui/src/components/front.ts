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

/** writeJSON is v in text's own indent (its second line's), so an edit changes only what it edits. */
export function writeJSON(text: string, v: unknown): string {
  const indent = /^\{\n( +)/.exec(text)?.[1].length ?? 1;
  return `${JSON.stringify(v, null, indent)}\n`;
}

/** isSpec: a tool's or subagent's JSON spec (the other editable files are Markdown). */
export const isSpec = (path: string) => path.endsWith('.json') && path.includes('/specs/');
