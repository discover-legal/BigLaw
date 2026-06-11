import { useCallback, useEffect, useState } from "react";
import { motion } from "framer-motion";
import { api } from "../api";
import type {
  CitationCheckResult, CitationSignal, HeadnoteReport, MissingClause, Playbook, PlaybookEntry,
  PlaybookQueryResult, PlaybookScope, PrecedentDocument, RedlineReport,
} from "../types";
import { PRACTICE_AREAS, PRECEDENT_TYPES } from "../types";
import { ErrorState } from "../Library";
import { timeAgo } from "../primitives";
import { Markdown } from "../Markdown";

type Tab = "review" | "draft" | "playbooks" | "headnotes" | "citations";

const SCOPES: PlaybookScope[] = ["firm", "personal", "matter", "client"];

const SCOPE_PILL: Record<PlaybookScope, string> = {
  firm: "", personal: "blue", matter: "amber", client: "gold",
};

export function DraftingPage({ notify, isPartner }: { notify: (m: string) => void; isPartner: boolean }) {
  const [tab, setTab] = useState<Tab>(isPartner ? "review" : "citations");

  // All engine routes except citation checking are partner-gated server-side
  // (they consume the confidential playbook tiers). Hide those tabs rather
  // than render a wall of 403s — same convention as App's section list.
  const tabs = ([
    ["review", "Playbook review"],
    ["draft", "Draft"],
    ["playbooks", "Playbooks"],
    ["headnotes", "Headnotes"],
    ["citations", "Citation check"],
  ] as Array<[Tab, string]>).filter(([t]) => isPartner || t === "citations");

  return (
    <div className="page-scroll">
      <div className="page">
        <div className="page-head">
          <h1 className="page-title">Drafting</h1>
          <p className="page-sub">Review contracts against your playbook, draft from it, and manage the firm's positions — plus headnotes and citation checking.</p>
        </div>

        <div className="tabs">
          {tabs.map(([t, label]) => (
            <button key={t} className={`tab ${tab === t ? "active" : ""}`} onClick={() => setTab(t)}>
              {label}{tab === t && <motion.span layoutId="draft-underline" className="tab-underline" />}
            </button>
          ))}
        </div>

        {tab === "review" && <PlaybookReviewTab notify={notify} />}
        {tab === "draft" && <PrecedentsTab notify={notify} />}
        {tab === "playbooks" && <PlaybooksTab notify={notify} isPartner={isPartner} />}
        {tab === "headnotes" && <HeadnotesTab notify={notify} />}
        {tab === "citations" && <CitationsTab notify={notify} />}
      </div>
    </div>
  );
}

// ─── Playbooks ─────────────────────────────────────────────────────────────────

