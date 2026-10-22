// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The small Markdown subset BigLaw deliverables use: #/## headings, "- " lists, paragraphs,
// **bold**, and [F-id] citations rendered as buttons. Text is never injected as HTML.
import type { ReactNode } from 'react';

function inline(text: string, onFid: (fid: string) => void): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /\*\*([^*]+)\*\*|\[(F\d+)\]/g;
  let last = 0, k = 0;
  for (let m = re.exec(text); m; m = re.exec(text)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    if (m[1] !== undefined) out.push(<strong key={k++}>{m[1]}</strong>);
    else { const fid = m[2]; out.push(<button key={k++} className="fid" onClick={() => onFid(fid)}>{fid}</button>); }
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function Markdown(props: { text: string; onFid: (fid: string) => void }) {
  const blocks: ReactNode[] = [];
  let list: ReactNode[] = [], para: string[] = [];
  const flush = () => {
    if (para.length) blocks.push(<p key={blocks.length}>{para.map((l, i) => [i > 0 && <br key={`b${i}`} />, ...inline(l, props.onFid)])}</p>);
    if (list.length) blocks.push(<ul key={blocks.length}>{list}</ul>);
    para = []; list = [];
  };
  for (const line of props.text.split('\n')) {
    const h = /^(#{1,3}) (.*)$/.exec(line);
    if (h) { flush(); const L = `h${h[1].length + 1}` as 'h2'; blocks.push(<L key={blocks.length}>{inline(h[2], props.onFid)}</L>); }
    else if (line.startsWith('- ')) { if (para.length) { const l = list; list = []; flush(); list = l; } list.push(<li key={list.length}>{inline(line.slice(2), props.onFid)}</li>); }
    else if (line.trim() === '') flush();
    else { if (list.length) flush(); para.push(line.replace(/ {2}$/, '')); }
  }
  flush();
  return <div className="md">{blocks}</div>;
}
