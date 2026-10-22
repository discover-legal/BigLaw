// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Fails when a built bundle can reach the network: the recorded demo must make no request at all.
// Usage: node scripts/offline-check.mjs <dist dir>
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

export const NETWORK = /\bfetch\s*\(|XMLHttpRequest|EventSource|WebSocket|sendBeacon|importScripts|\bnew\s+Worker\b|<link[^>]+rel=["']?(?:modulepreload|prefetch|preconnect|dns-prefetch)|https?:\/\/(?!www\.w3\.org\/|reactjs\.org\/|react\.dev\/|discover\.legal\/["'`]|127\.0\.0\.1:3101["'`])/;
// Two exact strings are allowed: discover.legal/ (the site root) is the banner's way back, a link the visitor clicks;
// 127.0.0.1:3101 is BIGLAW_PUBLIC_URL's default as Settings displays it. Neither is ever requested.

export function scan(dir) {
  const bad = [];
  const walk = (d) => {
    for (const f of readdirSync(d)) {
      const p = join(d, f);
      if (statSync(p).isDirectory()) walk(p);
      else if (/\.(m?js|html|css)$/.test(f)) {
        const m = NETWORK.exec(readFileSync(p, 'utf8'));
        if (m) bad.push(`${p}: ${m[0]}`);
      }
    }
  };
  walk(dir);
  return bad;
}

if (process.argv[2]) {
  const bad = scan(process.argv[2]);
  if (bad.length) { console.error(`network code in the recorded bundle:\n${bad.join('\n')}`); process.exit(1); }
  console.log(`${process.argv[2]}: no network calls`);
}
