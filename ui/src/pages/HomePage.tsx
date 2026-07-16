import { useMemo, useState } from "react";
import type { Task, Health, Me, GateRequest } from "../types";
import { api } from "../api";
import { ConfidenceBar } from "../primitives";

// HomePage is the role/mode-aware landing — it opens each user at the right
// altitude (Double Diamond "Deliver"): a partner sees what needs them across the
// whole portfolio; a lawyer sees their own matters; a Lite user gets a guided
// intake; an admin sees system health. One component, four front doors.
export function HomePage({ tasks, health, me, isPartner, onOpenMatter, onGo, onGoLibrary, onNew, notify }: {
  tasks: Task[];
  health: Health | null;
  me: Me | null;
  isPartner: boolean;
  onOpenMatter: (id: string) => void;
  onGo: (section: string) => void;
  onGoLibrary: (tab: "documents" | "upload" | "search") => void;
  onNew: () => void;
  notify: (m: string) => void;
}) {
  const mode = me?.mode ?? "admin";
  const name = me?.user?.name?.split(/\s+/)[0] ?? "there";
  const myId = me?.user?.profileId;

  // Cross-matter pending gates — the partner/lawyer "needs me" queue.
  const pendingGates = useMemo(() => {
    const out: Array<{ task: Task; gate: GateRequest }> = [];
    for (const t of tasks) for (const g of t.pendingGates ?? []) if (g.status === "pending") out.push({ task: t, gate: g });
    return out;
  }, [tasks]);

  const myMatters = useMemo(
    () => (myId ? tasks.filter((t) => (t.assignedLawyerIds ?? []).includes(myId)) : tasks),
    [tasks, myId],
  );
  const counts = {
    running: tasks.filter((t) => t.status === "running").length,
    gated: tasks.filter((t) => t.status === "awaiting_gate").length,
    complete: tasks.filter((t) => t.status === "complete").length,
    total: tasks.length,
  };

  const greeting = mode === "lite" ? "Let's get started" : `Good to see you, ${name}`;
  const subtitle =
    mode === "lite" ? "Drop in a document or open a matter — the bench does the rest."
    : isPartner ? "Here's what needs you across the firm."
    : "Your matters and what's waiting on you.";

  return (
    <div className="detail home">
      <div className="home-head">
        <h1 className="page-title">{greeting}</h1>
        <p className="home-sub">{subtitle}</p>
      </div>

      {/* ── Lite: guided, large-target intake ───────────────────────────── */}
      {mode === "lite" ? (
        <GuidedIntake onNew={onNew} onGoLibrary={onGoLibrary} />
      ) : (
        <>
          {/* Portfolio glance — partners/admin see the whole firm, lawyers their slice */}
          <div className="home-stats">
            <StatTile n={isPartner ? counts.total : myMatters.length} label={isPartner ? "Matters" : "My matters"} onClick={() => onGo("matters")} />
            <StatTile n={counts.running} label="Running" tone="blue" onClick={() => onGo("matters")} />
            <StatTile n={pendingGates.length} label="Awaiting you" tone="amber" onClick={() => onGo("matters")} />
            <StatTile n={counts.complete} label="Complete" tone="green" onClick={() => onGo("matters")} />
          </div>

          {/* The command strip: what needs the lawyer's judgment, across matters */}
          <NeedsYourReview items={isPartner ? pendingGates : pendingGates.filter((p) => (p.task.assignedLawyerIds ?? []).includes(myId ?? ""))}
            onOpenMatter={onOpenMatter} notify={notify} />

          {/* Recent matters to jump back into */}
          <RecentMatters matters={(isPartner ? tasks : myMatters).slice(0, 5)} onOpenMatter={onOpenMatter} onNew={onNew} />
        </>
      )}

      {/* Quick jumps — only what this level can reach */}
      <QuickJumps isPartner={isPartner} mode={mode} onGo={onGo} />

      {/* Admin/system footer line */}
      {(mode === "admin" || isPartner) && (
        <div className="home-sysline">
		  {health ? <>System healthy · API v{health.version} · up {Math.floor(health.uptime / 60)}m · {health.tasks.queued} queued, {health.tasks.running} running, {health.tasks.awaiting_gate} gated</> : "API offline"}
        </div>
      )}
    </div>
  );
}

function StatTile({ n, label, tone, onClick }: { n: number; label: string; tone?: string; onClick: () => void }) {
  return (
    <button className={`stat-tile ${tone ?? ""}`} onClick={onClick}>
      <span className="stat-n">{n}</span>
      <span className="stat-label">{label}</span>
    </button>
  );
}

