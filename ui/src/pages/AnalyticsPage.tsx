import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import type { NosLegalBreakdown } from "../types";
import { CostDashboard } from "../CostDashboard";
import { ErrorState } from "../Library";

export function AnalyticsPage({ notify }: { notify: (m: string) => void }) {
  return (
    <div className="page-scroll">
      <div className="page" style={{ maxWidth: 980 }}>
        <div className="page-head">
          <h1 className="page-title">Analytics</h1>
          <p className="page-sub">Model spend, token economics, and the NOSLEGAL taxonomy breakdown across the matter book.</p>
        </div>

        <NosLegalSection />

        <div className="section-card">
          <div className="section-card-title">Cost &amp; tokens</div>
          <CostDashboard notify={notify} />
        </div>
      </div>
    </div>
  );
}

function NosLegalSection() {
  const [data, setData] = useState<NosLegalBreakdown | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    api.noslegalAnalytics()
      .then(setData)
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => { load(); }, [load]);

  const facets: Array<[string, Record<string, number>]> = data ? [
    ["Area of law", data.byAreaOfLaw],
    ["Work type", data.byWorkType],
    ["Sector", data.bySector],
    ["Asset type", data.byAssetType],
  ] : [];

  const hasAny = facets.some(([, m]) => Object.keys(m).length > 0);

  return (
    <div className="section-card" style={{ marginBottom: 22 }}>
      <div className="section-card-head">
        <div className="section-card-title">NOSLEGAL taxonomy {data ? `· ${data.total} tasks` : ""}</div>
        <button className="btn ghost sm" onClick={load}>↻ Refresh</button>
      </div>
      {loading && <div className="placeholder">Loading NOSLEGAL breakdown…</div>}
      {error && <ErrorState message={error} onRetry={load} />}
      {!loading && !error && data && !hasAny && (
        <div className="placeholder">No NOSLEGAL tags yet. Tags are auto-detected as matters are submitted.</div>
      )}
      {!loading && !error && hasAny && (
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))", gap: 18 }}>
          {facets.map(([label, counts]) => <FacetBars key={label} label={label} counts={counts} />)}
        </div>
      )}
    </div>
  );
}

function FacetBars({ label, counts }: { label: string; counts: Record<string, number> }) {
  const entries = Object.entries(counts).sort((a, b) => b[1] - a[1]).slice(0, 8);
  const max = Math.max(...entries.map(([, v]) => v), 1);
  return (
    <div>
      <div style={{ fontSize: 10.5, fontWeight: 700, letterSpacing: "0.08em", textTransform: "uppercase", color: "var(--text-faint)", marginBottom: 10 }}>
        {label}
      </div>
      {entries.length === 0 && <div className="grid-meta">No data.</div>}
      {entries.map(([name, v]) => (
        <div key={name} style={{ marginBottom: 8 }}>
          <div style={{ display: "flex", justifyContent: "space-between", fontSize: 12, marginBottom: 3 }}>
            <span style={{ color: "var(--text-dim)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{name}</span>
            <span style={{ fontFamily: "var(--font-mono)", color: "var(--text)" }}>{v}</span>
          </div>
          <div className="conf-track">
            <div className="conf-fill" style={{ width: `${(v / max) * 100}%`, background: "var(--accent)" }} />
          </div>
        </div>
      ))}
    </div>
  );
}
