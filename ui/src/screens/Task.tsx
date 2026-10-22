// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Task: the live loop log (steps + SSE), findings with verification badges, the deliverable with
// its Limitations and Evidence Note, the deliverable gate, and the cost ledger.
import { useEffect, useState } from 'react';
import type { Doc, Finding, Gate, Step, Task, TaskCost } from '../api/types.ts';
import { Badge } from '../components/badge.ts';
import { Markdown } from '../components/Markdown.tsx';
import { Failure, go, useApi, useLoad } from '../lib.tsx';
import { StatusPill } from './Matters.tsx';

const TABS: [string, string][] = [['run', 'Run log'], ['findings', 'Findings'], ['memo', 'Deliverable'], ['cost', 'Cost']];

export function TaskScreen(props: { id: string; tab: string }) {
  const api = useApi();
  const [task, setTask] = useState<Task | null>(null);
  const [steps, setSteps] = useState<Step[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [focus, setFocus] = useState<string | null>(null);
  const docs = useLoad(() => (task ? api.documents(task.matterNumber) : Promise.resolve([] as Doc[])), [task?.matterNumber]);

  useEffect(() => {
    let live = true;
    setSteps([]);
    const refresh = () => api.task(props.id).then((t) => { if (live) setTask(t); }, (e: Error) => live && setError(e.message));
    void refresh();
    const stop = api.events(props.id, (e) => {
      if (!live) return;
      if (e.type === 'step') setSteps((s) => (s.some((x) => x.step === e.step.step) ? s : [...s, e.step]));
      else void refresh();
    });
    return () => { live = false; stop(); };
  }, [api, props.id]);

  if (error && !task) {
    return (
      <section>
        <h1>Task not found</h1>
        {api.mode === 'live' && <Failure error={error} />}
        {api.mode === 'recorded' && <p>The demo keeps nothing across a reload, so earlier tasks are gone. Start one again from <a href="#/matters">Matters</a>.</p>}
      </section>
    );
  }
  if (!task) return <p className="muted">Loading…</p>;
  const tab = (id: string) => go(`task/${encodeURIComponent(task.id)}`, { tab: id });
  const act = (p: Promise<Task>) => p.then((t) => { setError(null); setTask(t); }, (e: Error) => setError(e.message));
  const gate = task.pendingGates.find((g) => g.kind === 'deliverable');
  const quarantined = task.findings.filter((f) => f.status !== 'verified').length;

  return (
    <section>
      <p className="crumbs"><a href={`#/matters?matter=${encodeURIComponent(task.matterNumber)}`}>{task.matterNumber}</a> / task</p>
      <h1>{task.replay?.title ?? task.description}</h1>
      {task.replay && task.description !== task.replay.prompt && (
        <p className="muted">You asked: “{task.description}”. Replaying the recorded task: “{task.replay.prompt}”</p>
      )}
      <div className="runbar" aria-live="polite">
        <StatusPill status={task.status} />
        <span>step {task.run.step}</span>
        {task.run.stopReason && <span>stop: <b>{task.run.stopReason}</b></span>}
        <span>budget: {task.run.budget.calls} calls, ${(task.run.budget.usd ?? 0).toFixed(2)}{task.run.budget.usd === null ? ' + unpriced' : ''} spent, {task.run.budget.remaining} calls left</span>
        <span>{task.findings.length} findings{quarantined ? `, ${quarantined} quarantined` : ''}</span>
        <span className="spacer" />
        {task.status === 'running' && api.fastForward && <button onClick={() => { api.fastForward?.(task.id); void api.task(task.id).then(setTask); }}>Skip to end</button>}
        {task.status === 'running' && <button className="danger" onClick={() => void act(api.cancel(task.id))}>Cancel</button>}
      </div>
      {gate && <GatePanel gate={gate} decide={(approve, reason) => act(api.decideGate(task.id, gate.gateId, approve, reason))} />}
      {task.decidedGates?.map((d) => (
        <div key={d.gateId} className={`panel gate decided ${d.decision}`} role="status">
          {d.decision === 'approved'
            ? <p><strong>Deliverable approved.</strong> The task is complete.</p>
            : <>
              <p><strong>Deliverable rejected.</strong> Reason: “{d.reason}”</p>
              <p className="muted">{api.mode === 'recorded'
                ? 'A live run sends your reason back to the lead, which revises the deliverable and returns to this gate. The recording has no revised run, so the replay ends here; start the task again from its matter to see the other branch.'
                : 'The lead has your reason and is revising the deliverable; it comes back to this gate.'}</p>
            </>}
        </div>
      ))}
      <Failure error={error} />
      <div className="tabs" role="tablist">
        {TABS.map(([id, label]) => (
          <button key={id} role="tab" aria-selected={props.tab === id} className={props.tab === id ? 'on' : ''} onClick={() => tab(id)}>{label}</button>
        ))}
      </div>
      {props.tab === 'run' && <RunLog steps={steps} running={task.status === 'running'} />}
      {props.tab === 'findings' && <Findings findings={task.findings} docs={docs.data ?? []} focus={focus} running={task.status === 'running'} />}
      {props.tab === 'memo' && (task.output
        ? <article className="panel memo"><Markdown text={task.output} onFid={(f) => { setFocus(f); tab('findings'); }} /></article>
        : <p className="muted">The deliverable appears when the run finishes.</p>)}
      {props.tab === 'cost' && <Cost id={task.id} />}
    </section>
  );
}

function GatePanel(props: { gate: Gate; decide: (approve: boolean, reason?: string) => Promise<unknown> }) {
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState('');
  return (
    <div className="panel gate">
      <p><strong>Deliverable gate.</strong> The run has finished; no task is complete until a lawyer approves the deliverable.</p>
      {!rejecting ? (
        <div className="row">
          <button className="primary" onClick={() => void props.decide(true)}>Approve deliverable</button>
          <button onClick={() => setRejecting(true)}>Reject…</button>
        </div>
      ) : (
        <form onSubmit={(e) => { e.preventDefault(); void props.decide(false, reason); }}>
          <label htmlFor="reject-reason">What should the lead fix? Your reason goes back with the deliverable.</label>
          <textarea id="reject-reason" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} autoFocus
            placeholder="e.g. Add the Rule 26(f) conference deadline." />
          <div className="row">
            <button className="danger" disabled={reason.trim() === ''}>Reject deliverable</button>
            <button type="button" onClick={() => setRejecting(false)}>Cancel</button>
          </div>
        </form>
      )}
    </div>
  );
}

