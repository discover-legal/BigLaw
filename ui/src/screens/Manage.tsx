// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Manage: the agents, tools, packs (jurisdiction and practice guidance), skills and hooks BigLaw's
// runs use, with their contents, where each is used and its recent usage; admins edit them as
// versions (structured fields or the raw files), with history, diffs, revert and an export of the
// tuned files. A save reaches the next task; a running task keeps the set it started with.
import { useEffect, useState, type ReactNode } from 'react';
import { ApiError } from '../api/transport.ts';
import type { ManageDetail, ManageItem, ManageSection, Problem, SpecVersion } from '../api/types.ts';
import { DiffView } from '../components/diff.ts';
import { isSpec, parseFront, setField, writeFront, writeJSON, type Front } from '../components/front.ts';
import { Empty, Failure, go, Loading, plural, useApi, useLoad } from '../lib.tsx';

const SECTIONS: [ManageSection | 'changes', string, string][] = [
  ['agents', 'Agents', 'The lead and meta prompts, the subagents and the engine\'s passes: prompts, turn caps, budget class, role, allowed tools and typed schemas.'],
  ['tools', 'Tools', 'The typed tools agents call. Their code is Go; their specs (description, schemas, turn cap) are editable.'],
  ['packs', 'Packs', 'Packs: jurisdiction and practice guidance the lead, analyze_issue and the committee read. Candidate until a named lawyer reviews one.'],
  ['skills', 'Skills', 'Process guidance the lead reads with use_skill: the brief lists names and descriptions only. A skill points at tools; it never grants one.'],
  ['hooks', 'Hooks', 'Go code around some tools and subagents (quote verification, the figure reader, the section check). Compiled code: view only.'],
  ['changes', 'Change log', 'Every change, newest first: who, when, and from which version to which.'],
];
const CLASSES = ['plan', 'evidence', 'draft', 'committee', 'cover', 'free'];

