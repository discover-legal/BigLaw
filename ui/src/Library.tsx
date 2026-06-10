import { useEffect, useRef, useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { api } from "./api";
import type { DocumentRef, IngestResult, SearchResult } from "./types";
import { PRACTICE_AREAS } from "./types";
import { timeAgo } from "./primitives";

type Mode = "documents" | "ingest" | "upload" | "search";

export function LibraryPage({ notify }: { notify: (m: string) => void }) {
  const [mode, setMode] = useState<Mode>("documents");

  // document list
  const [docs, setDocs] = useState<DocumentRef[]>([]);
  const [docsLoading, setDocsLoading] = useState(true);
  const [docsError, setDocsError] = useState<string | null>(null);
  const [filter, setFilter] = useState("");

  // text ingest
  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  const [jurisdiction, setJurisdiction] = useState("");
  const [docType, setDocType] = useState("contract");
  const [manualPracticeArea, setManualPracticeArea] = useState("");
  const [busy, setBusy] = useState(false);
  const [lastResult, setLastResult] = useState<IngestResult | null>(null);

  // file upload
  const fileRef = useRef<HTMLInputElement>(null);
  const [uploadResult, setUploadResult] = useState<IngestResult | null>(null);
  const [uploading, setUploading] = useState(false);

  // search
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [searching, setSearching] = useState(false);

  function loadDocs() {
    setDocsLoading(true);
    setDocsError(null);
    api.listDocuments()
      .then(setDocs)
      .catch((e) => setDocsError((e as Error).message))
      .finally(() => setDocsLoading(false));
  }
  useEffect(() => { loadDocs(); }, []);

  async function ingest() {
    setBusy(true);
    setLastResult(null);
    try {
      const result = await api.ingestDocument({ title, content, jurisdiction, documentType: docType, practiceArea: manualPracticeArea || undefined });
      setLastResult(result);
      notify("Document ingested into the registry");
      setTitle(""); setContent(""); setManualPracticeArea("");
      loadDocs();
    } catch (e) { notify((e as Error).message); }
    finally { setBusy(false); }
  }

  async function upload(file: File) {
    setUploading(true);
    setUploadResult(null);
    try {
      const result = await api.uploadDocument(file);
      setUploadResult(result);
      notify(`"${result.title}" ingested`);
      loadDocs();
    } catch (e) { notify((e as Error).message); }
    finally { setUploading(false); }
  }

  async function search() {
    setSearching(true);
    try { setResults(await api.searchDocuments(query)); }
    catch (e) { notify((e as Error).message); }
    finally { setSearching(false); }
  }

  const filteredDocs = docs.filter((d) =>
    !filter.trim() || `${d.title} ${d.practiceArea ?? ""} ${d.documentType ?? ""} ${d.jurisdiction ?? ""}`.toLowerCase().includes(filter.trim().toLowerCase()));

  return (
    <div className="page-scroll">
      <div className="page">
        <div className="page-head">
          <h1 className="page-title">The library</h1>
          <p className="page-sub">Ingest documents into the knowledge registry, browse the collection, or search it semantically.</p>
        </div>

        <div className="tabs">
          {([
            ["documents", `Documents${docs.length ? ` · ${docs.length}` : ""}`],
            ["ingest", "Paste text"],
            ["upload", "Upload file"],
            ["search", "Search"],
          ] as Array<[Mode, string]>).map(([m, label]) => (
            <button key={m} className={`tab ${mode === m ? "active" : ""}`} onClick={() => setMode(m)}>
              {label}{mode === m && <motion.span layoutId="lib-underline" className="tab-underline" />}
            </button>
          ))}
        </div>

        {mode === "documents" && (
          <div className="panel-body">
            <div className="grid-toolbar">
              <div className="search">
                <span className="ico">⌕</span>
                <input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Filter by title, type, practice area…" />
              </div>
              <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
                <span className="grid-meta">{filteredDocs.length} of {docs.length}</span>
                <button className="btn ghost sm" onClick={loadDocs}>↻ Refresh</button>
              </div>
            </div>

            {docsLoading && <div className="placeholder">Loading documents…</div>}
            {docsError && <ErrorState message={docsError} onRetry={loadDocs} />}
            {!docsLoading && !docsError && docs.length === 0 && (
              <div className="empty" style={{ height: "auto", padding: "60px 20px" }}>
                <div className="glyph">⊞</div>
                <h2>The library is empty</h2>
                <p>Ingest a document by pasting text or uploading a PDF — every document becomes searchable and attachable to matters.</p>
                <div style={{ display: "flex", gap: 10 }}>
                  <button className="btn primary" onClick={() => setMode("upload")}>⬆ Upload a file</button>
                  <button className="btn" onClick={() => setMode("ingest")}>Paste text</button>
                </div>
              </div>
            )}
            {!docsLoading && !docsError && filteredDocs.length > 0 && (
              <div className="grid-wrap">
                <div className="grid-scroll">
                  <table className="grid">
                    <thead>
                      <tr>
                        <th>Title</th><th>Type</th><th>Practice area</th><th>Jurisdiction</th><th>Client</th><th>Ingested</th>
                      </tr>
                    </thead>
                    <tbody>
                      {filteredDocs.map((d) => (
                        <tr key={d.id}>
                          <td>
                            <div style={{ fontWeight: 500, color: "var(--text)" }}>{d.title}</div>
                            <div className="grid-meta" style={{ marginTop: 3 }}>{d.id}</div>
                          </td>
                          <td>{d.documentType ? <span className="doc-type">{d.documentType}</span> : "—"}</td>
                          <td>{d.practiceArea ? <span className="pill sm blue">{d.practiceArea}</span> : "—"}</td>
                          <td style={{ whiteSpace: "nowrap" }}>{d.jurisdiction || "—"}</td>
                          <td>{d.detectedClientNumber ? <span className="pill sm gold">{d.detectedClientNumber}</span> : "—"}</td>
                          <td style={{ whiteSpace: "nowrap", color: "var(--text-dim)" }}>{d.ingestedAt ? timeAgo(d.ingestedAt) : "—"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            )}
          </div>
        )}

        {mode === "ingest" && (
          <div className="panel-body" style={{ maxWidth: 760 }}>
            <div className="field">
              <label>Title</label>
              <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Master Services Agreement — Acme / Beta" />
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 14, margin: "16px 0" }}>
              <div className="field">
                <label>Jurisdiction</label>
                <input value={jurisdiction} onChange={(e) => setJurisdiction(e.target.value)} placeholder="e.g. England & Wales" />
              </div>
              <div className="field">
                <label>Document type</label>
                <input value={docType} onChange={(e) => setDocType(e.target.value)} placeholder="contract" />
              </div>
              <div className="field">
                <label>Practice area <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(or auto-detect)</span></label>
                <select value={manualPracticeArea} onChange={(e) => setManualPracticeArea(e.target.value)}>
                  <option value="">Auto-detect</option>
                  {PRACTICE_AREAS.map((pa) => <option key={pa} value={pa}>{pa}</option>)}
                </select>
              </div>
            </div>
            <div className="field">
              <label>Content</label>
              <textarea style={{ minHeight: 200 }} value={content} onChange={(e) => setContent(e.target.value)}
                placeholder="Paste the full text of the document…" />
            </div>
            <div style={{ marginTop: 16 }}>
              <button className="btn primary" disabled={busy || title.trim().length < 3 || content.trim().length < 20} onClick={ingest}>
                {busy ? "Ingesting…" : "⊕ Ingest document"}
              </button>
            </div>
            <IngestResultBanner result={lastResult} />
          </div>
        )}

        {mode === "upload" && (
          <div className="panel-body" style={{ maxWidth: 760 }}>
            <input ref={fileRef} type="file" accept=".pdf,.txt,.md,.csv,.json,.log,.rtf" style={{ display: "none" }}
              onChange={(e) => { const f = e.target.files?.[0]; if (f) upload(f); e.target.value = ""; }} />
            <div
              style={{
                border: "2px dashed var(--border)", borderRadius: 12, padding: "64px 32px",
                textAlign: "center", cursor: "pointer", color: "var(--text-dim)", fontSize: 14,
              }}
              onClick={() => fileRef.current?.click()}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => { e.preventDefault(); const f = e.dataTransfer.files?.[0]; if (f) upload(f); }}>
              {uploading ? (
                <span>Processing…</span>
              ) : (
                <>
                  <div style={{ fontSize: 32, marginBottom: 10 }}>⊕</div>
                  <div>Click to select or drag &amp; drop a file</div>
                  <div style={{ fontSize: 12, marginTop: 6, color: "var(--text-faint)" }}>PDF, TXT, MD, CSV, JSON up to 25 MB</div>
                </>
              )}
            </div>
            <IngestResultBanner result={uploadResult} />
          </div>
        )}

        {mode === "search" && (
          <div className="panel-body" style={{ maxWidth: 760 }}>
            <div className="field">
              <label>Semantic query</label>
              <div style={{ display: "flex", gap: 8 }}>
                <input value={query} onChange={(e) => setQuery(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && search()}
                  placeholder="e.g. exclusivity obligations under Article 101" />
                <button className="btn" disabled={searching || !query} onClick={search}>{searching ? "…" : "Search"}</button>
              </div>
            </div>
            <div style={{ display: "flex", flexDirection: "column", gap: 10, marginTop: 16 }}>
              {results.map((r) => (
                <div key={r.document.id} className="grid-wrap" style={{ padding: "12px 14px" }}>
                  <div style={{ display: "flex", justifyContent: "space-between", gap: 10, alignItems: "flex-start" }}>
                    <strong style={{ fontSize: 13.5 }}>{r.document.title}</strong>
                    <div style={{ display: "flex", gap: 6, flexShrink: 0 }}>
                      {r.document.practiceArea && <span className="pill sm blue">{r.document.practiceArea}</span>}
                      <span className="pill blue">{(r.score * 100).toFixed(0)}%</span>
                    </div>
                  </div>
                  <div style={{ color: "var(--text-dim)", fontSize: 12.5, marginTop: 6, lineHeight: 1.5 }}>{r.excerpt}</div>
                  <div className="grid-meta" style={{ marginTop: 6 }}>{r.document.id}</div>
                </div>
              ))}
              {!results.length && !searching && <div className="placeholder" style={{ padding: 24 }}>No results yet.</div>}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="error-state">
      <span className="error-state-mark">✕</span>
      <span style={{ flex: 1, wordBreak: "break-word" }}>{message}</span>
      {onRetry && <button className="btn ghost sm" onClick={onRetry}>Retry</button>}
    </div>
  );
}

function IngestResultBanner({ result }: { result: IngestResult | null }) {
  if (!result) return null;
  return (
    <AnimatePresence>
      <motion.div initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }}
        style={{ background: "var(--panel-2)", borderRadius: 8, padding: "12px 14px", marginTop: 12 }}>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap", marginBottom: result.practiceArea || result.detectedClient || result.suggestedLawyers?.length ? 8 : 0 }}>
          <span style={{ fontSize: 12, color: "var(--text-faint)" }}>Ingested:</span>
          <span style={{ fontFamily: "var(--font-mono)", fontSize: 11.5, color: "var(--green)" }}>{result.id}</span>
        </div>
        {result.practiceArea && (
          <div style={{ display: "flex", alignItems: "center", gap: 6, marginBottom: 6 }}>
            <span style={{ fontSize: 12, color: "var(--text-faint)" }}>Practice area:</span>
            <span className="pill sm blue">{result.practiceArea}</span>
          </div>
        )}
        {result.detectedClient && (
          <div style={{ display: "flex", alignItems: "center", gap: 6, marginBottom: 6 }}>
            <span style={{ fontSize: 12, color: "var(--text-faint)" }}>Detected client:</span>
            <span className="pill sm gold">{result.detectedClient.clientName} ({result.detectedClient.clientNumber})</span>
          </div>
        )}
        {result.suggestedLawyers && result.suggestedLawyers.length > 0 && (
          <div>
            <span style={{ fontSize: 12, color: "var(--text-faint)" }}>Suggested lawyers:</span>
            <div style={{ display: "flex", flexWrap: "wrap", gap: 4, marginTop: 4 }}>
              {result.suggestedLawyers.map((l) => (
                <span key={l.id} className="pill sm">{l.name}</span>
              ))}
            </div>
          </div>
        )}
      </motion.div>
    </AnimatePresence>
  );
}