function RunLog(props: { steps: Step[]; running: boolean }) {
  return (
    <ol className="steplog">
      {props.steps.map((s) => (
        <li key={s.step} className={`step step-${s.status}`}>
          <span className="n">{s.step}</span>
          <span className="tool">{s.tool}</span>
          <span className="sum">{s.summary}</span>
          {s.source && <span className={`src src-${s.source}`} title={s.source === 'computed' ? 'v2 code ran this step when the run was recorded' : 'stands for a model call; its content is the answer key'}>{s.source}</span>}
        </li>
      ))}
      {props.running && <li className="step pending"><span className="n">…</span><span className="tool">lead</span><span className="sum">choosing the next tool</span></li>}
    </ol>
  );
}

function Findings(props: { findings: Finding[]; docs: Doc[]; focus: string | null; running: boolean }) {
  const api = useApi();
  const name = (id: string) => props.docs.find((d) => d.id === id)?.name ?? '';
  useEffect(() => {
    if (props.focus) document.getElementById(`finding-${props.focus}`)?.scrollIntoView({ block: 'center' });
  }, [props.focus]);
  if (props.findings.length === 0) return <p className="muted">{props.running ? 'Findings appear as the run verifies them.' : 'No findings.'}</p>;
  const methods = new Map<string, number>();
  for (const c of props.findings.flatMap((f) => f.citations)) methods.set(c.method, (methods.get(c.method) ?? 0) + 1);
  return (
    <div className="findings">
      <p className="ladder" aria-label="Citations by verification method">
        {[...methods].map(([m, n]) => <span key={m}><Badge method={m} confidence={m === 'exact' ? 1 : m === 'tolerant' ? 0.95 : m.endsWith('@transcript') ? 0.85 : 0} /> ×{n}</span>)}
        <span className="muted small">exact and tolerant: found in the source; @transcript: found in a scan's machine transcript (at most 85%, check the original); excluded: not found, kept out of the deliverable.</span>
      </p>
      {props.findings.map((f) => (
        <article key={f.fid} id={`finding-${f.fid}`} className={`panel finding ${props.focus === f.fid ? 'focus' : ''} ${f.status}`}>
          <header><b>{f.fid}</b> <span className="muted">{f.question}</span> <span className={`sev sev-${f.severity}`}>{f.severity}</span>
            {f.status !== 'verified' && <span className="badge badge-bad">quarantined</span>}</header>
          <p>{f.claim}</p>
          <ul className="cites">
            {f.citations.map((c, i) => (
              <li key={i}>
                <Badge method={c.method} confidence={c.confidence} />
                {c.match
                  ? <a className="quote" href={`#/documents?doc=${encodeURIComponent(c.documentId)}&section=${encodeURIComponent(c.sectionId)}&hl=${encodeURIComponent(c.match)}`}>“{c.quote}”</a>
                  : <span className="quote">“{c.quote}”</span>}
                <span className="muted loc">{[name(c.documentId), c.locator].filter(Boolean).join(' · ')}</span>
                {c.method === 'none'
                  ? <span className="cite-note bad">Not found in the matter's documents, so excluded from the deliverable.{c.note ? ` Ladder: ${c.note}.` : ''}</span>
                  : c.note && <span className="cite-note">{c.note}{c.method.endsWith('@transcript') && api.fileUrl(c.documentId) && <> · <a href={api.fileUrl(c.documentId) ?? ''} target="_blank" rel="noopener">open the scan</a></>}</span>}
              </li>
            ))}
          </ul>
        </article>
      ))}
    </div>
  );
}

