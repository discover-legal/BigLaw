import type { TaskStatus, WorkflowType, Citation } from "./types";

export function StatusDot({ status }: { status: TaskStatus }) {
  return <span className={`dot ${status}`} title={status} />;
}

const STATUS_PILL: Record<TaskStatus, string> = {
	running: "gold",
	queued: "",
  awaiting_gate: "amber",
  complete: "green",
  failed: "red",
  interrupted: "amber",
  pending: "",
};

export function StatusPill({ status }: { status: TaskStatus }) {
  const label = status.replace("_", " ");
  return (
    <span className={`pill ${STATUS_PILL[status]}`}>
      <StatusDot status={status} />
      {label}
    </span>
  );
}

export function WorkflowPill({ workflow }: { workflow: WorkflowType }) {
  return <span className="pill blue">{workflow.replace("_", " ")}</span>;
}

export function ConfidenceBar({ value }: { value: number }) {
  const pct = Math.round(value * 100);
  const color = value >= 0.75 ? "var(--green)" : value >= 0.5 ? "var(--gold)" : "var(--red)";
  return (
    <div className="conf">
      <div className="conf-track">
        <div className="conf-fill" style={{ width: `${pct}%`, background: color }} />
      </div>
      <span className="conf-val">{pct}%</span>
    </div>
  );
}

export function CitationChips({ citations }: { citations: Citation[] }) {
  if (!citations?.length) return <span style={{ color: "var(--text-faint)" }}>—</span>;
  return (
    <div className="cites">
      {citations.map((c, i) => (
        <span
          key={i}
          className={`cite ${c.mechanicallyVerified ? "ok" : "no"}`}
          title={c.quote}
        >
          <span className="cite-mark">{c.mechanicallyVerified ? "✓" : "✕"}</span>
          <span className="src">{c.source}{c.page ? ` p.${c.page}` : ""}</span>
        </span>
      ))}
    </div>
  );
}

export function Spinner() {
  return <span className="spinner" />;
}

export function timeAgo(iso?: string): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  const s = Math.floor((Date.now() - then) / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}
