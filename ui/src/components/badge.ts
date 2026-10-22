// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The verification-method badge (README G2/G3/G11). Plain createElement so node:test renders it.
import { createElement } from 'react';

export interface BadgeView { label: string; tone: 'ok' | 'warn' | 'bad'; title: string }

/** badgeFor maps a ladder method to what a reviewing lawyer sees. Unknown methods are never shown as verified. */
export function badgeFor(method: string, confidence = 0): BadgeView {
  const pct = `${Math.round(confidence * 100)}%`;
  if (method.endsWith('@transcript')) {
    const base = method.slice(0, -'@transcript'.length);
    return { label: `${base} @transcript`, tone: 'warn', title: `Matched in a machine transcription of an image (confidence ${pct}, capped at 85%): check against the original.` };
  }
  switch (method) {
    case 'exact': return { label: 'exact', tone: 'ok', title: `The quote appears verbatim in the source (confidence ${pct}).` };
    case 'tolerant': return { label: 'tolerant', tone: 'ok', title: `The quote matches once quotes, case and white space are normalised (confidence ${pct}).` };
    case 'judge': return { label: 'judge', tone: 'warn', title: `A paraphrase judge found the quote supported (confidence ${pct}, capped at 80%).` };
    case 'ensemble': return { label: 'ensemble', tone: 'warn', title: `Three independent judge votes supported the quote (confidence ${pct}).` };
    default: return { label: 'excluded', tone: 'bad', title: 'Not verified against the source: quarantined and left out of the deliverable.' };
  }
}

export function Badge(props: { method: string; confidence?: number }) {
  const b = badgeFor(props.method, props.confidence);
  return createElement('span', { className: `badge badge-${b.tone}`, title: b.title, 'data-method': props.method }, b.label);
}
