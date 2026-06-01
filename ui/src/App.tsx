import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { api, streamTask } from "./api";
import type { Task, Health, AgentSummary, LawyerProfile, Me } from "./types";
import { StatusDot, WorkflowPill, timeAgo } from "./primitives";
import { TaskView } from "./TaskView";
import { SubmitModal } from "./SubmitModal";
import { Library } from "./Library";
import { AuditRail } from "./AuditRail";
import { AdminPanel } from "./AdminPanel";
import { Login } from "./Login";

export default function App() {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [task, setTask] = useState<Task | null>(null);
  const [health, setHealth] = useState<Health | null>(null);
  const [agents, setAgents] = useState<AgentSummary[]>([]);
  const [me, setMe] = useState<Me | null>(null);
  const [profiles, setProfiles] = useState<LawyerProfile[]>([]);
  const loadProfiles = useCallback(() => { api.listProfiles().then(setProfiles).catch(() => {}); }, []);
  const [submitOpen, setSubmitOpen] = useState(false);
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [adminOpen, setAdminOpen] = useState(false);
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
    const load = () => api.listTasks().then((t) => {
      setTasks(t);
      setSelectedId((cur) => cur ?? (t.length ? t[0].id : null));
    }).catch(() => {});
    load();
    const iv = window.setInterval(load, 4000);
    return () => window.clearInterval(iv);
  }, []);

  useEffect(() => {
    const load = () => api.health().then(setHealth).catch(() => setHealth(null));
    load();
    const iv = window.setInterval(load, 8000);
    return () => window.clearInterval(iv);
  }, []);

  // The agent registry is effectively static for a session — fetch once and
  // build an id→registered-name map so the Rounds view can label every agent
  // (including those that activated but produced no finding).
  useEffect(() => { api.listAgents().then(setAgents).catch(() => {}); }, []);
  useEffect(() => { api.me().then(setMe).catch(() => {}); loadProfiles(); }, [loadProfiles]);

  const isPartner = me?.user?.role === "partner";

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
    if (!selectedId) { setTask(null); return; }
    let alive = true;
    const refetch = () => api.getTask(selectedId).then((t) => { if (alive) setTask(t); }).catch(() => {});
    refetch();
    const stop = streamTask(selectedId, {
      onSnapshot: (t) => { if (alive) setTask(t); },
      onPing: refetch,
    });
    return () => { alive = false; stop(); };
  }, [selectedId]);

  const refetchSelected = useCallback(() => {
    if (selectedId) api.getTask(selectedId).then(setTask).catch(() => {});
    api.listTasks().then(setTasks).catch(() => {});
  }, [selectedId]);

  function onCreated(t: Task) {
    setSubmitOpen(false);
    setTasks((prev) => [t, ...prev.filter((p) => p.id !== t.id)]);
    setSelectedId(t.id);
  }

  // Production with auth on, but no session → show the login screen.
  if (me?.authEnabled && !me.user) return <Login />;

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">
            <span className="big">Big</span>&nbsp;<span className="michael">Michael</span><span className="dot">.</span>
          </div>
          <div className="brand-sub">Legal Intelligence Bench</div>
        </div>

        <div className="rail-actions" style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          <button className="btn primary full" onClick={() => setSubmitOpen(true)}>＋ New matter</button>
          <button className="btn full ghost" onClick={() => setLibraryOpen(true)}>⊕ Library · ingest &amp; search</button>
          <button className="btn full ghost" onClick={() => setAdminOpen(true)}>⚙ Admin · settings</button>
        </div>

        <div className="rail-scroll">
          <div className="rail-label">Matters · {tasks.length}</div>
          {tasks.length === 0 && (
            <div style={{ padding: "10px 8px", color: "var(--text-faint)", fontSize: 13 }}>
              No matters yet. Convene the bench to begin.
            </div>
          )}
          {tasks.map((t, i) => (
            <motion.button
              key={t.id}
              className={`task-card ${t.id === selectedId ? "active" : ""}`}
              onClick={() => setSelectedId(t.id)}
              initial={{ opacity: 0, x: -8 }}
              animate={{ opacity: 1, x: 0 }}
              transition={{ delay: Math.min(i * 0.03, 0.4) }}
            >
              <div className="task-card-top">
                <StatusDot status={t.status} />
                <span className="task-card-title">{t.description}</span>
              </div>
              <div className="task-card-meta">
                <WorkflowPill workflow={t.workflowType} />
                {t.matterNumber && <span className="card-matter">· {t.matterNumber}</span>}
                <span>· {timeAgo(t.updatedAt)}</span>
                {t.pendingGates?.some((g) => g.status === "pending") && <span style={{ color: "var(--amber)" }}>· ⚖ review</span>}
              </div>
            </motion.button>
          ))}
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
              <span>{health.tasks.running} running</span>
              <span style={{ color: "var(--amber)" }}>{health.tasks.awaiting_gate} gated</span>
              <span style={{ color: "var(--green)" }}>{health.tasks.complete} done</span>
            </>}
            {me?.user && (
              <span className="whoami" title={`${me.user.email} · ${me.user.role}`}>
                {me.user.name}{me.user.role === "partner" && <span className="pill sm gold">partner</span>}
                {me.authEnabled && <button className="logout" onClick={() => api.logout().then(() => location.reload())}>sign out</button>}
              </span>
            )}
          </div>
        </div>

        <div className="detail-scroll">
          {task
            ? <TaskView key={task.id} task={task} agentNames={agentNames} profiles={profiles}
                isPartner={isPartner} onChange={refetchSelected} onDeleted={onDeleted} notify={notify} />
            : <EmptyState onNew={() => setSubmitOpen(true)} offline={!health} />}
        </div>
      </main>

      <AuditRail open={auditOpen} onToggle={() => setAuditOpen((o) => !o)} />

      <AnimatePresence>
        {submitOpen && <SubmitModal onClose={() => setSubmitOpen(false)} onCreated={onCreated} notify={notify} />}
        {libraryOpen && <Library onClose={() => setLibraryOpen(false)} notify={notify} />}
        {adminOpen && <AdminPanel onClose={() => setAdminOpen(false)} notify={notify} isPartner={isPartner} profiles={profiles} onProfilesChange={loadProfiles} />}
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

function EmptyState({ onNew, offline }: { onNew: () => void; offline: boolean }) {
  return (
    <div className="empty">
      <div className="glyph">§</div>
      <h2>The bench is in session</h2>
      <p>
        {offline
          ? "Can't reach the API on :3101. Start Big Michael with npm run dev, then convene a matter."
          : "Brief the orchestrator and watch granular epistemic, conceptual, and writing agents debate, cite, and verify every finding before synthesis."}
      </p>
      <button className="btn primary" onClick={onNew}>⚖ Convene the bench</button>
    </div>
  );
}
