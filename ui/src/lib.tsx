// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Shared by every screen: the transport context, hash routes and the load hook.
import { createContext, useContext, useEffect, useState } from 'react';
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

export function Failure(props: { error: string | null }) {
  return props.error ? <p className="error" role="alert">{props.error}</p> : null;
}
