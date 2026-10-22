// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// How a source quote is shown. Display only: verification ran on the raw text, which the quote
// card keeps as its tooltip. Markdown emphasis (**x**, __x__) is dropped and a quoted table row
// ("| a | b |") becomes its cells, so a reader sees the words, not the source's markup.

/** plainText drops paired Markdown emphasis markers. */
export const plainText = (s: string) => s.replace(/\*\*(.+?)\*\*|__(.+?)__/g, (_m, a: string | undefined, b: string | undefined) => a ?? b ?? '');

/** quoteCells splits a quoted table row into its cells; any other quote is one cell. */
export function quoteCells(quote: string): string[] {
  const q = plainText(quote).trim();
  if (q.length > 1 && q.startsWith('|') && q.endsWith('|')) {
    const cells = q.slice(1, -1).split('|').map((c) => c.trim()).filter(Boolean);
    if (cells.length > 0) return cells;
  }
  return [q];
}
