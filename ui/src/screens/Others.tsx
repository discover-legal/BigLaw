// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Playbooks, Clients & Conflicts, Time & Billing and Settings: read views over the routes PRs 6,
// 8 and 9 add. The recorded demo has clients (the sample matters) and nothing else to show here.
import type { ReactNode } from 'react';
import { Failure, useApi, useLoad } from '../lib.tsx';

function Screen(props: { title: string; pr: string; empty: string; error: string | null; children: ReactNode }) {
  const api = useApi();
  return (
    <section>
      <h1>{props.title}</h1>
      <Failure error={props.error} />
      {props.children}
      <p className="muted small">{api.mode === 'recorded'
        ? `Not part of the recorded demo: these routes arrive with ${props.pr}, and nothing was recorded for them.`
        : props.empty}</p>
    </section>
  );
}

export function Playbooks() {
  const api = useApi();
  const r = useLoad(() => api.playbooks(), []);
  return (
    <Screen title="Playbooks" pr="PR 6 (contract review)" empty="Playbooks resolve client > matter > personal > firm." error={r.error}>
      {r.data && r.data.length > 0 && (
        <table className="grid"><thead><tr><th>Playbook</th><th>Scope</th><th>Clauses</th></tr></thead>
          <tbody>{r.data.map((p) => <tr key={p.id}><td>{p.name}</td><td>{p.scope}</td><td>{p.clauses}</td></tr>)}</tbody></table>
      )}
    </Screen>
  );
}

export function Clients() {
  const api = useApi();
  const r = useLoad(() => api.clients(), []);
  return (
    <Screen title="Clients & Conflicts" pr="PR 8 (conflict checks and acknowledgements)" empty="Conflict checks run on every new client and matter." error={r.error}>
      {r.data?.map((c) => (
        <div key={c.id} className="panel">
          <h2>{c.name}</h2>
          <ul>{c.matters.map((m) => <li key={m.number}><a href={`#/matters?matter=${encodeURIComponent(m.number)}`}>{m.number}</a> {m.title} · {m.documentIds.length} documents</li>)}</ul>
        </div>
      ))}
    </Screen>
  );
}

export function Billing() {
  const api = useApi();
  const r = useLoad(() => api.timeEntries(), []);
  return (
    <Screen title="Time & Billing" pr="PR 9 (time entries, pre-bills, LEDES)" empty="Exports: GET /time-entries/export.csv and export.ledes." error={r.error}>
      {r.data && r.data.length > 0 && (
        <table className="grid"><thead><tr><th>Date</th><th>Matter</th><th>Hours</th><th>Timekeeper</th><th>Narrative</th></tr></thead>
          <tbody>{r.data.map((e) => <tr key={e.id}><td>{e.date}</td><td>{e.matterNumber}</td><td>{e.hours.toFixed(1)}</td><td>{e.timekeeper}</td><td>{e.narrative}</td></tr>)}</tbody></table>
      )}
    </Screen>
  );
}

export function Settings() {
  const api = useApi();
  const r = useLoad(() => api.profiles(), []);
  return (
    <Screen title="Settings" pr="PR 8 (profiles, OIDC sign-in and bearer tokens)" empty="Tokens: POST /profiles/{id}/tokens." error={r.error}>
      {r.data && r.data.length > 0 && (
        <table className="grid"><thead><tr><th>Profile</th><th>Role</th></tr></thead>
          <tbody>{r.data.map((p) => <tr key={p.id}><td>{p.name}</td><td>{p.role}</td></tr>)}</tbody></table>
      )}
    </Screen>
  );
}
