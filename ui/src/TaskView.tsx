import { useMemo, useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import type { Task, RoundState } from "./types";
import { PHASE_SEQUENCES } from "./types";
import { StatusPill, WorkflowPill, ConfidenceBar, CitationChips } from "./primitives";
import { FindingsTable } from "./FindingsTable";
import { TabulateGrid } from "./TabulateGrid";
import { Markdown } from "./Markdown";

type Tab = "findings" | "tabulate" | "synthesis" | "rounds";

// Agents and edges carry raw agent IDs (e.g. "art101-object-analyst",
// "lavern:contract-reviewer"). Resolve to a display name in priority order:
// the live agent registry → a name seen on a finding → a prettified ID.
function useAgentNames(task: Task, registry: Map<string, string>): (id: string) => string {
  return useMemo(() => {
    const byId = new Map<string, string>();
    for (const f of task.findings) byId.set(f.agentId, f.agentName);
    for (const r of task.rounds) for (const f of r.findings) byId.set(f.agentId, f.agentName);
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
  const r = round;
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

export function TaskView({ task, agentNames, onChange, notify }: {
  task: Task;
  agentNames: Map<string, string>;
  onChange: () => void;
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
          {pendingGates > 0 && <span className="pill amber">⚖ {pendingGates} awaiting review</span>}
          {task.error && <span className="pill red">{task.error}</span>}
        </div>
        <h1 className="task-title">{task.description}</h1>
        <div className="task-id">{task.id} · round {task.currentRound}/{task.maxRounds} · {task.findings.length} findings</div>
        <PhaseStepper task={task} />
      </div>

      <div className="tabs">
        {tabs.filter((t) => t.show).map((t) => (
          <button key={t.id} className={`tab ${tab === t.id ? "active" : ""}`} onClick={() => setTab(t.id)}>
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
