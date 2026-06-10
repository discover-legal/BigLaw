import { useState } from "react";
import { motion } from "framer-motion";
import type { Task, LawyerProfile } from "../types";
import { StatusDot, WorkflowPill, timeAgo } from "../primitives";
import { TaskView } from "../TaskView";

/**
 * The core workspace: matter list on the left, the live TaskView on the right.
 * This is the section where Big Michael convenes the bench.
 */
export function MattersPage({
  tasks, selectedId, onSelect, task, agentNames, profiles,
  isPartner, onChange, onDeleted, notify, onNew, offline,
}: {
  tasks: Task[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  task: Task | null;
  agentNames: Map<string, string>;
  profiles: LawyerProfile[];
  isPartner: boolean;
  onChange: () => void;
  onDeleted: (id: string) => void;
  notify: (m: string) => void;
  onNew: () => void;
  offline: boolean;
}) {
  const [clientFilter, setClientFilter] = useState<string | null>(null);
  const filtered = tasks.filter((t) => !clientFilter || t.clientNumber === clientFilter);

  return (
    <div className="matters-layout">
      <div className="matters-rail">
        {clientFilter && (
          <div style={{ padding: "6px 8px", display: "flex", alignItems: "center", gap: 6 }}>
            <span className="pill sm gold">{clientFilter}</span>
            <button className="btn ghost sm" style={{ padding: "2px 6px" }} onClick={() => setClientFilter(null)}>✕</button>
          </div>
        )}
        <div className="rail-label">Matters · {filtered.length}</div>
        {filtered.length === 0 && (
          <div style={{ padding: "10px 8px", color: "var(--text-faint)", fontSize: 13 }}>
            {clientFilter ? "No matters for this client." : "No matters yet. Convene the bench to begin."}
          </div>
        )}
        {filtered.map((t, i) => (
          <motion.button
            key={t.id}
            className={`task-card ${t.id === selectedId ? "active" : ""}`}
            onClick={() => onSelect(t.id)}
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
              {t.clientNumber && (
                <span className="card-matter" style={{ cursor: "pointer", color: "var(--gold-soft)" }}
                  onClick={(e) => { e.stopPropagation(); setClientFilter(t.clientNumber!); }}>
                  · {t.clientNumber}
                </span>
              )}
              {t.matterNumber && <span className="card-matter">· {t.matterNumber}</span>}
              <span>· {timeAgo(t.updatedAt)}</span>
              {t.pendingGates?.some((g) => g.status === "pending") && <span style={{ color: "var(--amber)" }}>· ⚖ review</span>}
            </div>
          </motion.button>
        ))}
      </div>

      <div className="detail-scroll">
        {task
          ? <TaskView key={task.id} task={task} agentNames={agentNames} profiles={profiles}
              isPartner={isPartner} onChange={onChange} onDeleted={onDeleted} notify={notify} />
          : <EmptyState onNew={onNew} offline={offline} />}
      </div>
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
          ? "Can't reach the API on :3101. Start BigLaw with npm run dev, then convene a matter."
          : "Brief Big Michael and watch granular epistemic, conceptual, and writing agents debate, cite, and verify every finding before synthesis."}
      </p>
      <button className="btn primary" onClick={onNew}>⚖ Convene the bench</button>
    </div>
  );
}
