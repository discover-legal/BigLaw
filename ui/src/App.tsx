import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { api, streamTask } from "./api";
import type { Task, Health, AgentSummary, LawyerProfile, Me } from "./types";
import { MODE_ACCENT, MODE_LABEL } from "./types";
import { SubmitModal } from "./SubmitModal";
import { AuditRail } from "./AuditRail";
import { Login } from "./Login";
import { ErrorBoundary } from "./pages/ErrorBoundary";
import { HomePage } from "./pages/HomePage";
import { MattersPage } from "./pages/MattersPage";
import { LibraryPage } from "./Library";
import { ClientsPanel } from "./ClientsPanel";
import { BillingPage } from "./pages/BillingPage";
import { BudgetsPage } from "./pages/BudgetsPage";
import { WatchtowerPage } from "./pages/WatchtowerPage";
import { DraftingPage } from "./pages/DraftingPage";
import { ReviewsPage } from "./pages/ReviewsPage";
import { AnalyticsPage } from "./pages/AnalyticsPage";
import { AdminPanel } from "./AdminPanel";

type Section =
  | "home" | "matters" | "library" | "clients" | "billing" | "budgets"
  | "watchtower" | "drafting" | "reviews" | "analytics" | "admin";

const NAV: Array<{ id: Section; glyph: string; label: string }> = [
  { id: "home",       glyph: "⌂", label: "Home" },
  { id: "matters",    glyph: "⚖", label: "Matters" },
  { id: "library",    glyph: "⊞", label: "Library" },
  { id: "clients",    glyph: "☷", label: "Clients" },
  { id: "billing",    glyph: "⏱", label: "Billing & Time" },
  { id: "budgets",    glyph: "◔", label: "Budgets & Deadlines" },
  { id: "watchtower", glyph: "◉", label: "Watchtower" },
  { id: "drafting",   glyph: "✎", label: "Drafting" },
  { id: "reviews",    glyph: "▦", label: "Reviews" },
  { id: "analytics",  glyph: "∿", label: "Analytics" },
  { id: "admin",      glyph: "⚙", label: "Admin" },
];

