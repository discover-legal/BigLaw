import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import type {
  BudgetBurn, BudgetPrediction, Client, DeadlineJurisdiction, DeadlineResult,
  HealthSignal, MatterHealthScore, PortfolioHealthSummary,
} from "../types";
import { ErrorState } from "../Library";

function fmt$(n: number): string {
  return `$${n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

const SIGNAL_COLOR: Record<HealthSignal, string> = {
  green: "var(--green)", amber: "var(--amber)", red: "var(--red)",
};

export function BudgetsPage({ notify, isPartner }: { notify: (m: string) => void; isPartner: boolean }) {
  return (
    <div className="page-scroll">
      <div className="page">
        <div className="page-head">
          <h1 className="page-title">Budgets &amp; deadlines</h1>
          <p className="page-sub">Matter budgets and burn, cost prediction, portfolio health, and jurisdictional deadline computation.</p>
        </div>

        {isPartner && <PortfolioHealthSection />}
        {isPartner && <MatterBudgetSection notify={notify} />}
        <DeadlinesSection notify={notify} />
        {!isPartner && (
          <div className="section-card">
            <div className="section-card-title">Budgets &amp; portfolio health</div>
            <div className="placeholder">Budget tracking and portfolio health are partner-only.</div>
          </div>
        )}
      </div>
    </div>
  );
}

// ─── Portfolio health ──────────────────────────────────────────────────────────

function PortfolioHealthSection() {
  const [data, setData] = useState<PortfolioHealthSummary | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    api.portfolioHealth()
      .then(setData)
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => { load(); }, [load]);

  return (
    <div className="section-card" style={{ marginBottom: 22 }}>
      <div className="section-card-head">
        <div className="section-card-title">Portfolio health</div>
        <button className="btn ghost sm" onClick={load}>↻ Refresh</button>
      </div>
      {loading && <div className="placeholder">Computing portfolio health…</div>}
      {error && <ErrorState message={error} onRetry={load} />}
      {!loading && !error && data && data.totalMatters === 0 && (
        <div className="placeholder">No matters with matter numbers yet. Submit a matter with a matter number to see health scores.</div>
      )}
      {!loading && !error && data && data.totalMatters > 0 && (
        <>
          <div style={{ display: "flex", gap: 10, marginBottom: 16, flexWrap: "wrap" }}>
            <span className="pill green">● {data.green} healthy</span>
            <span className="pill amber">● {data.amber} watch</span>
            <span className="pill red">● {data.red} at risk</span>
            <span className="grid-meta" style={{ alignSelf: "center" }}>{data.totalMatters} matters</span>
          </div>
          <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
            {data.matters.map((m) => (
              <HealthRow key={m.matterNumber} m={m}
                open={expanded === m.matterNumber}
                onToggle={() => setExpanded((cur) => (cur === m.matterNumber ? null : m.matterNumber))} />
            ))}
          </div>
        </>
      )}
    </div>
  );
}

function HealthRow({ m, open, onToggle }: { m: MatterHealthScore; open: boolean; onToggle: () => void }) {
  return (
    <div className="grid-wrap" style={{ overflow: "hidden" }}>
      <button className="round-head" onClick={onToggle} style={{ width: "100%" }}>
        <span className="round-chevron" style={{ transform: open ? "rotate(90deg)" : undefined }}>▸</span>
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 13, color: "var(--text)" }}>{m.matterNumber}</span>
        <span style={{ flex: 1 }} />
        {m.riskFactors.length > 0 && <span className="grid-meta">{m.riskFactors.length} risk{m.riskFactors.length === 1 ? "" : "s"}</span>}
        <span className="grid-meta">{m.trend}</span>
        <span style={{
          fontFamily: "var(--font-display)", fontSize: 18,
          color: SIGNAL_COLOR[m.signal], minWidth: 38, textAlign: "right",
        }}>{m.score}</span>
        <span className="dot" style={{ background: SIGNAL_COLOR[m.signal] }} />
      </button>
      {open && (
        <div style={{ padding: "0 18px 16px" }}>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(140px, 1fr))", gap: 8, marginBottom: 12 }}>
            {([
              ["Budget", m.dimensions.budgetHealth],
              ["Deadlines", m.dimensions.deadlineHealth],
              ["Activity", m.dimensions.activityFreshness],
              ["Gates", m.dimensions.gateBacklog],
              ["OCG", m.dimensions.ocgCompliance],
            ] as Array<[string, number]>).map(([label, v]) => (
              <div key={label}>
                <div style={{ fontSize: 10.5, color: "var(--text-faint)", textTransform: "uppercase", letterSpacing: "0.06em", marginBottom: 4 }}>{label}</div>
                <div className="conf-track"><div className="conf-fill" style={{ width: `${v}%`, background: v >= 70 ? "var(--green)" : v >= 40 ? "var(--amber)" : "var(--red)" }} /></div>
              </div>
            ))}
          </div>
          {m.riskFactors.length === 0 && <div className="grid-meta">No active risk factors.</div>}
          {m.riskFactors.map((r, i) => (
            <div key={i} style={{ display: "flex", gap: 8, alignItems: "flex-start", marginBottom: 6, fontSize: 12.5 }}>
              <span className={`pill sm ${r.severity === "high" ? "red" : r.severity === "medium" ? "amber" : ""}`}>{r.severity}</span>
              <span style={{ color: "var(--text-dim)" }}>
                {r.message}
                {r.suggestedAction && <em style={{ color: "var(--text-faint)" }}> — {r.suggestedAction}</em>}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// ─── Matter budget ─────────────────────────────────────────────────────────────

function MatterBudgetSection({ notify }: { notify: (m: string) => void }) {
  const [clients, setClients] = useState<Client[]>([]);
  const [clientsError, setClientsError] = useState<string | null>(null);
  const [clientId, setClientId] = useState("");
  const [matterNumber, setMatterNumber] = useState("");
  const [budgetInput, setBudgetInput] = useState("");
  const [burn, setBurn] = useState<BudgetBurn | null>(null);
  const [burnMsg, setBurnMsg] = useState<string | null>(null);
  const [prediction, setPrediction] = useState<BudgetPrediction | null>(null);
  const [predictionMsg, setPredictionMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.listClients().then(setClients).catch((e) => setClientsError((e as Error).message));
  }, []);

  const client = clients.find((c) => c.id === clientId) ?? null;
  const matters = client?.matters ?? [];

  const loadBurn = useCallback((cid: string, mn: string) => {
    setBurn(null); setBurnMsg(null);
    if (!cid || !mn) return;
    api.getMatterBudget(cid, mn)
      .then(setBurn)
      .catch((e) => setBurnMsg((e as Error).message.includes("404") ? "No budget set for this matter yet." : (e as Error).message));
  }, []);

  const loadPrediction = useCallback((mn: string) => {
    setPrediction(null); setPredictionMsg(null);
    if (!mn) return;
    api.budgetPrediction(mn)
      .then(setPrediction)
      .catch((e) => setPredictionMsg((e as Error).message.includes("404") ? "No billing data for this matter yet." : (e as Error).message));
  }, []);

  function selectMatter(mn: string) {
    setMatterNumber(mn);
    loadBurn(clientId, mn);
    loadPrediction(mn);
  }

  async function setBudget() {
    const usd = parseFloat(budgetInput);
    if (!clientId || !matterNumber || !Number.isFinite(usd) || usd <= 0) { notify("Pick a matter and a positive budget"); return; }
    setBusy(true);
    try {
      await api.setMatterBudget(clientId, matterNumber, { budgetUsd: usd });
      notify(`Budget set: ${fmt$(usd)} on ${matterNumber}`);
      setBudgetInput("");
      loadBurn(clientId, matterNumber);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  async function checkNow() {
    if (!clientId || !matterNumber) return;
    setBusy(true);
    try {
      await api.checkMatterBudget(clientId, matterNumber);
      notify("Budget thresholds checked — alerts (if any) hit the Watchtower stream");
      loadBurn(clientId, matterNumber);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  return (
    <div className="section-card" style={{ marginBottom: 22 }}>
      <div className="section-card-title">Matter budget &amp; burn</div>
      {clientsError && <ErrorState message={clientsError} />}
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr auto auto", gap: 10, alignItems: "end", marginBottom: 14 }}>
        <div className="field">
          <label>Client</label>
          <select value={clientId} onChange={(e) => { setClientId(e.target.value); setMatterNumber(""); setBurn(null); setPrediction(null); setBurnMsg(null); setPredictionMsg(null); }}>
            <option value="">— select —</option>
            {clients.map((c) => <option key={c.id} value={c.id}>{c.name} ({c.clientNumber})</option>)}
          </select>
        </div>
        <div className="field">
          <label>Matter</label>
          <select value={matterNumber} onChange={(e) => selectMatter(e.target.value)} disabled={!client}>
            <option value="">— select —</option>
            {matters.map((m) => <option key={m.matterNumber} value={m.matterNumber}>{m.matterNumber} — {m.description.slice(0, 40)}</option>)}
          </select>
        </div>
        <div className="field">
          <label>Budget (USD)</label>
          <input type="number" min={1} value={budgetInput} onChange={(e) => setBudgetInput(e.target.value)} placeholder="50000" />
        </div>
        <button className="btn primary" disabled={busy || !clientId || !matterNumber || !budgetInput} onClick={setBudget}>Set budget</button>
        <button className="btn" disabled={busy || !clientId || !matterNumber} onClick={checkNow}>Check now</button>
      </div>

      {burnMsg && <div className="grid-meta" style={{ marginBottom: 10 }}>{burnMsg}</div>}
      {burn && (
        <div style={{ marginBottom: 16 }}>
          <div style={{ display: "flex", justifyContent: "space-between", fontSize: 12.5, color: "var(--text-dim)", marginBottom: 6 }}>
            <span>Burn: <strong style={{ color: "var(--text)" }}>{fmt$(burn.burnUsd)}</strong> of {fmt$(burn.budgetUsd)}</span>
            <span>Remaining: <strong style={{ color: burn.remaining >= 0 ? "var(--green)" : "var(--red)" }}>{fmt$(burn.remaining)}</strong></span>
          </div>
          <div className="conf-track" style={{ height: 8 }}>
            <div className="conf-fill" style={{
              width: `${Math.min(100, burn.burnPct * 100)}%`,
              background: burn.burnPct >= 1 ? "var(--red)" : burn.burnPct >= 0.75 ? "var(--amber)" : "var(--green)",
            }} />
          </div>
          <div className="grid-meta" style={{ marginTop: 4 }}>{(burn.burnPct * 100).toFixed(1)}% consumed</div>
        </div>
      )}

      {predictionMsg && matterNumber && <div className="grid-meta">{predictionMsg}</div>}
      {prediction && (
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(160px, 1fr))", gap: 10 }}>
          <PredStat label="Spent" value={fmt$(prediction.spentUsd)} />
          <PredStat label="Estimated total" value={fmt$(prediction.estimatedTotalUsd)} />
          <PredStat label="Estimated remaining" value={fmt$(prediction.estimatedRemainingUsd)} />
          <PredStat label="Completion" value={`${Math.round(prediction.completionPct * 100)}%`} />
          <PredStat label="Confidence" value={prediction.confidence.replace("_", " ")}
            sub={`${prediction.comparableMatterCount} comparable matters · ${prediction.basedOn.replace(/_/g, " ")}`} />
          <PredStat label="Median final cost" value={fmt$(prediction.medianFinalCost)}
            sub={`p25 ${fmt$(prediction.p25FinalCost)} · p75 ${fmt$(prediction.p75FinalCost)}`} />
        </div>
      )}
    </div>
  );
}

function PredStat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div style={{ background: "var(--panel-2)", border: "1px solid var(--border)", borderRadius: "var(--r)", padding: "12px 16px" }}>
      <div style={{ fontSize: 10.5, color: "var(--text-dim)", marginBottom: 4, textTransform: "uppercase", letterSpacing: "0.06em" }}>{label}</div>
      <div style={{ fontSize: 17, fontFamily: "var(--font-display)", color: "var(--gold)", lineHeight: 1.15 }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: "var(--text-faint)", marginTop: 4 }}>{sub}</div>}
    </div>
  );
}

// ─── Deadlines ─────────────────────────────────────────────────────────────────

function DeadlinesSection({ notify }: { notify: (m: string) => void }) {
  const [rules, setRules] = useState<DeadlineJurisdiction[]>([]);
  const [rulesError, setRulesError] = useState<string | null>(null);
  const [jurisdiction, setJurisdiction] = useState("");
  const [triggerEvent, setTriggerEvent] = useState("");
  const [triggerDate, setTriggerDate] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<DeadlineResult | null>(null);

  const loadRules = useCallback(() => {
    setRulesError(null);
    api.deadlineRules()
      .then((r) => { setRules(r); if (r.length && !jurisdiction) setJurisdiction(r[0].jurisdiction); })
      .catch((e) => setRulesError((e as Error).message));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  useEffect(() => { loadRules(); }, [loadRules]);

  async function compute() {
    if (!jurisdiction || !triggerEvent.trim() || !triggerDate) { notify("Jurisdiction, trigger event, and date required"); return; }
    setBusy(true);
    setResult(null);
    try {
      setResult(await api.computeDeadlines({ jurisdiction, triggerEvent: triggerEvent.trim(), triggerDate }));
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  return (
    <div className="section-card">
      <div className="section-card-title">Deadline calculator</div>
      {rulesError && <ErrorState message={rulesError} onRetry={loadRules} />}
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1.4fr 1fr auto", gap: 10, alignItems: "end", marginBottom: 14 }}>
        <div className="field">
          <label>Jurisdiction</label>
          <select value={jurisdiction} onChange={(e) => setJurisdiction(e.target.value)}>
            {rules.length === 0 && <option value="">— none loaded —</option>}
            {rules.map((r) => <option key={r.id} value={r.jurisdiction}>{r.name} ({r.ruleCount} rules)</option>)}
          </select>
        </div>
        <div className="field">
          <label>Trigger event</label>
          <input value={triggerEvent} onChange={(e) => setTriggerEvent(e.target.value)}
            placeholder="e.g. complaint_served, claim_form_issued" />
        </div>
        <div className="field">
          <label>Trigger date</label>
          <input type="date" value={triggerDate} onChange={(e) => setTriggerDate(e.target.value)} />
        </div>
        <button className="btn primary" disabled={busy || !jurisdiction || !triggerEvent.trim() || !triggerDate} onClick={compute}>
          {busy ? "Computing…" : "Compute"}
        </button>
      </div>

      {result && (
        <>
          <div className="grid-meta" style={{ marginBottom: 8 }}>
            {result.jurisdictionName} · {result.triggerEvent} · {new Date(result.triggerDate).toLocaleDateString()} · {result.deadlines.length} deadlines
          </div>
          {result.deadlines.length === 0 && <div className="placeholder">No rules matched this trigger event.</div>}
          {result.deadlines.length > 0 && (
            <div className="grid-wrap">
              <div className="grid-scroll">
                <table className="grid">
                  <thead><tr><th>Due</th><th>Event</th><th>Window</th><th>Authority</th><th>Note</th></tr></thead>
                  <tbody>
                    {result.deadlines.map((d) => (
                      <tr key={d.ruleId}>
                        <td style={{ whiteSpace: "nowrap", fontFamily: "var(--font-mono)", color: "var(--gold)" }}>
                          {new Date(d.dueDate).toLocaleDateString()}
                          {d.warningDate && <div className="grid-meta" style={{ marginTop: 2 }}>warn {new Date(d.warningDate).toLocaleDateString()}</div>}
                        </td>
                        <td>{d.event}</td>
                        <td style={{ whiteSpace: "nowrap" }}>{d.days} {d.dayType} days</td>
                        <td style={{ fontFamily: "var(--font-mono)", fontSize: 11.5 }}>{d.cite}</td>
                        <td style={{ color: "var(--text-dim)", maxWidth: 280 }}>{d.note ?? "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
