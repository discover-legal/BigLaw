// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Task: the live loop log (steps + SSE), findings with verification badges, the deliverable with
// its Limitations and Evidence Note (download, copy, print), the gates, why a run failed, and the cost ledger.
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { follow, type Follow } from '../api/follow.ts';
import type { Doc, Finding, Gate, Step, Task, TaskCost } from '../api/types.ts';
import { Badge, badgeFor } from '../components/badge.ts';
import { Icon, stepIcon } from '../components/icons.tsx';
import { quoteCells } from '../components/quote.ts';
import { Markdown } from '../components/Markdown.tsx';
import { memoExport } from '../components/memo.ts';
import { Empty, Failure, go, Loading, plural, useAction, useApi, useLoad } from '../lib.tsx';
import { StatusPill } from './Matters.tsx';

const TABS: [string, string][] = [['run', 'Run log'], ['findings', 'Findings'], ['memo', 'Deliverable'], ['cost', 'Cost']];
const SEVERITY: Record<string, number> = { low: 1, medium: 2, high: 3 };
const STOPPED: Record<string, string> = { budget_exhausted: "it reached this task's spending limit", max_steps: 'it reached its step limit' };

export function TaskScreen(props: { id: string; tab: string }) {
  const api = useApi();
  const [task, setTask] = useState<Task | null>(null);
  const [steps, setSteps] = useState<Step[]>([]);
  const { error, setError, run } = useAction();
  const [focus, setFocus] = useState<string | null>(null);
  const docs = useLoad(() => (task ? api.documents(task.matterNumber) : Promise.resolve([] as Doc[])), [task?.matterNumber]);

  const stream = useRef<Follow | null>(null);
  useEffect(() => {
    let live = true;
    setSteps([]);
    const refresh = () => api.task(props.id).then((t) => { if (live) setTask(t); }, (e: Error) => live && setError(e.message));
    void refresh();
    const f = stream.current = follow(api, props.id, (e) => {
      if (e.type === 'step') setSteps((s) => (s.some((x) => x.step === e.step.step) ? s : [...s, e.step]));
      else void refresh();
    });
    return () => { live = false; f.close(); };
  }, [api, props.id]);
  useEffect(() => { if (task?.id === props.id) stream.current?.status(task.status); }, [task, props.id]); // a decided gate resumes the run: stream it again

  if (error && !task) {
    return (
      <Empty icon="doc" title="Task not found" action={<a className="button" href="#/matters">Go to Matters</a>}>
        {api.mode === 'live' ? <Failure error={error} /> : 'The demo keeps nothing across a reload, so earlier tasks are gone. Start one again from its matter.'}
      </Empty>
    );
  }
  if (!task) return <Loading label="Loading the task" />;
  const tab = (id: string) => go(`task/${encodeURIComponent(task.id)}`, { tab: id });
  const act = (p: Promise<Task>) => run(p, setTask);
  const gate = task.pendingGates.find((g) => g.kind === 'deliverable');
  const held = task.pendingGates.filter((g) => g.kind !== 'deliverable');
  const quarantined = task.findings.filter((f) => f.status !== 'verified').length;

  return (
    <section>
      <nav className="crumbs" aria-label="Breadcrumb">
        <a href={`#/matters?matter=${encodeURIComponent(task.matterNumber)}`}><Icon name="back" size={14} />{task.matterNumber}</a>
      </nav>
      <h1>{task.replay?.title ?? task.description}</h1>
      {task.replay && task.description !== task.replay.prompt && (
        <p className="lede">You asked “{task.description}”. Replaying the recorded task “{task.replay.prompt}”</p>
      )}
      <div className="runbar" aria-live="polite">
        <dl className="runstats">
          <div><dt>Status</dt><dd><StatusPill status={task.status} /></dd></div>
          {task.submittedBy && <div><dt>Submitted by</dt><dd title={task.submittedBy.email ?? task.submittedBy.id}>{task.submittedBy.name}</dd></div>}
          <div><dt>Step</dt><dd>{task.run.step}</dd></div>
          {task.run.stopReason && <div><dt>Stopped</dt><dd>{STOPPED[task.run.stopReason] ?? task.run.stopReason.replace('_', ' ')}</dd></div>}
          <div><dt>Budget spent</dt><dd>{plural(task.run.budget.calls, 'call')}, ${(task.run.budget.usd ?? 0).toFixed(2)}{task.run.budget.usd === null ? ' + unpriced' : ''}</dd></div>
          <div><dt>Calls left</dt><dd>{task.run.budget.remaining}</dd></div>
          <div><dt>Findings</dt><dd>{task.findings.length}{quarantined ? <>, <span className="bad-text">{quarantined} quarantined</span></> : ''}</dd></div>
        </dl>
        {task.status === 'running' && (
          <div className="actions">
            {api.fastForward && <button onClick={() => { api.fastForward?.(task.id); void api.task(task.id).then(setTask); }}>Skip to end</button>}
            <button className="danger" onClick={() => act(api.cancel(task.id))}>Cancel run</button>
          </div>
        )}
      </div>
      {task.error && <p className="error" role="alert"><strong>This task failed.</strong> {task.error.replace(/^[a-z_]+: /, '')}</p>}
      {task.groundingAlert && task.groundingAlert !== task.error && <p className="notice" role="status"><strong>Evidence warning:</strong> {task.groundingAlert}.</p>}
      {gate && <GatePanel gate={gate} read={() => tab('memo')} decide={(approve, reason) => act(api.decideGate(task.id, gate.gateId, approve, reason))} />}
      {held.length > 0 && <HeldGates gates={held} open={task.status === 'awaiting_gate'} decide={(id, approve, reason) => act(api.decideGate(task.id, id, approve, reason))}
        finding={(fid) => { const f = task.findings.find((x) => x.fid === fid); return f && <FindingCard f={f} docs={docs.data ?? []} />; }} />}
      {task.decidedGates.filter((d) => d.kind !== 'deliverable').map((d) => (
        <p key={d.gateId} className="small muted">{d.kind === 'hook' ? `The call of ${d.subject} held by hook ${d.hook}` : `Finding ${d.findingId}`}: {d.decision}{d.decidedBy ? ` by ${d.decidedBy.name}` : ''}{d.reason ? ` (“${d.reason}”)` : ''}.</p>
      ))}
      {task.decidedGates.filter((d) => d.kind === 'deliverable').map((d) => (
        <div key={d.gateId} className={`gate decided ${d.decision}`} role="status">
          <span className="gate-icon"><Icon name={d.decision === 'approved' ? 'check' : 'x'} size={18} /></span>
          {d.decision === 'approved'
            ? <div><p><strong>Deliverable approved{d.decidedBy ? ` by ${d.decidedBy.name}` : ''}.</strong> The task is complete.</p></div>
            : <div>
              <p><strong>Deliverable rejected{d.decidedBy ? ` by ${d.decidedBy.name}` : ''}.</strong> Reason: “{d.reason}”</p>
              <p className="muted">{api.mode === 'recorded'
                ? 'A live run sends your reason back to the lead, which revises the deliverable and returns to this gate. The recording has no revised run, so the replay ends here; start the task again from its matter to see the other branch.'
                : 'The lead has your reason and is revising the deliverable; it comes back to this gate.'}</p>
            </div>}
        </div>
      ))}
      <Failure error={error} />
      <div className="tabs" role="tablist">
        {TABS.map(([id, label]) => (
          <button key={id} role="tab" aria-selected={props.tab === id} className={props.tab === id ? 'on' : ''} onClick={() => tab(id)}>
            {label}{id === 'findings' && task.findings.length > 0 && <span className="count">{task.findings.length}</span>}
            {id === 'run' && steps.length > 0 && <span className="count">{steps.length}</span>}
          </button>
        ))}
      </div>
      {props.tab === 'run' && <RunLog steps={steps} running={task.status === 'running'} />}
      {props.tab === 'findings' && <Findings findings={task.findings} docs={docs.data ?? []} focus={focus} running={task.status === 'running'} />}
      {props.tab === 'memo' && (task.output
        ? <><MemoActions text={memoExport(task.output, api.provenance)} name={task.matterNumber || 'memo'} />
          <article className="memo"><Markdown text={task.output} onFid={(f) => { setFocus(f); tab('findings'); }} /></article></>
        : <Empty icon="draft_section" title="No deliverable yet">The memo appears here when the run finishes; every factual sentence in it cites a verified finding.</Empty>)}
      {props.tab === 'cost' && <Cost id={task.id} budget={task.run.budget.calls + task.run.budget.remaining} />}
    </section>
  );
}

