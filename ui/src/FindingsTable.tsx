import { useMemo, useState } from "react";
import {
  createColumnHelper, flexRender, getCoreRowModel, getFilteredRowModel,
  getSortedRowModel, useReactTable, type SortingState,
} from "@tanstack/react-table";
import { motion } from "framer-motion";
import type { Finding, Task, GateRequest } from "./types";
import { ConfidenceBar, CitationChips } from "./primitives";
import { api } from "./api";

const col = createColumnHelper<Finding>();

export function FindingsTable({ task, onChange, notify }: {
  task: Task;
  onChange: () => void;
  notify: (msg: string) => void;
}) {
  const [sorting, setSorting] = useState<SortingState>([{ id: "confidence", desc: false }]);
  const [filter, setFilter] = useState("");
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [busy, setBusy] = useState<string | null>(null);
  // Per-lawyer display preference: some reviewers don't want client-voice
  // hints on every gate. Persisted locally, not a firm-wide setting.
  const [showClientVoice, setShowClientVoice] = useState(
    () => localStorage.getItem("biglaw.showClientVoice") !== "0",
  );
  function toggleClientVoice() {
    setShowClientVoice((v) => {
      localStorage.setItem("biglaw.showClientVoice", v ? "0" : "1");
      return !v;
    });
  }

  const gateByFinding = useMemo(() => {
    const m = new Map<string, GateRequest>();
    for (const g of task.pendingGates) if (g.status === "pending") m.set(g.findingId, g);
    return m;
  }, [task.pendingGates]);

  async function act(gate: GateRequest, kind: "approve" | "reject") {
    if (kind === "reject") {
      const reason = window.prompt("Reason for rejecting this finding?");
      if (!reason) return;
      setBusy(gate.id);
      try { await api.rejectGate(task.id, gate.id, reason); notify("Finding rejected"); onChange(); }
      catch (e) { notify((e as Error).message); } finally { setBusy(null); }
    } else {
      setBusy(gate.id);
      try { await api.approveGate(task.id, gate.id); notify("Finding approved"); onChange(); }
      catch (e) { notify((e as Error).message); } finally { setBusy(null); }
    }
  }

  const columns = useMemo(() => [
    col.accessor("agentName", {
      header: "Agent",
      cell: (c) => (
        <div className="cell-agent">
          {c.getValue()}
          <span className="agent-id">R{c.row.original.round} · {c.row.original.agentId}</span>
        </div>
      ),
    }),
    col.accessor("content", {
      header: "Finding",
      cell: (c) => {
        const f = c.row.original;
        const open = expanded[f.id];
        return (
          <div className="cell-finding">
            {c.getValue()}
            {f.challenged && f.challenge && (
              <>
                <button className="expand-btn" onClick={() => setExpanded((s) => ({ ...s, [f.id]: !s[f.id] }))}>
                  ⚔ {open ? "hide challenge" : "challenged — view"}
                </button>
                {open && (
                  <div className="challenge-box">
                    <div className="label">{f.challenge.challengerName} challenges</div>
                    <div>{f.challenge.content}</div>
                    {f.challenge.resolution && (
                      <div style={{ marginTop: 8, color: "var(--text-dim)" }}>
                        <strong style={{ color: "var(--gold)" }}>Resolution:</strong> {f.challenge.resolution}
                      </div>
                    )}
                  </div>
                )}
              </>
            )}
          </div>
        );
      },
    }),
    col.accessor("confidence", { header: "Confidence", cell: (c) => <ConfidenceBar value={c.getValue()} /> }),
    col.display({ id: "cites", header: "Citations", cell: (c) => <CitationChips citations={c.row.original.citations} /> }),
    col.display({
      id: "verified",
      header: "Verified",
      cell: (c) => {
        const v = c.row.original.verificationResult;
        if (!v) return <span style={{ color: "var(--text-faint)" }}>—</span>;
        const ok = v.passed;
        const passed = v.checks.filter((k) => k.passed).length;
        return <span className={`pill ${ok ? "green" : "red"}`}>{ok ? "✓" : "✕"} {passed}/{v.checks.length}</span>;
      },
    }),
    col.display({
      id: "review",
      header: "Review",
      enableSorting: false,
      cell: (c) => {
        const f = c.row.original;
        const gate = gateByFinding.get(f.id);
        if (gate) {
          return (
            <div className="gate-actions">
              {gate.clientVoiceNote && showClientVoice && (
                <div className="client-voice-note" title="From the client's advocacy brief (Remy / CNTXT)">
                  <div className="label" style={{ color: "var(--gold)", fontSize: 11, marginBottom: 3, display: "flex", justifyContent: "space-between", gap: 8 }}>
                    <span>⚖ Remy — client advocate</span>
                    <button className="expand-btn" style={{ margin: 0 }} onClick={toggleClientVoice} title="Hide Remy's notes (just for you)">hide</button>
                  </div>
                  <div style={{ fontSize: 12, color: "var(--text-dim)", whiteSpace: "pre-wrap", marginBottom: 6, maxWidth: 260 }}>
                    {gate.clientVoiceNote}
                  </div>
                </div>
              )}
              {gate.clientVoiceNote && !showClientVoice && (
                <button className="expand-btn" style={{ marginBottom: 4 }} onClick={toggleClientVoice} title="Show Remy's client-advocate note">
                  ⚖ remy
                </button>
              )}
              <button className="btn approve sm" disabled={busy === gate.id} onClick={() => act(gate, "approve")}>
                {busy === gate.id ? "…" : "✓ Approve"}
              </button>
              <button className="btn reject sm" disabled={busy === gate.id} onClick={() => act(gate, "reject")}>
                ✕ Reject
              </button>
            </div>
          );
        }
        return <span className="pill green">cleared</span>;
      },
    }),
  ], [gateByFinding, busy, expanded]);

  const table = useReactTable({
    data: task.findings,
    columns,
    state: { sorting, globalFilter: filter },
    onSortingChange: setSorting,
    onGlobalFilterChange: setFilter,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
  });

  if (!task.findings.length) {
    return <div className="placeholder">No findings yet — agents are still deliberating.</div>;
  }

  const gateCount = gateByFinding.size;

  return (
    <div>
      <div className="grid-toolbar">
        <div className="search">
          <span className="ico">⌕</span>
          <input
            placeholder="Filter findings…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </div>
        <div className="grid-meta">
          {table.getRowModel().rows.length} findings
          {gateCount > 0 && <span style={{ color: "var(--amber)" }}> · {gateCount} awaiting review</span>}
        </div>
      </div>

      <div className="grid-wrap">
        <div className="grid-scroll">
          <table className="grid">
            <thead>
              {table.getHeaderGroups().map((hg) => (
                <tr key={hg.id}>
                  {hg.headers.map((h) => {
                    const dir = h.column.getIsSorted();
                    return (
                      <th key={h.id} onClick={h.column.getCanSort() ? h.column.getToggleSortingHandler() : undefined}
                          style={{ cursor: h.column.getCanSort() ? "pointer" : "default" }}>
                        {flexRender(h.column.columnDef.header, h.getContext())}
                        {dir && <span className="sort">{dir === "asc" ? "▲" : "▼"}</span>}
                      </th>
                    );
                  })}
                </tr>
              ))}
            </thead>
            <tbody>
              {table.getRowModel().rows.map((row, i) => (
                <motion.tr
                  key={row.id}
                  initial={{ opacity: 0, y: 6 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ delay: Math.min(i * 0.02, 0.3), duration: 0.25 }}
                >
                  {row.getVisibleCells().map((cell) => (
                    <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
                  ))}
                </motion.tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
