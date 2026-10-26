// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// The shell: the seven screens and Manage (README § UI screens), hash routes so a static bundle works under
// any base path, the demo banner when the engine is recorded, and with sign-in (README § Sign-in) a
// sign-in page until there is a session, then the user and Sign out in the header.
import { useEffect, useState } from 'react';
import { ApiError, type Transport } from './api/transport.ts';
import type { Me } from './api/types.ts';
import { Api, parseRoute } from './lib.tsx';
import { Icon } from './components/icons.tsx';
import { Matters } from './screens/Matters.tsx';
import { TaskScreen } from './screens/Task.tsx';
import { Documents } from './screens/Documents.tsx';
import { Billing, Clients, Playbooks, Settings } from './screens/Others.tsx';
import { Manage } from './screens/Manage.tsx';

const NAV: [string, string][] = [
  ['matters', 'Matters'], ['documents', 'Documents'], ['playbooks', 'Playbooks'],
  ['clients', 'Clients & Conflicts'], ['billing', 'Time & Billing'], ['manage', 'Manage'], ['settings', 'Settings'],
];

const logo = <svg width="22" height="22" viewBox="0 0 24 24" aria-hidden="true"><rect width="24" height="24" rx="6" fill="currentColor" /><path d="M7 7v10h5.5a3 3 0 0 0 0-6H7m0 0h4.5a2 2 0 0 0 0-4H7" fill="none" stroke="var(--accent-ink)" strokeWidth="2" strokeLinejoin="round" /></svg>;

/** SignIn is the whole page when sign-in is on and there is no session (GET /me is 401). */
function SignIn() {
  return (
    <main className="signin">
      <div className="panel">
        <p className="brand">{logo}BigLaw<sup>2</sup></p>
        <h1>Sign in</h1>
        <p className="muted">This server signs you in with your firm's identity provider. Everyone signed in can run tasks and decide gates; admins also change what its agents use.</p>
        <a className="button primary" href={`/auth/login?next=${encodeURIComponent('/' + window.location.hash)}`}>Sign in with your firm account</a>
      </div>
    </main>
  );
}

export function App(props: { api: Transport }) {
  const [route, setRoute] = useState(() => parseRoute(window.location.hash));
  const [about, setAbout] = useState(false);
  const [me, setMe] = useState<Me | 'signin' | null>(); // undefined until /me answers: no screen asks the API before
  useEffect(() => { props.api.me().then(setMe, (e: unknown) => setMe(e instanceof ApiError && e.status === 401 ? 'signin' : null)); }, [props.api]);
  useEffect(() => {
    const on = () => { setRoute(parseRoute(window.location.hash)); window.scrollTo(0, 0); };
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  const [screen = 'matters', arg] = route.path;
  const active = screen === 'task' || !NAV.some(([id]) => id === screen) ? 'matters' : screen;
  if (me === 'signin') return <SignIn />;
  if (me === undefined) return null;
  let body;
  switch (screen) {
    case 'task': body = <TaskScreen id={arg ?? ''} tab={route.q.get('tab') ?? 'run'} />; break;
    case 'documents': body = <Documents q={route.q} />; break;
    case 'playbooks': body = <Playbooks />; break;
    case 'clients': body = <Clients />; break;
    case 'billing': body = <Billing />; break;
    case 'settings': body = <Settings />; break;
    case 'manage': body = <Manage section={route.q.get('section') ?? 'agents'} item={route.q.get('item') ?? ''} tab={route.q.get('tab') ?? 'contents'} />; break;
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
            {logo}BigLaw<sup>2</sup>
          </a>
          <nav aria-label="Screens">
            {NAV.map(([id, label]) => (
              <a key={id} href={`#/${id}`} className={active === id ? 'on' : ''} aria-current={active === id ? 'page' : undefined}>{label}</a>
            ))}
          </nav>
          {me?.authEnabled && (
            <div className="user">
              <span title={me.email}>{me.name}</span><span className="badge">{me.admin ? 'admin' : 'member'}</span>
              <button className="small" onClick={() => void props.api.signOut?.().finally(() => window.location.reload())}>Sign out</button>
            </div>
          )}
        </div>
      </header>
      <main className="shell">{body}</main>
    </Api.Provider>
  );
}
