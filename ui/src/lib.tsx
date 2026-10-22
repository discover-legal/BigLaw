// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Shared by every screen: the transport context, hash routes and the load hook.
import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { Icon } from './components/icons.tsx';
import type { Transport } from './api/transport.ts';

export const Api = createContext<Transport>(null as unknown as Transport);
export const useApi = () => useContext(Api);

export interface Route { path: string[]; q: URLSearchParams }

export function parseRoute(hash: string): Route {
  const [p, qs] = hash.replace(/^#\/?/, '').split('?');
  return { path: p.split('/').filter(Boolean).map(decodeURIComponent), q: new URLSearchParams(qs ?? '') };
}

export function go(path: string, q?: Record<string, string | undefined>) {
  const qs = new URLSearchParams(Object.entries(q ?? {}).filter((e): e is [string, string] => !!e[1])).toString();
  window.location.hash = `#/${path}${qs ? `?${qs}` : ''}`;
}

/** useLoad runs an API read and re-runs it when deps change. */
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[]): { data: T | null; error: string | null; reload: () => void } {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    let live = true;
    setError(null);
    fn().then((d) => { if (live) setData(d); }, (e: Error) => { if (live) setError(e.message); });
    return () => { live = false; };
  }, [...deps, n]);
  return { data, error, reload: () => setN((x) => x + 1) };
}

/** plural: "1 task", "2 tasks". */
export const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;

export function Failure(props: { error: string | null }) {
  return props.error ? <p className="error" role="alert">{props.error}</p> : null;
}

/** Loading: a quiet skeleton with its label for screen readers (and as a caption). */
export function Loading(props: { label: string }) {
  return (
    <div className="loading" role="status" aria-live="polite">
      <span className="sk" /><span className="sk" /><span className="sk short" />
      <p className="muted small">{props.label}…</p>
    </div>
  );
}

/** Empty: what is missing and what to do about it. */
export function Empty(props: { icon: string; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty">
      <span className="empty-icon"><Icon name={props.icon} size={20} /></span>
      <p className="empty-title">{props.title}</p>
      {props.children && <div className="muted">{props.children}</div>}
      {props.action}
    </div>
  );
}