function Cost(props: { id: string }) {
  const api = useApi();
  const [c, setC] = useState<TaskCost | null>(null);
  useEffect(() => { void api.cost(props.id).then(setC); }, [api, props.id]);
  if (!c) return null;
  const s = c.summary;
  return (
    <div className="panel">
      <div className="stats">
        <div><b>{s.calls}</b><span>model calls</span></div>
        <div><b>{s.inputTokens.toLocaleString()}</b><span>input tokens</span></div>
        <div><b>{s.outputTokens.toLocaleString()}</b><span>output tokens</span></div>
        <div><b>{s.usd === null ? 'unknown' : `$${s.usd.toFixed(4)}`}</b><span>cost{s.unpricedCalls ? ` (${s.unpricedCalls} unpriced)` : ''}</span></div>
      </div>
      {api.mode === 'recorded' && (
        <p className="muted">This recording made no model calls, so it cost nothing. A live run charges every call (lead, subagents,
          critics, embeddings) to this ledger from the price table, against BIGLAW_BUDGET_CALLS (150) and BIGLAW_BUDGET_USD ($5).
          For scale, v1's best recorded Haiku run on one Harvey LAB task (8 documents) used 8.3 million tokens and cost $11.52
          (benchmarks/runs on archive/v1-final, haiku-v5b).</p>
      )}
      {c.entries.length > 0 && (
        <table className="grid">
          <thead><tr><th>Step</th><th>Tool</th><th>Model</th><th>In</th><th>Out</th><th>USD</th></tr></thead>
          <tbody>{c.entries.map((e, i) => <tr key={i}><td>{e.step}</td><td>{e.tool}</td><td>{e.model}</td><td>{e.inputTokens}</td><td>{e.outputTokens}</td><td>{e.usd === null ? 'unpriced' : e.usd.toFixed(4)}</td></tr>)}</tbody>
        </table>
      )}
    </div>
  );
}
