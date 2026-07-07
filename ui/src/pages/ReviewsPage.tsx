import { useEffect, useRef, useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { api } from "../api";
import type {
  ClauseEvent, ClauseTimeline, DriftStatus, RedtimeSource, RedtimeTimeline, RedtimeVersion,
  ReviewCell, ReviewCitation, ReviewCitationMethod, ReviewRecord, ReviewRow,
} from "../types";
import { ErrorState } from "../Library";
import { timeAgo } from "../primitives";

type Tab = "grid" | "timeline";

export function ReviewsPage({ notify }: { notify: (m: string) => void }) {
  const [tab, setTab] = useState<Tab>("grid");

  return (
    <div className="page-scroll">
      <div className="page">
        <div className="page-head">
          <h1 className="page-title">Reviews</h1>
          <p className="page-sub">Due-diligence grids from tabular reviews — every extraction flagged and citation-verified — plus the Redtime per-clause redline timeline of a negotiation.</p>
        </div>

        <div className="tabs">
          {([
            ["grid", "Due-diligence grid"],
            ["timeline", "Redtime timeline"],
          ] as Array<[Tab, string]>).map(([t, label]) => (
            <button key={t} className={`tab ${tab === t ? "active" : ""}`} onClick={() => setTab(t)}>
              {label}{tab === t && <motion.span layoutId="reviews-underline" className="tab-underline" />}
            </button>
          ))}
        </div>

        {tab === "grid" && <DueDiligenceTab notify={notify} />}
        {tab === "timeline" && <RedtimeTab notify={notify} />}
      </div>
    </div>
  );
}

// ─── Shared text helpers ───────────────────────────────────────────────────────

// stripCitationMarkers removes inline [[page:N||quote:...]] markers from a cell
// summary for display (mirrors the marker grammar in internal/tools/tabcite.go;
// non-greedy so a marker ends at its own "]]").
export function stripCitationMarkers(summary: string): string {
  return summary
    .replace(/\[\[page:[^|[\]]*\|\|quote:[\s\S]*?\]\]/g, "")
    .replace(/[ \t]{2,}/g, " ")
    .replace(/ ([.,;:])/g, "$1")
    .trim();
}

// normalizeWithMap normalizes text for tolerant quote matching — curly quotes
// and apostrophes straightened, whitespace runs collapsed to one space — and
// returns a map from each normalized index back to the original index (the
// same approach as normalizeText/normalizeWithMap in the Go redline anchoring).
function normalizeWithMap(s: string): { norm: string; map: number[] } {
  const out: string[] = [];
  const map: number[] = [];
  let lastWasSpace = false;
  for (let i = 0; i < s.length; i++) {
    let ch = s[i];
    if (ch === "‘" || ch === "’") ch = "'";
    else if (ch === "“" || ch === "”") ch = '"';
    if (/\s/.test(ch)) {
      if (lastWasSpace) continue;
      out.push(" ");
      map.push(i);
      lastWasSpace = true;
    } else {
      out.push(ch);
      map.push(i);
      lastWasSpace = false;
    }
  }
  return { norm: out.join(""), map };
}

// locateQuote finds a citation quote in the source text, tolerating curly-quote
// and whitespace differences (then case), and maps the match back to a span of
// the original text. Returns null when the quote genuinely isn't there.
export function locateQuote(doc: string, quote: string): { start: number; end: number } | null {
  const d = normalizeWithMap(doc);
  const q = normalizeWithMap(quote).norm.trim();
  if (!q) return null;
  let idx = d.norm.indexOf(q);
  if (idx < 0) idx = d.norm.toLowerCase().indexOf(q.toLowerCase());
  if (idx < 0) return null;
  return { start: d.map[idx], end: d.map[idx + q.length - 1] + 1 };
}

// ─── Due-diligence grid ────────────────────────────────────────────────────────

const FLAG_ORDER = ["green", "yellow", "red", "grey"] as const;

const FLAG_TINT: Record<string, string> = {
  green: "var(--green-soft)", yellow: "var(--amber-soft)", red: "var(--red-soft)", grey: "transparent",
};
const FLAG_DOT: Record<string, string> = {
  green: "var(--green)", yellow: "var(--amber)", red: "var(--red)", grey: "var(--text-faint)",
};
const FLAG_PILL: Record<string, string> = { green: "green", yellow: "amber", red: "red", grey: "" };

// Pill color per verification method: string matches green, a model paraphrase
// verdict blue, the 3-vote ensemble amber, and unverified red.
const METHOD_PILL: Record<ReviewCitationMethod, string> = {
  exact_match: "green", tolerant_match: "green", paraphrase_judge: "blue",
  ensemble_majority: "amber", unverified: "red",
};
const METHOD_LABEL: Record<ReviewCitationMethod, string> = {
  exact_match: "exact", tolerant_match: "tolerant", paraphrase_judge: "paraphrase",
  ensemble_majority: "ensemble", unverified: "unverified",
};

function DueDiligenceTab({ notify }: { notify: (m: string) => void }) {
  const [idInput, setIdInput] = useState("");
  const [review, setReview] = useState<ReviewRecord | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sel, setSel] = useState<{ ri: number; ci: number } | null>(null);
  const [source, setSource] = useState<{ row: ReviewRow; citation: ReviewCitation; index: number } | null>(null);

  async function load() {
    const id = idInput.trim();
    if (!id) return;
    setLoading(true);
    setError(null);
    setSel(null);
    try {
      const rec = await api.getReview(id);
      setReview(rec);
      notify(`Review loaded — ${rec.rows.length} documents × ${rec.columns.length} columns`);
    } catch (e) {
      setReview(null);
      setError((e as Error).message);
    } finally { setLoading(false); }
  }

  const selCell: ReviewCell | null =
    review && sel ? review.rows[sel.ri]?.cells[sel.ci] ?? null : null;
  const selRow: ReviewRow | null = review && sel ? review.rows[sel.ri] ?? null : null;

  return (
    <div className="panel-body">
      <div className="field" style={{ maxWidth: 640 }}>
        <label>Review ID <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(returned by the tabular_review tool)</span></label>
        <div style={{ display: "flex", gap: 8 }}>
          <input value={idInput} onChange={(e) => setIdInput(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && load()}
            placeholder="e.g. 7f3a1c2e-…" style={{ fontFamily: "var(--font-mono)", fontSize: 12.5 }} />
          <button className="btn primary" disabled={loading || !idInput.trim()} onClick={load}>
            {loading ? "Loading…" : "⊞ Load review"}
          </button>
        </div>
        <div className="grid-meta" style={{ marginTop: 6 }}>
          There is no review-list endpoint yet — paste the reviewId from the task output or audit log.
        </div>
      </div>

      {error && <div style={{ marginTop: 14 }}><ErrorState message={error} onRetry={load} /></div>}

      {review && (
        <div style={{ marginTop: 18 }}>
          {/* Header stat strip: flag tally + citation verification roll-up. */}
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "center", marginBottom: 12 }}>
            {FLAG_ORDER.map((f) => (
              <span key={f} className={`pill ${FLAG_PILL[f]}`} title={review.legend[f] ?? f}>
                <span className="dot" style={{ background: FLAG_DOT[f] }} />
                {review.flagTally[f] ?? 0} {f}
              </span>
            ))}
            {review.citationTally && (
              <span className="pill blue" title={Object.entries(review.citationTally.byMethod)
                .filter(([, n]) => n > 0)
                .map(([m, n]) => `${METHOD_LABEL[m as ReviewCitationMethod] ?? m}: ${n}`)
                .join(" · ") || "no citations"}>
                Citations verified: {review.citationTally.verified}/{review.citationTally.total}
              </span>
            )}
            <span className="grid-meta">
              {review.rows.length} documents · {review.columns.length} columns · {timeAgo(review.createdAt)}
            </span>
            <a className="btn ghost sm" style={{ marginLeft: "auto" }} href={api.reviewCsvUrl(review.reviewId)} download>⤓ Export CSV</a>
          </div>

          <div className="grid-wrap">
            <div className="grid-scroll">
              <table className="grid">
                <thead>
                  <tr>
                    <th>Document</th>
                    {review.columns.map((c) => <th key={c}>{c}</th>)}
                  </tr>
                </thead>
                <tbody>
                  {review.rows.map((row, ri) => (
                    <tr key={row.documentId || ri}>
                      <td style={{ fontWeight: 500, color: "var(--text)", minWidth: 160 }}>
                        {row.document || row.documentId}
                        <div className="grid-meta" style={{ marginTop: 3 }}>{row.documentId}</div>
                      </td>
                      {review.columns.map((_, ci) => {
                        const cell = row.cells[ci];
                        if (!cell) return <td key={ci} style={{ color: "var(--text-faint)" }}>—</td>;
                        const active = sel?.ri === ri && sel?.ci === ci;
                        return (
                          <td key={ci}
                            onClick={() => setSel(active ? null : { ri, ci })}
                            title={`${cell.flag} — ${review.legend[cell.flag] ?? ""}`}
                            style={{
                              background: FLAG_TINT[cell.flag] ?? "transparent",
                              cursor: "pointer", minWidth: 180, maxWidth: 360, verticalAlign: "top",
                              boxShadow: active ? "inset 0 0 0 1.5px var(--accent, var(--gold))" : undefined,
                            }}>
                            <span style={{ display: "inline-block", width: 7, height: 7, borderRadius: "50%", background: FLAG_DOT[cell.flag], marginRight: 6, flexShrink: 0 }} />
                            {stripCitationMarkers(cell.summary)}
                            {cell.citationsTotal > 0 && (
                              <span className="grid-meta" style={{ marginLeft: 6, whiteSpace: "nowrap" }}>
                                {cell.citationsVerified}/{cell.citationsTotal} ✓
                              </span>
                            )}
                          </td>
                        );
                      })}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>

          {/* Cell detail: full summary, reasoning, and the citation pills. */}
          {selCell && selRow && (
            <div className="section-card" style={{ marginTop: 14 }}>
              <div style={{ display: "flex", gap: 8, alignItems: "center", flexWrap: "wrap", marginBottom: 8 }}>
                <strong style={{ fontSize: 13.5 }}>{selRow.document || selRow.documentId}</strong>
                <span className="grid-meta">·</span>
                <span style={{ fontFamily: "var(--font-mono)", fontSize: 12.5, color: "var(--gold)" }}>{selCell.column}</span>
                <span className={`pill sm ${FLAG_PILL[selCell.flag]}`} title={review.legend[selCell.flag] ?? ""}>{selCell.flag}</span>
                <button className="btn ghost sm" style={{ marginLeft: "auto" }} onClick={() => setSel(null)}>✕ Close</button>
              </div>
              <div style={{ fontSize: 13.5, lineHeight: 1.55, marginBottom: 8 }}>{stripCitationMarkers(selCell.summary)}</div>
              {selCell.reasoning && (
                <div style={{ fontSize: 12.5, color: "var(--text-dim)", marginBottom: 10 }}>
                  <span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Reasoning: </span>{selCell.reasoning}
                </div>
              )}
              {selCell.citations.length > 0 ? (
                <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
                  {selCell.citations.map((c, i) => (
                    <button key={i}
                      className={`pill sm ${METHOD_PILL[c.method] ?? "red"}`}
                      style={{ cursor: "pointer", border: "none" }}
                      title={`${METHOD_LABEL[c.method] ?? c.method} · ${Math.round(c.confidence * 100)}% confidence${c.note ? ` — ${c.note}` : ""} — click to view in the source document`}
                      onClick={() => setSource({ row: selRow, citation: c, index: i + 1 })}>
                      #{i + 1} · p.{c.page} · {METHOD_LABEL[c.method] ?? c.method} {Math.round(c.confidence * 100)}%
                    </button>
                  ))}
                </div>
              ) : (
                <div className="grid-meta">No citations in this cell.</div>
              )}
            </div>
          )}
        </div>
      )}

      {!review && !error && !loading && (
        <div className="placeholder" style={{ marginTop: 18 }}>
          Load a tabular review to see the document × question matrix: RAG-flagged cells, per-cell reasoning, and verified-citation pills that open the source document.
        </div>
      )}

      <AnimatePresence>
        {source && <SourcePanel row={source.row} citation={source.citation} index={source.index} onClose={() => setSource(null)} />}
      </AnimatePresence>
    </div>
  );
}

// ─── Source panel ──────────────────────────────────────────────────────────────

// Resolved document texts, cached per document id for the session. `null`
// records a miss so a missing document isn't re-searched on every pill click.
const docTextCache = new Map<string, { title: string; content: string } | null>();

// resolveDocText finds a knowledge-store document's full text through the
// existing semantic-search endpoint (there is no GET /documents/:id route):
// search by title first, then by the quote itself, and take the result whose
// document id matches the row.
async function resolveDocText(row: ReviewRow, quote: string): Promise<{ title: string; content: string } | null> {
  if (docTextCache.has(row.documentId)) return docTextCache.get(row.documentId) ?? null;
  const queries = [row.document || row.documentId, quote.slice(0, 200)];
  for (const q of queries) {
    if (!q.trim()) continue;
    try {
      const results = await api.searchDocuments(q, 30);
      const hit = results.find((r) => r.document.id === row.documentId);
      if (hit && hit.document.content) {
        const doc = { title: hit.document.title, content: hit.document.content };
        docTextCache.set(row.documentId, doc);
        return doc;
      }
    } catch { /* fall through to the next query */ }
  }
  docTextCache.set(row.documentId, null);
  return null;
}

function SourcePanel({ row, citation, index, onClose }: {
  row: ReviewRow; citation: ReviewCitation; index: number; onClose: () => void;
}) {
  const [state, setState] = useState<"loading" | "missing" | "ready">("loading");
  const [doc, setDoc] = useState<{ title: string; content: string } | null>(null);
  const markRef = useRef<HTMLElement>(null);

  useEffect(() => {
    let alive = true;
    setState("loading");
    resolveDocText(row, citation.quote).then((d) => {
      if (!alive) return;
      setDoc(d);
      setState(d ? "ready" : "missing");
    });
    return () => { alive = false; };
  }, [row, citation]);

  // Scroll the highlighted quote into view once the text has rendered.
  useEffect(() => {
    if (state === "ready") markRef.current?.scrollIntoView({ block: "center" });
  }, [state, doc]);

  const span = doc ? locateQuote(doc.content, citation.quote) : null;

  return (
    <div className="modal-scrim" style={{ zIndex: 60 }} onClick={onClose}>
      <motion.div className="modal" style={{ maxWidth: 860, width: "92vw" }} onClick={(e) => e.stopPropagation()}
        initial={{ opacity: 0, y: 16 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: 16 }}>
        <div className="modal-head">
          <div>
            <div style={{ fontWeight: 600, fontSize: 15 }}>{doc?.title || row.document || row.documentId}</div>
            <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap", marginTop: 6 }}>
              <span className="pill sm">citation #{index}</span>
              <span className="pill sm">p.{citation.page}</span>
              <span className={`pill sm ${METHOD_PILL[citation.method] ?? "red"}`}>
                {METHOD_LABEL[citation.method] ?? citation.method} · {Math.round(citation.confidence * 100)}%
              </span>
              {citation.verified ? <span className="pill sm green">✓ verified</span> : <span className="pill sm red">✕ unverified</span>}
            </div>
          </div>
          <button className="btn ghost sm" onClick={onClose}>✕</button>
        </div>
        <div className="modal-body">
          <div style={{ background: "var(--panel-2)", borderRadius: 8, padding: "10px 12px", fontSize: 13, fontStyle: "italic", lineHeight: 1.55 }}>
            "{citation.quote}"
          </div>
          {citation.note && (
            <div style={{ fontSize: 12.5, color: "var(--amber)", marginTop: 8 }}>{citation.note}</div>
          )}

          {state === "loading" && <div className="placeholder" style={{ marginTop: 12 }}>Locating the source document…</div>}
          {state === "missing" && (
            <div className="placeholder" style={{ marginTop: 12 }}>
              The source document ({row.documentId}) could not be resolved from the knowledge store — it may have been removed, or you may not have access to it.
            </div>
          )}
          {state === "ready" && doc && (
            <>
              {!span && (
                <div style={{ fontSize: 12.5, color: "var(--amber)", marginTop: 10 }}>
                  ⚠ The quote was not located verbatim in the source text — the citation points at page {citation.page}; the full document is shown below.
                </div>
              )}
              <div style={{
                marginTop: 12, maxHeight: "48vh", overflow: "auto", fontFamily: "var(--font-mono)",
                fontSize: 12, lineHeight: 1.6, whiteSpace: "pre-wrap", wordBreak: "break-word",
                border: "1px solid var(--border)", borderRadius: 8, padding: "12px 14px",
              }}>
                {span ? (
                  <>
                    {doc.content.slice(0, span.start)}
                    <mark ref={markRef} style={{ background: "var(--gold-soft)", color: "var(--text)", outline: "1px solid var(--gold)", borderRadius: 3, padding: "1px 0" }}>
                      {doc.content.slice(span.start, span.end)}
                    </mark>
                    {doc.content.slice(span.end)}
                  </>
                ) : doc.content}
              </div>
            </>
          )}
        </div>
      </motion.div>
    </div>
  );
}

// ─── Redtime timeline ──────────────────────────────────────────────────────────

const SOURCE_PILL: Record<RedtimeSource, string> = { ours: "green", theirs: "red", upload: "" };

const KIND_GLYPH: Record<string, { glyph: string; color: string; label: string }> = {
  insertion:    { glyph: "＋", color: "var(--green)", label: "insertion" },
  deletion:     { glyph: "−",  color: "var(--red)",   label: "deletion" },
  substitution: { glyph: "±",  color: "var(--amber)", label: "substitution" },
};

const DECISION_PILL: Record<string, string> = {
  accept: "green", reject: "red", counter: "amber", review: "blue",
};

const DRIFT_STYLE: Record<DriftStatus, { pill: string; label: string }> = {
  at_position: { pill: "green", label: "at position" },
  above:       { pill: "blue",  label: "above" },
  below:       { pill: "red",   label: "below" },
  unknown:     { pill: "",      label: "unknown" },
};

function truncText(s: string, max = 90): string {
  if (s.length <= max) return s;
  const cut = s.slice(0, max);
  const i = cut.lastIndexOf(" ");
  return (i > 40 ? cut.slice(0, i) : cut) + "…";
}

function RedtimeTab({ notify }: { notify: (m: string) => void }) {
  const [idInput, setIdInput] = useState("");
  const [matterNumber, setMatterNumber] = useState("");
  const [practiceArea, setPracticeArea] = useState("");
  const [timeline, setTimeline] = useState<RedtimeTimeline | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  async function load() {
    const id = idInput.trim();
    if (!id) return;
    setLoading(true);
    setError(null);
    setExpanded(null);
    try {
      const tl = await api.getTimeline(id, {
        matterNumber: matterNumber.trim() || undefined,
        practiceArea: practiceArea.trim() || undefined,
      });
      setTimeline(tl);
      notify(`Timeline loaded — ${tl.rounds} rounds, ${tl.clauses.length} clauses`);
    } catch (e) {
      setTimeline(null);
      setError((e as Error).message);
    } finally { setLoading(false); }
  }

  // Column per round: version rounds plus any event round with no version
  // (defensive — normally every event round has a version), ascending.
  const rounds: number[] = timeline
    ? [...new Set([
        ...timeline.versions.map((v) => v.round),
        ...timeline.clauses.flatMap((cl) => cl.events.map((e) => e.round)),
      ])].sort((a, b) => a - b)
    : [];
  const versionsByRound = new Map<number, RedtimeVersion[]>();
  for (const v of timeline?.versions ?? []) {
    versionsByRound.set(v.round, [...(versionsByRound.get(v.round) ?? []), v]);
  }

  return (
    <div className="panel-body">
      <div style={{ display: "grid", gridTemplateColumns: "1.6fr 0.8fr 0.8fr auto", gap: 10, alignItems: "end", maxWidth: 900 }}>
        <div className="field">
          <label>Lineage or version ID <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(from redtime_register / the negotiation task)</span></label>
          <input value={idInput} onChange={(e) => setIdInput(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && load()}
            placeholder="e.g. lin-4b9d…" style={{ fontFamily: "var(--font-mono)", fontSize: 12.5 }} />
        </div>
        <div className="field"><label>Matter <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(scopes drift playbooks)</span></label>
          <input value={matterNumber} onChange={(e) => setMatterNumber(e.target.value)} placeholder="M-2026-001" /></div>
        <div className="field"><label>Practice area</label>
          <input value={practiceArea} onChange={(e) => setPracticeArea(e.target.value)} placeholder="— optional —" /></div>
        <button className="btn primary" disabled={loading || !idInput.trim()} onClick={load}>
          {loading ? "Loading…" : "↹ Load timeline"}
        </button>
      </div>
      <div className="grid-meta" style={{ marginTop: 6 }}>
        There is no lineage-list endpoint yet — paste the lineage (or any version) id.
      </div>

      {error && <div style={{ marginTop: 14 }}><ErrorState message={error} onRetry={load} /></div>}

      {timeline && (
        <div style={{ marginTop: 18 }}>
          <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "center", marginBottom: 12 }}>
            <span className="pill blue">{timeline.rounds} rounds</span>
            <span className="pill">{timeline.clauses.length} clauses</span>
            {timeline.clauses.some((cl) => cl.events.some((e) => !e.viaTrackedChange)) && (
              <span className="pill amber" title="At least one edit was made without tracked changes">⚠ silent edits present</span>
            )}
            <span className="grid-meta">
              lineage {timeline.lineageId} · generated {timeAgo(timeline.generatedAt)}
            </span>
          </div>

          <div className="grid-wrap">
            <div className="grid-scroll">
              <table className="grid">
                <thead>
                  <tr>
                    <th style={{ minWidth: 160 }}>Clause</th>
                    {rounds.map((r) => (
                      <th key={r} style={{ minWidth: 200 }}>
                        <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                          <span style={{ fontWeight: 700 }}>R{r}</span>
                          {(versionsByRound.get(r) ?? []).map((v) => (
                            <span key={v.id} style={{ display: "inline-flex", gap: 5, alignItems: "center" }} title={v.id}>
                              <span className={`pill sm ${SOURCE_PILL[v.source] ?? ""}`}>{v.source}</span>
                              {v.author && <span className="grid-meta">{v.author}</span>}
                              {v.createdAt && <span className="grid-meta">{timeAgo(v.createdAt)}</span>}
                            </span>
                          ))}
                        </div>
                      </th>
                    ))}
                    <th style={{ minWidth: 110 }}>Drift</th>
                  </tr>
                </thead>
                <tbody>
                  {timeline.clauses.map((cl) => (
                    <ClauseRow key={cl.clause} clause={cl} rounds={rounds}
                      expanded={expanded === cl.clause}
                      onToggle={() => setExpanded(expanded === cl.clause ? null : cl.clause)} />
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      )}

      {!timeline && !error && !loading && (
        <div className="placeholder" style={{ marginTop: 18 }}>
          Load a document lineage to see the negotiation round by round: who changed each clause, tracked vs silent edits, decisions, and where each clause has drifted relative to your playbook position.
        </div>
      )}
    </div>
  );
}

function ClauseRow({ clause, rounds, expanded, onToggle }: {
  clause: ClauseTimeline; rounds: number[]; expanded: boolean; onToggle: () => void;
}) {
  const drift = clause.drift;
  const ds = drift ? DRIFT_STYLE[drift.status] ?? DRIFT_STYLE.unknown : null;
  return (
    <>
      <tr>
        <td onClick={onToggle} style={{ cursor: clause.currentText ? "pointer" : "default", fontWeight: 500, color: "var(--text)", verticalAlign: "top" }}>
          {clause.currentText && <span style={{ color: "var(--text-faint)", marginRight: 6 }}>{expanded ? "▾" : "▸"}</span>}
          {clause.clause}
        </td>
        {rounds.map((r) => {
          const events = clause.events.filter((e) => e.round === r);
          return (
            <td key={r} style={{ verticalAlign: "top" }}>
              {events.length === 0
                ? <span style={{ color: "var(--text-faint)" }}>·</span>
                : events.map((e, i) => <EventCard key={i} ev={e} />)}
            </td>
          );
        })}
        <td style={{ verticalAlign: "top" }}>
          {ds ? (
            <span className={`pill sm ${ds.pill}`}
              title={`${drift!.note}${drift!.playbookTier ? ` (playbook tier: ${drift!.playbookTier})` : ""}`}>
              {ds.label}
            </span>
          ) : <span style={{ color: "var(--text-faint)" }}>—</span>}
        </td>
      </tr>
      {expanded && clause.currentText && (
        <tr>
          <td colSpan={rounds.length + 2} style={{ background: "var(--panel-2)" }}>
            <div style={{ fontSize: 11, fontWeight: 600, color: "var(--text-faint)", marginBottom: 4 }}>CURRENT TEXT</div>
            <div style={{ fontFamily: "var(--font-mono)", fontSize: 12, lineHeight: 1.6, whiteSpace: "pre-wrap" }}>{clause.currentText}</div>
          </td>
        </tr>
      )}
    </>
  );
}

function EventCard({ ev }: { ev: ClauseEvent }) {
  const k = KIND_GLYPH[ev.kind] ?? { glyph: "•", color: "var(--text-dim)", label: ev.kind };
  return (
    <div style={{ marginBottom: 8 }}>
      <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap", marginBottom: 3 }}>
        <span style={{ color: k.color, fontWeight: 700 }} title={k.label}>{k.glyph}</span>
        <span className="pill sm">{ev.actor}</span>
        {ev.viaTrackedChange
          ? <span className="pill sm blue" title="Made as a tracked change">tracked</span>
          : <span className="pill sm amber" title="Edit made without tracked changes — it would not appear in a compare of the markup alone">⚠ silent edit</span>}
        {ev.decision && (
          <span className={`pill sm ${DECISION_PILL[ev.decision] ?? ""}`} title={ev.decisionNote || undefined}>{ev.decision}</span>
        )}
      </div>
      <div style={{ fontSize: 12, lineHeight: 1.5 }}>
        {ev.fromText && (
          <span style={{ textDecoration: "line-through", color: "var(--red)" }} title={ev.fromText}>{truncText(ev.fromText)}</span>
        )}
        {ev.fromText && ev.toText && <span style={{ color: "var(--text-faint)" }}> → </span>}
        {ev.toText && (
          <span style={{ color: "var(--green)" }} title={ev.toText}>{truncText(ev.toText)}</span>
        )}
      </div>
    </div>
  );
}