function PlaybooksTab({ notify, isPartner }: { notify: (m: string) => void; isPartner: boolean }) {
  const [playbooks, setPlaybooks] = useState<Playbook[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [buildOpen, setBuildOpen] = useState(false);

  const [nb, setNb] = useState({
    name: "", practiceArea: PRACTICE_AREAS[0] as string, scope: "firm" as PlaybookScope,
    jurisdiction: "", description: "", clauseTypes: "",
  });

  // resolve tool
  const [resClause, setResClause] = useState("");
  const [resArea, setResArea] = useState("");
  const [resBusy, setResBusy] = useState(false);
  const [resolved, setResolved] = useState<PlaybookQueryResult | null>(null);

  const selected = playbooks.find((p) => p.id === selectedId) ?? null;

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    api.listPlaybooks()
      .then(setPlaybooks)
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => { load(); }, [load]);

  async function build() {
    if (!nb.name.trim() || !nb.practiceArea) { notify("Name and practice area required"); return; }
    setBusy(true);
    try {
      const pb = await api.buildPlaybook({
        name: nb.name.trim(),
        practiceArea: nb.practiceArea,
        scope: nb.scope,
        jurisdiction: nb.jurisdiction.trim() || undefined,
        description: nb.description.trim() || undefined,
        clauseTypes: nb.clauseTypes.trim() ? nb.clauseTypes.split(",").map((s) => s.trim()).filter(Boolean) : undefined,
      });
      notify(`Playbook built — ${pb.entries.length} clause positions from ${pb.documentCount} documents`);
      setBuildOpen(false);
      setNb({ name: "", practiceArea: PRACTICE_AREAS[0], scope: "firm", jurisdiction: "", description: "", clauseTypes: "" });
      load();
      setSelectedId(pb.id);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  async function remove(id: string) {
    if (!window.confirm("Delete this playbook?")) return;
    try { await api.deletePlaybook(id); notify("Playbook deleted"); setSelectedId(null); load(); }
    catch (e) { notify((e as Error).message); }
  }

  async function resolve() {
    if (!resClause.trim()) return;
    setResBusy(true);
    setResolved(null);
    try {
      setResolved(await api.resolvePlaybook(resClause.trim(), { practiceArea: resArea || undefined }));
    } catch (e) { notify((e as Error).message); }
    finally { setResBusy(false); }
  }

  return (
    <div className="panel-body">
      <p style={{ fontSize: 13, color: "var(--text-dim)", margin: "0 0 14px" }}>
        Playbooks are the firm's position bank: standard, fallback, and red-line positions
        per clause type, layered client &gt; matter &gt; personal &gt; firm. They're applied
        automatically in <strong>Playbook review</strong> and <strong>Draft</strong> — this
        tab is where you build and maintain them.
      </p>

      <div className="section-card-head" style={{ marginBottom: 12 }}>
        <div className="section-card-title" style={{ margin: 0 }}>Playbooks · {playbooks.length}</div>
        <button className="btn primary sm" onClick={() => setBuildOpen((o) => !o)}>{buildOpen ? "✕ Cancel" : "＋ Build playbook"}</button>
      </div>

      {buildOpen && (
        <div className="section-card" style={{ marginBottom: 16 }}>
          <div style={{ display: "grid", gridTemplateColumns: "1.2fr 1fr 0.8fr 0.8fr", gap: 10, marginBottom: 10 }}>
            <div className="field"><label>Name</label>
              <input value={nb.name} onChange={(e) => setNb({ ...nb, name: e.target.value })} placeholder="M&A standard positions" /></div>
            <div className="field"><label>Practice area</label>
              <select value={nb.practiceArea} onChange={(e) => setNb({ ...nb, practiceArea: e.target.value })}>
                {PRACTICE_AREAS.map((pa) => <option key={pa} value={pa}>{pa}</option>)}
              </select></div>
            <div className="field"><label>Scope</label>
              <select value={nb.scope} onChange={(e) => setNb({ ...nb, scope: e.target.value as PlaybookScope })}>
                {SCOPES.map((s) => <option key={s} value={s}>{s}</option>)}
              </select></div>
            <div className="field"><label>Jurisdiction</label>
              <input value={nb.jurisdiction} onChange={(e) => setNb({ ...nb, jurisdiction: e.target.value })} placeholder="UK" /></div>
          </div>
          <div className="field" style={{ marginBottom: 10 }}>
            <label>Clause types <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(comma-separated, blank = auto-discover)</span></label>
            <input value={nb.clauseTypes} onChange={(e) => setNb({ ...nb, clauseTypes: e.target.value })}
              placeholder="limitation_of_liability, indemnity, governing_law" />
          </div>
          <button className="btn primary" disabled={busy || !nb.name.trim()} onClick={build}>
            {busy ? "Building from knowledge store…" : "⚒ Build from precedents"}
          </button>
        </div>
      )}

      {loading && <div className="placeholder">Loading playbooks…</div>}
      {error && <ErrorState message={error} onRetry={load} />}
      {!loading && !error && playbooks.length === 0 && (
        <div className="placeholder">No playbooks yet. Build one from the documents in your library — it becomes the firm's negotiating position bank.</div>
      )}

      {!loading && !error && playbooks.length > 0 && (
        <div style={{ display: "flex", gap: 16, alignItems: "flex-start" }}>
          <div style={{ width: 280, flexShrink: 0, display: "flex", flexDirection: "column", gap: 6 }}>
            {playbooks.map((p) => (
              <button key={p.id} className={`task-card ${p.id === selectedId ? "active" : ""}`} onClick={() => setSelectedId(p.id)}>
                <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 4 }}>
                  <span className={`pill sm ${SCOPE_PILL[p.scope]}`}>{p.scope}</span>
                  <span style={{ fontWeight: 600, fontSize: 13 }}>{p.name}</span>
                </div>
                <div style={{ color: "var(--text-faint)", fontSize: 11.5 }}>
                  {p.practiceArea} · {p.entries.length} clauses · {timeAgo(p.updatedAt)}
                </div>
              </button>
            ))}
          </div>
          <div style={{ flex: 1, minWidth: 0 }}>
            {!selected ? (
              <div className="placeholder" style={{ paddingTop: 48 }}>Select a playbook to view its positions.</div>
            ) : (
              <div>
                <div style={{ display: "flex", justifyContent: "space-between", gap: 12, alignItems: "flex-start", marginBottom: 14 }}>
                  <div>
                    <div style={{ fontFamily: "var(--font-display)", fontSize: 20 }}>{selected.name}</div>
                    <div style={{ color: "var(--text-dim)", fontSize: 12.5, marginTop: 2 }}>
                      {selected.practiceArea}{selected.jurisdiction && ` · ${selected.jurisdiction}`} · built from {selected.documentCount} documents
                      {selected.ownerName && ` · ${selected.ownerName}`}
                    </div>
                    {selected.description && <div style={{ color: "var(--text-dim)", fontSize: 12.5, marginTop: 4 }}>{selected.description}</div>}
                  </div>
                  {isPartner && <button className="btn reject sm" onClick={() => remove(selected.id)}>✕ Delete</button>}
                </div>
                {(selected.entries ?? []).length === 0 && <div className="placeholder">No clause entries in this playbook.</div>}
                {(selected.entries ?? []).map((en) => (
                  <div key={en.clauseType} className="alert-card">
                    <div style={{ fontFamily: "var(--font-mono)", fontSize: 12.5, color: "var(--gold)", marginBottom: 6 }}>{en.clauseType}</div>
                    <PlaybookEntryView entry={en} />
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Cascade inspector — debugging aid: see which tier wins for a clause type */}
      <details className="section-card" style={{ marginTop: 20 }}>
        <summary style={{ cursor: "pointer", fontSize: 13, color: "var(--text-dim)" }}>
          Test the cascade — which position wins for a clause type?
        </summary>
        <div style={{ display: "grid", gridTemplateColumns: "1.2fr 1fr auto", gap: 10, alignItems: "end", marginTop: 12 }}>
          <div className="field"><label>Clause type <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(or * for all)</span></label>
            <input value={resClause} onChange={(e) => setResClause(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && resolve()} placeholder="e.g. limitation_of_liability" /></div>
          <div className="field"><label>Practice area</label>
            <select value={resArea} onChange={(e) => setResArea(e.target.value)}>
              <option value="">— any —</option>
              {PRACTICE_AREAS.map((pa) => <option key={pa} value={pa}>{pa}</option>)}
            </select></div>
          <button className="btn primary" disabled={resBusy || !resClause.trim()} onClick={resolve}>{resBusy ? "…" : "Resolve"}</button>
        </div>
        {resolved && (
          <div style={{ marginTop: 14 }}>
            {resolved.message && <div className="grid-meta">{resolved.message}</div>}
            {resolved.cascadeSummary && <div style={{ fontSize: 12.5, color: "var(--text-dim)", marginBottom: 10 }}>{resolved.cascadeSummary}</div>}
            {(resolved.resolved ?? []).map((rc) => (
              <div key={rc.clauseType} className="alert-card">
                <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 6, flexWrap: "wrap" }}>
                  <strong style={{ fontFamily: "var(--font-mono)", fontSize: 12.5 }}>{rc.clauseType}</strong>
                  <span className={`pill sm ${SCOPE_PILL[rc.resolvedFrom]}`}>from {rc.resolvedFrom}</span>
                  <span className="grid-meta">tiers: {rc.availableTiers.join(", ")}</span>
                </div>
                <PlaybookEntryView entry={rc.effectiveEntry} />
                {rc.personalNote && (
                  <div style={{ marginTop: 8, fontSize: 12.5, color: "var(--blue)" }}>Personal note: {rc.personalNote}</div>
                )}
              </div>
            ))}
          </div>
        )}
      </details>
    </div>
  );
}

function PlaybookEntryView({ entry }: { entry: PlaybookEntry }) {
  return (
    <div style={{ fontSize: 13, lineHeight: 1.55 }}>
      <div><span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Standard: </span>{entry.standardPosition}</div>
      {entry.fallbackPosition && <div style={{ marginTop: 4 }}><span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Fallback: </span>{entry.fallbackPosition}</div>}
      {entry.redLines.length > 0 && (
        <div style={{ marginTop: 4 }}>
          <span style={{ color: "var(--red)", fontWeight: 600 }}>Red lines: </span>
          {entry.redLines.join("; ")}
        </div>
      )}
      {entry.dealPoints.length > 0 && (
        <div style={{ marginTop: 4, color: "var(--text-dim)" }}>
          <span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Deal points: </span>
          {entry.dealPoints.join("; ")}
        </div>
      )}
    </div>
  );
}

// ─── Playbook review ───────────────────────────────────────────────────────────
// The Spellbook-shaped workflow: load the contract, apply the whole playbook
// cascade across it, get clause-by-clause verdicts plus missing-clause flags,
// disposition each finding, export the markup.

const ACTION_PILL: Record<string, string> = {
  accept: "green", redline: "amber", escalate: "red", delete: "red", no_position: "",
};

type Disposition = "pending" | "accepted" | "dismissed";

function sevPill(sev: string): string {
  return sev === "critical" || sev === "high" ? "red" : sev === "medium" ? "amber" : "";
}

function PlaybookReviewTab({ notify }: { notify: (m: string) => void }) {
  const [text, setText] = useState("");
  const [practiceArea, setPracticeArea] = useState("");
  const [jurisdiction, setJurisdiction] = useState("");
  const [matterNumber, setMatterNumber] = useState("");
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<RedlineReport | null>(null);
  // Disposition per finding: issues keyed "i<n>", missing clauses "m<n>".
  const [dispositions, setDispositions] = useState<Record<string, Disposition>>({});

  function setDisp(key: string, d: Disposition) {
    setDispositions((prev) => ({ ...prev, [key]: prev[key] === d ? "pending" : d }));
  }

  async function run() {
    setBusy(true);
    setReport(null);
    setDispositions({});
    try {
      const res = await api.redline({
        documentText: text,
        practiceArea: practiceArea || undefined,
        jurisdiction: jurisdiction.trim() || undefined,
        matterNumber: matterNumber.trim() || undefined,
        documentTitle: title.trim() || undefined,
      });
      setReport(res);
      notify(`Review complete — ${res.redlineCount} redlines, ${res.missingCount ?? 0} missing clauses, ${res.escalateCount} escalations across ${res.totalClauses} clauses`);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  function exportMarkup() {
    if (!report) return;
    const missing = report.missingClauses ?? [];
    const lines: string[] = [
      `# Playbook review — ${report.documentTitle || "untitled draft"}`,
      ``,
      `Generated ${report.generatedAt}${report.practiceArea ? ` · ${report.practiceArea}` : ""}${report.jurisdiction ? ` · ${report.jurisdiction}` : ""}`,
      ``,
      `## Executive summary`,
      ``,
      report.executiveSummary,
      ``,
      `## Clause findings (${report.issues.length})`,
      ``,
    ];
    report.issues.forEach((iss, i) => {
      const d = dispositions[`i${i}`] ?? "pending";
      lines.push(`### ${i + 1}. ${iss.clauseType} — ${iss.action.toUpperCase()} (${iss.severity}${iss.isRedLine ? ", RED LINE" : ""}) · disposition: ${d}`);
      lines.push(``);
      lines.push(`**Counterparty text:** ${iss.counterpartyText}`);
      lines.push(``);
      lines.push(`**Firm position (${iss.positionSource}):** ${iss.firmPosition}`);
      if (iss.proposedText) {
        lines.push(``);
        lines.push(`**Proposed replacement:**`);
        lines.push(``);
        lines.push(`> ${iss.proposedText.replace(/\n/g, "\n> ")}`);
      }
      lines.push(``);
      lines.push(`*${iss.rationale}*`);
      lines.push(``);
    });
    if (missing.length > 0) {
      lines.push(`## Missing clauses (${missing.length})`);
      lines.push(``);
      missing.forEach((m, i) => {
        const d = dispositions[`m${i}`] ?? "pending";
        lines.push(`### ${m.clauseType} — MISSING (${m.severity}${m.isRedLine ? ", RED LINE" : ""}) · disposition: ${d}`);
        lines.push(``);
        lines.push(`**Firm position (${m.positionSource}):** ${m.firmPosition}`);
        if (m.suggestedText) {
          lines.push(``);
          lines.push(`**Suggested insert:**`);
          lines.push(``);
          lines.push(`> ${m.suggestedText.replace(/\n/g, "\n> ")}`);
        }
        lines.push(``);
        lines.push(`*${m.rationale}*`);
        lines.push(``);
      });
    }
    const blob = new Blob([lines.join("\n")], { type: "text/markdown" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `playbook-review-${(report.documentTitle || "draft").replace(/[^A-Za-z0-9-]+/g, "-").toLowerCase()}.md`;
    a.click();
    URL.revokeObjectURL(url);
  }

  const missing = report?.missingClauses ?? [];
  const decided = Object.values(dispositions).filter((d) => d !== "pending").length;
  const totalFindings = (report?.issues.length ?? 0) + missing.length;

  return (
    <div className="panel-body" style={{ maxWidth: 920 }}>
      <p style={{ fontSize: 13, color: "var(--text-dim)", margin: "0 0 14px" }}>
        Paste a counterparty draft and the engine applies your whole playbook cascade
        (client &gt; matter &gt; personal &gt; firm) across it: every clause checked against the
        firm position, deviations marked up with replacement language, and expected
        protections that are <em>absent</em> flagged with suggested inserts. Disposition each
        finding, then export the markup.
      </p>
      <div className="field">
        <label>Counterparty draft</label>
        <textarea style={{ minHeight: 220, fontFamily: "var(--font-mono)", fontSize: 12 }}
          value={text} onChange={(e) => setText(e.target.value)}
          placeholder="Paste the counterparty's draft…" />
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 0.7fr 0.8fr 1fr", gap: 14, margin: "14px 0" }}>
        <div className="field"><label>Practice area</label>
          <select value={practiceArea} onChange={(e) => setPracticeArea(e.target.value)}>
            <option value="">— auto —</option>
            {PRACTICE_AREAS.map((pa) => <option key={pa} value={pa}>{pa}</option>)}
          </select></div>
        <div className="field"><label>Jurisdiction</label>
          <input value={jurisdiction} onChange={(e) => setJurisdiction(e.target.value)} placeholder="UK" /></div>
        <div className="field"><label>Matter <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(pulls matter/client playbooks)</span></label>
          <input value={matterNumber} onChange={(e) => setMatterNumber(e.target.value)} placeholder="M-2026-001" /></div>
        <div className="field"><label>Document title</label>
          <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="MSA — Beta draft v3" /></div>
      </div>
      <button className="btn primary" disabled={busy || text.trim().length < 50} onClick={run}>
        {busy ? "Reviewing against playbook…" : "⚖ Review against playbook"}
      </button>

      {report && (
        <div style={{ marginTop: 24 }}>
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap", marginBottom: 16, alignItems: "center" }}>
            <span className="pill green">{report.acceptCount} compliant</span>
            <span className="pill amber">{report.redlineCount} redline</span>
            <span className="pill red">{report.escalateCount} escalate</span>
            <span className="pill red">{report.deleteCount} delete</span>
            {missing.length > 0 && <span className="pill red">∅ {missing.length} missing</span>}
            {report.criticalCount > 0 && <span className="pill red">⚠ {report.criticalCount} critical</span>}
            <span className="grid-meta">{report.totalClauses} clauses reviewed</span>
            <span style={{ marginLeft: "auto", display: "flex", gap: 8, alignItems: "center" }}>
              <span className="grid-meta">{decided}/{totalFindings} dispositioned</span>
              <button className="btn ghost sm" onClick={exportMarkup}>⤓ Export markup</button>
            </span>
          </div>

          <div className="synthesis" style={{ marginBottom: 18 }}>
            <div className="synthesis-head">Executive summary</div>
            <div className="prose" style={{ fontSize: 14.5 }}>{report.executiveSummary}</div>
          </div>

          {report.issues.map((iss, i) => {
            const key = `i${i}`;
            const d = dispositions[key] ?? "pending";
            return (
              <div key={key} className="alert-card" style={d === "dismissed" ? { opacity: 0.55 } : undefined}>
                <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 8, flexWrap: "wrap" }}>
                  <strong style={{ fontFamily: "var(--font-mono)", fontSize: 12.5 }}>{iss.clauseType}</strong>
                  <span className={`pill sm ${ACTION_PILL[iss.action] ?? ""}`}>{iss.action.replace("_", " ")}</span>
                  <span className={`pill sm ${sevPill(iss.severity)}`}>{iss.severity}</span>
                  {iss.isRedLine && <span className="pill sm red">red line</span>}
                  <span className="grid-meta">position from {iss.positionSource}</span>
                  <span style={{ marginLeft: "auto", display: "flex", gap: 4 }}>
                    <button className={`btn sm ${d === "accepted" ? "approve" : "ghost"}`} onClick={() => setDisp(key, "accepted")}>✓ {d === "accepted" ? "Accepted" : "Accept"}</button>
                    <button className={`btn sm ${d === "dismissed" ? "reject" : "ghost"}`} onClick={() => setDisp(key, "dismissed")}>✕ {d === "dismissed" ? "Dismissed" : "Dismiss"}</button>
                  </span>
                </div>
                <div style={{ fontSize: 12.5, color: "var(--text-faint)", marginBottom: 6 }}>
                  Counterparty: <em style={{ color: "var(--text-dim)" }}>{iss.counterpartyText}</em>
                </div>
                <div style={{ fontSize: 12.5, marginBottom: 6 }}>
                  <span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Firm position: </span>{iss.firmPosition}
                </div>
                {iss.proposedText && (
                  <div style={{ background: "var(--green-soft)", border: "1px solid rgba(127,176,105,0.3)", borderRadius: 6, padding: "8px 10px", fontSize: 12.5, marginBottom: 6 }}>
                    <div style={{ fontWeight: 600, fontSize: 11, color: "var(--green)", marginBottom: 3 }}>PROPOSED REPLACEMENT</div>
                    {iss.proposedText}
                  </div>
                )}
                <div style={{ fontSize: 12.5, color: "var(--text-dim)" }}>{iss.rationale}</div>
              </div>
            );
          })}

          {missing.length > 0 && (
            <>
              <div className="rnd-label" style={{ marginTop: 20 }}>Missing clauses — expected by your playbook, absent from the draft</div>
              {missing.map((m: MissingClause, i: number) => {
                const key = `m${i}`;
                const d = dispositions[key] ?? "pending";
                return (
                  <div key={key} className="alert-card" style={d === "dismissed" ? { opacity: 0.55 } : undefined}>
                    <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 8, flexWrap: "wrap" }}>
                      <strong style={{ fontFamily: "var(--font-mono)", fontSize: 12.5 }}>{m.clauseType}</strong>
                      <span className="pill sm red">missing</span>
                      <span className={`pill sm ${sevPill(m.severity)}`}>{m.severity}</span>
                      {m.isRedLine && <span className="pill sm red">red line</span>}
                      <span className="grid-meta">position from {m.positionSource}</span>
                      <span style={{ marginLeft: "auto", display: "flex", gap: 4 }}>
                        <button className={`btn sm ${d === "accepted" ? "approve" : "ghost"}`} onClick={() => setDisp(key, "accepted")}>✓ {d === "accepted" ? "Insert" : "Insert"}</button>
                        <button className={`btn sm ${d === "dismissed" ? "reject" : "ghost"}`} onClick={() => setDisp(key, "dismissed")}>✕ {d === "dismissed" ? "Dismissed" : "Dismiss"}</button>
                      </span>
                    </div>
                    <div style={{ fontSize: 12.5, marginBottom: 6 }}>
                      <span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Firm position: </span>{m.firmPosition}
                    </div>
                    {m.suggestedText && (
                      <div style={{ background: "var(--green-soft)", border: "1px solid rgba(127,176,105,0.3)", borderRadius: 6, padding: "8px 10px", fontSize: 12.5, marginBottom: 6 }}>
                        <div style={{ fontWeight: 600, fontSize: 11, color: "var(--green)", marginBottom: 3 }}>SUGGESTED INSERT</div>
                        {m.suggestedText}
                      </div>
                    )}
                    <div style={{ fontSize: 12.5, color: "var(--text-dim)" }}>{m.rationale}</div>
                  </div>
                );
              })}
            </>
          )}
        </div>
      )}
    </div>
  );
}

// ─── Headnotes ─────────────────────────────────────────────────────────────────

function HeadnotesTab({ notify }: { notify: (m: string) => void }) {
  const [opinionText, setOpinionText] = useState("");
  const [meta, setMeta] = useState({ caseName: "", citation: "", court: "", jurisdiction: "" });
  const [busy, setBusy] = useState(false);
  const [report, setReport] = useState<HeadnoteReport | null>(null);

  async function run() {
    setBusy(true);
    setReport(null);
    try {
      const res = await api.generateHeadnotes({
        opinionText,
        caseName: meta.caseName.trim() || undefined,
        citation: meta.citation.trim() || undefined,
        court: meta.court.trim() || undefined,
        jurisdiction: meta.jurisdiction.trim() || undefined,
      });
      setReport(res);
      notify(`${res.totalHeadnotes} headnotes extracted (${res.ratioCount} ratio, ${res.obiterCount} obiter)`);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  return (
    <div className="panel-body" style={{ maxWidth: 920 }}>
      <div className="field">
        <label>Case opinion text</label>
        <textarea style={{ minHeight: 220, fontFamily: "var(--font-mono)", fontSize: 12 }}
          value={opinionText} onChange={(e) => setOpinionText(e.target.value)}
          placeholder="Paste the full text of the judgment / opinion…" />
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "1.2fr 1fr 0.8fr 0.6fr", gap: 14, margin: "14px 0" }}>
        <div className="field"><label>Case name</label>
          <input value={meta.caseName} onChange={(e) => setMeta({ ...meta, caseName: e.target.value })} placeholder="Acme v. Beta" /></div>
        <div className="field"><label>Citation</label>
          <input value={meta.citation} onChange={(e) => setMeta({ ...meta, citation: e.target.value })} placeholder="123 F.3d 456" /></div>
        <div className="field"><label>Court</label>
          <input value={meta.court} onChange={(e) => setMeta({ ...meta, court: e.target.value })} placeholder="2d Cir." /></div>
        <div className="field"><label>Jurisdiction</label>
          <input value={meta.jurisdiction} onChange={(e) => setMeta({ ...meta, jurisdiction: e.target.value })} placeholder="US" /></div>
      </div>
      <button className="btn primary" disabled={busy || opinionText.trim().length < 100} onClick={run}>
        {busy ? "Extracting…" : "§ Generate headnotes"}
      </button>

      {report && (
        <div style={{ marginTop: 24 }}>
          <div className="synthesis" style={{ marginBottom: 18 }}>
            <div className="synthesis-head">{report.caseName}{report.citation && ` · ${report.citation}`}</div>
            <div className="prose" style={{ fontSize: 15 }}>{report.keyHolding}</div>
            <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 12 }}>
              {report.practiceAreas.map((pa) => <span key={pa} className="pill sm blue">{pa}</span>)}
              {report.noslegalArea && <span className="pill sm">{report.noslegalArea}</span>}
            </div>
          </div>

          {report.headnotes.map((h) => (
            <div key={h.number} className="alert-card">
              <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 6, flexWrap: "wrap" }}>
                <span style={{ fontFamily: "var(--font-display)", color: "var(--gold)", fontSize: 15 }}>¶{h.number}</span>
                <span className={`pill sm ${h.holdingType === "ratio" ? "gold" : "blue"}`}>{h.holdingType}</span>
                {h.areaOfLaw && <span className="pill sm">{h.areaOfLaw}</span>}
                <span className="grid-meta" style={{ marginLeft: "auto" }}>{Math.round(h.confidence * 100)}% confident</span>
              </div>
              <div style={{ fontSize: 13.5, lineHeight: 1.55 }}>{h.proposition}</div>
              {h.sourceText && (
                <div style={{ fontSize: 12, color: "var(--text-faint)", marginTop: 6, fontStyle: "italic" }}>
                  "{h.sourceText}"{h.location && ` — ${h.location}`}
                </div>
              )}
              {h.distinguishingFactors.length > 0 && (
                <div style={{ fontSize: 12, color: "var(--text-dim)", marginTop: 6 }}>
                  <span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Distinguish on: </span>
                  {h.distinguishingFactors.join("; ")}
                </div>
              )}
            </div>
          ))}

          {report.relatedPrinciples.length > 0 && (
            <div className="section-card" style={{ marginTop: 14 }}>
              <div className="section-card-title">Related principles</div>
              <ul style={{ paddingLeft: 20, fontSize: 13, color: "var(--text-dim)", lineHeight: 1.7 }}>
                {report.relatedPrinciples.map((p, i) => <li key={i}>{p}</li>)}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ─── Precedents ────────────────────────────────────────────────────────────────

function PrecedentsTab({ notify }: { notify: (m: string) => void }) {
  const [docType, setDocType] = useState("nda");
  const [practiceArea, setPracticeArea] = useState("");
  const [jurisdiction, setJurisdiction] = useState("");
  const [actingFor, setActingFor] = useState("");
  const [instructions, setInstructions] = useState("");
  const [busy, setBusy] = useState(false);
  const [doc, setDoc] = useState<PrecedentDocument | null>(null);

  async function run() {
    setBusy(true);
    setDoc(null);
    try {
      const res = await api.generatePrecedent({
        documentType: docType,
        practiceArea: practiceArea || undefined,
        jurisdiction: jurisdiction.trim() || undefined,
        actingFor: actingFor.trim() || undefined,
        specialInstructions: instructions.trim() || undefined,
      });
      setDoc(res);
      notify(`"${res.title}" generated — ${res.clauses.length} clauses, ${res.playbookPositionCount} playbook positions applied`);
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  return (
    <div className="panel-body" style={{ maxWidth: 920 }}>
      <p style={{ fontSize: 13, color: "var(--text-dim)", margin: "0 0 14px" }}>
        Drafts a complete first-cut document from your precedent library with the playbook
        cascade applied — your standard positions baked into every clause, red lines embedded,
        provenance per clause.
      </p>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 0.7fr 0.9fr", gap: 14, marginBottom: 14 }}>
        <div className="field"><label>Document type</label>
          <select value={docType} onChange={(e) => setDocType(e.target.value)}>
            {PRECEDENT_TYPES.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
          </select></div>
        <div className="field"><label>Practice area</label>
          <select value={practiceArea} onChange={(e) => setPracticeArea(e.target.value)}>
            <option value="">— auto —</option>
            {PRACTICE_AREAS.map((pa) => <option key={pa} value={pa}>{pa}</option>)}
          </select></div>
        <div className="field"><label>Jurisdiction</label>
          <input value={jurisdiction} onChange={(e) => setJurisdiction(e.target.value)} placeholder="England & Wales" /></div>
        <div className="field"><label>Acting for</label>
          <input value={actingFor} onChange={(e) => setActingFor(e.target.value)} placeholder="buyer / disclosing party" /></div>
      </div>
      <div className="field">
        <label>Special instructions</label>
        <textarea style={{ minHeight: 72 }} value={instructions} onChange={(e) => setInstructions(e.target.value)}
          placeholder="e.g. mutual NDA, 3-year confidentiality term, carve-out for residuals…" />
      </div>
      <button className="btn primary" style={{ marginTop: 12 }} disabled={busy} onClick={run}>
        {busy ? "Drafting from precedents…" : "⚒ Generate precedent"}
      </button>

      {doc && (
        <div style={{ marginTop: 24 }}>
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap", marginBottom: 14, alignItems: "center" }}>
            <span style={{ fontFamily: "var(--font-display)", fontSize: 19 }}>{doc.title}</span>
            <span className="grid-meta">
              {doc.sourcePrecedentCount} source precedents · {doc.playbookPositionCount} playbook positions
            </span>
          </div>

          {doc.draftingNotes.length > 0 && (
            <div className="section-card" style={{ marginBottom: 16 }}>
              <div className="section-card-title">Drafting notes</div>
              <ul style={{ paddingLeft: 20, fontSize: 13, color: "var(--text-dim)", lineHeight: 1.7 }}>
                {doc.draftingNotes.map((n, i) => <li key={i}>{n}</li>)}
              </ul>
            </div>
          )}

          <div className="synthesis">
            <div className="synthesis-head">Assembled document</div>
            <div className="prose md"><Markdown source={doc.document} /></div>
          </div>

          {doc.clauses.length > 0 && (
            <div style={{ marginTop: 16 }}>
              <div className="rnd-label">Clause provenance</div>
              {doc.clauses.map((c, i) => (
                <div key={i} className="alert-card">
                  <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 4, flexWrap: "wrap" }}>
                    <strong style={{ fontSize: 13 }}>{c.heading}</strong>
                    <span className="pill sm blue">{c.source.replace("_", " ")}</span>
                    {c.hasRedLine && <span className="pill sm red">red line</span>}
                  </div>
                  {c.notes && <div style={{ fontSize: 12.5, color: "var(--text-dim)" }}>{c.notes}</div>}
                  {c.fallback && <div style={{ fontSize: 12.5, color: "var(--text-faint)", marginTop: 3 }}>Fallback: {c.fallback}</div>}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ─── Citation check ────────────────────────────────────────────────────────────

const SIGNAL_STYLE: Record<CitationSignal, { color: string; glyph: string }> = {
  green:  { color: "var(--green)", glyph: "●" },
  yellow: { color: "var(--amber)", glyph: "▲" },
  red:    { color: "var(--red)",   glyph: "■" },
  blue:   { color: "var(--blue)",  glyph: "◆" },
};

function CitationsTab({ notify }: { notify: (m: string) => void }) {
  const [q, setQ] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<CitationCheckResult | null>(null);

  async function check() {
    if (!q.trim()) return;
    setBusy(true);
    setResult(null);
    try { setResult(await api.checkCitation(q.trim())); }
    catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  return (
    <div className="panel-body" style={{ maxWidth: 780 }}>
      <div className="field">
        <label>Citation or case name</label>
        <div style={{ display: "flex", gap: 8 }}>
          <input value={q} onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && check()}
            placeholder="e.g. 410 U.S. 113, or Chevron v. NRDC" />
          <button className="btn primary" disabled={busy || !q.trim()} onClick={check}>{busy ? "Checking…" : "⚖ Check"}</button>
        </div>
      </div>

      {result && (
        <div style={{ marginTop: 20 }}>
          <div className="section-card">
            <div style={{ display: "flex", gap: 12, alignItems: "center", marginBottom: 10, flexWrap: "wrap" }}>
              <span style={{ color: SIGNAL_STYLE[result.signal].color, fontSize: 22 }}>{SIGNAL_STYLE[result.signal].glyph}</span>
              <div>
                <div style={{ fontWeight: 600, fontSize: 15 }}>
                  {result.caseName ?? result.query}
                  {result.year && <span style={{ color: "var(--text-faint)", fontWeight: 400 }}> ({result.year})</span>}
                </div>
                <div style={{ fontSize: 12.5, color: "var(--text-dim)" }}>
                  {result.resolvedCitation && <>{result.resolvedCitation} · </>}
                  {result.court && <>{result.court} · </>}
                  <span style={{ color: SIGNAL_STYLE[result.signal].color, fontWeight: 600 }}>{result.signalLabel}</span>
                  {" "}· {Math.round(result.confidence * 100)}% confidence
                </div>
              </div>
              {result.courtListenerUrl && (
                <a className="btn ghost sm" style={{ marginLeft: "auto" }} href={result.courtListenerUrl} target="_blank" rel="noreferrer">CourtListener ↗</a>
              )}
            </div>
            <div style={{ display: "flex", gap: 10, marginBottom: 10 }}>
              <span className="pill green">{result.positiveTreatmentCount} positive</span>
              <span className="pill red">{result.negativeTreatmentCount} negative</span>
              <span className="pill">{result.status.replace("_", " ")}</span>
            </div>
            <div style={{ fontSize: 13.5, lineHeight: 1.6, color: "var(--text-dim)" }}>{result.reasoning}</div>
          </div>

          {result.topNegativeTreatments.length > 0 && (
            <div className="section-card" style={{ marginTop: 14 }}>
              <div className="section-card-title">Negative treatments</div>
              {result.topNegativeTreatments.map((t, i) => (
                <div key={i} style={{ display: "flex", gap: 8, alignItems: "baseline", marginBottom: 8, fontSize: 13, flexWrap: "wrap" }}>
                  <span className="pill sm red">{t.treatmentType}</span>
                  <span style={{ fontWeight: 500 }}>{t.caseName}</span>
                  <span className="grid-meta">{t.citation}{t.court && ` · ${t.court}`}{t.year && ` · ${t.year}`}</span>
                  {t.url && <a href={t.url} target="_blank" rel="noreferrer" style={{ color: "var(--blue)", fontSize: 12 }}>view ↗</a>}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
