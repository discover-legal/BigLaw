// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The Task screen's event stream. The server ends a task's stream with `done` whenever the run
// stops, a pause at a gate included (internal/api events); a decided gate puts the run back to
// running, so the stream is opened again then, and the server sends the steps from the first again
// (the screen keeps each step once).
import type { RunEvent, TaskStatus } from './types.ts';
import type { Transport } from './transport.ts';

export interface Follow {
  /** The task's status as the screen last read it: a running or queued run with no stream reopens it. */
  status(s: TaskStatus): void;
  close(): void;
}

export function follow(t: Pick<Transport, 'events'>, id: string, on: (e: RunEvent) => void): Follow {
  let open = false, closed = false, stop = () => {};
  const start = () => {
    stop();
    open = true;
    stop = t.events(id, (e) => { if (e.type === 'done') open = false; if (!closed) on(e); });
  };
  start();
  return {
    status(s) { if (!closed && !open && (s === 'running' || s === 'queued')) start(); },
    close() { closed = true; stop(); },
  };
}
