import { useEffect, useRef, useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { api } from "./api";
import type { Client, ClientMatter, ConflictCheckResult, OcgDocument, OcgRule } from "./types";
import { PRACTICE_AREAS } from "./types";

export function ClientsPanel({ notify }: { notify: (m: string) => void }) {
  const [clients, setClients] = useState<Client[]>([]);
  const [loading, setLoading] = useState(true);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [tab, setTab] = useState<"clients" | "new-client" | "new-matter">("clients");
  const [busy, setBusy] = useState(false);
  const [conflict, setConflict] = useState<ConflictCheckResult | null>(null);

  const [nc, setNc] = useState({ name: "", clientNumber: "", adversaries: "", notes: "" });
  const [nm, setNm] = useState({ matterNumber: "", description: "", practiceArea: "" });

  // OCG sub-panel state
  const [ocgOpen, setOcgOpen] = useState(false);
  const [ocgDoc, setOcgDoc] = useState<OcgDocument | null>(null);
  const [ocgTitle, setOcgTitle] = useState("Outside Counsel Guidelines");
  const [ocgText, setOcgText] = useState("");
  const [ocgBusy, setOcgBusy] = useState(false);

  // Voice guide sub-panel state
  const [voiceOpen, setVoiceOpen] = useState(false);
  const [voiceBusy, setVoiceBusy] = useState(false);
  const voiceFileRef = useRef<HTMLInputElement>(null);
  const ocgFileRef = useRef<HTMLInputElement>(null);

  const selected = clients.find((c) => c.id === selectedId) ?? null;

  const load = () => api.listClients().then(setClients).catch((e) => notify((e as Error).message)).finally(() => setLoading(false));
  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // Load OCG doc when switching to a new client
  useEffect(() => {
    setOcgDoc(null);
    setOcgOpen(false);
    setVoiceOpen(false);
    if (!selectedId) return;
    api.getClientOcg(selectedId).then(setOcgDoc).catch(() => setOcgDoc(null));
  }, [selectedId]);

  async function extractOcg() {
    if (!selectedId || !ocgText.trim()) return;
    setOcgBusy(true);
    try {
      const res = await api.ingestClientOcg(selectedId, { title: ocgTitle, text: ocgText });
      setOcgDoc(res.ocg);
      setOcgText("");
      notify(`${res.ruleCount} OCG rules extracted`);
      await load();
    } catch (e) { notify((e as Error).message); } finally { setOcgBusy(false); }
  }

  async function uploadOcgFile(file: File) {
    if (!selectedId) return;
    setOcgBusy(true);
    try {
      const res = await api.uploadClientOcg(selectedId, ocgTitle, file);
      setOcgDoc(res.ocg);
      notify(`${res.ruleCount} OCG rules extracted from file`);
      await load();
    } catch (e) { notify((e as Error).message); } finally { setOcgBusy(false); }
  }

  async function deleteOcg() {
    if (!selectedId || !window.confirm("Delete OCG document for this client?")) return;
    setOcgBusy(true);
    try {
      await api.deleteClientOcg(selectedId);
      setOcgDoc(null);
      notify("OCG document removed");
      await load();
    } catch (e) { notify((e as Error).message); } finally { setOcgBusy(false); }
  }

  async function uploadVoiceFile(file: File) {
    if (!selectedId) return;
    setVoiceBusy(true);
    try {
      const res = await api.importClientVoice(selectedId, file);
      notify(`Voice guide generated from ${res.samplesAnalysed} samples`);
      await load();
    } catch (e) { notify((e as Error).message); } finally { setVoiceBusy(false); }
  }

  async function deleteVoiceGuide() {
    if (!selectedId || !window.confirm("Delete voice guide for this client?")) return;
    setVoiceBusy(true);
    try {
      await api.deleteClientVoice(selectedId);
      notify("Voice guide removed");
      await load();
    } catch (e) { notify((e as Error).message); } finally { setVoiceBusy(false); }
  }

  async function checkConflict() {
    if (!nc.name.trim()) return;
    try {
      const res = await api.checkConflict(nc.name.trim());
      setConflict(res);
    } catch { /* ignore */ }
  }

  async function addClient() {
    if (!nc.name.trim() || !nc.clientNumber.trim()) { notify("Name and client number required"); return; }
    setBusy(true);
    try {
      const adversaries = nc.adversaries.split(",").map((s) => s.trim()).filter(Boolean);
      const res = await api.createClient({ name: nc.name, clientNumber: nc.clientNumber, adversaries, notes: nc.notes || undefined });
      if (res.conflict?.hasConflict) {
        notify(`⚠ Conflict detected: ${res.conflict.conflictingClientName} lists "${res.conflict.matchedAdversary}" as an adverse party`);
      } else {
        notify("Client added");
      }
      setNc({ name: "", clientNumber: "", adversaries: "", notes: "" });
      setConflict(null);
      await load();
      setSelectedId(res.id);
      setTab("clients");
    } catch (e) { notify((e as Error).message); } finally { setBusy(false); }
  }

  async function removeClient(id: string) {
    if (!window.confirm("Delete this client and all their matters?")) return;
    try { await api.deleteClient(id); await load(); setSelectedId(null); notify("Client removed"); }
    catch (e) { notify((e as Error).message); }
  }

  async function addMatter() {
    if (!selectedId || !nm.matterNumber.trim() || !nm.description.trim()) { notify("Matter number and description required"); return; }
    setBusy(true);
    try {
      await api.addMatter(selectedId, { matterNumber: nm.matterNumber, description: nm.description, practiceArea: nm.practiceArea || undefined });
      setNm({ matterNumber: "", description: "", practiceArea: "" });
      await load();
      setTab("clients");
      notify("Matter added");
    } catch (e) { notify((e as Error).message); } finally { setBusy(false); }
  }

  async function removeMatter(clientId: string, matterNumber: string) {
    if (!window.confirm("Remove this matter?")) return;
    try { await api.removeMatter(clientId, matterNumber); await load(); notify("Matter removed"); }
    catch (e) { notify((e as Error).message); }
  }

  return (
    <div className="page-scroll">
      <div className="page" style={{ maxWidth: 960 }}>
        <div className="page-head">
          <h1 className="page-title">Clients &amp; matters</h1>
          <p className="page-sub">Manage client roster, matters, conflicts of interest, OCG rules, and voice guides.</p>
        </div>

        <div className="tabs">
          <button className={`tab ${tab === "clients" ? "active" : ""}`} onClick={() => setTab("clients")}>
            Clients {tab === "clients" && <motion.span layoutId="cp-ul" className="tab-underline" />}
          </button>
          <button className={`tab ${tab === "new-client" ? "active" : ""}`} onClick={() => setTab("new-client")}>
            Add client {tab === "new-client" && <motion.span layoutId="cp-ul" className="tab-underline" />}
          </button>
          {selected && (
            <button className={`tab ${tab === "new-matter" ? "active" : ""}`} onClick={() => setTab("new-matter")}>
              Add matter {tab === "new-matter" && <motion.span layoutId="cp-ul" className="tab-underline" />}
            </button>
          )}
        </div>

        <div className="panel-body">
          <AnimatePresence mode="wait">
            {tab === "clients" && (
              <motion.div key="clients" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
                {loading && <div className="placeholder">Loading…</div>}
                {!loading && !clients.length && (
                  <div className="empty" style={{ height: "auto", padding: "60px 20px" }}>
                    <div className="glyph">☷</div>
                    <h2>No clients yet</h2>
                    <p>Add your first client to start tracking matters, conflicts, and outside-counsel guidelines.</p>
                    <button className="btn primary" onClick={() => setTab("new-client")}>＋ Add client</button>
                  </div>
                )}
                <div style={{ display: clients.length ? "flex" : "none", gap: 16, height: "min(560px, calc(100vh - 340px))", minHeight: 360 }}>
                  {/* Client list */}
                  <div style={{ width: 240, flexShrink: 0, display: "flex", flexDirection: "column", gap: 6, overflowY: "auto" }}>
                    {clients.map((c) => (
                      <button key={c.id}
                        className={`task-card ${c.id === selectedId ? "active" : ""}`}
                        style={{ textAlign: "left" }}
                        onClick={() => setSelectedId(c.id)}>
                        <div style={{ fontWeight: 600, fontSize: 13 }}>{c.name}</div>
                        <div style={{ color: "var(--text-faint)", fontSize: 11.5 }}>{c.clientNumber} · {c.matters.length} matter{c.matters.length !== 1 ? "s" : ""}</div>
                      </button>
                    ))}
                  </div>

                  {/* Client detail */}
                  <div style={{ flex: 1, overflowY: "auto", paddingLeft: 8 }}>
                    {!selected ? (
                      <div className="placeholder" style={{ paddingTop: 48 }}>Select a client to view details.</div>
                    ) : (
                      <div>
                        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", marginBottom: 16 }}>
                          <div>
                            <div style={{ fontWeight: 700, fontSize: 16 }}>{selected.name}</div>
                            <div style={{ color: "var(--text-dim)", fontSize: 12.5 }}>Client #{selected.clientNumber}</div>
                            {selected.notes && <div style={{ color: "var(--text-dim)", fontSize: 12.5, marginTop: 4 }}>{selected.notes}</div>}
                          </div>
                          <div style={{ display: "flex", gap: 8 }}>
                            <button className="btn primary sm" onClick={() => setTab("new-matter")}>＋ Matter</button>
                            <button className="btn reject sm" onClick={() => removeClient(selected.id)}>✕</button>
                          </div>
                        </div>

                        {selected.adversaries.length > 0 && (
                          <div style={{ marginBottom: 14 }}>
                            <div style={{ fontSize: 11, fontWeight: 600, color: "var(--amber)", textTransform: "uppercase", letterSpacing: "0.08em", marginBottom: 4 }}>Adverse parties</div>
                            <div style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
                              {selected.adversaries.map((a) => (
                                <span key={a} className="pill" style={{ background: "rgba(218,106,96,0.15)", color: "var(--red)" }}>{a}</span>
                              ))}
                            </div>
                          </div>
                        )}

                        <div style={{ fontSize: 11, fontWeight: 600, color: "var(--text-faint)", textTransform: "uppercase", letterSpacing: "0.08em", marginBottom: 8 }}>
                          Matters · {selected.matters.length}
                        </div>
                        {selected.matters.length === 0 && (
                          <div className="placeholder">No matters yet.</div>
                        )}
                        {selected.matters.map((m) => (
                          <MatterRow key={m.matterNumber} matter={m} onRemove={() => removeMatter(selected.id, m.matterNumber)} />
                        ))}

                        {/* ── Client guidelines ── */}
                        <div style={{ marginTop: 18 }}>
                          <div style={{ fontSize: 11, fontWeight: 600, color: "var(--text-faint)", textTransform: "uppercase", letterSpacing: "0.08em", marginBottom: 8 }}>
                            Client guidelines
                          </div>
                          <div style={{ display: "flex", gap: 8, marginBottom: 10 }}>
                            <button className="btn ghost sm" onClick={() => { setOcgOpen((o) => !o); setVoiceOpen(false); }}>
                              {ocgDoc ? `⚖ OCG · ${ocgDoc.rules.length} rules` : "⚖ OCG rules"}
                            </button>
                            <button className="btn ghost sm" onClick={() => { setVoiceOpen((o) => !o); setOcgOpen(false); }}>
                              {selected.voiceGuide ? "✦ Voice guide ✓" : "✦ Voice guide"}
                            </button>
                          </div>

                          {/* OCG sub-panel */}
                          <AnimatePresence>
                            {ocgOpen && (
                              <motion.div key="ocg-panel" initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }}
                                style={{ overflow: "hidden", border: "1px solid var(--border)", borderRadius: 10, padding: "14px 16px", marginBottom: 10 }}>
                                <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 10 }}>Outside Counsel Guidelines</div>

                                {ocgDoc ? (
                                  <div>
                                    <div style={{ color: "var(--text-dim)", fontSize: 12.5, marginBottom: 8 }}>{ocgDoc.title} · {ocgDoc.rules.length} rules extracted</div>
                                    {ocgDoc.rules.slice(0, 5).map((r: OcgRule) => (
                                      <div key={r.id} style={{ display: "flex", gap: 6, alignItems: "flex-start", marginBottom: 5 }}>
                                        <span className={`pill sm ${r.severity === "hard" ? "red" : "gold"}`} style={{ fontSize: 10, marginTop: 1 }}>{r.severity.toUpperCase()}</span>
                                        <span className="pill sm blue" style={{ fontSize: 10, marginTop: 1 }}>{r.category.replace(/_/g, " ")}</span>
                                        <span style={{ fontSize: 12.5, color: "var(--text-dim)", flex: 1 }}>{r.text}</span>
                                      </div>
                                    ))}
                                    {ocgDoc.rules.length > 5 && <div style={{ fontSize: 12, color: "var(--text-faint)", marginTop: 4 }}>…and {ocgDoc.rules.length - 5} more rules</div>}
                                    <button className="btn reject sm" style={{ marginTop: 10 }} disabled={ocgBusy} onClick={deleteOcg}>Delete OCG</button>
                                  </div>
                                ) : null}

                                <div style={{ marginTop: ocgDoc ? 14 : 0 }}>
                                  <div style={{ fontSize: 12, fontWeight: 600, color: "var(--text-faint)", marginBottom: 6 }}>
                                    {ocgDoc ? "Re-import OCG" : "Import OCG document"}
                                  </div>
                                  <div className="field" style={{ marginBottom: 8 }}>
                                    <label>Title</label>
                                    <input value={ocgTitle} onChange={(e) => setOcgTitle(e.target.value)} placeholder="Outside Counsel Guidelines" />
                                  </div>
                                  <div className="field" style={{ marginBottom: 8 }}>
                                    <label>OCG text <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(paste billing rules)</span></label>
                                    <textarea style={{ minHeight: 72, fontSize: 12 }} value={ocgText} onChange={(e) => setOcgText(e.target.value)} placeholder="Paste OCG billing provisions here…" />
                                  </div>
                                  <div style={{ display: "flex", gap: 8 }}>
                                    <button className="btn primary sm" disabled={ocgBusy || !ocgText.trim()} onClick={extractOcg}>
                                      {ocgBusy ? "Extracting…" : "Extract rules"}
                                    </button>
                                    <span style={{ color: "var(--text-faint)", fontSize: 12, alignSelf: "center" }}>or</span>
                                    <button className="btn ghost sm" disabled={ocgBusy} onClick={() => ocgFileRef.current?.click()}>
                                      Upload file
                                    </button>
                                    <input ref={ocgFileRef} type="file" accept=".pdf,.txt,.docx,.doc" style={{ display: "none" }}
                                      onChange={(e) => { const f = e.target.files?.[0]; if (f) uploadOcgFile(f); e.target.value = ""; }} />
                                  </div>
                                </div>
                              </motion.div>
                            )}
                          </AnimatePresence>

                          {/* Voice guide sub-panel */}
                          <AnimatePresence>
                            {voiceOpen && (
                              <motion.div key="voice-panel" initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }}
                                style={{ overflow: "hidden", border: "1px solid var(--border)", borderRadius: 10, padding: "14px 16px" }}>
                                <div style={{ fontWeight: 600, fontSize: 13, marginBottom: 10 }}>Client voice guide</div>

                                {selected.voiceGuide ? (
                                  <div>
                                    <div style={{ display: "flex", flexDirection: "column", gap: 6, marginBottom: 12 }}>
                                      <div style={{ fontSize: 12.5 }}><span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Formality: </span>{selected.voiceGuide.preferredFormality}</div>
                                      <div style={{ fontSize: 12.5 }}><span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Style: </span>{selected.voiceGuide.communicationStyle}</div>
                                      <div style={{ fontSize: 12.5 }}><span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Terminology: </span>{selected.voiceGuide.terminologyPreferences}</div>
                                      <div style={{ fontSize: 12.5 }}><span style={{ color: "var(--text-faint)", fontWeight: 600 }}>Reporting focus: </span>{selected.voiceGuide.reportingPreferences}</div>
                                    </div>
                                    <button className="btn reject sm" disabled={voiceBusy} onClick={deleteVoiceGuide}>Delete voice guide</button>
                                  </div>
                                ) : (
                                  <div style={{ color: "var(--text-faint)", fontSize: 12.5, marginBottom: 10 }}>
                                    No voice guide yet. Upload client emails, briefs, or other writing samples to generate one.
                                  </div>
                                )}

                                <div style={{ marginTop: selected.voiceGuide ? 14 : 0 }}>
                                  <div style={{ fontSize: 12, fontWeight: 600, color: "var(--text-faint)", marginBottom: 8 }}>
                                    {selected.voiceGuide ? "Re-generate from new samples" : "Upload writing samples"}
                                  </div>
                                  <button className="btn primary sm" disabled={voiceBusy} onClick={() => voiceFileRef.current?.click()}>
                                    {voiceBusy ? "Analysing…" : "Upload samples (PDF/TXT/DOCX)"}
                                  </button>
                                  <input ref={voiceFileRef} type="file" accept=".pdf,.txt,.docx,.doc" style={{ display: "none" }}
                                    onChange={(e) => { const f = e.target.files?.[0]; if (f) uploadVoiceFile(f); e.target.value = ""; }} />
                                </div>
                              </motion.div>
                            )}
                          </AnimatePresence>
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              </motion.div>
            )}

            {tab === "new-client" && (
              <motion.div key="new-client" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
                <div className="field">
                  <label>Client name</label>
                  <input value={nc.name} onChange={(e) => setNc({ ...nc, name: e.target.value })}
                    onBlur={checkConflict} placeholder="Acme Corporation" />
                </div>
                <AnimatePresence>
                  {conflict?.hasConflict && (
                    <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }}
                      style={{ background: "rgba(218,106,96,0.12)", border: "1px solid rgba(218,106,96,0.4)", borderRadius: 8, padding: "10px 14px", marginBottom: 12, color: "var(--red)", fontSize: 13 }}>
                      ⚠ Potential conflict of interest — <strong>{conflict.conflictingClientName}</strong> lists "<em>{conflict.matchedAdversary}</em>" as an adverse party.
                    </motion.div>
                  )}
                </AnimatePresence>
                <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 14 }}>
                  <div className="field">
                    <label>Client number</label>
                    <input value={nc.clientNumber} onChange={(e) => setNc({ ...nc, clientNumber: e.target.value })} placeholder="C-001" />
                  </div>
                </div>
                <div className="field">
                  <label>Adverse parties <span style={{ fontWeight: 400, color: "var(--text-faint)" }}>(comma-separated, for conflict checks)</span></label>
                  <input value={nc.adversaries} onChange={(e) => setNc({ ...nc, adversaries: e.target.value })} placeholder="Beta Inc, Gamma Ltd" />
                </div>
                <div className="field">
                  <label>Notes</label>
                  <textarea style={{ minHeight: 72 }} value={nc.notes} onChange={(e) => setNc({ ...nc, notes: e.target.value })} placeholder="Optional internal notes…" />
                </div>
              </motion.div>
            )}

            {tab === "new-matter" && selected && (
              <motion.div key="new-matter" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
                <div style={{ color: "var(--text-dim)", fontSize: 13, marginBottom: 16 }}>
                  Adding matter to <strong>{selected.name}</strong>
                </div>
                <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 14 }}>
                  <div className="field">
                    <label>Matter number</label>
                    <input value={nm.matterNumber} onChange={(e) => setNm({ ...nm, matterNumber: e.target.value })} placeholder="M-2026-001" />
                  </div>
                  <div className="field">
                    <label>Practice area</label>
                    <select value={nm.practiceArea} onChange={(e) => setNm({ ...nm, practiceArea: e.target.value })}>
                      <option value="">— Select —</option>
                      {PRACTICE_AREAS.map((pa) => <option key={pa} value={pa}>{pa}</option>)}
                    </select>
                  </div>
                </div>
                <div className="field">
                  <label>Description</label>
                  <textarea style={{ minHeight: 80 }} value={nm.description} onChange={(e) => setNm({ ...nm, description: e.target.value })} placeholder="Brief description of the matter…" />
                </div>
              </motion.div>
            )}
          </AnimatePresence>

          {tab === "new-client" && (
            <div style={{ marginTop: 18, display: "flex", gap: 10 }}>
              <button className="btn primary" disabled={busy || !nc.name.trim() || !nc.clientNumber.trim()} onClick={addClient}>
                {busy ? "Adding…" : "＋ Add client"}
              </button>
              <button className="btn ghost" onClick={() => setTab("clients")}>Cancel</button>
            </div>
          )}
          {tab === "new-matter" && selected && (
            <div style={{ marginTop: 18, display: "flex", gap: 10 }}>
              <button className="btn primary" disabled={busy || !nm.matterNumber.trim() || !nm.description.trim()} onClick={addMatter}>
                {busy ? "Adding…" : "＋ Add matter"}
              </button>
              <button className="btn ghost" onClick={() => setTab("clients")}>Cancel</button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function MatterRow({ matter, onRemove }: { matter: ClientMatter; onRemove: () => void }) {
  return (
    <div className="lawyer-row" style={{ marginBottom: 8 }}>
      <div style={{ flex: 1 }}>
        <div style={{ fontWeight: 600, fontSize: 13 }}>{matter.matterNumber}</div>
        <div style={{ color: "var(--text-dim)", fontSize: 12.5 }}>{matter.description}</div>
      </div>
      {matter.practiceArea && <span className="pill sm blue">{matter.practiceArea}</span>}
      <button className="btn reject sm" onClick={onRemove} title="Remove matter">✕</button>
    </div>
  );
}
