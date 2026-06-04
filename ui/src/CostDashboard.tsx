// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

import { useEffect, useState, type ReactNode } from "react";
import { api } from "./api";
import type { CostSummary } from "./types";

// ─── Helpers ──────────────────────────────────────────────────────────────────

function fmt$(n: number): string {
  if (n < 0.001) return `$${(n * 1000).toFixed(3)}m`;
  if (n < 1)    return `$${n.toFixed(4)}`;
  if (n < 10)   return `$${n.toFixed(3)}`;
  return `$${n.toFixed(2)}`;
}

function fmtTok(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000)     return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

function fmtWh(n: number): string {
  if (n < 1)    return `${(n * 1000).toFixed(1)} mWh`;
  if (n < 1000) return `${n.toFixed(2)} Wh`;
  return `${(n / 1000).toFixed(3)} kWh`;
}

function fmtCo2(g: number): string {
  if (g < 1)    return `${(g * 1000).toFixed(1)} mg`;
  if (g < 1000) return `${g.toFixed(1)} g`;
  return `${(g / 1000).toFixed(3)} kg`;
}

// ─── Tiny SVG bar chart (horizontal) ─────────────────────────────────────────

interface BarEntry { label: string; value: number; sub?: string; color: string }

function BarChart({ entries, width = 340, rowH = 32 }: { entries: BarEntry[]; width?: number; rowH?: number }) {
  if (!entries.length) return null;
  const max = Math.max(...entries.map((e) => e.value), 0.000001);
  const labelW = 148;
  const barW = width - labelW - 56;   // 56 = value label column
  const height = entries.length * rowH + 4;

  return (
    <svg width={width} height={height} style={{ overflow: "visible", display: "block" }}>
      {entries.map((e, i) => {
        const y = i * rowH + 4;
        const w = Math.max(2, (e.value / max) * barW);
        return (
          <g key={e.label}>
            {/* label */}
            <text x={labelW - 8} y={y + rowH / 2 + 1} textAnchor="end"
              fontSize={11} fill="var(--text-dim)" dominantBaseline="middle"
              style={{ fontFamily: "var(--font-mono)" }}>
              {e.label.length > 20 ? e.label.slice(0, 19) + "…" : e.label}
            </text>
            {/* track */}
            <rect x={labelW} y={y + (rowH - 14) / 2} width={barW} height={14}
              rx={4} fill="rgba(236,231,218,0.05)" />
            {/* fill */}
            <rect x={labelW} y={y + (rowH - 14) / 2} width={w} height={14}
              rx={4} fill={e.color} opacity={0.88} />
            {/* value */}
            <text x={labelW + barW + 8} y={y + rowH / 2 + 1} fontSize={11}
              fill="var(--text)" dominantBaseline="middle"
              style={{ fontFamily: "var(--font-mono)" }}>
              {e.sub ?? ""}
            </text>
          </g>
        );
      })}
    </svg>
  );
}

// ─── Stacked token bar (input / cache-write / cache-read / output) ────────────

