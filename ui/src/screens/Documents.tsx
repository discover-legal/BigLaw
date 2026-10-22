// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Documents: the library, a document's outline, the section reader (with a cited quote
// highlighted) and search within a matter's documents.
import { useEffect, useMemo, useRef, useState } from 'react';
import type { Doc, Hit } from '../api/types.ts';
import { Failure, go, useApi, useLoad } from '../lib.tsx';

/** split cuts text around the first occurrence of mark, for highlighting ("" or absent: no mark). */
export function split(text: string, mark: string): [string, string, string] | null {
  const i = mark ? text.indexOf(mark) : -1;
  return i < 0 ? null : [text.slice(0, i), mark, text.slice(i + mark.length)];
}

export function Documents(props: { q: URLSearchParams }) {
  const api = useApi();
  const docs = useLoad(() => api.documents(), []);
  const docId = props.q.get('doc') ?? '';
  const sectionId = props.q.get('section') ?? '';
  const hl = props.q.get('hl') ?? '';
  const matter = props.q.get('matter') ?? docs.data?.find((d) => d.id === docId)?.matterNumber ?? docs.data?.[0]?.matterNumber ?? '';
  const inMatter = (docs.data ?? []).filter((d) => !matter || d.matterNumber === matter);
  const matters = [...new Set((docs.data ?? []).map((d) => d.matterNumber ?? ''))].filter(Boolean);
  const nav = (q: Record<string, string | undefined>) => go('documents', { matter, ...q });

  return (
    <section>
      <h1>Documents</h1>
      <Failure error={docs.error} />
      <div className="row">
        {matters.length > 1 && (
          <label>Matter <select value={matter} onChange={(e) => go('documents', { matter: e.target.value })}>
            {matters.map((m) => <option key={m}>{m}</option>)}
          </select></label>
        )}
        {api.mode === 'live' && <span className="muted">Upload: POST /documents/upload (multipart), or POST /documents with text.</span>}
      </div>
      <Search ids={inMatter.map((d) => d.id)} docs={inMatter} onOpen={(h) => nav({ doc: h.documentId, section: h.sectionId, hl: h.text.slice(0, 120) })} />
      <div className="reader">
        <ul className="doclist" aria-label="Documents">
          {inMatter.map((d) => (
            <li key={d.id}><button className={d.id === docId ? 'on' : ''} onClick={() => nav({ doc: d.id })}>
              <span>{d.name}</span><span className="muted">{d.sections} sections · {d.method}{d.status !== 'ready' ? ` · ${d.status}` : ''}</span>
            </button></li>
          ))}
        </ul>
        {docId ? <Reader key={docId} doc={inMatter.find((d) => d.id === docId) ?? null} docId={docId} sectionId={sectionId} hl={hl}
          onSection={(s) => nav({ doc: docId, section: s })} /> : <p className="muted">Pick a document to read its outline.</p>}
      </div>
    </section>
  );
}

function Search(props: { ids: string[]; docs: Doc[]; onOpen: (h: Hit) => void }) {
  const api = useApi();
  const [q, setQ] = useState('');
  const [hits, setHits] = useState<Hit[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const name = (id: string) => props.docs.find((d) => d.id === id)?.name ?? id;
  return (
    <form className="search" role="search" onSubmit={(e) => { e.preventDefault(); setError(null); api.search(q, props.ids).then(setHits, (er: Error) => setError(er.message)); }}>
      <input aria-label="Search this matter's documents" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search this matter's documents" />
      <button>Search</button>
      {api.mode === 'recorded' && <span className="muted small">Lexical search in your browser over the recorded chunks.</span>}
      <Failure error={error} />
      {hits && (hits.length === 0 ? <p className="muted">No hits.</p> : (
        <ol className="hits">
          {hits.map((h, i) => (
            <li key={i}><button type="button" onClick={() => props.onOpen(h)}>
              <b>{name(h.documentId)}</b> <span className="muted">{h.title}</span>
              <span className="snippet">{h.text.length > 240 ? `${h.text.slice(0, 240)}…` : h.text}</span>
            </button></li>
          ))}
        </ol>
      ))}
    </form>
  );
}

function Reader(props: { doc: Doc | null; docId: string; sectionId: string; hl: string; onSection: (s: string) => void }) {
  const api = useApi();
  const outline = useLoad(() => api.outline(props.docId), [props.docId]);
  const sid = props.sectionId || outline.data?.[0]?.sectionId || '';
  const page = useLoad(() => (sid ? api.section(props.docId, sid) : Promise.resolve(null)), [props.docId, sid]);
  const mark = useRef<HTMLElement>(null);
  const parts = useMemo(() => (page.data ? split(page.data.text, props.hl) : null), [page.data, props.hl]);
  useEffect(() => { mark.current?.scrollIntoView({ block: 'center' }); }, [parts]);
  return (
    <>
      <nav className="outline" aria-label="Outline">
        <Failure error={outline.error} />
        {outline.data?.map((o) => (
          <button key={o.sectionId} className={o.sectionId === sid ? 'on' : ''} style={{ paddingLeft: `${0.5 + o.level * 0.8}rem` }} onClick={() => props.onSection(o.sectionId)}>
            {o.number} {o.title || (o.level === 0 ? '(preface)' : '')}
          </button>
        ))}
      </nav>
      <article className="panel section" aria-label="Section">
        <Failure error={page.error} />
        {props.doc && <p className="eyebrow">{props.doc.name}</p>}
        {page.data && <h2>{page.data.number} {page.data.title}</h2>}
        {props.hl && page.data && !parts && <p className="notice">The quoted text is not in this section.</p>}
        {page.data && (
          <pre className="text">{parts ? <>{parts[0]}<mark ref={mark}>{parts[1]}</mark>{parts[2]}</> : page.data.text}</pre>
        )}
        {page.data?.truncated && <p className="muted">The section continues (paged reader: offset {page.data.next}).</p>}
      </article>
    </>
  );
}
