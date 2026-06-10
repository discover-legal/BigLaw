import { useCallback, useEffect, useState } from "react";
import { motion } from "framer-motion";
import { api } from "../api";
import type { AgentBillingSummary, InvoiceValidationResult, PreBill, PreBillStatus } from "../types";
import { TimeEntryPane } from "../TimeEntryPane";
import { ErrorState } from "../Library";
import { timeAgo } from "../primitives";

type Tab = "time" | "prebills" | "invoices" | "exports";

function fmt$(n: number): string {
  return `$${n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

const NEXT_STATUS: Partial<Record<PreBillStatus, { to: PreBillStatus; label: string }>> = {
  draft:    { to: "reviewed", label: "Mark reviewed" },
  reviewed: { to: "approved", label: "Approve" },
  approved: { to: "invoiced", label: "Mark invoiced" },
};

const STATUS_PILL: Record<PreBillStatus, string> = {
  draft: "", reviewed: "blue", approved: "gold", invoiced: "green",
};

export function BillingPage({ notify, isPartner }: { notify: (m: string) => void; isPartner: boolean }) {
  const [tab, setTab] = useState<Tab>("time");

  return (
    <div className="page-scroll">
      <div className="page">
        <div className="page-head">
          <h1 className="page-title">Billing &amp; time</h1>
          <p className="page-sub">Time entries, OCG compliance, pre-bill review, invoice validation, and exports.</p>
        </div>

        <div className="tabs">
          {([
            ["time", "Time & OCG"],
            ["prebills", "Pre-bills"],
            ["invoices", "Invoice validation"],
            ["exports", "Exports & agent billing"],
          ] as Array<[Tab, string]>).map(([t, label]) => (
            <button key={t} className={`tab ${tab === t ? "active" : ""}`} onClick={() => setTab(t)}>
              {label}{tab === t && <motion.span layoutId="bill-underline" className="tab-underline" />}
            </button>
          ))}
        </div>

        {tab === "time" && (
          <div className="panel-body" style={{ paddingTop: 6 }}>
            <TimeEntryPane notify={notify} isPartner={isPartner} />
          </div>
        )}
        {tab === "prebills" && <PreBillsTab notify={notify} isPartner={isPartner} />}
        {tab === "invoices" && <InvoiceValidationTab notify={notify} />}
        {tab === "exports" && <ExportsTab isPartner={isPartner} />}
      </div>
    </div>
  );
}

// ─── Pre-bills ─────────────────────────────────────────────────────────────────

function PreBillsTab({ notify, isPartner }: { notify: (m: string) => void; isPartner: boolean }) {
  const [bills, setBills] = useState<PreBill[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // create form
  const [nb, setNb] = useState({ matterNumber: "", clientNumber: "", from: "", to: "" });

  // detail editing
  const [notesDraft, setNotesDraft] = useState("");
  const [editingEntry, setEditingEntry] = useState<string | null>(null);
  const [entryDraft, setEntryDraft] = useState("");

  const selected = bills.find((b) => b.id === selectedId) ?? null;

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    api.listPreBills()
      .then((b) => setBills(b))
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => { load(); }, [load]);
  useEffect(() => { setNotesDraft(selected?.notes ?? ""); setEditingEntry(null); }, [selectedId, selected?.notes]);

  async function create() {
    if (!nb.matterNumber.trim()) { notify("Matter number required"); return; }
    setBusy(true);
    try {
      const bill = await api.createPreBill({
        matterNumber: nb.matterNumber.trim(),
        clientNumber: nb.clientNumber.trim() || undefined,
        from: nb.from || undefined,
        to: nb.to || undefined,
      });
      setBills((prev) => [bill, ...prev]);
      setSelectedId(bill.id);
      setNb({ matterNumber: "", clientNumber: "", from: "", to: "" });
      notify(`Pre-bill created — ${bill.entries.length} entries, ${fmt$(bill.totalAmountUsd)}`);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  async function patch(body: Parameters<typeof api.patchPreBill>[1]) {
    if (!selected) return;
    setBusy(true);
    try {
      const updated = await api.patchPreBill(selected.id, body);
      setBills((prev) => prev.map((b) => (b.id === updated.id ? updated : b)));
      setEditingEntry(null);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  if (!isPartner) {
    return <div className="panel-body"><div className="placeholder">Pre-bill review is partner-only.</div></div>;
  }

  return (
    <div className="panel-body">
      {/* Create */}
      <div className="section-card" style={{ marginBottom: 20 }}>
        <div className="section-card-title">Generate a pre-bill</div>
        <div style={{ display: "grid", gridTemplateColumns: "1.2fr 1fr 1fr 1fr auto", gap: 10, alignItems: "end" }}>
          <div className="field"><label>Matter number</label>
            <input value={nb.matterNumber} onChange={(e) => setNb({ ...nb, matterNumber: e.target.value })} placeholder="M-2026-001" /></div>
          <div className="field"><label>Client (optional)</label>
            <input value={nb.clientNumber} onChange={(e) => setNb({ ...nb, clientNumber: e.target.value })} placeholder="C-001" /></div>
          <div className="field"><label>From</label>
            <input type="date" value={nb.from} onChange={(e) => setNb({ ...nb, from: e.target.value })} /></div>
          <div className="field"><label>To</label>
            <input type="date" value={nb.to} onChange={(e) => setNb({ ...nb, to: e.target.value })} /></div>
          <button className="btn primary" disabled={busy || !nb.matterNumber.trim()} onClick={create}>
            {busy ? "…" : "＋ Create"}
          </button>
        </div>
      </div>

      {loading && <div className="placeholder">Loading pre-bills…</div>}
      {error && <ErrorState message={error} onRetry={load} />}
      {!loading && !error && bills.length === 0 && (
        <div className="placeholder">No pre-bills yet. Generate one from a matter's closed time entries above.</div>
      )}

      {!loading && !error && bills.length > 0 && (
        <div style={{ display: "flex", gap: 16, alignItems: "flex-start" }}>
          {/* List */}
          <div style={{ width: 280, flexShrink: 0, display: "flex", flexDirection: "column", gap: 6 }}>
            {bills.map((b) => (
              <button key={b.id} className={`task-card ${b.id === selectedId ? "active" : ""}`} onClick={() => setSelectedId(b.id)}>
                <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
                  <span className={`pill sm ${STATUS_PILL[b.status]}`}>{b.status}</span>
                  <span style={{ fontWeight: 600, fontSize: 13 }}>{b.matterNumber}</span>
                </div>
                <div style={{ color: "var(--text-faint)", fontSize: 11.5 }}>
                  {b.entries.length} entries · {fmt$(b.totalAmountUsd)} · {timeAgo(b.createdAt)}
                </div>
              </button>
            ))}
          </div>

          {/* Detail */}
          <div style={{ flex: 1, minWidth: 0 }}>
            {!selected ? (
              <div className="placeholder" style={{ paddingTop: 48 }}>Select a pre-bill to review.</div>
            ) : (
              <div>
                <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", gap: 12, marginBottom: 14, flexWrap: "wrap" }}>
                  <div>
                    <div style={{ fontFamily: "var(--font-display)", fontSize: 20 }}>{selected.matterNumber}</div>
                    <div style={{ color: "var(--text-dim)", fontSize: 12.5, marginTop: 2 }}>
                      {selected.clientNumber && <>Client {selected.clientNumber} · </>}
                      {selected.totalBillingUnits} units · <strong style={{ color: "var(--gold)" }}>{fmt$(selected.totalAmountUsd)}</strong>
                    </div>
                  </div>
                  <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
                    <span className={`pill ${STATUS_PILL[selected.status]}`}>{selected.status}</span>
                    {NEXT_STATUS[selected.status] && (
                      <button className="btn primary sm" disabled={busy}
                        onClick={() => patch({ status: NEXT_STATUS[selected.status]!.to })}>
                        {NEXT_STATUS[selected.status]!.label}
                      </button>
                    )}
                  </div>
                </div>

                <div className="grid-wrap" style={{ marginBottom: 14 }}>
                  <div className="grid-scroll">
                    <table className="grid">
                      <thead>
                        <tr><th>Description</th><th>Who</th><th>Units</th><th>Amount</th><th>OCG</th><th></th></tr>
                      </thead>
                      <tbody>
                        {selected.entries.map((en) => (
                          <tr key={en.entryId}>
                            <td style={{ maxWidth: 420 }}>
                              {editingEntry === en.entryId ? (
                                <div style={{ display: "flex", gap: 6 }}>
                                  <input style={{ flex: 1, fontSize: 13 }} value={entryDraft} onChange={(e) => setEntryDraft(e.target.value)} />
                                  <button className="btn primary sm" disabled={busy || !entryDraft.trim()}
                                    onClick={() => patch({ entryEdit: { entryId: en.entryId, description: entryDraft.trim() } })}>Save</button>
                                  <button className="btn ghost sm" onClick={() => setEditingEntry(null)}>✕</button>
                                </div>
                              ) : en.description}
                            </td>
                            <td style={{ whiteSpace: "nowrap", color: "var(--text-dim)" }}>{en.agentName ?? en.profileName ?? "—"}</td>
                            <td style={{ fontFamily: "var(--font-mono)" }}>{en.billingUnits}</td>
                            <td style={{ fontFamily: "var(--font-mono)", whiteSpace: "nowrap" }}>{en.billingAmountUsd != null ? fmt$(en.billingAmountUsd) : "—"}</td>
                            <td>{en.ocgSuggestionCount > 0 ? <span className="pill sm amber">{en.ocgSuggestionCount}</span> : "—"}</td>
                            <td>
                              {selected.status !== "invoiced" && editingEntry !== en.entryId && (
                                <button className="btn ghost sm" onClick={() => { setEditingEntry(en.entryId); setEntryDraft(en.description); }}>Edit</button>
                              )}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </div>

                <div className="field">
                  <label>Review notes</label>
                  <textarea style={{ minHeight: 64 }} value={notesDraft} onChange={(e) => setNotesDraft(e.target.value)}
                    placeholder="Notes for the billing partner…" />
                </div>
                <button className="btn ghost sm" style={{ marginTop: 8 }} disabled={busy || notesDraft === (selected.notes ?? "")}
                  onClick={() => patch({ notes: notesDraft })}>Save notes</button>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// ─── Invoice validation ────────────────────────────────────────────────────────

function InvoiceValidationTab({ notify }: { notify: (m: string) => void }) {
  const [invoiceText, setInvoiceText] = useState("");
  const [clientId, setClientId] = useState("");
  const [firm, setFirm] = useState("");
  const [matterNumber, setMatterNumber] = useState("");
  const [disputeLetter, setDisputeLetter] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<InvoiceValidationResult | null>(null);
  const [clients, setClients] = useState<Array<{ id: string; name: string; clientNumber: string }>>([]);

  useEffect(() => {
    api.listClients().then((cs) => setClients(cs.map((c) => ({ id: c.id, name: c.name, clientNumber: c.clientNumber })))).catch(() => {});
  }, []);

  async function validate() {
    setBusy(true);
    setResult(null);
    try {
      const res = await api.validateInvoice({
        invoiceText,
        clientId: clientId || undefined,
        submittedByFirm: firm.trim() || undefined,
        matterNumber: matterNumber.trim() || undefined,
        generateDisputeLetter: disputeLetter,
      });
      setResult(res);
      notify(`${res.violationCount} violation${res.violationCount === 1 ? "" : "s"} found — suggested reduction ${fmt$(res.totalSuggestedReduction)}`);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  return (
    <div className="panel-body" style={{ maxWidth: 880 }}>
      <div className="field">
        <label>Invoice text <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(paste LEDES or plain-text invoice)</span></label>
        <textarea style={{ minHeight: 180, fontFamily: "var(--font-mono)", fontSize: 12 }}
          value={invoiceText} onChange={(e) => setInvoiceText(e.target.value)}
          placeholder="Paste the outside-counsel invoice to validate against the client's OCG…" />
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 14, margin: "14px 0" }}>
        <div className="field">
          <label>Client (applies their OCG)</label>
          <select value={clientId} onChange={(e) => setClientId(e.target.value)}>
            <option value="">— none —</option>
            {clients.map((c) => <option key={c.id} value={c.id}>{c.name} ({c.clientNumber})</option>)}
          </select>
        </div>
        <div className="field"><label>Submitted by firm</label>
          <input value={firm} onChange={(e) => setFirm(e.target.value)} placeholder="Opposing Counsel LLP" /></div>
        <div className="field"><label>Matter number</label>
          <input value={matterNumber} onChange={(e) => setMatterNumber(e.target.value)} placeholder="M-2026-001" /></div>
      </div>
      <label className="check">
        <input type="checkbox" checked={disputeLetter} onChange={(e) => setDisputeLetter(e.target.checked)} />
        Draft a dispute letter for flagged lines
      </label>
      <div style={{ marginTop: 12 }}>
        <button className="btn primary" disabled={busy || invoiceText.trim().length < 20} onClick={validate}>
          {busy ? "Validating…" : "⚖ Validate invoice"}
        </button>
      </div>

      {result && (
        <div style={{ marginTop: 24 }}>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: 10, marginBottom: 18 }}>
            <Stat label="Original" value={fmt$(result.totalOriginalAmount)} />
            <Stat label="Suggested reduction" value={fmt$(result.totalSuggestedReduction)} accent="var(--red)" />
            <Stat label="Approved" value={fmt$(result.totalApprovedAmount)} accent="var(--green)" />
            <Stat label="Violations" value={`${result.violationCount} (${result.hardViolationCount} hard)`} accent="var(--amber)" />
          </div>

          {result.violations.length > 0 && (
            <div className="grid-wrap" style={{ marginBottom: 18 }}>
              <div className="grid-scroll">
                <table className="grid">
                  <thead><tr><th>Line</th><th>Type</th><th>Severity</th><th>Issue</th><th>Action</th><th>Reduction</th></tr></thead>
                  <tbody>
                    {result.violations.map((v, i) => (
                      <tr key={i}>
                        <td style={{ fontFamily: "var(--font-mono)", fontSize: 11 }}>{v.lineId}</td>
                        <td><span className="pill sm blue">{v.type.replace(/_/g, " ")}</span></td>
                        <td><span className={`pill sm ${v.severity === "hard" ? "red" : "gold"}`}>{v.severity}</span></td>
                        <td style={{ maxWidth: 380 }}>{v.message}</td>
                        <td style={{ whiteSpace: "nowrap" }}>{v.suggestedAction.replace(/_/g, " ")}</td>
                        <td style={{ fontFamily: "var(--font-mono)", whiteSpace: "nowrap" }}>{v.suggestedReduction != null ? fmt$(v.suggestedReduction) : "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {result.disputeLetter && (
            <div className="synthesis">
              <div className="synthesis-head">Dispute letter draft</div>
              <div className="prose" style={{ fontSize: 14.5 }}>{result.disputeLetter}</div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ─── Exports & per-agent billing ───────────────────────────────────────────────

function ExportsTab({ isPartner }: { isPartner: boolean }) {
  const [summary, setSummary] = useState<AgentBillingSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [ledesMatter, setLedesMatter] = useState("");

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    api.agentBillingSummary()
      .then(setSummary)
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => { load(); }, [load]);

  if (!isPartner) {
    return <div className="panel-body"><div className="placeholder">Exports and agent billing are partner-only.</div></div>;
  }

  return (
    <div className="panel-body" style={{ maxWidth: 880 }}>
      <div className="section-card" style={{ marginBottom: 20 }}>
        <div className="section-card-title">Time entry exports</div>
        <div style={{ display: "flex", gap: 10, flexWrap: "wrap", alignItems: "end" }}>
          <a className="btn" href={api.timeExportCsvUrl()} download>⬇ CSV</a>
          <a className="btn" href={api.timeExportJsonUrl()} download>⬇ JSON</a>
          <div className="field" style={{ width: 200 }}>
            <label>LEDES 1998B — matter</label>
            <input value={ledesMatter} onChange={(e) => setLedesMatter(e.target.value)} placeholder="M-2026-001" />
          </div>
          {ledesMatter.trim()
            ? <a className="btn" href={api.timeExportLedesUrl(ledesMatter.trim())} download>⬇ LEDES</a>
            : <button className="btn" disabled>⬇ LEDES</button>}
        </div>
      </div>

      <div className="section-card">
        <div className="section-card-title">Per-agent billing</div>
        {loading && <div className="placeholder">Loading agent billing…</div>}
        {error && <ErrorState message={error} onRetry={load} />}
        {!loading && !error && summary.length === 0 && (
          <div className="placeholder">No agent time recorded yet. Run a matter and the bench's billable time lands here.</div>
        )}
        {!loading && !error && summary.length > 0 && (
          <table className="grid" style={{ width: "100%" }}>
            <thead><tr><th>Agent</th><th>Entries</th><th>Units</th><th>Amount</th></tr></thead>
            <tbody>
              {summary.map((a) => (
                <tr key={a.agentId}>
                  <td>
                    <div style={{ fontWeight: 500, color: "var(--text)" }}>{a.agentName}</div>
                    <div className="grid-meta" style={{ marginTop: 2 }}>{a.agentId}</div>
                  </td>
                  <td style={{ fontFamily: "var(--font-mono)" }}>{a.entries}</td>
                  <td style={{ fontFamily: "var(--font-mono)" }}>{a.billingUnits}</td>
                  <td style={{ fontFamily: "var(--font-mono)", color: "var(--gold)" }}>{fmt$(a.billingAmountUsd)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function Stat({ label, value, accent }: { label: string; value: string; accent?: string }) {
  return (
    <div style={{ background: "var(--panel-2)", border: "1px solid var(--border)", borderRadius: "var(--r)", padding: "12px 16px" }}>
      <div style={{ fontSize: 10.5, color: "var(--text-dim)", marginBottom: 4, textTransform: "uppercase", letterSpacing: "0.06em" }}>{label}</div>
      <div style={{ fontSize: 19, fontFamily: "var(--font-display)", color: accent ?? "var(--gold)", lineHeight: 1.1 }}>{value}</div>
    </div>
  );
}
