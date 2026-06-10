import { useCallback, useEffect, useState } from "react";
import { api, streamAlerts } from "../api";
import type { BudgetAlert, DocketAlert, RegulationAlert, WatchedDocket } from "../types";
import { ErrorState } from "../Library";
import { timeAgo } from "../primitives";

/**
 * The Watchtower — live monitoring: docket watch list + alerts, regulatory
 * pulse, and budget threshold alerts. Streams follow the AuditRail SSE pattern.
 */
export function WatchtowerPage({ notify }: { notify: (m: string) => void }) {
  return (
    <div className="page-scroll">
      <div className="page">
        <div className="page-head">
          <h1 className="page-title">Watchtower</h1>
          <p className="page-sub">Court dockets, regulatory developments, and budget thresholds — watched continuously, streamed live.</p>
        </div>

        <DocketsSection notify={notify} />
        <div className="watch-streams">
          <RegulatorySection notify={notify} />
          <BudgetAlertsSection />
        </div>
      </div>
    </div>
  );
}

// ─── Dockets ───────────────────────────────────────────────────────────────────

function DocketsSection({ notify }: { notify: (m: string) => void }) {
  const [dockets, setDockets] = useState<WatchedDocket[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [nw, setNw] = useState({ matterNumber: "", docketNumber: "", court: "", caseName: "" });
  const [alerts, setAlerts] = useState<DocketAlert[]>([]);
  const [streamDown, setStreamDown] = useState(false);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    api.listDockets()
      .then(setDockets)
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => { load(); }, [load]);

  useEffect(() => {
    return streamAlerts<DocketAlert>(
      "/dockets/alerts/stream",
      (a) => setAlerts((prev) => [a, ...prev].slice(0, 50)),
      () => setStreamDown(true),
    );
  }, []);

  async function watch() {
    if (!nw.matterNumber.trim() || !nw.docketNumber.trim() || !nw.court.trim()) {
      notify("Matter, docket number, and court slug are required");
      return;
    }
    setBusy(true);
    try {
      await api.watchDocket({
        matterNumber: nw.matterNumber.trim(),
        docketNumber: nw.docketNumber.trim(),
        court: nw.court.trim(),
        caseName: nw.caseName.trim() || undefined,
      });
      setNw({ matterNumber: "", docketNumber: "", court: "", caseName: "" });
      notify("Docket added to the watch list");
      load();
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  async function unwatch(matterNumber: string) {
    if (!window.confirm(`Stop watching the docket for ${matterNumber}?`)) return;
    try { await api.unwatchDocket(matterNumber); notify("Docket unwatched"); load(); }
    catch (e) { notify((e as Error).message); }
  }

  async function checkNow() {
    setBusy(true);
    try {
      const res = await api.docketCheckNow();
      notify(`Checked ${res.watching} watched docket${res.watching === 1 ? "" : "s"}`);
      load();
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  return (
    <div className="section-card" style={{ marginBottom: 22 }}>
      <div className="section-card-head">
        <div className="section-card-title">Docket watch</div>
        <button className="btn ghost sm" disabled={busy} onClick={checkNow}>⟳ Check now</button>
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 0.7fr 1.2fr auto", gap: 10, alignItems: "end", marginBottom: 16 }}>
        <div className="field"><label>Matter</label>
          <input value={nw.matterNumber} onChange={(e) => setNw({ ...nw, matterNumber: e.target.value })} placeholder="M-2026-001" /></div>
        <div className="field"><label>Docket number</label>
          <input value={nw.docketNumber} onChange={(e) => setNw({ ...nw, docketNumber: e.target.value })} placeholder="1:23-cv-01234" /></div>
        <div className="field"><label>Court slug</label>
          <input value={nw.court} onChange={(e) => setNw({ ...nw, court: e.target.value })} placeholder="nysd" /></div>
        <div className="field"><label>Case name (optional)</label>
          <input value={nw.caseName} onChange={(e) => setNw({ ...nw, caseName: e.target.value })} placeholder="Acme v. Beta" /></div>
        <button className="btn primary" disabled={busy} onClick={watch}>＋ Watch</button>
      </div>

      {loading && <div className="placeholder">Loading watch list…</div>}
      {error && <ErrorState message={error} onRetry={load} />}
      {!loading && !error && dockets.length === 0 && (
        <div className="placeholder">No dockets watched yet. Add a CourtListener docket above and the Watchtower polls it for new filings.</div>
      )}
      {!loading && !error && dockets.length > 0 && (
        <div className="grid-wrap">
          <div className="grid-scroll">
            <table className="grid">
              <thead><tr><th>Matter</th><th>Docket</th><th>Court</th><th>Case</th><th>Filings seen</th><th>Last checked</th><th></th></tr></thead>
              <tbody>
                {dockets.map((d) => (
                  <tr key={d.matterNumber}>
                    <td style={{ fontFamily: "var(--font-mono)" }}>{d.matterNumber}</td>
                    <td style={{ fontFamily: "var(--font-mono)" }}>{d.docketNumber}</td>
                    <td><span className="pill sm blue">{d.court}</span></td>
                    <td style={{ color: "var(--text-dim)" }}>{d.caseName ?? "—"}</td>
                    <td style={{ fontFamily: "var(--font-mono)" }}>{d.totalFilingsSeen}</td>
                    <td style={{ whiteSpace: "nowrap", color: "var(--text-dim)" }}>{d.lastCheckedAt ? timeAgo(d.lastCheckedAt) : "never"}</td>
                    <td><button className="btn reject sm" onClick={() => unwatch(d.matterNumber)}>✕</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {(alerts.length > 0 || streamDown) && (
        <div style={{ marginTop: 14 }}>
          <div className="rnd-label">Live docket alerts {streamDown && <span style={{ color: "var(--text-faint)" }}>· stream unavailable</span>}</div>
          {alerts.map((a) => (
            <div key={a.id} className="alert-row">
              <span className="audit-time">{timeAgo(a.detectedAt)}</span>
              <span style={{ color: "var(--gold)", fontFamily: "var(--font-mono)", fontSize: 11.5 }}>{a.matterNumber}</span>
              <span style={{ flex: 1 }}>
                {a.newFilingCount} new filing{a.newFilingCount === 1 ? "" : "s"} in <strong>{a.caseName}</strong>
              </span>
              <a className="btn ghost sm" href={a.courtListenerUrl} target="_blank" rel="noreferrer">View ↗</a>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// ─── Regulatory pulse ──────────────────────────────────────────────────────────

function RegulatorySection({ notify }: { notify: (m: string) => void }) {
  const [alerts, setAlerts] = useState<RegulationAlert[]>([]);
  const [streamDown, setStreamDown] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    return streamAlerts<RegulationAlert>(
      "/regulatory/alerts/stream",
      (a) => setAlerts((prev) => [a, ...prev.filter((p) => p.id !== a.id)].slice(0, 50)),
      () => setStreamDown(true),
    );
  }, []);

  async function checkNow() {
    setBusy(true);
    try {
      const res = await api.regulatoryCheckNow();
      if (res.alerts.length) {
        setAlerts((prev) => {
          const seen = new Set(prev.map((p) => p.id));
          return [...res.alerts.filter((a) => !seen.has(a.id)), ...prev].slice(0, 50);
        });
      }
      notify(`Regulatory pulse — ${res.checked} matters scanned, ${res.alerts.length} alert${res.alerts.length === 1 ? "" : "s"}`);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  return (
    <div className="section-card">
      <div className="section-card-head">
        <div className="section-card-title">Regulatory pulse</div>
        <button className="btn ghost sm" disabled={busy} onClick={checkNow}>{busy ? "Scanning…" : "⟳ Check now"}</button>
      </div>
      {streamDown && (
        <div className="grid-meta" style={{ marginBottom: 10 }}>
          Live stream unavailable — regulatory pulse may be disabled (REG_PULSE_ENABLED) or partner-only.
        </div>
      )}
      {alerts.length === 0 && (
        <div className="placeholder">No regulatory alerts yet. New developments affecting active matters appear here.</div>
      )}
      {alerts.map((a) => (
        <div key={a.id} className="alert-card">
          <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 6, flexWrap: "wrap" }}>
            <span className="pill sm blue">{a.practiceArea}</span>
            <span className="pill sm">{a.jurisdiction}</span>
            {a.matterNumber && <span className="pill sm gold">{a.matterNumber}</span>}
            <span className="audit-time" style={{ marginLeft: "auto" }}>{timeAgo(a.detectedAt)}</span>
          </div>
          <a href={a.url} target="_blank" rel="noreferrer" style={{ color: "var(--text)", fontWeight: 600, fontSize: 13.5, textDecoration: "none" }}>
            {a.headline} ↗
          </a>
          <div style={{ color: "var(--text-dim)", fontSize: 12.5, marginTop: 5, lineHeight: 1.5 }}>{a.summary}</div>
        </div>
      ))}
    </div>
  );
}

// ─── Budget alerts ─────────────────────────────────────────────────────────────

function BudgetAlertsSection() {
  const [alerts, setAlerts] = useState<BudgetAlert[]>([]);
  const [streamDown, setStreamDown] = useState(false);

  useEffect(() => {
    return streamAlerts<BudgetAlert>(
      "/budget/alerts/stream",
      (a) => setAlerts((prev) => [a, ...prev].slice(0, 50)),
      () => setStreamDown(true),
    );
  }, []);

  return (
    <div className="section-card">
      <div className="section-card-head">
        <div className="section-card-title">Budget alerts</div>
        <span className={`dot ${streamDown ? "failed" : "complete"}`} title={streamDown ? "Stream unavailable" : "Streaming"} />
      </div>
      {alerts.length === 0 && (
        <div className="placeholder">
          {streamDown
            ? "Live stream unavailable — budget alerts are partner-only."
            : "No budget alerts this session. Threshold crossings on matter budgets land here live."}
        </div>
      )}
      {alerts.map((a, i) => (
        <div key={`${a.matterNumber}-${a.threshold}-${i}`} className="alert-card">
          <div style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap" }}>
            <span className="pill sm amber">{Math.round(a.threshold * 100)}% threshold</span>
            <span style={{ fontFamily: "var(--font-mono)", fontSize: 12 }}>{a.matterNumber}</span>
            <span className="audit-time" style={{ marginLeft: "auto" }}>{timeAgo(a.triggeredAt)}</span>
          </div>
          <div style={{ color: "var(--text-dim)", fontSize: 12.5, marginTop: 5 }}>
            ${a.burnUsd.toLocaleString()} burned of ${a.budgetUsd.toLocaleString()} ({Math.round(a.burnPct * 100)}%) — client {a.clientNumber}
          </div>
        </div>
      ))}
    </div>
  );
}