/** HeldGates are the finding and hook gates a lawyer decides before the deliverable's own gate opens. */
function HeldGates(props: { gates: Gate[]; open: boolean; decide: (id: string, approve: boolean, reason?: string) => void; finding: (fid: string) => ReactNode }) {
  const [reasons, setReasons] = useState<Record<string, string>>({});
  return (
    <div className="gate open">
      <span className="gate-icon"><Icon name="gate" size={18} /></span>
      <div>
        <p><strong>{plural(props.gates.length, 'decision')} needed.</strong> {props.open ? 'Decide each; the lead then goes on with your decisions.' : 'You can decide them when the run pauses at its gates.'}</p>
        {props.gates.map((g) => (
          <div key={g.gateId} className="held">
            <p>{g.kind === 'hook' ? <>Hook <code>{g.hook}</code> holds a call of <code>{g.subject}</code> for a lawyer's approval.</> : <>Check this finding before the memo goes out: it is cited at low confidence or was marked unsupported.</>}</p>
            {g.findingId && props.finding(g.findingId)}
            {g.detail && <p className="small">{g.detail}</p>}
            {g.input !== undefined && <details open={JSON.stringify(g.input).length < 1500}><summary className="small">The call's whole input ({JSON.stringify(g.input).length} characters): an approval covers exactly this, once</summary>
              <pre className="small">{JSON.stringify(g.input, null, 1)}</pre></details>}
            {props.open && <div className="row">
              <button className="primary small" onClick={() => props.decide(g.gateId, true)}>Approve</button>
              <input aria-label={`reason for ${g.gateId}`} placeholder="Reason (needed to reject)" value={reasons[g.gateId] ?? ''} onChange={(e) => setReasons({ ...reasons, [g.gateId]: e.target.value })} />
              <button className="small" disabled={!(reasons[g.gateId] ?? '').trim()} onClick={() => props.decide(g.gateId, false, reasons[g.gateId])}>Reject</button>
            </div>}
          </div>
        ))}
      </div>
    </div>
  );
}

function GatePanel(props: { gate: Gate; decide: (approve: boolean, reason?: string) => void; read: () => void }) {
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState('');
  return (
    <div className="gate open">
      <span className="gate-icon"><Icon name="gate" size={18} /></span>
      <div>
      <p><strong>Your review is needed.</strong> The run has finished; no task is complete until a lawyer approves the deliverable. Approve when you are satisfied; download it for your own edits.</p>
      {!rejecting ? (
        <div className="row">
          <button className="primary" onClick={() => props.decide(true)}>Approve deliverable</button>
          <button onClick={() => setRejecting(true)}>Reject…</button>
          <a href="?tab=memo" className="small" onClick={(e) => { e.preventDefault(); props.read(); }}>Read the deliverable first</a>
        </div>
      ) : (
        <form onSubmit={(e) => { e.preventDefault(); props.decide(false, reason); }}>
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
    </div>
  );
}

function RunLog(props: { steps: Step[]; running: boolean }) {
  if (props.steps.length === 0 && !props.running) return <Empty icon="ledger" title="No steps">This run recorded no steps.</Empty>;
  return (
    <ol className="timeline" aria-label="Run log">
      {props.steps.map((s) => (
        <li key={s.step} className={`tl tl-${s.status}`}>
          <span className="tl-node"><Icon name={stepIcon(s.tool, s.summary)} size={15} /></span>
          <div className="tl-body">
            <div className="tl-head">
              <code className="tl-tool">{s.tool}</code>
              <span className="tl-n">step {s.step}</span>
              {s.status !== 'committed' && <span className={`pill ${s.status === 'started' ? 'pill-running' : 'pill-failed'}`}>{s.status}</span>}
              {s.source && <span className={`src src-${s.source}`} title={s.source === 'computed' ? 'v2 code ran this step when the run was recorded' : 'stands for a model call; its content is the answer key'}>{s.source}</span>}
            </div>
            <p className="tl-sum">{s.summary}</p>
          </div>
        </li>
      ))}
      {props.running && (
        <li className="tl tl-pending">
          <span className="tl-node"><Icon name="lead" size={15} /></span>
          <div className="tl-body"><div className="tl-head"><code className="tl-tool">lead</code></div><p className="tl-sum">Choosing the next tool…</p></div>
        </li>
      )}
    </ol>
  );
}

const ORDER = ['exact', 'tolerant', 'judge', 'ensemble'];
const rungRank = (m: string) => (m === 'none' ? 99 : ORDER.indexOf(m.replace('@transcript', '')) + (m.endsWith('@transcript') ? 10 : 0));

function Severity(props: { level: string }) {
  const n = SEVERITY[props.level] ?? 0;
  return (
    <span className={`sev sev-${props.level}`} title={`Severity: ${props.level}`}>
      <span className="bars" aria-hidden="true">{[1, 2, 3].map((i) => <i key={i} className={i <= n ? 'on' : ''} />)}</span>
      {props.level.charAt(0).toUpperCase() + props.level.slice(1)}
    </span>
  );
}

function Findings(props: { findings: Finding[]; docs: Doc[]; focus: string | null; running: boolean }) {
  useEffect(() => {
    if (props.focus) document.getElementById(`finding-${props.focus}`)?.scrollIntoView({ block: 'center' });
  }, [props.focus]);
  if (props.findings.length === 0) {
    return props.running ? <Loading label="Findings appear here as the run verifies them" /> : <Empty icon="extract_facts" title="No findings">This run produced no findings.</Empty>;
  }
  const methods = new Map<string, number>();
  for (const c of props.findings.flatMap((f) => f.citations)) methods.set(c.method, (methods.get(c.method) ?? 0) + 1);
  const ladder = [...methods].sort((a, b) => rungRank(a[0]) - rungRank(b[0]));
  const total = ladder.reduce((n, [, k]) => n + k, 0);
  return (
    <div className="findings">
      <section className="verify" aria-label="Citations by verification method">
        <p className="verify-title"><strong>{plural(total, 'citation')}</strong> checked against the matter's documents</p>
        <div className="vbar" aria-hidden="true">
          {ladder.map(([m, n]) => <span key={m} className={`seg seg-${badgeFor(m).tone}`} style={{ flexGrow: n }} />)}
        </div>
        <ul className="vlegend">
          {ladder.map(([m, n]) => <li key={m}><Badge method={m} confidence={m === 'exact' ? 1 : m === 'tolerant' ? 0.95 : m.endsWith('@transcript') ? 0.85 : 0} /> {n}</li>)}
        </ul>
        <p className="muted small">Exact and tolerant: found in the source. @transcript: found in a scan's machine transcript (at most 85%; check the original). Excluded: not found, kept out of the deliverable.</p>
      </section>
      {props.findings.map((f) => <FindingCard key={f.fid} f={f} docs={props.docs} focus={props.focus === f.fid} />)}
    </div>
  );
}

/** FindingCard is one finding: its claim and each quote with its badge and source (the Findings tab, and its gate). */
function FindingCard(props: { f: Finding; docs: Doc[]; focus?: boolean /* in the Findings tab: anchored */ }) {
  const api = useApi();
  const f = props.f;
  const name = (id: string) => props.docs.find((d) => d.id === id)?.name ?? '';
  return (
    <article id={props.focus === undefined ? undefined : `finding-${f.fid}`} className={`finding ${props.focus ? 'focus' : ''} ${f.status}`}>
      <header>
        <span className="fid-tag">{f.fid}</span>
        <Severity level={f.severity} />
        {f.status !== 'verified' && <span className="badge badge-bad">quarantined</span>}
      </header>
      <p className="f-q">{f.question}</p>
      <p className="f-claim">{f.claim}</p>
      <ul className="quotes">
        {f.citations.map((c, i) => {
          const tone = badgeFor(c.method).tone;
          const file = c.method.endsWith('@transcript') ? api.fileUrl(c.documentId) : null;
          return (
            <li key={i} className={`qcard tone-${tone}`}>
              <QuoteText quote={c.quote} />
              <div className="qmeta">
                <Badge method={c.method} confidence={c.confidence} />
                {name(c.documentId) && <span className="qdoc"><Icon name={file ? 'scan' : 'doc'} size={13} />{name(c.documentId)}</span>}
                {c.match && <a className="qlink" href={`#/documents?doc=${encodeURIComponent(c.documentId)}&section=${encodeURIComponent(c.sectionId)}&hl=${encodeURIComponent(c.match)}`}>Show in source<Icon name="arrow" size={13} /></a>}
              </div>
              {c.locator && <p className="qloc">{c.locator}</p>}
              {c.method === 'none'
                ? <p className="cite-note bad">Not found in the matter's documents, so excluded from the deliverable.{c.note ? ` Ladder: ${c.note}.` : ''}</p>
                : c.note && <p className="cite-note">{c.note}{file && <> <a href={file} target="_blank" rel="noopener">Open the scan<Icon name="external" size={12} /></a></>}</p>}
            </li>
          );
        })}
      </ul>
      {f.valueNotes?.length ? <ul className="value-notes muted small">{f.valueNotes.map((n, i) => <li key={i}>Value dropped: {n}</li>)}</ul> : null}
    </article>
  );
}

/** QuoteText shows a source quote without the source's markup; the tooltip keeps it verbatim. */
function QuoteText(props: { quote: string }) {
  const cells = quoteCells(props.quote);
  const shown = cells.join(' ');
  return (
    <blockquote title={shown === props.quote ? undefined : `Verbatim, as verified: ${props.quote}`}>
      {cells.length === 1 ? `“${cells[0]}”` : <>“{cells.map((c, i) => <span key={i} className="qcell">{c}</span>)}”<span className="qrow">table row</span></>}
    </blockquote>
  );
}

/** MemoActions: the deliverable as a Markdown file, on the clipboard, or printed (print styles hide the rest of the page). */
function MemoActions(props: { text: string; name: string }) {
  const [copied, setCopied] = useState(false);
  const url = useMemo(() => URL.createObjectURL(new Blob([props.text], { type: 'text/markdown' })), [props.text]);
  useEffect(() => () => URL.revokeObjectURL(url), [url]);
  return (
    <div className="row memo-actions">
      <a className="button small" href={url} download={`${props.name}-memo.md`}>Download (.md)</a>
      <button className="small" onClick={() => void navigator.clipboard.writeText(props.text).then(() => setCopied(true))}>{copied ? 'Copied' : 'Copy'}</button>
      <button className="small" onClick={() => window.print()}>Print</button>
    </div>
  );
}

function Cost(props: { id: string; budget: number }) {
  const api = useApi();
  const [c, setC] = useState<TaskCost | null>(null);
  useEffect(() => { void api.cost(props.id).then(setC); }, [api, props.id]);
  if (!c) return <Loading label="Loading the cost ledger" />;
  const s = c.summary;
  return (
    <div className="cost">
      <div className="stats">
        <div><b>{s.calls}</b><span>model calls</span></div>
        <div><b>{s.inputTokens.toLocaleString()}</b><span>input tokens</span></div>
        <div><b>{s.outputTokens.toLocaleString()}</b><span>output tokens</span></div>
        <div><b>{s.usd === null ? 'unknown' : `$${s.usd.toFixed(4)}`}</b><span>cost{s.unpricedCalls ? ` (${s.unpricedCalls} unpriced)` : ''}</span></div>
        {s.wattHours ? <div><b>{s.wattHours.toFixed(2)} Wh</b><span>estimated energy (local models)</span></div> : null}
      </div>
      {api.mode === 'recorded' && (
        <p className="muted prose">This recording made no model calls, so it cost nothing. A live run charges every call (lead, subagents,
          critics, embeddings) to this ledger from the price table, against the task's budget (by default {props.budget} calls and a dollar cap).
          For scale, v1's best recorded Haiku run on one Harvey LAB task (8 documents) used 8.3 million tokens and cost $11.52.</p>
      )}
      {c.entries.length > 0 && (
        <div className="table-wrap"><table className="grid">
          <thead><tr><th>Step</th><th>Tool</th><th>Model</th><th className="num">In</th><th className="num">Out</th><th className="num">USD</th></tr></thead>
          <tbody>{c.entries.map((e, i) => <tr key={i}><td>{e.step}</td><td><code>{e.tool}</code></td><td>{e.model}</td><td className="num">{e.inputTokens}</td><td className="num">{e.outputTokens}</td><td className="num">{e.usd === null ? 'unpriced' : e.usd.toFixed(4)}</td></tr>)}</tbody>
        </table></div>
      )}
    </div>
  );
}
