// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Matters: the matter list with each matter's tasks, and the new-task wizard.
import { useState } from 'react';
import type { PromptMatch } from '../api/transport.ts';
import type { Client, Matter, Task } from '../api/types.ts';
import { Failure, go, plural, useApi, useLoad } from '../lib.tsx';

export function StatusPill(props: { status: string }) {
  return <span className={`pill pill-${props.status}`}>{props.status.replace('_', ' ')}</span>;
}

export function Matters(props: { matter: string }) {
  const api = useApi();
  const clients = useLoad(() => api.clients(), []);
  const tasks = useLoad(() => api.tasks(), []);
  const all: (Matter & { client: string })[] = (clients.data ?? []).flatMap((c: Client) => c.matters.map((m) => ({ ...m, client: c.name })));
  const picked = all.find((m) => m.number === props.matter);
  return (
    <section>
      <h1>Matters</h1>
      <p className="lede">{api.mode === 'recorded'
        ? 'Three fictional sample matters. Pick one, then ask a question or replay one of its recorded tasks.'
        : 'Pick a matter to start a task on its documents.'}</p>
      <Failure error={clients.error ?? tasks.error} />
      {clients.data && all.length === 0 && <p className="muted">No matters yet. Matters come from Clients (PR 8).</p>}
      <div className="cards">
        {all.map((m) => (
          <button key={m.number} className={`card matter ${picked?.number === m.number ? 'on' : ''}`} onClick={() => go('matters', { matter: m.number })}>
            <span className="eyebrow">{m.client} · {m.number}</span>
            <span className="card-title">{m.title}</span>
            <span className="muted">{plural(m.documentIds.length, 'document')} · {plural((tasks.data ?? []).filter((t) => t.matterNumber === m.number).length, 'task')}</span>
          </button>
        ))}
      </div>
      {picked && <NewTask key={picked.number} matter={picked} />}
      <h2>Tasks</h2>
      {tasks.data && tasks.data.length === 0 && <p className="muted">No tasks yet{api.mode === 'recorded' ? ': pick a matter above and start one.' : '.'}</p>}
      <TaskList tasks={tasks.data ?? []} />
    </section>
  );
}

function TaskList(props: { tasks: Task[] }) {
  if (props.tasks.length === 0) return null;
  return (
    <table className="grid stack">
      <thead><tr><th>Task</th><th>Matter</th><th>Status</th><th>Findings</th></tr></thead>
      <tbody>
        {props.tasks.map((t) => (
          <tr key={t.id}>
            <td className="wide"><a href={`#/task/${encodeURIComponent(t.id)}`}>{t.replay?.title ?? t.description}</a></td>
            <td>{t.matterNumber}</td>
            <td><StatusPill status={t.status} /></td>
            <td data-label="Findings">{t.findings.length}{t.findings.some((f) => f.status !== 'verified') ? ` (${t.findings.filter((f) => f.status !== 'verified').length} quarantined)` : ''}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function NewTask(props: { matter: Matter & { client: string } }) {
  const api = useApi();
  const [prompt, setPrompt] = useState('');
  const [jurisdiction, setJurisdiction] = useState('US');
  const [offScript, setOffScript] = useState<PromptMatch | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const suggestions = api.suggestions?.(props.matter.number) ?? [];

  async function start(replayKey?: string) {
    setError(null);
    if (!replayKey && api.mode === 'recorded') {
      const m = api.match(prompt, props.matter.number);
      if (!m || !m.onScript) { setOffScript(m); return; }
      replayKey = m.key;
    }
    setBusy(true);
    try {
      const t = await api.submit({ description: prompt, workflowType: 'memo', jurisdiction, matterNumber: props.matter.number, documentIds: props.matter.documentIds }, replayKey);
      go(`task/${encodeURIComponent(t.id)}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="panel wizard" onSubmit={(e) => { e.preventDefault(); void start(); }}>
      <h2>New task · {props.matter.title}</h2>
      <label htmlFor="prompt">What should BigLaw answer from this matter's {plural(props.matter.documentIds.length, 'document')}?</label>
      <textarea id="prompt" rows={3} value={prompt} onChange={(e) => { setPrompt(e.target.value); setOffScript(null); }}
        placeholder="e.g. When is our answer to the complaint due?" />
      {suggestions.length > 0 && (
        <div className="chips" aria-label="Recorded tasks">
          <span className="muted">Recorded tasks:</span>
          {suggestions.map((s) => <button type="button" key={s.key} className="chip" onClick={() => { setPrompt(s.prompt); setOffScript(null); if (s.jurisdiction) setJurisdiction(s.jurisdiction); }}>{s.title}</button>)}
        </div>
      )}
      <div className="row">
        <label>Workflow <select value="memo" disabled><option value="memo">memo</option></select></label>
        <label>Jurisdiction <input value={jurisdiction} onChange={(e) => setJurisdiction(e.target.value)} size={6} /></label>
        <button className="primary" disabled={busy || prompt.trim() === ''}>Start task</button>
      </div>
      {offScript !== undefined && offScript !== null && !offScript.onScript && (
        <div className="notice" role="status">
          <p><strong>Off script.</strong> This demo replays recorded runs only, and none of them answers that prompt
            (closest match {Math.round(offScript.score * 100)}%). The nearest recorded task is “{offScript.title}”.</p>
          <button type="button" onClick={() => void start(offScript.key)}>Replay “{offScript.title}” instead</button>
        </div>
      )}
      <Failure error={error} />
    </form>
  );
}
