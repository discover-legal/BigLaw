// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Deterministic text helpers for the recorded engine: prompt → nearest recorded task, and a small
// lexical search over the recorded chunks. Pure functions, no model and no network.

const STOP = new Set(('a an and are as at be by can do does for from has have how i in is it its me my of on or our ' +
  'please the their them this to under was we what when where which who why will with you your all any every each ' +
  'give list make prepare tell show find out about into than that these those').split(' '));

/** Lower-cased content words, a trailing plural "s" dropped (deadlines → deadline). */
export function terms(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.toLowerCase().split(/[^\p{L}\p{N}]+/u)) {
    if (!raw || STOP.has(raw)) continue;
    out.push(raw.length > 3 && raw.endsWith('s') && !raw.endsWith('ss') ? raw.slice(0, -1) : raw);
  }
  return out;
}

export interface Scriptable {
  key: string;
  title: string;
  prompt: string;
  keywords: string[];
}

/** The share of the visitor's words a recorded task covers must reach this to count as on script. */
export const ON_SCRIPT = 0.3;

/**
 * nearest picks the recorded task whose prompt, title and keywords cover the largest share of the
 * prompt's content words (ties: more words covered, then the earlier task). Deterministic.
 */
export function nearest<T extends Scriptable>(prompt: string, tasks: T[]): { task: T; score: number; onScript: boolean } | null {
  if (tasks.length === 0) return null;
  const words = [...new Set(terms(prompt))];
  let best = tasks[0], bestScore = -1, bestHits = -1;
  for (const t of tasks) {
    const vocab = new Set(terms([t.prompt, t.title, ...t.keywords].join(' ')));
    const hits = words.filter((w) => vocab.has(w)).length;
    const score = words.length ? hits / words.length : 0;
    if (score > bestScore || (score === bestScore && hits > bestHits)) {
      best = t; bestScore = score; bestHits = hits;
    }
  }
  return { task: best, score: Math.round(bestScore * 100) / 100, onScript: bestHits > 0 && bestScore >= ON_SCRIPT };
}

export interface Searchable {
  documentId: string;
  sectionId: string;
  title: string;
  text: string;
  search?: string; // what the server indexes instead of text (a table row with its headers)
}

/** Lower-cased words as written (Postgres' "simple" configuration: no stemming, stop words kept). */
export function words(text: string): string[] {
  return text.toLowerCase().split(/[^\p{L}\p{N}]+/u).filter(Boolean);
}

/**
 * rank ports the server's lexical ranking (internal/docs searchSQL) to the browser: every distinct
 * query word is a term (OR, not AND) in two legs, stemmed without stop words (here: terms(), a plural
 * "s" dropped, where the server runs the English stemmer) and exact (words()); a chunk scores, per
 * term it contains, idf × tf/(tf+10) (ts_rank_cd of one term with normalisation 32), idf =
 * ln(1 + N/(1+df)) over the chunks searched; a chunk is its section path plus its indexed text. The
 * stemmer differs, so the order can differ from the server's. Ties keep chunk order; at most k hits.
 */
export function rank<T extends Searchable>(query: string, chunks: T[], k = 8): (T & { rank: number })[] {
  const legs = [terms, words].map((split) => {
    const q = [...new Set(split(query))].slice(0, 32);
    const bags = chunks.map((c) => {
      const m = new Map<string, number>();
      for (const w of split(`${c.title} ${c.search || c.text}`)) m.set(w, (m.get(w) ?? 0) + 1);
      return m;
    });
    const idf = new Map(q.map((w) => [w, Math.log(1 + chunks.length / (1 + bags.filter((b) => b.has(w)).length))]));
    return { q, bags, idf };
  });
  if (legs[1].q.length === 0 || chunks.length === 0) return [];
  return chunks
    .map((c, i) => {
      let s = 0;
      for (const { q, bags, idf } of legs) {
        for (const w of q) {
          const tf = bags[i].get(w) ?? 0;
          if (tf) s += (idf.get(w) ?? 0) * (tf / (tf + 10));
        }
      }
      return { c, s, i };
    })
    .filter((x) => x.s > 0)
    .sort((a, b) => b.s - a.s || a.i - b.i)
    .slice(0, k)
    .map((x) => ({ ...x.c, rank: Math.round(x.s * 1e4) / 1e4 }));
}

/**
 * snippet is the part of text around the first query word it holds (at most ~max chars, cut at
 * spaces) and the query's words found in it, for highlighting. Verbatim: a substring of text.
 */
export function snippet(text: string, query: string, max = 240): { text: string; cut: [boolean, boolean]; marks: string[] } {
  const qs = new Set([...words(query), ...terms(query)]);
  const found = [...text.matchAll(/[\p{L}\p{N}]+/gu)].filter((m) => qs.has(m[0].toLowerCase()) || qs.has(terms(m[0])[0] ?? ''));
  const at = found[0]?.index ?? 0;
  let start = Math.max(0, at - max / 3);
  if (start > 0) start = text.indexOf(' ', start) + 1 || start;
  let end = Math.min(text.length, start + max);
  if (end < text.length) end = text.lastIndexOf(' ', end) > start ? text.lastIndexOf(' ', end) : end;
  return { text: text.slice(start, end), cut: [start > 0, end < text.length], marks: [...new Set(found.map((m) => m[0]))] };
}
