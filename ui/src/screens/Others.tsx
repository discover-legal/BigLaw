// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Playbooks, Clients & Conflicts, Time & Billing and Settings: read views over the routes PRs 6,
// 8 and 9 add. The recorded demo shows sample records written for it (recording.json, labelled).
import type { ReactNode } from 'react';
import type { Playbook } from '../api/types.ts';
import { Empty, Failure, Loading, plural, useApi, useLoad } from '../lib.tsx';

function Screen(props: { title: string; lede: string; sample?: string; error: string | null; loading?: boolean; children: ReactNode }) {
  const api = useApi();
  return (
    <section>
      <h1>{props.title}</h1>
      <p className="lede">{props.lede}</p>
      {api.mode === 'recorded' && props.sample && <p className="sample small">{props.sample}</p>}
      <Failure error={props.error} />
      {props.loading && !props.error ? <Loading label={`Loading ${props.title.toLowerCase()}`} /> : props.children}
    </section>
  );
}

const SAMPLE = 'Sample records written for this demo (fictional people), consistent with the three matters; not computed by BigLaw.';
const TIERS: Playbook['scope'][] = ['client', 'matter', 'personal', 'firm'];

export function Playbooks() {
  const api = useApi();
  const r = useLoad(() => api.playbooks(), []);
  const rows = [...(r.data ?? [])].sort((a, b) => TIERS.indexOf(a.scope) - TIERS.indexOf(b.scope));
  return (
    <Screen title="Playbooks" error={r.error} loading={!r.data} sample={`${SAMPLE} Contract review that applies them arrives with PR 6.`}
      lede="Negotiation positions BigLaw reviews contracts against. When two playbooks cover the same clause, the narrower wins: client, then matter, then personal, then firm.">
      {r.data && rows.length === 0 && <Empty icon="ledger" title="No playbooks yet">Playbooks hold the positions contract review checks clauses against.</Empty>}
      {rows.length > 0 && (
        <div className="table-wrap"><table className="grid"><thead><tr><th>Playbook</th><th>Tier</th><th>Positions</th></tr></thead>
          <tbody>{rows.map((p) => <tr key={p.id}><td>{p.name}</td><td><span className={`pill tier-${p.scope}`}>{p.scope}</span></td><td>{plural(p.clauses, 'clause')}</td></tr>)}</tbody></table></div>
      )}
    </Screen>
  );
}

export function Clients() {
  const api = useApi();
  const r = useLoad(() => api.clients(), []);
  return (
    <Screen title="Clients & Conflicts" error={r.error} loading={!r.data}
      lede={api.mode === 'recorded'
        ? 'The three sample clients and their matters. Conflict checks against adverse parties arrive with PR 8.'
        : 'Conflict checks run on every new client and matter.'}>
      {r.data?.length === 0 && <Empty icon="doc" title="No clients yet">Clients and their matters appear here once added.</Empty>}
      {r.data?.map((c) => (
        <div key={c.id} className="panel client">
          <h2>{c.name}</h2>
          <ul className="plain">{c.matters.map((m) => (
            <li key={m.number}><a className="num-tag" href={`#/matters?matter=${encodeURIComponent(m.number)}`}>{m.number}</a> <span>{m.title}</span>
              <a className="small" href={`#/documents?matter=${encodeURIComponent(m.number)}`}>{plural(m.documentIds.length, 'document')}</a></li>
          ))}</ul>
        </div>
      ))}
    </Screen>
  );
}

export function Billing() {
  const api = useApi();
  const r = useLoad(() => api.timeEntries(), []);
  const rows = [...(r.data ?? [])].sort((a, b) => b.date.localeCompare(a.date));
  const byMatter = new Map<string, number>();
  for (const e of rows) byMatter.set(e.matterNumber, (byMatter.get(e.matterNumber) ?? 0) + e.hours);
  return (
    <Screen title="Time & Billing" error={r.error} loading={!r.data} sample={`${SAMPLE} Pre-bills and LEDES export arrive with PR 9.`}
      lede="Time recorded against each matter, in tenths of an hour.">
      {r.data && rows.length === 0 && <Empty icon="ledger" title="No time entries yet">Time recorded against a matter appears here.</Empty>}
      {byMatter.size > 0 && (
        <div className="stats">{[...byMatter].map(([m, h]) => <div key={m}><b>{h.toFixed(1)} h</b><span>{m}</span></div>)}</div>
      )}
      {rows.length > 0 && (
        <table className="grid stack"><thead><tr><th>Date</th><th>Matter</th><th className="num">Hours</th><th>Timekeeper</th><th>Narrative</th></tr></thead>
          <tbody>{rows.map((e) => (
            <tr key={e.id}><td className="nowrap">{e.date}</td><td className="nowrap"><span className="num-tag">{e.matterNumber}</span></td><td data-label="Hours" className="num">{e.hours.toFixed(1)}</td>
              <td>{e.timekeeper}</td><td className="wide">{e.narrative}</td></tr>
          ))}</tbody></table>
      )}
    </Screen>
  );
}

export function Settings() {
  const api = useApi();
  const r = useLoad(() => api.profiles(), []);
  const cfg = useLoad(() => api.settings?.() ?? Promise.resolve(null), []);
  return (
    <Screen title="Settings" error={r.error ?? cfg.error} loading={!r.data} sample="Read-only. The profiles are sample records written for this demo (fictional people); sign-in and tokens arrive with PR 8."
      lede="Who can work on matters, and how this BigLaw is configured.">
      <h2>Profiles</h2>
      {r.data && r.data.length === 0 && <Empty icon="doc" title="No profiles yet">Profiles arrive with sign-in (PR 8).</Empty>}
      {r.data && r.data.length > 0 && (
        <div className="table-wrap"><table className="grid"><thead><tr><th>Name</th><th>Role</th></tr></thead>
          <tbody>{r.data.map((p) => <tr key={p.id}><td>{p.name}</td><td><span className={`pill role-${p.role}`}>{p.role}</span></td></tr>)}</tbody></table></div>
      )}
      {cfg.data && (
        <>
          <h2>Configuration</h2>
          <p className="muted small">This build's environment variables and their defaults (cmd/biglaw/config.go, read when the demo was recorded).
            A server prints its own with <code>biglaw version --config</code>; it is set by environment, not here.</p>
          <table className="grid stack"><thead><tr><th>Variable</th><th>Default</th><th>First read in</th></tr></thead>
            <tbody>{cfg.data.map((c) => <tr key={c.name}><td className="wide"><code>{c.name}</code></td><td data-label="Default">{c.secret ? <span className="muted">secret, never shown</span> : c.default || <span className="muted">unset</span>}</td><td data-label="First read in">PR {c.pr}</td></tr>)}</tbody></table>
        </>
      )}
    </Screen>
  );
}
