import { useEffect, useRef, useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { api, streamAudit } from "./api";
import type { AuditEntry } from "./types";

export function tone(event: string): string {
  if (event.startsWith("gate")) return "var(--amber)";
  if (event.includes("complete") || event.includes("resolved") || event.includes("response")) return "var(--green)";
  if (event.includes("fail") || event.includes("reject") || event.includes("denied") || event.includes("expired")) return "var(--red)";
  if (event.startsWith("model") || event.startsWith("tool")) return "var(--blue)";
  if (event.startsWith("finding") || event.startsWith("debate") || event.startsWith("verification")) return "var(--gold)";
  if (event.startsWith("task.assigned") || event.startsWith("time")) return "var(--purple, #a78bfa)";
  return "var(--text-dim)";
}

/** Display name for an actorId. */
function actorLabel(actorId: string | undefined): string {
  if (!actorId || actorId === "system") return "";
  if (actorId === "anonymous") return "anon";
  return actorId;
}

export function auditSummary(e: AuditEntry): string {
  const d = e.data ?? {};
  const bits: string[] = [];

  // Actor attribution — always first, the most important field for a partner
  const actor = actorLabel(e.actorId);
  if (actor) bits.push(`actor=${actor}`);

  // Event-specific fields
  switch (e.event) {
    case "task.assigned": {
      const added = (d.added as string[] | undefined) ?? [];
      const removed = (d.removed as string[] | undefined) ?? [];
      const lawyers = (d.lawyerIds as string[] | undefined) ?? [];
      if (added.length) bits.push(`+[${added.join(", ")}]`);
      if (removed.length) bits.push(`−[${removed.join(", ")}]`);
      if (!added.length && !removed.length && lawyers.length) bits.push(`lawyers=[${lawyers.join(", ")}]`);
      if (d.note) bits.push(`note="${d.note}"`);
      break;
    }
    case "tool.call":
    case "tool.result": {
      if (d.tool) bits.push(`tool=${d.tool}`);
      if (d.category === "external_connector") bits.push("⚡external");
      if (d.resultCount != null) bits.push(`results=${d.resultCount}`);
      if (e.durationMs != null) bits.push(`${e.durationMs}ms`);
      break;
    }
    case "document.searched": {
      if (d.query) bits.push(`query="${d.query}"`);
      if (d.resultCount != null) bits.push(`results=${d.resultCount}`);
      break;
    }
    case "agent.processing":
    case "agent.complete": {
      if (d.agentName) bits.push(String(d.agentName));
      if (d.round != null) bits.push(`round=${d.round}`);
      if (d.findingCount != null) bits.push(`findings=${d.findingCount}`);
      if (e.durationMs != null) bits.push(`${e.durationMs}ms`);
      break;
    }
    case "finding.produced": {
      if (d.confidence != null) bits.push(`confidence=${d.confidence}`);
      const text = d.findingText as string | undefined;
      if (text) bits.push(`"${text.slice(0, 72)}…"`);
      break;
    }
    case "round.start":
    case "round.complete": {
      if (d.round != null) bits.push(`round=${d.round}`);
      if (d.agentCount != null) bits.push(`agents=${d.agentCount}`);
      if (d.findingCount != null) bits.push(`findings=${d.findingCount}`);
      if (d.edgeCount != null) bits.push(`edges=${d.edgeCount}`);
      if (e.durationMs != null) bits.push(`${e.durationMs}ms`);
      break;
    }
    case "access.denied": {
      if (d.method && d.url) bits.push(`${d.method} ${d.url}`);
      break;
    }
    case "time.closed":
    case "time.opened": {
      if (d.matterNumber) bits.push(`matter=${d.matterNumber}`);
      if (d.billableHours != null) bits.push(`${d.billableHours}h`);
      if (d.description) bits.push(`"${String(d.description).slice(0, 48)}…"`);
      break;
    }
    case "task.submitted": {
      if (d.clientNumber) bits.push(`client=${d.clientNumber}`);
      if (d.matterNumber) bits.push(`matter=${d.matterNumber}`);
      if (d.workflowType) bits.push(`workflow=${d.workflowType}`);
      break;
    }
    case "debate.complete": {
      if (d.challengedFindings != null) bits.push(`challenged=${d.challengedFindings}`);
      if (d.confirmedFindings != null) bits.push(`confirmed=${d.confirmedFindings}`);
      break;
    }
    case "verification.complete": {
      if (d.passed != null) bits.push(`passed=${d.passed}`);
      if (d.failed != null) bits.push(`failed=${d.failed}`);
      if (d.passes != null) bits.push(`passes=${d.passes}`);
      break;
    }
    default: {
      for (const k of ["phase", "round", "model", "findings", "gates", "workflow", "confidence", "passed", "durationMs"]) {
        if (d[k] != null) bits.push(`${k}=${d[k]}`);
      }
      if (e.agentId) bits.push(e.agentId);
      if (e.durationMs != null) bits.push(`${e.durationMs}ms`);
    }
  }

  return bits.join(" · ");
}

function hhmmss(iso: string): string {
  const d = new Date(iso);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export function AuditRail({ open, onToggle, profileId }: { open: boolean; onToggle: () => void; profileId?: string }) {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [paused, setPaused] = useState(false);
  const pausedRef = useRef(paused);
  pausedRef.current = paused;
  const openRef = useRef(open);
  openRef.current = open;

  // The rail is personal: it shows the current user's own actions. The full
  // firm-wide log lives in Admin → Audit (partner only, server-enforced).
  const scope = profileId ? { actorId: profileId } : undefined;

  // Load recent history on mount so the panel isn't empty
  useEffect(() => {
    api.recentAudit(80, scope).then((hist) => {
      setEntries((prev) => {
        // Merge: history oldest-first, then deduplicate by id
        const seen = new Set(prev.map((e) => e.id));
        const fresh = hist.filter((e) => !seen.has(e.id));
        // Return newest-first (history is oldest-first from server)
        return [...prev, ...fresh.reverse()].slice(0, 200);
      });
    }).catch(() => { /* server may not have entries yet */ });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [profileId]);

  useEffect(() => {
    const stop = streamAudit((entry) => {
      if (pausedRef.current) return;
      setEntries((prev) => [entry, ...prev].slice(0, 200));
    }, scope);
    return stop;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [profileId]);

  // Close on Escape and on any pointer-down outside the rail (and not on the
  // toggle FAB, which manages its own state).
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && openRef.current) onToggle();
    };
    const onPointer = (e: PointerEvent) => {
      const t = e.target as HTMLElement | null;
      if (!t) return;
      if (t.closest(".audit-rail") || t.closest(".audit-fab")) return;
      if (openRef.current) onToggle();
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onPointer);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onPointer);
    };
  }, [open, onToggle]);

  const seenIds = new Set<string>();
  const dedupedEntries = entries.filter((e) => {
    if (seenIds.has(e.id)) return false;
    seenIds.add(e.id);
    return true;
  });

  return (
    <>
      <button className={`audit-fab ${open ? "on" : ""}`} onClick={onToggle} title="Your activity log">
        <span className="audit-fab-dot" /> {open ? "▸" : "◂"} Activity
      </button>

      <AnimatePresence>
        {open && (
          <motion.aside className="audit-rail"
            initial={{ x: 340 }} animate={{ x: 0 }} exit={{ x: 340 }}
            transition={{ type: "spring", stiffness: 320, damping: 34 }}>
            <div className="audit-head">
              <div>
                <div className="audit-title">My activity</div>
                <div className="audit-sub">{dedupedEntries.length} events · your actions only</div>
              </div>
              <div style={{ display: "flex", gap: 6 }}>
                <button className="btn ghost sm" onClick={() => setPaused((p) => !p)}>
                  {paused ? "▶ Resume" : "⏸ Pause"}
                </button>
                <button className="btn ghost sm" onClick={onToggle} title="Close (Esc)">✕</button>
              </div>
            </div>
            <div className="audit-feed">
              {dedupedEntries.length === 0 && <div className="placeholder" style={{ fontSize: 13 }}>Waiting for activity…</div>}
              <AnimatePresence initial={false}>
                {dedupedEntries.map((e) => (
                  <motion.div key={e.id} className="audit-row"
                    initial={{ opacity: 0, x: 16 }} animate={{ opacity: 1, x: 0 }}
                    transition={{ duration: 0.2 }} layout>
                    <span className="audit-time">{hhmmss(e.ts)}</span>
                    <span className="audit-evt" style={{ color: tone(e.event) }}>
                      <span className="audit-dot" style={{ background: tone(e.event) }} />
                      {e.event}
                    </span>
                    {auditSummary(e) && <span className="audit-detail">{auditSummary(e)}</span>}
                  </motion.div>
                ))}
              </AnimatePresence>
            </div>
          </motion.aside>
        )}
      </AnimatePresence>
    </>
  );
}
