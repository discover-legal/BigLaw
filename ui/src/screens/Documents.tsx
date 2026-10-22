// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Documents: the library, a document's outline, the section reader (with a cited quote
// highlighted) and search within a matter's documents.
import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { snippet } from '../api/match.ts';
import type { Doc, Hit } from '../api/types.ts';
import { Failure, go, plural, useApi, useLoad } from '../lib.tsx';

/** split cuts text around the first occurrence of mark, for highlighting ("" or absent: no mark). */
export function split(text: string, mark: string): [string, string, string] | null {
  const i = mark ? text.indexOf(mark) : -1;
  return i < 0 ? null : [text.slice(0, i), mark, text.slice(i + mark.length)];
}

export function Documents(props: { q: URLSearchParams }) {
  const api = useApi();
  const docs = useLoad(() => api.documents(), []);
  const clients = useLoad(() => api.clients(), []);
  const docId = props.q.get('doc') ?? '';
  const sectionId = props.q.get('section') ?? '';
  const hl = props.q.get('hl') ?? '';
  const matter = props.q.get('matter') ?? docs.data?.find((d) => d.id === docId)?.matterNumber ?? docs.data?.[0]?.matterNumber ?? '';
  const inMatter = (docs.data ?? []).filter((d) => !matter || d.matterNumber === matter);
  const matters = [...new Set((docs.data ?? []).map((d) => d.matterNumber ?? ''))].filter(Boolean);
  const title = (n: string) => clients.data?.flatMap((c) => c.matters).find((m) => m.number === n)?.title;
  const nav = (q: Record<string, string | undefined>) => go('documents', { matter, ...q });

  return (
    <section>
      <h1>Documents</h1>
      <Failure error={docs.error} />
      <div className="row">
        {matters.length > 1 && (
          <label>Matter <select value={matter} onChange={(e) => go('documents', { matter: e.target.value })}>
            {matters.map((m) => <option key={m} value={m}>{m}{title(m) ? ` · ${title(m)}` : ''}</option>)}
          </select></label>
        )}
        {api.mode === 'live' && <span className="muted">Upload: POST /documents/upload (multipart), or POST /documents with text.</span>}
      </div>
      <Search key={matter} matter={matter} ids={inMatter.map((d) => d.id)} docs={inMatter}
        onOpen={(h, hl) => nav({ doc: h.documentId, section: h.sectionId, hl })} />
      <div className="reader">
        <ul className="doclist" aria-label="Documents">
          {inMatter.map((d) => (
            <li key={d.id}><button className={d.id === docId ? 'on' : ''} onClick={() => nav({ doc: d.id })}>
              <span>{d.name}</span>
              <span className="muted">{plural(d.sections, 'section')} · {d.method === 'vision' ? 'scan, transcribed' : d.method}{d.status !== 'ready' ? ` · ${d.status}` : ''}</span>
            </button></li>
          ))}
        </ul>
        {docId ? <Reader key={docId} doc={inMatter.find((d) => d.id === docId) ?? null} docId={docId} sectionId={sectionId} hl={hl}
          onSection={(s) => nav({ doc: docId, section: s })} /> : <p className="muted pick">Pick a document to read it, or search above.</p>}
      </div>
    </section>
  );
}

