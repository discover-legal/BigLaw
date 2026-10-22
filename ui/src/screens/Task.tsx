// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Task: the live loop log (steps + SSE), findings with verification badges, the deliverable with
// its Limitations and Evidence Note, the deliverable gate, and the cost ledger.
import { useEffect, useState } from 'react';
import type { Finding, Step, Task, TaskCost } from '../api/types.ts';
import { Badge } from '../components/badge.ts';
import { Markdown } from '../components/Markdown.tsx';
import { Failure, go, useApi } from '../lib.tsx';
import { StatusPill } from './Matters.tsx';

const TABS: [string, string][] = [['run', 'Run log'], ['findings', 'Findings'], ['memo', 'Deliverable'], ['cost', 'Cost']];

export function TaskScreen(props: { id: string; tab: string }) {
  const api = useApi();
  const [task, setTask] = useState<Task | null>(null);
  const [steps, setSteps] = useState<Step[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [focus, setFocus] = useState<string | null>(null);

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

  if (error) return <Failure error={error} />;
  if (!task) return <p className="muted">Loading…</p>;
  const tab = (id: string) => go(`task/${encodeURIComponent(task.id)}`, { tab: id });
  const act = (p: Promise<Task>) => p.then(setTask, (e: Error) => setError(e.message));
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
      {gate && (
        <div className="panel gate">
          <p><strong>Deliverable gate.</strong> The run has finished; no task is complete until a lawyer approves the deliverable.</p>
          <button className="primary" onClick={() => void act(api.decideGate(task.id, gate.gateId, true))}>Approve deliverable</button>{' '}
          <button onClick={() => void act(api.decideGate(task.id, gate.gateId, false))}>Reject</button>
        </div>
      )}
      <div className="tabs" role="tablist">
        {TABS.map(([id, label]) => (
          <button key={id} role="tab" aria-selected={props.tab === id} className={props.tab === id ? 'on' : ''} onClick={() => tab(id)}>{label}</button>
        ))}
      </div>
      {props.tab === 'run' && <RunLog steps={steps} running={task.status === 'running'} />}
      {props.tab === 'findings' && <Findings findings={task.findings} focus={focus} running={task.status === 'running'} />}
      {props.tab === 'memo' && (task.output
        ? <article className="panel memo"><Markdown text={task.output} onFid={(f) => { setFocus(f); tab('findings'); }} /></article>
        : <p className="muted">The deliverable appears when the run finishes.</p>)}
      {props.tab === 'cost' && <Cost id={task.id} />}
    </section>
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

function Findings(props: { findings: Finding[]; focus: string | null; running: boolean }) {
  useEffect(() => {
    if (props.focus) document.getElementById(`finding-${props.focus}`)?.scrollIntoView({ block: 'center' });
  }, [props.focus]);
  if (props.findings.length === 0) return <p className="muted">{props.running ? 'Findings appear as the run verifies them.' : 'No findings.'}</p>;
  return (
    <div className="findings">
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
                <span className="muted loc">{c.locator}</span>
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
