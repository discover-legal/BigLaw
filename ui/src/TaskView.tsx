import { useMemo, useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import type { Task, RoundState, LawyerProfile } from "./types";
import { PHASE_SEQUENCES } from "./types";
import { StatusPill, WorkflowPill, ConfidenceBar, CitationChips } from "./primitives";
import { FindingsTable } from "./FindingsTable";
import { TabulateGrid } from "./TabulateGrid";
import { Markdown } from "./Markdown";
import { api } from "./api";

function initials(name: string): string {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0]?.toUpperCase()).join("");
}

// conciseTitle keeps the matter header to one legible line — the full brief
// (often a multi-paragraph prompt that even embeds the document list) lives in
// the collapsible "The request" accordion instead of dominating the view.
function conciseTitle(desc: string): string {
  const firstLine = (desc.split(/\n/)[0] || desc).trim();
  return firstLine.length > 150 ? firstLine.slice(0, 150).trimEnd() + "…" : firstLine;
}

// NeedsReview surfaces pending human gates as the first, actionable thing a
// lawyer sees — approve/reject inline rather than hunting through Findings.
function NeedsReview({ task, onChange, notify }: {
  task: Task; onChange: () => void; notify: (m: string) => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const pending = task.pendingGates.filter((g) => g.status === "pending");
  if (!pending.length) return null;

  async function act(gate: typeof pending[number], kind: "approve" | "reject") {
    setBusy(gate.id);
    try {
      if (kind === "approve") { await api.approveGate(task.id, gate.id); notify("Finding approved"); }
      else { const r = window.prompt("Reason for rejecting this finding?") || "rejected by reviewer"; await api.rejectGate(task.id, gate.id, r); notify("Finding rejected"); }
      onChange();
    } catch (e) { notify((e as Error).message); } finally { setBusy(null); }
  }

  return (
    <div className="needs-review">
      <div className="needs-review-head">⚖ Needs your review · {pending.length}</div>
      {pending.map((g) => (
        <div key={g.id} className="review-item">
          <div className="review-main">
            <div className="review-claim">{g.finding.content}</div>
            <div className="review-meta">
              <span className="review-agent">{g.finding.agentName}</span>
              <ConfidenceBar value={g.finding.confidence} />
              {g.finding.challenged && <span className="pill red sm">⚔ challenged</span>}
            </div>
          </div>
          <div className="review-actions">
            <button className="btn approve sm" disabled={busy === g.id} onClick={() => act(g, "approve")}>Approve</button>
            <button className="btn reject sm" disabled={busy === g.id} onClick={() => act(g, "reject")}>Reject</button>
          </div>
        </div>
      ))}
    </div>
  );
}

function MatterActions({ task, profiles, isPartner, onChange, onDeleted, notify }: {
  task: Task; profiles: LawyerProfile[]; isPartner: boolean;
  onChange: () => void; onDeleted: (id: string) => void; notify: (m: string) => void;
}) {
  const [assignOpen, setAssignOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const assigned = task.assignedLawyerIds ?? [];
  const assignedProfiles = profiles.filter((p) => assigned.includes(p.id));

  async function toggle(id: string) {
    const next = assigned.includes(id) ? assigned.filter((x) => x !== id) : [...assigned, id];
    setBusy(true);
    try { await api.assignLawyers(task.id, next); onChange(); }
    catch (e) { notify((e as Error).message); } finally { setBusy(false); }
  }

  async function del() {
    if (!window.confirm(`Delete this matter? This can't be undone.\n\n${task.description.slice(0, 120)}`)) return;
    setBusy(true);
    try { await api.deleteTask(task.id); notify("Matter deleted"); onDeleted(task.id); }
    catch (e) { notify((e as Error).message); setBusy(false); }
  }

  return (
    <div className="matter-actions">
      <div className="assignees">
        {assignedProfiles.length === 0 && <span className="assignees-none">Unassigned</span>}
        {assignedProfiles.map((p) => (
          <span key={p.id} className="avatar" style={{ backgroundColor: p.color ?? "var(--gold-soft)" }} title={`${p.name} · ${p.role}`}>{initials(p.name)}</span>
        ))}
      </div>
      {isPartner && (
        <div className="assign-wrap">
          <button className="btn ghost sm" disabled={busy} onClick={() => setAssignOpen((o) => !o)}>⚖ Assign ▾</button>
          {assignOpen && (
            <div className="assign-menu" onMouseLeave={() => setAssignOpen(false)}>
              <div className="assign-menu-head">Assign lawyers</div>
              {profiles.length === 0 && <div className="assign-empty">No lawyers yet — add them in Admin.</div>}
              {profiles.map((p) => (
                <label key={p.id} className="assign-row">
                  <input type="checkbox" checked={assigned.includes(p.id)} disabled={busy} onChange={() => toggle(p.id)} />
                  <span className="avatar sm" style={{ backgroundColor: p.color ?? "var(--gold-soft)" }}>{initials(p.name)}</span>
                  <span className="assign-name">{p.name}</span>
                  {p.role === "partner" && <span className="pill sm gold">partner</span>}
                </label>
              ))}
            </div>
          )}
        </div>
      )}
      {isPartner && <button className="btn reject sm" disabled={busy} onClick={del} title="Delete matter">✕ Delete</button>}
    </div>
  );
}

type Tab = "findings" | "tabulate" | "synthesis" | "rounds";

// Agents and edges carry raw agent IDs (e.g. "art101-object-analyst",
// "lavern:contract-reviewer"). Resolve to a display name in priority order:
// the live agent registry → a name seen on a finding → a prettified ID.
function useAgentNames(task: Task, registry: Map<string, string>): (id: string) => string {
  return useMemo(() => {
    const byId = new Map<string, string>();
    // ?? [] — a backend predating the nil-slice fix serializes empty
    // round fields as null.
    for (const f of task.findings ?? []) byId.set(f.agentId, f.agentName);
    for (const r of task.rounds ?? []) for (const f of r.findings ?? []) byId.set(f.agentId, f.agentName);
    return (id: string) => registry.get(id) ?? byId.get(id) ?? prettifyId(id);
  }, [task, registry]);
}

function prettifyId(id: string): string {
  const bare = id.replace(/^lavern:/, "");
  const label = bare.replace(/[-_]/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
  return id.startsWith("lavern:") ? `${label} ◦ Lavern` : label;
}

function PhaseStepper({ task }: { task: Task }) {
  const phases = PHASE_SEQUENCES[task.workflowType];
  const currentIdx = phases.indexOf(task.currentPhase);
  const done = task.status === "complete";
  return (
    <div className="stepper">
      {phases.map((p, i) => {
        const isDone = done || i < currentIdx;
        const isCurrent = !done && i === currentIdx;
        return (
          <div key={p} className={`step ${isDone ? "done" : ""} ${isCurrent ? "current" : ""}`}>
            {i > 0 && <span className="step-link" />}
            <span className="step-node">
              <span className="step-num">{isDone ? "✓" : i + 1}</span>
              {p}
            </span>
          </div>
        );
      })}
    </div>
  );
}

function RoundCard({ round, defaultOpen, nameOf }: {
  round: RoundState;
  defaultOpen: boolean;
  nameOf: (id: string) => string;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const r = {
    ...round,
    activeAgentIds: round.activeAgentIds ?? [],
    edges: round.edges ?? [],
    findings: round.findings ?? [],
  };
  return (
    <div className={`round-card ${open ? "open" : ""}`}>
      <button className="round-head" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
        <span className="round-chevron">▸</span>
        <span className="pill gold">Round {r.goal.round}</span>
        <span className="pill blue">{r.goal.phase}</span>
        <span className="round-desc">{r.goal.description}</span>
        <span className="round-counts">
          <span>{r.activeAgentIds.length} agents</span>
          <span>{r.edges.length} edges</span>
          <span>{r.findings.length} findings</span>
        </span>
      </button>

      <AnimatePresence initial={false}>
        {open && (
          <motion.div
            className="round-body"
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: "auto", opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={{ duration: 0.22, ease: "easeInOut" }}
          >
            <div className="round-body-inner">
              {/* Agents */}
              <section className="rnd-section">
                <div className="rnd-label">Active agents · {r.activeAgentIds.length}</div>
                {r.activeAgentIds.length
                  ? <div className="agent-chips">
                      {r.activeAgentIds.map((id) => <span key={id} className="agent-chip" title={id}>{nameOf(id)}</span>)}
                    </div>
                  : <div className="rnd-empty">No agents activated this round.</div>}
              </section>

              {/* Communication edges */}
              <section className="rnd-section">
                <div className="rnd-label">Communication graph · {r.edges.length} edges</div>
                {r.edges.length
                  ? <div className="edge-list">
                      {r.edges.map((e, i) => (
                        <div key={i} className="edge-row">
                          <span className="edge-node">{nameOf(e.from)}</span>
                          <span className="edge-arrow">→</span>
                          <span className="edge-node">{nameOf(e.to)}</span>
                          <span className="edge-sim" title="Need/Offer cosine similarity">{(e.similarity * 100).toFixed(0)}%</span>
                          {e.offerText && <span className="edge-offer">“{e.offerText}”</span>}
                        </div>
                      ))}
                    </div>
                  : <div className="rnd-empty">No Need/Offer matches — agents worked independently.</div>}
              </section>

              {/* Findings produced this round */}
              <section className="rnd-section">
                <div className="rnd-label">Findings this round · {r.findings.length}</div>
                {r.findings.length
                  ? <div className="rnd-findings">
                      {r.findings.map((f) => (
                        <div key={f.id} className="rnd-finding">
                          <div className="rnd-finding-top">
                            <span className="rnd-finding-agent">{f.agentName}</span>
                            <ConfidenceBar value={f.confidence} />
                            {f.challenged && <span className="pill red sm">⚔ challenged</span>}
                            {f.verificationResult && (
                              <span className={`pill sm ${f.verificationResult.passed ? "green" : "red"}`}>
                                {f.verificationResult.passed ? "✓ verified" : "✕ failed"}
                              </span>
                            )}
                          </div>
                          <div className="rnd-finding-body">{f.content}</div>
                          <CitationChips citations={f.citations} />
                        </div>
                      ))}
                    </div>
                  : <div className="rnd-empty">No findings produced this round.</div>}
              </section>
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
}

function RoundsPanel({ task, agentNames }: { task: Task; agentNames: Map<string, string> }) {
  const nameOf = useAgentNames(task, agentNames);
  if (!task.rounds.length) return <div className="placeholder">No rounds executed yet.</div>;
  const lastIdx = task.rounds.length - 1;
  return (
    <div className="rounds">
      {task.rounds.map((r, i) => (
        <RoundCard key={r.roundId} round={r} defaultOpen={i === lastIdx} nameOf={nameOf} />
      ))}
    </div>
  );
}

export function TaskView({ task, agentNames, profiles, isPartner, onChange, onDeleted, notify }: {
  task: Task;
  agentNames: Map<string, string>;
  profiles: LawyerProfile[];
  isPartner: boolean;
  onChange: () => void;
  onDeleted: (id: string) => void;
  notify: (msg: string) => void;
}) {
  const hasTable = !!task.table || task.workflowType === "tabulate";
  const initial: Tab = task.findings.length ? "findings" : task.output ? "synthesis" : "findings";
  const [tab, setTab] = useState<Tab>(initial);

  const pendingGates = task.pendingGates.filter((g) => g.status === "pending").length;

  const tabs: { id: Tab; label: string; count?: number; show: boolean }[] = [
    { id: "findings", label: "Findings", count: task.findings.length, show: true },
    { id: "tabulate", label: "Tabulate", count: task.table?.rows.length, show: hasTable },
    { id: "synthesis", label: "Synthesis", show: !!task.output },
    { id: "rounds", label: "Rounds", count: task.rounds.length, show: true },
  ];

  return (
    <div className="detail">
      <div className="task-head">
        <div className="eyebrow">
          <WorkflowPill workflow={task.workflowType} />
          <StatusPill status={task.status} />
          {(task.matterNumber || task.clientNumber) && (
            <span className="pill matter-ref" title="Client / matter reference">
              {task.clientNumber && <>CLIENT {task.clientNumber}</>}
              {task.clientNumber && task.matterNumber && " · "}
              {task.matterNumber && <>MATTER {task.matterNumber}</>}
            </span>
          )}
		  {pendingGates > 0 && <span className="pill amber">⚖ {pendingGates} awaiting review</span>}
		  {task.queue && (
			<span className="pill" title={`${task.queue.confidence} confidence · range ${new Date(task.queue.estimatedCompletion.earliest).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}–${new Date(task.queue.estimatedCompletion.latest).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}`}>
			  {task.queue.position > 0 ? `#${task.queue.position} in queue · ` : ""}ETA {new Date(task.queue.estimatedCompletion.likely).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}
			</span>
		  )}
          {task.error && <span className="pill red">{task.error}</span>}
        </div>
        <h1 className="task-title">{conciseTitle(task.description)}</h1>
        <div className="task-id">{task.id} · round {task.currentRound}/{task.maxRounds} · {task.findings.length} findings</div>
        <MatterActions task={task} profiles={profiles} isPartner={isPartner} onChange={onChange} onDeleted={onDeleted} notify={notify} />
        <PhaseStepper task={task} />
      </div>

      {/* Answer-first: the bottom line, then what needs the lawyer, then detail. */}
      {task.output && (
        <div className="bottom-line">
          <div className="bottom-line-head">Bottom line<span className="bl-caveat">draft — verify against the cited sources</span></div>
          <div className="prose md bottom-line-body"><Markdown source={task.output} /></div>
        </div>
      )}

      <NeedsReview task={task} onChange={onChange} notify={notify} />

      {/* The brief is an input, not the answer — collapsed by default. */}
      <details className="request-accordion">
        <summary>The request{task.documentIds?.length ? ` · ${task.documentIds.length} documents` : ""}</summary>
        <div className="prose md request-body"><Markdown source={task.description} /></div>
      </details>

      <div className="tabs" role="tablist" aria-label="Matter details">
        {tabs.filter((t) => t.show).map((t) => (
          <button key={t.id} role="tab" aria-selected={tab === t.id} className={`tab ${tab === t.id ? "active" : ""}`} onClick={() => setTab(t.id)}>
            {t.label}
            {t.count != null && <span className="tab-count">{t.count}</span>}
            {tab === t.id && <motion.span layoutId="tab-underline" className="tab-underline" />}
          </button>
        ))}
      </div>

      <div className="panel-body">
        <AnimatePresence mode="wait">
          <motion.div key={tab}
            initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -8 }}
            transition={{ duration: 0.2 }}>
            {tab === "findings" && <FindingsTable task={task} onChange={onChange} notify={notify} />}
            {tab === "tabulate" && <TabulateGrid task={task} />}
            {tab === "rounds" && <RoundsPanel task={task} agentNames={agentNames} />}
            {tab === "synthesis" && (
              task.output
                ? <div className="synthesis"><div className="synthesis-head">Final synthesis</div><div className="prose md"><Markdown source={task.output} /></div></div>
                : <div className="placeholder">Synthesis appears once all phases complete.</div>
            )}
          </motion.div>
        </AnimatePresence>
      </div>
    </div>
  );
}