export function Manage(props: { section: string; item: string; tab: string }) {
  const api = useApi();
  const me = useLoad(() => api.me(), []);
  const [n, setN] = useState(0);
  const list = useLoad(() => api.manageItems(), [n]);
  const section = (SECTIONS.find(([id]) => id === props.section)?.[0] ?? 'agents');
  const items = (list.data ?? []).filter((i) => i.section === section);
  const admin = me.data?.admin ?? false;
  const all = api.exportUrl('patch');
  return (
    <section>
      <h1>Manage</h1>
      <p className="lede">What BigLaw's agents use, and where. An admin's change is a new version that the next task runs on; a running task keeps
        the versions it started with, also when it resumes.</p>
      {api.mode === 'recorded' && <p className="sample small">Read-only in the demo: this build's built-in catalogue, recorded from its files. Nothing can be saved, and no usage is recorded.</p>}
      {me.data && !admin && api.mode === 'live' && <p className="notice small">You can view everything here; only an admin can change it.</p>}
      <Failure error={list.error ?? me.error} />
      <div className="tabs" role="tablist" aria-label="Sections">
        {SECTIONS.map(([id, label]) => (
          <button key={id} role="tab" aria-selected={section === id} className={section === id ? 'on' : ''} onClick={() => go('manage', { section: id })}>
            {label}{id !== 'changes' && list.data && <span className="count">{list.data.filter((i) => i.section === id).length}</span>}
          </button>
        ))}
      </div>
      <p className="muted small">{SECTIONS.find(([id]) => id === section)?.[2]}
        {all && section !== 'changes' && list.data?.some((i) => i.version > 0 || i.deleted) && <> <a href={all}>Download every override as a patch</a> or <a href={api.exportUrl('zip') ?? ''}>a zip</a>.</>}
      </p>
      {section === 'changes' ? <Changes /> : !list.data ? <Loading label="Loading the catalogue" /> : (
        <div className="reader manage">
          <ul className="doclist" aria-label={section}>
            {items.map((i) => (
              <li key={i.id}><button className={props.item === i.id ? 'on' : ''} onClick={() => go('manage', { section, item: i.id })}>
                <span className="dl-name">{i.name}</span><span className="dl-meta"><ItemBadges item={i} /></span>
              </button></li>
            ))}
            {section === 'skills' && admin && <li><button onClick={() => go('manage', { section, item: 'new', tab: 'edit' })}><span className="dl-name">+ New skill</span></button></li>}
          </ul>
          {props.item === 'new' && section === 'skills' && admin ? <NewSkill saved={(id) => { setN(n + 1); go('manage', { section, item: id }); }} />
            : props.item && items.some((i) => i.id === props.item) ? <Detail key={props.item + n} id={props.item} tab={props.tab} admin={admin} changed={() => setN(n + 1)} />
              : <Empty icon="ledger" title="Pick an item">Each one shows its contents, where it is used and its versions.</Empty>}
        </div>
      )}
    </section>
  );
}

function ItemBadges(props: { item: ManageItem }) {
  const i = props.item;
  return (
    <>
      {i.kind}{' · '}
      {i.deleted ? <span className="badge badge-bad">deleted</span> : i.version > 0 ? <span className="badge badge-warn">override v{i.version}</span>
        : i.builtin ? <span className="badge badge-ok">built-in</span> : <span className="badge badge-near">created</span>}
      {i.status && <> <span className={`pill${i.status === 'reviewed' ? ' pill-complete' : ''}`}>{i.status}</span></>}
    </>
  );
}

function Detail(props: { id: string; tab: string; admin: boolean; changed: () => void }) {
  const api = useApi();
  const [n, setN] = useState(0);
  const d = useLoad(() => api.manageItem(props.id), [props.id, n]);
  const done = () => { setN(n + 1); props.changed(); };
  if (d.error) return <Failure error={d.error} />;
  if (!d.data) return <Loading label="Loading the item" />;
  const it = d.data;
  const tabs: [string, string][] = [['contents', 'Contents'], ...(it.editable && props.admin && !it.deleted ? [['edit', 'Edit'] as [string, string]] : []),
    ...(it.editable && api.mode === 'live' ? [['history', 'History'] as [string, string]] : [])];
  const tab = tabs.some(([t]) => t === props.tab) ? props.tab : 'contents';
  const url = (f: 'patch' | 'zip') => api.exportUrl(f, it.id);
  return (
    <article className="panel detail">
      <h2>{it.name} <code className="small">{it.id}</code></h2>
      <p className="meta"><ItemBadges item={it} />{it.latest > 0 && <span>latest version {it.latest}</span>}</p>
      <p>{it.summary}</p>
      <dl className="uses">
        {it.uses.length > 0 && <div><dt>{it.kind === 'hook' ? 'Wraps' : it.section === 'skills' ? 'Points at' : 'Calls'}</dt><dd>{it.uses.map((u) => <code key={u}>{u}</code>)}</dd></div>}
        {it.usedBy.length > 0 && <div><dt>Used by</dt><dd>{it.usedBy.map((u) => <code key={u}>{u}</code>)}</dd></div>}
        <div><dt>Recent usage</dt><dd>{it.usage ? `${plural(it.usage.steps, 'step')} in ${plural(it.usage.tasks, 'task')} of the last 20: ${plural(it.usage.calls, 'model call')}, $${it.usage.usd.toFixed(2)}${it.usage.unpriced ? ` + ${it.usage.unpriced} unpriced` : ''}`
          : <span className="muted">none in the last 20 tasks (a fan-out's own calls count under it)</span>}</dd></div>
      </dl>
      {(it.version > 0 || it.deleted) && url('patch') && <p className="small">Export this override: <a href={url('patch') ?? ''}>patch</a> · <a href={url('zip') ?? ''}>zip</a> (repo paths; open the pull request from it).</p>}
      <div className="tabs" role="tablist">
        {tabs.map(([t, label]) => <button key={t} role="tab" aria-selected={tab === t} className={tab === t ? 'on' : ''} onClick={() => go('manage', { section: it.section, item: it.id, tab: t })}>{label}</button>)}
      </div>
      {tab === 'contents' && <Contents item={it} />}
      {tab === 'edit' && <Editor item={it} saved={done} />}
      {tab === 'history' && <History item={it} admin={props.admin} reverted={done} />}
    </article>
  );
}

function Contents(props: { item: ManageDetail }) {
  const it = props.item;
  if (it.hook) {
    return (
      <>
        <p>{it.hook.what}</p>
        {it.hook.source.map((s) => <Code key={s.func} title={<><code>{s.func}</code> in <code>{s.file}</code></>} text={s.text} />)}
      </>
    );
  }
  if (it.deleted) return <p className="muted">This skill is deleted: new tasks do not list it. Its History has every earlier version to restore.</p>;
  return <>{Object.entries(it.content).map(([path, text]) => <Code key={path} title={<code>{path}</code>} text={text} />)}</>;
}

function Code(props: { title: ReactNode; text: string }) {
  return <figure className="code"><figcaption>{props.title}</figcaption><pre>{props.text}</pre></figure>;
}

/** Editor edits every file of an item: structured fields over the raw text, or the raw text itself. */
function Editor(props: { item: ManageDetail; saved: () => void; create?: string }) {
  const api = useApi();
  const it = props.item;
  const [files, setFiles] = useState<Record<string, string>>(it.content);
  const [raw, setRaw] = useState(false);
  const [note, setNote] = useState('');
  const [show, setShow] = useState(false);
  const [problems, setProblems] = useState<Problem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notes, setNotes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const tools = useLoad(() => api.manageItems().then((xs) => xs.filter((x) => x.section === 'tools' || x.id === 'pass.ledger').map((x) => x.name).sort()), []);
  const set = (path: string, text: string) => setFiles((f) => ({ ...f, [path]: text }));
  const id = props.create ?? it.id;
  const save = () => {
    setBusy(true);
    api.manageSave(id, it.latest, files, note).then((r) => { setProblems([]); setError(null); setNotes(r.notes); props.saved(); },
      (e: Error) => { setError(e.message); setProblems(e instanceof ApiError ? e.problems : []); }).finally(() => setBusy(false));
  };
  const at = (path: string) => problems.filter((p) => p.file === path);
  const skillDir = it.section === 'skills' ? (Object.keys(files).find((p) => p.endsWith('/SKILL.md')) ?? '').replace(/SKILL\.md$/, '') : '';
  return (
    <form onSubmit={(e) => { e.preventDefault(); save(); }}>
      <label className="row"><input type="checkbox" checked={raw} onChange={(e) => setRaw(e.target.checked)} /> Edit the raw files</label>
      {Object.keys(files).sort().map((path) => (
        <fieldset key={path} className="file">
          <legend><code>{path}</code>{skillDir && !path.endsWith('/SKILL.md') && <button type="button" className="link" onClick={() => setFiles(({ [path]: _, ...f }) => f)}>remove</button>}</legend>
          {at(path).map((p, k) => <p key={k} className="error small" role="alert">{p.message}</p>)}
          {raw ? <textarea rows={16} className="mono" value={files[path]} onChange={(e) => set(path, e.target.value)} aria-label={path} />
            : isSpec(path) ? <SpecFields text={files[path]} tools={tools.data ?? []} set={(t) => set(path, t)} />
              : parseFront(files[path]) && !(skillDir && !path.endsWith('/SKILL.md')) ? <FrontFields text={files[path]} skill={!!skillDir} callers={it.callers} set={(t) => set(path, t)} />
                : <textarea rows={14} value={files[path]} onChange={(e) => set(path, e.target.value)} aria-label={path} />}
        </fieldset>
      ))}
      {skillDir && <AddFile add={(name) => set(skillDir + name, '')} />}
      {problems.filter((p) => !p.file).map((p, k) => <p key={k} className="error small" role="alert">{p.message}</p>)}
      {error && problems.length === 0 && <Failure error={error} />}
      {notes.map((x) => <p key={x} className="notice small">{x}</p>)}
      <label className="block">Note (optional)<input value={note} onChange={(e) => setNote(e.target.value)} maxLength={2000} placeholder="Why this change" /></label>
      <div className="row">
        <button className="primary" disabled={busy}>{busy ? 'Saving…' : `Save as version ${it.latest + 1}`}</button>
        <button type="button" onClick={() => setShow(!show)}>{show ? 'Hide changes' : 'Show changes'}</button>
        <button type="button" onClick={() => { setFiles(it.content); setProblems([]); setError(null); }}>Discard edits</button>
      </div>
      {show && <DiffView files={[...new Set([...Object.keys(it.content), ...Object.keys(files)])].sort().map((p) => ({ path: p, a: it.content[p] ?? '', b: files[p] ?? '' }))} empty="No unsaved changes." />}
    </form>
  );
}

function AddFile(props: { add: (name: string) => void }) {
  const [name, setName] = useState('');
  return (
    <div className="row">
      <input value={name} onChange={(e) => setName(e.target.value)} placeholder="reference.md, .json or .csv" aria-label="New reference file name" />
      <button type="button" disabled={!name.trim()} onClick={() => { props.add(name.trim()); setName(''); }}>Add reference file</button>
    </div>
  );
}

interface SpecDoc { kind?: string; description?: string; maxTurns?: number; class?: string; role?: string; allowed?: string[]; in?: unknown; out?: unknown }

/** SpecFields edits a spec's description, turn cap, class, role, allowed tools and schemas. */
function SpecFields(props: { text: string; tools: string[]; set: (text: string) => void }) {
  let spec: SpecDoc;
  try { spec = JSON.parse(props.text) as SpecDoc; } catch { return <p className="error small">Not well-formed JSON: edit the raw file.</p>; }
  const put = (patch: Partial<SpecDoc>) => props.set(writeJSON(props.text, { ...spec, ...patch }));
  const sub = spec.kind === 'subagent';
  return (
    <div className="fields">
      <label className="block">Description (the model reads it: 3+ sentences, a "Do not use" line, 1–3 Example lines)
        <textarea rows={6} value={spec.description ?? ''} onChange={(e) => put({ description: e.target.value })} /></label>
      <div className="row">
        <label>Max turns <input type="number" min={sub ? 1 : 0} max={sub ? 32 : 10000} value={spec.maxTurns ?? 0} onChange={(e) => put({ maxTurns: Number(e.target.value) })} /></label>
        <label>Budget class <select value={spec.class} onChange={(e) => put({ class: e.target.value })}>{CLASSES.map((c) => <option key={c}>{c}</option>)}</select></label>
        {sub && <label>Model role <select value={spec.role} onChange={(e) => put({ role: e.target.value })}><option>specialist</option><option>critic</option></select></label>}
      </div>
      {sub && (
        <fieldset className="checks"><legend>Allowed tools</legend>
          {props.tools.map((t) => (
            <label key={t}><input type="checkbox" checked={spec.allowed?.includes(t) ?? false}
              onChange={(e) => put({ allowed: e.target.checked ? [...(spec.allowed ?? []), t] : (spec.allowed ?? []).filter((x) => x !== t) })} /> {t}</label>
          ))}
        </fieldset>
      )}
      {(['in', 'out'] as const).map((k) => <SchemaField key={k} label={k === 'in' ? 'Input schema' : 'Output schema'} value={spec[k]} set={(v) => put({ [k]: v })} />)}
    </div>
  );
}

function SchemaField(props: { label: string; value: unknown; set: (v: unknown) => void }) {
  const [text, setText] = useState(() => JSON.stringify(props.value, null, 1));
  const [bad, setBad] = useState(false);
  useEffect(() => { setText(JSON.stringify(props.value, null, 1)); }, [JSON.stringify(props.value)]);
  return (
    <label className="block">{props.label} (JSON Schema, BigLaw's subset; Go checks it still fits what it reads and writes)
      <textarea rows={8} className="mono" value={text} aria-invalid={bad} onChange={(e) => {
        setText(e.target.value);
        try { props.set(JSON.parse(e.target.value)); setBad(false); } catch { setBad(true); }
      }} />
      {bad && <span className="bad-text small">Not JSON yet: this schema is not applied until it parses.</span>}
    </label>
  );
}

/** FrontFields edits a pack's or skill's front matter and body. */
function FrontFields(props: { text: string; skill: boolean; callers: string[]; set: (text: string) => void }) {
  const f = parseFront(props.text) as Front;
  const put = (k: string, v: string | string[]) => props.set(writeFront(setField(f, k, v)));
  return (
    <div className="fields">
      {props.skill && <label className="block">Description (one line; the brief lists it)<input value={f.fields.description ?? ''} onChange={(e) => put('description', e.target.value)} maxLength={200} /></label>}
      <div className="row">
        <label>Status <select value={f.fields.status} onChange={(e) => put('status', e.target.value)}><option>candidate</option><option>reviewed</option></select></label>
        <label>Reviewed by <input value={f.fields.reviewedBy ?? ''} onChange={(e) => put('reviewedBy', e.target.value)} placeholder="the reviewing lawyer" /></label>
      </div>
      <p className="muted small">Changing the content resets the status to candidate unless you name a new reviewer.</p>
      {props.skill ? (
        <fieldset className="checks"><legend>Tools it points at (only tools its readers may already call)</legend>
          {props.callers.map((t) => <label key={t}><input type="checkbox" checked={f.lists.tools?.includes(t) ?? false}
            onChange={(e) => put('tools', e.target.checked ? [...(f.lists.tools ?? []), t] : (f.lists.tools ?? []).filter((x) => x !== t))} /> {t}</label>)}
        </fieldset>
      ) : (
        <label className="block">Checklist (one item a line)<textarea rows={5} value={(f.lists.checklist ?? []).join('\n')} onChange={(e) => put('checklist', e.target.value.split('\n'))} /></label>
      )}
      <label className="block">{props.skill ? 'Instructions' : 'Body'}<textarea rows={12} value={f.body} onChange={(e) => props.set(writeFront({ ...f, body: e.target.value.trim() }))} /></label>
    </div>
  );
}

const SKILL_TEMPLATE = (name: string) => `---\nname: ${name}\ndescription: \nstatus: candidate\nreviewedBy:\ntools:\n---\nWrite the instructions here.\n`;

function NewSkill(props: { saved: (id: string) => void }) {
  const api = useApi();
  const [name, setName] = useState('');
  const ref = useLoad(() => api.manageItem('tool.use_skill'), []); // every item carries the tools a skill may name
  const ok = /^[a-z][a-z0-9-]{1,39}$/.test(name);
  const blank: ManageDetail = { id: `skill.${name}`, section: 'skills', kind: 'skill', name, summary: '', editable: true, builtin: false, version: 0, latest: 0, deleted: false,
    files: [], uses: [], usedBy: [], usage: null, content: { [`internal/engine/skills/${name}/SKILL.md`]: SKILL_TEMPLATE(name) }, original: {}, callers: ref.data?.callers ?? [] };
  return (
    <article className="panel detail">
      <h2>New skill</h2>
      <label className="block">Name (2–40 lower-case letters, digits and -)<input value={name} onChange={(e) => setName(e.target.value.trim())} placeholder="chronology" /></label>
      {ok ? <Editor key={name + (ref.data ? 1 : 0)} item={blank} create={`skill.${name}`} saved={() => props.saved(`skill.${name}`)} />
        : <p className="muted small">Name the skill to write it.</p>}
    </article>
  );
}

/** History lists an item's versions; pick any two (or the built-in) to diff, and revert to any. */
function History(props: { item: ManageDetail; admin: boolean; reverted: () => void }) {
  const api = useApi();
  const it = props.item;
  const h = useLoad(() => api.manageHistory(it.id), [it.id, it.latest]);
  const [a, setA] = useState(0);
  const [b, setB] = useState(-1);
  const [error, setError] = useState<string | null>(null);
  if (h.error) return <Failure error={h.error} />;
  if (!h.data) return <Loading label="Loading the history" />;
  const vs = h.data;
  const filesOf = (n: number) => (n === 0 ? (it.original ?? {}) : n < 0 ? (vs[0]?.files ?? it.content) : (vs.find((v) => v.version === n)?.files ?? {}));
  const to = b < 0 ? (vs[0]?.version ?? 0) : b;
  const revert = (n: number) => api.manageRevert(it.id, n, it.latest, n === 0 ? 'revert to the built-in' : `revert to version ${n}`)
    .then(() => { setError(null); props.reverted(); }, (e: Error) => setError(e.message));
  const del = () => api.manageDelete(it.id, it.latest, 'deleted').then(() => { setError(null); props.reverted(); }, (e: Error) => setError(e.message));
  const rows: (SpecVersion | null)[] = [...vs, null]; // null: the built-in
  return (
    <>
      <Failure error={error} />
      {vs.length === 0 && <p className="muted">No versions yet: new tasks run on the built-in.</p>}
      <div className="table-wrap"><table className="grid">
        <thead><tr><th>Version</th><th>What</th><th>Who</th><th>When</th><th>Diff: from</th><th>to</th>{props.admin && <th />}</tr></thead>
        <tbody>{rows.map((v) => {
          const n = v?.version ?? 0;
          return (
            <tr key={n}>
              <td>{v ? `v${n}` : 'built-in'}{v && n === it.version && !it.deleted ? ' (in use)' : ''}{!v && it.version === 0 && !it.deleted ? ' (in use)' : ''}</td>
              <td>{v ? <>{v.action}{v.restored !== null && ` to ${v.restored ? `v${v.restored}` : 'the built-in'}`}{v.deleted && ' (deleted)'}{v.note && <span className="muted"> — {v.note}</span>}</> : it.builtin ? 'as shipped' : 'none (created here)'}</td>
              <td>{v?.author ?? ''}</td><td className="nowrap">{v ? new Date(v.createdAt).toLocaleString() : ''}</td>
              <td><input type="radio" name="from" checked={a === n} onChange={() => setA(n)} aria-label={`diff from ${n || 'built-in'}`} /></td>
              <td><input type="radio" name="to" checked={to === n} onChange={() => setB(n)} aria-label={`diff to ${n || 'built-in'}`} /></td>
              {props.admin && <td>{(v ? n !== it.latest : vs.length > 0 && it.version !== 0) && <button className="small" onClick={() => void revert(n)}>Revert to this</button>}</td>}
            </tr>
          );
        })}</tbody>
      </table></div>
      {props.admin && it.section === 'skills' && !it.deleted && <p><button className="danger" onClick={() => void del()}>Delete this skill</button> <span className="muted small">A deleted skill is a version too: revert restores it.</span></p>}
      <h3>{a ? `v${a}` : 'Built-in'} → {to ? `v${to}` : 'built-in'}</h3>
      <DiffView files={[...new Set([...Object.keys(filesOf(a)), ...Object.keys(filesOf(to))])].sort().map((p) => ({ path: p, a: filesOf(a)[p] ?? '', b: filesOf(to)[p] ?? '' }))} />
    </>
  );
}

function Changes() {
  const api = useApi();
  const r = useLoad(() => api.manageChanges(), []);
  if (r.error) return <Failure error={r.error} />;
  if (!r.data) return <Loading label="Loading the change log" />;
  if (r.data.length === 0) return <Empty icon="ledger" title="No changes yet">Every task runs on the built-in agents, tools, packs and skills.</Empty>;
  return (
    <div className="table-wrap"><table className="grid stack">
      <thead><tr><th>When</th><th>Item</th><th>Change</th><th>Who</th><th>Note</th></tr></thead>
      <tbody>{r.data.map((v) => (
        <tr key={`${v.item}-${v.version}`}>
          <td className="nowrap">{new Date(v.createdAt).toLocaleString()}</td>
          <td><a href={`#/manage?section=${sectionOf(v.item)}&item=${encodeURIComponent(v.item)}&tab=history`}>{v.item}</a></td>
          <td data-label="Change">{v.action}: {v.from ? `v${v.from}` : 'built-in'} → {v.builtin ? 'built-in' : `v${v.version}`}{v.deleted ? ' (deleted)' : ''}</td>
          <td data-label="Who">{v.author}</td><td className="wide">{v.note}</td>
        </tr>
      ))}</tbody>
    </table></div>
  );
}

const sectionOf = (id: string) => ({ prompt: 'agents', agent: 'agents', pass: 'agents', tool: 'tools', pack: 'packs', skill: 'skills' } as Record<string, string>)[id.split('.')[0]] ?? 'agents';