function TokenStackBar({ input, cacheWrite, cacheRead, output }: {
  input: number; cacheWrite: number; cacheRead: number; output: number;
}) {
  const total = input + cacheWrite + cacheRead + output || 1;
  const segs = [
    { label: "Input",        value: input,      color: "var(--gold)" },
    { label: "Cache write",  value: cacheWrite, color: "var(--amber)" },
    { label: "Cache read",   value: cacheRead,  color: "var(--green)" },
    { label: "Output",       value: output,     color: "var(--blue)" },
  ].filter((s) => s.value > 0);

  let x = 0;
  return (
    <div>
      <svg width="100%" height={18} style={{ display: "block", borderRadius: 5, overflow: "hidden" }}
        viewBox="0 0 400 18" preserveAspectRatio="none">
        {segs.map((s) => {
          const w = (s.value / total) * 400;
          const rect = <rect key={s.label} x={x} y={0} width={w} height={18} fill={s.color} opacity={0.82} />;
          x += w;
          return rect;
        })}
      </svg>
      <div style={{ display: "flex", gap: 14, marginTop: 7, flexWrap: "wrap" }}>
        {segs.map((s) => (
          <div key={s.label} style={{ display: "flex", alignItems: "center", gap: 5, fontSize: 11, color: "var(--text-dim)" }}>
            <span style={{ width: 10, height: 10, borderRadius: 3, background: s.color, opacity: 0.85, display: "inline-block" }} />
            {s.label} <span style={{ color: "var(--text)", fontFamily: "var(--font-mono)" }}>{fmtTok(s.value)}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

// ─── Stat card ────────────────────────────────────────────────────────────────

function StatCard({ label, value, sub, accent }: { label: string; value: string; sub?: string; accent?: string }) {
  return (
    <div style={{
      background: "var(--panel-2)", border: "1px solid var(--border)",
      borderRadius: "var(--r)", padding: "14px 18px",
    }}>
      <div style={{ fontSize: 11, color: "var(--text-dim)", marginBottom: 5, textTransform: "uppercase", letterSpacing: "0.05em" }}>{label}</div>
      <div style={{ fontSize: 22, fontFamily: "var(--font-display)", color: accent ?? "var(--gold)", lineHeight: 1 }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: "var(--text-faint)", marginTop: 5 }}>{sub}</div>}
    </div>
  );
}

// ─── Section header ───────────────────────────────────────────────────────────

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div style={{ marginBottom: 28 }}>
      <div style={{
        fontSize: 11, fontWeight: 700, letterSpacing: "0.08em", textTransform: "uppercase",
        color: "var(--text-faint)", marginBottom: 14, paddingBottom: 6,
        borderBottom: "1px solid var(--border)",
      }}>{title}</div>
      {children}
    </div>
  );
}

// ─── Main component ───────────────────────────────────────────────────────────

const CONTEXT_LABEL: Record<string, string> = {
  task:             "Agent task",
  descriptor:       "Need/Offer",
  synthesis:        "Synthesis",
  tabulate:         "Tabulate",
  round_goal:       "Round goal",
  protocol_debate:  "Debate",
  protocol_verify:  "Verify",
  tone_analysis:    "Tone analysis",
  classification:   "Classification",
};

const MODEL_COLORS: Record<string, string> = {
  "claude-haiku-4-5-20251001": "var(--blue)",
  "claude-haiku-4-5":          "var(--blue)",
  "claude-sonnet-4-6":         "var(--gold)",
  "claude-opus-4-8":           "var(--amber)",
  "claude-opus-4-5":           "var(--amber)",
};

function modelColor(m: string): string {
  return MODEL_COLORS[m] ?? "var(--text-dim)";
}

export function CostDashboard({ notify }: { notify: (m: string) => void }) {
  const [summary, setSummary] = useState<CostSummary | null>(null);
  const [busy, setBusy] = useState(true);

  useEffect(() => {
    setBusy(true);
    api.getCostSummary()
      .then(setSummary)
      .catch((e: Error) => notify(e.message))
      .finally(() => setBusy(false));
  }, [notify]);

  if (busy) return <div className="placeholder">Loading cost data…</div>;
  if (!summary) return <div className="placeholder">No cost data available.</div>;
  if (summary.entryCount === 0) return <div className="placeholder">No API calls recorded yet. Run a task to see costs here.</div>;

  const totalTokens = summary.totalInputTokens + summary.totalOutputTokens
    + summary.totalCacheWriteTokens + summary.totalCacheReadTokens;

  // Cache read savings: tokens that cost 0.10× instead of 1.0×, so saved 0.90× input cost.
  // We don't have per-model prices here, so estimate at $1/MTok (Haiku).
  const cacheReadSavingsApprox = (summary.totalCacheReadTokens * 0.90) / 1_000_000;

  type ModelStats = CostSummary["byModel"][string];
  type CtxStats = CostSummary["byContext"][string];

  // ── Cost by model ──────────────────────────────────────────────────────────
  const modelEntries: BarEntry[] = (Object.entries(summary.byModel) as [string, ModelStats][])
    .sort((a, b) => b[1].usd - a[1].usd)
    .map(([model, stats]) => ({
      label: model.replace("claude-", "").replace(/-20\d+/, ""),
      value: stats.usd,
      sub: fmt$(stats.usd),
      color: modelColor(model),
    }));

  // ── Cost by context ────────────────────────────────────────────────────────
  const contextEntries: BarEntry[] = (Object.entries(summary.byContext) as [string, CtxStats][])
    .sort((a, b) => b[1].usd - a[1].usd)
    .map(([ctx, stats]) => ({
      label: CONTEXT_LABEL[ctx] ?? ctx,
      value: stats.usd,
      sub: `${fmt$(stats.usd)} · ${stats.calls} calls`,
      color: "var(--accent)",
    }));

  // ── Per-model table ────────────────────────────────────────────────────────
  const modelRows = (Object.entries(summary.byModel) as [string, ModelStats][]).sort((a, b) => b[1].usd - a[1].usd);

  return (
    <div style={{ padding: "0 2px" }}>
      {/* ── Overview cards ─────────────────────────────────────────────────── */}
      <Section title="Overview">
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(140px, 1fr))", gap: 10, marginBottom: 4 }}>
          <StatCard label="Total cost" value={fmt$(summary.totalUsd)} sub={`${summary.entryCount} calls`} />
          <StatCard label="Total tokens" value={fmtTok(totalTokens)} sub={`in ${fmtTok(summary.totalInputTokens)} · out ${fmtTok(summary.totalOutputTokens)}`} accent="var(--blue)" />
          <StatCard label="Cache reads" value={fmtTok(summary.totalCacheReadTokens)} sub={`≈ ${fmt$(cacheReadSavingsApprox)} saved`} accent="var(--green)" />
          {summary.totalWh > 0 ? (
            <StatCard label="Power draw" value={fmtWh(summary.totalWh)} sub="local inference estimate" accent="var(--text-dim)" />
          ) : (
            <StatCard label="Cache writes" value={fmtTok(summary.totalCacheWriteTokens)} sub="tokens written to cache" accent="var(--amber)" />
          )}
          {summary.totalCo2Grams > 0 && (
            <StatCard label="CO₂ emitted" value={fmtCo2(summary.totalCo2Grams)} sub="grid intensity · local only" accent="var(--text-faint)" />
          )}
          {summary.totalElectricityCostUsd > 0 && (
            <StatCard label="Elec. cost" value={fmt$(summary.totalElectricityCostUsd)} sub="IEA tariff · local only" accent="var(--text-faint)" />
          )}
        </div>
      </Section>

      {/* ── Token composition ──────────────────────────────────────────────── */}
      <Section title="Token breakdown">
        <TokenStackBar
          input={summary.totalInputTokens}
          cacheWrite={summary.totalCacheWriteTokens}
          cacheRead={summary.totalCacheReadTokens}
          output={summary.totalOutputTokens}
        />
      </Section>

      {/* ── Cost by model ──────────────────────────────────────────────────── */}
      {modelEntries.length > 0 && (
        <Section title="Cost by model">
          <BarChart entries={modelEntries} />
        </Section>
      )}

      {/* ── Cost by context ────────────────────────────────────────────────── */}
      {contextEntries.length > 0 && (
        <Section title="Cost by context">
          <BarChart entries={contextEntries} />
        </Section>
      )}

      {/* ── Per-model detail table ──────────────────────────────────────────── */}
      <Section title="Model detail">
        <div style={{ overflowX: "auto" }}>
          <table style={{ width: "100%", borderCollapse: "collapse", fontSize: 12 }}>
            <thead>
              <tr style={{ color: "var(--text-faint)", textAlign: "left" }}>
                {["Model", "Calls", "Cost", "Input", "Output", "Cache W", "Cache R", "Wh", "CO₂", "Elec."].map((h) => (
                  <th key={h} style={{ padding: "4px 10px 8px", fontWeight: 600, whiteSpace: "nowrap",
                    borderBottom: "1px solid var(--border)", letterSpacing: "0.04em", fontSize: 10, textTransform: "uppercase" }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {modelRows.map(([model, s]) => (
                <tr key={model} style={{ borderBottom: "1px solid var(--border)" }}>
                  <td style={{ padding: "7px 10px", fontFamily: "var(--font-mono)", fontSize: 11, color: modelColor(model) }}>
                    {model.replace("claude-", "").replace(/-20\d+/, "")}
                  </td>
                  <td style={{ padding: "7px 10px", color: "var(--text-dim)", textAlign: "right" }}>{s.calls}</td>
                  <td style={{ padding: "7px 10px", fontFamily: "var(--font-mono)", color: "var(--gold)" }}>{fmt$(s.usd)}</td>
                  <td style={{ padding: "7px 10px", fontFamily: "var(--font-mono)", color: "var(--text-dim)", textAlign: "right" }}>{fmtTok(s.inputTokens)}</td>
                  <td style={{ padding: "7px 10px", fontFamily: "var(--font-mono)", color: "var(--text-dim)", textAlign: "right" }}>{fmtTok(s.outputTokens)}</td>
                  <td style={{ padding: "7px 10px", fontFamily: "var(--font-mono)", color: "var(--amber)", textAlign: "right" }}>{s.cacheWriteTokens ? fmtTok(s.cacheWriteTokens) : "—"}</td>
                  <td style={{ padding: "7px 10px", fontFamily: "var(--font-mono)", color: "var(--green)", textAlign: "right" }}>{s.cacheReadTokens ? fmtTok(s.cacheReadTokens) : "—"}</td>
                  <td style={{ padding: "7px 10px", fontFamily: "var(--font-mono)", color: "var(--text-faint)", textAlign: "right" }}>{s.wh ? fmtWh(s.wh) : "—"}</td>
                  <td style={{ padding: "7px 10px", fontFamily: "var(--font-mono)", color: "var(--text-faint)", textAlign: "right" }}>{s.co2Grams ? fmtCo2(s.co2Grams) : "—"}</td>
                  <td style={{ padding: "7px 10px", fontFamily: "var(--font-mono)", color: "var(--text-faint)", textAlign: "right" }}>{s.electricityCostUsd ? fmt$(s.electricityCostUsd) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Section>
    </div>
  );
}
