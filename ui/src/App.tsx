// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The shell: the seven screens (README § UI screens), hash routes so a static bundle works under
// any base path, and the demo banner when the engine is recorded.
import { useEffect, useState } from 'react';
import type { Transport } from './api/transport.ts';
import { Api, parseRoute } from './lib.tsx';
import { Icon } from './components/icons.tsx';
import { Matters } from './screens/Matters.tsx';
import { TaskScreen } from './screens/Task.tsx';
import { Documents } from './screens/Documents.tsx';
import { Billing, Clients, Playbooks, Settings } from './screens/Others.tsx';

const NAV: [string, string][] = [
  ['matters', 'Matters'], ['documents', 'Documents'], ['playbooks', 'Playbooks'],
  ['clients', 'Clients & Conflicts'], ['billing', 'Time & Billing'], ['settings', 'Settings'],
];

export function App(props: { api: Transport }) {
  const [route, setRoute] = useState(() => parseRoute(window.location.hash));
  const [about, setAbout] = useState(false);
  useEffect(() => {
    const on = () => { setRoute(parseRoute(window.location.hash)); window.scrollTo(0, 0); };
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  const [screen = 'matters', arg] = route.path;
  const active = screen === 'task' || !NAV.some(([id]) => id === screen) ? 'matters' : screen;
  let body;
  switch (screen) {
    case 'task': body = <TaskScreen id={arg ?? ''} tab={route.q.get('tab') ?? 'run'} />; break;
    case 'documents': body = <Documents q={route.q} />; break;
    case 'playbooks': body = <Playbooks />; break;
    case 'clients': body = <Clients />; break;
    case 'billing': body = <Billing />; break;
    case 'settings': body = <Settings />; break;
    default: body = <Matters matter={route.q.get('matter') ?? ''} />;
  }
  return (
    <Api.Provider value={props.api}>
      {props.api.provenance && (
        <div className="demo-banner" role="note">
          <div className="demo-line">
            {props.api.home && <a className="home" href={props.api.home}><Icon name="back" size={13} />discover.legal</a>}
            <span><span className="rec-dot" aria-hidden="true" /><strong>Recorded run; not live.</strong> No model is called and nothing is saved. Fictional matters; not legal advice.</span>
            <button className="link" onClick={() => setAbout(!about)} aria-expanded={about} aria-controls="how">{about ? 'Hide' : 'How this demo works'}</button>
          </div>
          {about && <p id="how" className="provenance">{props.api.provenance}</p>}
        </div>
      )}
      <header className="top">
        <div className="top-in">
          <a className="brand" href="#/matters" aria-label="BigLaw 2, Matters">
            <svg width="22" height="22" viewBox="0 0 24 24" aria-hidden="true"><rect width="24" height="24" rx="6" fill="currentColor" /><path d="M7 7v10h5.5a3 3 0 0 0 0-6H7m0 0h4.5a2 2 0 0 0 0-4H7" fill="none" stroke="var(--accent-ink)" strokeWidth="2" strokeLinejoin="round" /></svg>
            BigLaw<sup>2</sup>
          </a>
          <nav aria-label="Screens">
            {NAV.map(([id, label]) => (
              <a key={id} href={`#/${id}`} className={active === id ? 'on' : ''} aria-current={active === id ? 'page' : undefined}>{label}</a>
            ))}
          </nav>
        </div>
      </header>
      <main className="shell">{body}</main>
    </Api.Provider>
  );
}