export default function App() {
  const [section, setSection] = useState<Section>("home");
  const [tasks, setTasks] = useState<Task[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [task, setTask] = useState<Task | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [agents, setAgents] = useState<AgentSummary[]>([]);
  const [me, setMe] = useState<Me | null>(null);
	const [authReady, setAuthReady] = useState(false);
  const [profiles, setProfiles] = useState<LawyerProfile[]>([]);
  const loadProfiles = useCallback(() => { api.listProfiles().then(setProfiles).catch(() => {}); }, []);
  const [libraryTab, setLibraryTab] = useState<"documents" | "upload" | "search" | undefined>(undefined);
  const [submitOpen, setSubmitOpen] = useState(false);
  const [auditOpen, setAuditOpen] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const toastTimer = useRef<number | undefined>(undefined);

  const notify = useCallback((msg: string) => {
    setToast(msg);
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(null), 3200);
  }, []);

  // Poll task list + health.
  useEffect(() => {
	if (!authReady || (me?.authEnabled && !me.user)) return;
    const load = () => api.listTasks().then((t) => {
      setTasks(t);
      setSelectedId((cur) => cur ?? (t.length ? t[0].id : null));
    }).catch(() => {});
    load();
    const iv = window.setInterval(load, 4000);
    return () => window.clearInterval(iv);
  }, [authReady, me?.authEnabled, me?.user]);

  useEffect(() => {
	if (!authReady || (me?.authEnabled && !me.user)) return;
    const load = () => api.health().then(setHealth).catch(() => setHealth(null));
    load();
    const iv = window.setInterval(load, 8000);
    return () => window.clearInterval(iv);
  }, [authReady, me?.authEnabled, me?.user]);

  // The agent registry is effectively static for a session — fetch once and
  // build an id→registered-name map so the Rounds view can label every agent
  // (including those that activated but produced no finding).
  useEffect(() => {
	api.me().then(setMe).catch(() => setMe(null)).finally(() => setAuthReady(true));
	const unauthorized = () => {
	  setTasks([]); setTask(null); setProfiles([]); setSelectedId(null);
	  setMe((current) => current ? { ...current, user: null } : current);
	};
	window.addEventListener("biglaw:unauthorized", unauthorized);
	return () => window.removeEventListener("biglaw:unauthorized", unauthorized);
  }, []);
  useEffect(() => {
	if (!authReady || (me?.authEnabled && !me.user)) return;
	api.listAgents().then(setAgents).catch(() => {});
	loadProfiles();
  }, [authReady, me?.authEnabled, me?.user, loadProfiles]);

  const isPartner = me?.user?.role === "partner";

  // Inject --accent CSS vars whenever the user's mode changes.
  useEffect(() => {
    const root = document.documentElement.style;
    const mode = me?.mode ?? "admin";
    const hex = MODE_ACCENT[mode];
    root.setProperty("--accent", hex);
    root.setProperty("--accent-bright", mode === "admin" ? "#F3CB73" : hex);
    root.setProperty("--accent-soft", `color-mix(in srgb, ${hex} 13%, transparent)`);
    root.setProperty("--accent-border", `color-mix(in srgb, ${hex} 38%, transparent)`);
  }, [me?.mode]);

  const onDeleted = useCallback((id: string) => {
    setTasks((prev) => prev.filter((t) => t.id !== id));
    setSelectedId((cur) => (cur === id ? null : cur));
    api.listTasks().then(setTasks).catch(() => {});
  }, []);

  const agentNames = useMemo(() => {
    const m = new Map<string, string>();
    for (const a of agents) m.set(a.id, a.name);
    return m;
  }, [agents]);

  // Live-track the selected task: snapshot + stream-triggered refetch.
  useEffect(() => {
	if (!authReady || (me?.authEnabled && !me.user)) return;
    if (!selectedId) { setTask(null); return; }
    let alive = true;
    const refetch = () => api.getTask(selectedId).then((t) => { if (alive) setTask(t); }).catch(() => {});
    refetch();
    const stop = streamTask(selectedId, {
      onSnapshot: (t) => { if (alive) setTask(t); },
      onPing: refetch,
    });
    return () => { alive = false; stop(); };
  }, [selectedId, authReady, me?.authEnabled, me?.user]);

  const refetchSelected = useCallback(() => {
    if (selectedId) api.getTask(selectedId).then(setTask).catch(() => {});
    api.listTasks().then(setTasks).catch(() => {});
  }, [selectedId]);

  function onCreated(t: Task) {
    setSubmitOpen(false);
    setTasks((prev) => [t, ...prev.filter((p) => p.id !== t.id)]);
    setSelectedId(t.id);
    setSection("matters");
  }

  // Which sections this principal can see. Partner-only workspaces are hidden
  // rather than rendered as a wall of 403s.
  const caps = me?.capabilities;
  const visible = useMemo(() => {
    const out = new Set<Section>(["home", "matters", "library", "drafting", "reviews", "budgets"]);
    if (isPartner && caps?.clientRoster !== false) out.add("clients");
    if (caps?.timeTracking !== false) out.add("billing");
    if (isPartner) out.add("watchtower");
    if (isPartner && caps?.matterAnalytics !== false) out.add("analytics");
    if (caps?.adminSettings !== false) out.add("admin");
    return out;
  }, [isPartner, caps]);

  // If capabilities shrink (e.g. logout/login as lawyer), fall back to Matters.
  useEffect(() => {
    if (!visible.has(section)) setSection("matters");
  }, [visible, section]);

  // A Home deep-link can preselect a Library tab; once consumed (Library has
  // mounted and read it as its initial tab), clear it so a later direct visit
  // opens on Documents rather than re-forcing the deep-linked tab.
  useEffect(() => {
    if (section === "library" && libraryTab) setLibraryTab(undefined);
  }, [section, libraryTab]);

  // Production with auth on, but no session → show the login screen.
  if (!authReady) return <div className="app-loading" role="status">Loading workspace…</div>;
  if (me?.authEnabled && !me.user) return <Login />;

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">
            <span className="big">Big</span><span className="michael">Law</span><span className="dot">.</span>
          </div>
          <div className="brand-sub">the biglaw tool stack · open &amp; free</div>
        </div>

        <div className="rail-actions">
          <button className="btn primary full" onClick={() => setSubmitOpen(true)}>＋ New matter</button>
        </div>

        <nav className="nav">
          {NAV.filter((n) => visible.has(n.id)).map((n) => (
            <button
              key={n.id}
              className={`nav-item ${section === n.id ? "active" : ""}`}
              onClick={() => setSection(n.id)}
            >
              <span className="nav-glyph">{n.glyph}</span>
              <span className="nav-label">{n.label}</span>
              {n.id === "matters" && tasks.some((t) => t.pendingGates?.some((g) => g.status === "pending")) && (
                <span className="nav-badge" title="Findings awaiting review">⚖</span>
              )}
            </button>
          ))}
        </nav>

        <div className="sidebar-foot">
          <span className="sidebar-foot-line">Big Michael convenes the bench</span>
        </div>
      </aside>

      <main className="main">
        <div className="topbar">
          <div className="health">
            <span className={`dot ${health ? "complete" : "failed"}`} />
            {health ? <>API v{health.version} · up {Math.floor(health.uptime / 60)}m</> : "API offline"}
          </div>
          <div className="health">
			{health && <>
			  <span>{health.tasks.queued} queued</span>
			  <span>{health.tasks.running} running</span>
              <span style={{ color: "var(--amber)" }}>{health.tasks.awaiting_gate} gated</span>
              <span style={{ color: "var(--green)" }}>{health.tasks.complete} done</span>
            </>}
            {me?.user && (
              <span className="whoami" title={`${me.user.email} · ${me.user.role}`}>
                {me.user.name}
                {me.user.role === "partner" && <span className="pill sm gold">partner</span>}
                {me.mode && <span className="mode-chip" data-mode={me.mode}>{MODE_LABEL[me.mode]}</span>}
                {me.authEnabled && <button className="logout" onClick={() => api.logout().then(() => location.reload())}>sign out</button>}
              </span>
            )}
          </div>
        </div>

        <div className="workspace">
          <ErrorBoundary key={section}>
            {section === "home" && (
              <HomePage
                tasks={tasks} health={health} me={me} isPartner={isPartner}
                onOpenMatter={(id) => { setSelectedId(id); setSection("matters"); }}
                onGo={(s) => setSection(s as Section)}
                onGoLibrary={(tab) => { setLibraryTab(tab); setSection("library"); }}
                onNew={() => setSubmitOpen(true)} notify={notify}
              />
            )}
            {section === "matters" && (
              <MattersPage
                tasks={tasks} selectedId={selectedId} onSelect={setSelectedId}
                task={task} agentNames={agentNames} profiles={profiles}
                isPartner={isPartner} onChange={refetchSelected} onDeleted={onDeleted}
                notify={notify} onNew={() => setSubmitOpen(true)} offline={!health}
              />
            )}
            {section === "library" && <LibraryPage notify={notify} initialMode={libraryTab} />}
            {section === "clients" && <ClientsPanel notify={notify} />}
            {section === "billing" && <BillingPage notify={notify} isPartner={isPartner} />}
            {section === "budgets" && <BudgetsPage notify={notify} isPartner={isPartner} />}
            {section === "watchtower" && <WatchtowerPage notify={notify} />}
            {section === "drafting" && <DraftingPage notify={notify} isPartner={isPartner} />}
            {section === "reviews" && <ReviewsPage notify={notify} />}
            {section === "analytics" && <AnalyticsPage notify={notify} />}
            {section === "admin" && (
              <AdminPanel
                notify={notify} isPartner={isPartner} profiles={profiles}
                onProfilesChange={loadProfiles} me={me?.user}
              />
            )}
          </ErrorBoundary>
        </div>
      </main>

      <AuditRail open={auditOpen} onToggle={() => setAuditOpen((o) => !o)} profileId={me?.user?.profileId} />

      <AnimatePresence>
        {submitOpen && <SubmitModal onClose={() => setSubmitOpen(false)} onCreated={onCreated} notify={notify} />}
      </AnimatePresence>

      <AnimatePresence>
        {toast && (
          <motion.div className="toast"
            initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: 20 }}>
            <span className="dot complete" />{toast}
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
}