function Search(props: { matter: string; ids: string[]; docs: Doc[]; onOpen: (h: Hit, hl: string) => void }) {
  const api = useApi();
  const [q, setQ] = useState('');
  const [ran, setRan] = useState('');
  const [hits, setHits] = useState<Hit[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const name = (id: string) => props.docs.find((d) => d.id === id)?.name ?? id;
  const recorded = api.recordedSearches?.(props.matter) ?? [];
  const replayed = recorded.some((s) => s.query === ran.trim());
  const run = (query: string) => {
    setQ(query); setError(null);
    api.search(query, props.ids).then((h) => { setHits(h); setRan(query); }, (er: Error) => setError(er.message));
  };
  return (
    <form className="search" role="search" onSubmit={(e) => { e.preventDefault(); if (q.trim()) run(q); }}>
      <div className="search-bar">
        <input type="search" aria-label="Search this matter's documents" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search this matter's documents" />
        <button>Search</button>
      </div>
      {recorded.length > 0 && (
        <div className="chips">
          <span className="muted small">Recorded searches:</span>
          {recorded.map((s) => <button type="button" key={s.query} className="chip" onClick={() => run(s.query)}>{s.query}</button>)}
        </div>
      )}
      <Failure error={error} />
      {hits && (
        <p className="muted small" role="status">
          {plural(hits.length, 'hit')} for “{ran}”.{' '}
          {api.mode === 'recorded' && (replayed
            ? 'Ranked by v2’s own search when the demo was recorded.'
            : 'Ranked in your browser by a port of v2’s lexical formula; the server stems words properly, so its order can differ. The recorded searches show its exact ranking.')}
        </p>
      )}
      {hits && hits.length > 0 && (
        <ol className="hits">
          {hits.map((h, i) => {
            const sn = snippet(h.text, ran);
            return (
              <li key={i}><button type="button" onClick={() => { setHits(null); props.onOpen(h, sn.text.length > 160 ? sn.text.slice(0, Math.max(sn.text.lastIndexOf(' ', 160), 80)) : sn.text); }}>
                <span><b>{name(h.documentId)}</b>{h.transcribed && <span className="badge badge-warn">transcript</span>}</span>
                {h.title && <span className="muted small">{h.title}</span>}
                <span className="snippet">{sn.cut[0] && '…'}<Marked text={sn.text} marks={sn.marks} />{sn.cut[1] && '…'}</span>
              </button></li>
            );
          })}
        </ol>
      )}
    </form>
  );
}

/** Marked shows text with every whole-word occurrence of marks highlighted. */
function Marked(props: { text: string; marks: string[] }) {
  if (props.marks.length === 0) return <>{props.text}</>;
  const esc = props.marks.map((m) => m.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
  const parts = props.text.split(new RegExp(`(?<![\\p{L}\\p{N}])(${esc.join('|')})(?![\\p{L}\\p{N}])`, 'u'));
  return <>{parts.map((p, i) => (i % 2 ? <mark key={i}>{p}</mark> : <Fragment key={i}>{p}</Fragment>))}</>;
}

function Reader(props: { doc: Doc | null; docId: string; sectionId: string; hl: string; onSection: (s: string) => void }) {
  const api = useApi();
  const outline = useLoad(() => api.outline(props.docId), [props.docId]);
  const sid = props.sectionId || outline.data?.[0]?.sectionId || '';
  const page = useLoad(() => (sid ? api.section(props.docId, sid) : Promise.resolve(null)), [props.docId, sid]);
  const mark = useRef<HTMLElement>(null);
  const parts = useMemo(() => (page.data ? split(page.data.text, props.hl) : null), [page.data, props.hl]);
  useEffect(() => { mark.current?.scrollIntoView({ block: 'center' }); }, [parts]);
  const file = api.fileUrl(props.docId);
  const only = outline.data?.length === 1;
  return (
    <>
      <nav className="outline" aria-label="Outline">
        <Failure error={outline.error} />
        {outline.data?.map((o) => (
          <button key={o.sectionId} className={o.sectionId === sid ? 'on' : ''} style={{ paddingLeft: `${0.5 + o.level * 0.8}rem` }} onClick={() => props.onSection(o.sectionId)}>
            {o.number} {o.title || (only ? '(the whole document)' : o.level === 0 ? '(preface)' : '(untitled)')}
          </button>
        ))}
      </nav>
      <article className="panel section" aria-label="Section">
        <Failure error={page.error} />
        {props.doc && <p className="eyebrow">{props.doc.name}</p>}
        {props.doc?.transcript && (
          <div className="notice">
            <p><strong>Machine transcript of a scan.</strong> Quotes in it verify at most as <code>@transcript</code> (confidence capped at 85%); check them against the original.
              {file && <> <a href={file} target="_blank" rel="noopener">Open the original scan</a>.</>}</p>
            {props.doc.notes.map((n) => <p key={n} className="small">{n}</p>)}
          </div>
        )}
        {page.data && (page.data.title || page.data.number) && <h2>{page.data.number} {page.data.title}</h2>}
        {props.hl && page.data && !parts && <p className="notice">The quoted text is not in this section.</p>}
        {page.data && (
          <pre className="text">{parts ? <>{parts[0]}<mark ref={mark}>{parts[1]}</mark>{parts[2]}</> : page.data.text}</pre>
        )}
        {page.data?.truncated && <p className="muted">The section continues (paged reader: offset {page.data.next}).</p>}
      </article>
    </>
  );
}