function NeedsYourReview({ items, onOpenMatter, notify }: {
  items: Array<{ task: Task; gate: GateRequest }>;
  onOpenMatter: (id: string) => void;
  notify: (m: string) => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  if (!items.length) {
    return (
      <div className="home-card">
        <div className="home-card-head">⚖ Needs your review</div>
        <div className="home-empty">Nothing waiting on you right now. Clear desk.</div>
      </div>
    );
  }
  async function act(task: Task, gate: GateRequest, kind: "approve" | "reject") {
    setBusy(gate.id);
    try {
      if (kind === "approve") { await api.approveGate(task.id, gate.id); notify("Finding approved"); }
      else { const r = window.prompt("Reason for rejecting?") || "rejected by reviewer"; await api.rejectGate(task.id, gate.id, r); notify("Finding rejected"); }
    } catch (e) { notify((e as Error).message); } finally { setBusy(null); }
  }
  return (
    <div className="home-card amber-card">
      <div className="home-card-head">⚖ Needs your review · {items.length}</div>
      {items.slice(0, 6).map(({ task, gate }) => (
        <div key={gate.id} className="review-item">
          <div className="review-main">
            <button className="review-matter" onClick={() => onOpenMatter(task.id)} title="Open matter">
              {task.matterNumber ? `${task.matterNumber} · ` : ""}{task.description.split(/\n/)[0].slice(0, 64)}
            </button>
            <div className="review-claim">{(gate.finding?.content ?? "").slice(0, 160)}</div>
            <div className="review-meta"><ConfidenceBar value={gate.finding?.confidence ?? 0} /></div>
          </div>
          <div className="review-actions">
            <button className="btn approve sm" disabled={busy === gate.id} onClick={() => act(task, gate, "approve")}>Approve</button>
            <button className="btn reject sm" disabled={busy === gate.id} onClick={() => act(task, gate, "reject")}>Reject</button>
          </div>
        </div>
      ))}
      {items.length > 6 && <div className="home-more">+ {items.length - 6} more in Matters</div>}
    </div>
  );
}

function RecentMatters({ matters, onOpenMatter, onNew }: {
  matters: Task[]; onOpenMatter: (id: string) => void; onNew: () => void;
}) {
  if (!matters.length) {
    return (
      <div className="home-card">
        <div className="home-card-head">Your matters</div>
        <div className="home-empty">
          No matters yet.
          <button className="btn primary sm" style={{ marginLeft: 10 }} onClick={onNew}>＋ Convene the bench</button>
        </div>
      </div>
    );
  }
  const statusTone: Record<string, string> = { running: "blue", awaiting_gate: "amber", complete: "green", failed: "red" };
  return (
    <div className="home-card">
      <div className="home-card-head">Recent matters</div>
      {matters.map((t) => (
        <button key={t.id} className="home-matter-row" onClick={() => onOpenMatter(t.id)}>
          <span className={`pill sm ${statusTone[t.status] ?? ""}`}>{t.status.replace("_", " ")}</span>
          <span className="home-matter-desc">{t.description.split(/\n/)[0].slice(0, 90)}</span>
          {t.findings?.length ? <span className="home-matter-meta">{t.findings.length} findings</span> : null}
        </button>
      ))}
    </div>
  );
}

function GuidedIntake({ onNew, onGoLibrary }: { onNew: () => void; onGoLibrary: (tab: "documents" | "upload" | "search") => void }) {
  return (
    <div className="guided-grid">
      <button className="guided-tile" onClick={() => onGoLibrary("upload")}>
        <span className="guided-glyph">⬆</span>
        <span className="guided-title">Upload a document</span>
        <span className="guided-sub">PDF, Word, an image or a scan — it's read, classified and made searchable.</span>
      </button>
      <button className="guided-tile" onClick={onNew}>
        <span className="guided-glyph">⚖</span>
        <span className="guided-title">Start a matter</span>
        <span className="guided-sub">Ask a legal question; the bench researches, debates and reports back.</span>
      </button>
      <button className="guided-tile" onClick={() => onGoLibrary("search")}>
        <span className="guided-glyph">⌕</span>
        <span className="guided-title">Search your documents</span>
        <span className="guided-sub">Find anything by meaning, not just keywords.</span>
      </button>
    </div>
  );
}

function QuickJumps({ isPartner, mode, onGo }: { isPartner: boolean; mode: string; onGo: (s: string) => void }) {
  const jumps: Array<{ id: string; glyph: string; label: string; show: boolean }> = [
    { id: "matters", glyph: "⚖", label: "Matters", show: true },
    { id: "library", glyph: "⊞", label: "Library", show: true },
    { id: "drafting", glyph: "✎", label: "Drafting", show: true },
    { id: "clients", glyph: "☷", label: "Clients", show: isPartner },
    { id: "billing", glyph: "⏱", label: "Billing & Time", show: true },
    { id: "budgets", glyph: "◔", label: "Budgets & Deadlines", show: true },
    { id: "watchtower", glyph: "◉", label: "Watchtower", show: isPartner },
    { id: "analytics", glyph: "∿", label: "Analytics", show: isPartner },
    { id: "admin", glyph: "⚙", label: "Admin", show: mode === "admin" },
  ];
  return (
    <div className="home-jumps">
      {jumps.filter((j) => j.show).map((j) => (
        <button key={j.id} className="home-jump" onClick={() => onGo(j.id)}>
          <span className="home-jump-glyph">{j.glyph}</span>{j.label}
        </button>
      ))}
    </div>
  );
}
