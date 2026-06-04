import { useEffect, useState } from "react";
import { motion } from "framer-motion";
import { api } from "./api";
import type { AppSettings, LawyerProfile, UserMode } from "./types";
import { PRACTICE_AREAS, MODE_LABEL } from "./types";
import { CostDashboard } from "./CostDashboard";
import { ToneImportModal } from "./ToneImportModal";

export function AdminPanel({ onClose, notify, isPartner, profiles, onProfilesChange, me }: {
  onClose: () => void; notify: (m: string) => void;
  isPartner: boolean; profiles: LawyerProfile[]; onProfilesChange: () => void;
  me?: { profileId: string } | null;
}) {
  const [s, setS] = useState<AppSettings | null>(null);
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState<"users" | "settings" | "cost">(isPartner ? "users" : "settings");
  const [np, setNp] = useState({ name: "", email: "", role: "lawyer", title: "", practiceAreas: [] as string[], bio: "" });
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editPatch, setEditPatch] = useState<Partial<LawyerProfile>>({});
  const [toneModalProfile, setToneModalProfile] = useState<LawyerProfile | null>(null);

  useEffect(() => { api.getSettings().then(setS).catch((e) => notify((e as Error).message)); }, [notify]);

  function patch<K extends keyof AppSettings>(section: K, key: keyof AppSettings[K], value: unknown) {
    setS((prev) => prev && ({ ...prev, [section]: { ...prev[section], [key]: value } }));
  }

  async function save() {
    if (!s) return;
    setBusy(true);
    try {
      const next = await api.updateSettings({
        presentation: s.presentation,
        dytopo: s.dytopo,
        debate: s.debate,
        docuseal: { enabled: s.docuseal.enabled, url: s.docuseal.url, ...(apiKey ? { apiKey } : {}) },
      });
      setS(next); setApiKey("");
      notify("Settings saved — applied live");
    } catch (e) { notify((e as Error).message); } finally { setBusy(false); }
  }

  async function addLawyer() {
    if (!np.name.trim() || !np.email.trim()) { notify("Name and email required"); return; }
    setBusy(true);
    try {
      await api.createProfile({ name: np.name, email: np.email, role: np.role, title: np.title || undefined, practiceAreas: np.practiceAreas, bio: np.bio || undefined });
      setNp({ name: "", email: "", role: "lawyer", title: "", practiceAreas: [], bio: "" });
      onProfilesChange();
      notify("User added");
    } catch (e) { notify((e as Error).message); } finally { setBusy(false); }
  }

  async function removeLawyer(id: string) {
    if (!window.confirm("Remove this user?")) return;
    try { await api.deleteProfile(id); onProfilesChange(); notify("User removed"); }
    catch (e) { notify((e as Error).message); }
  }

  function startEdit(p: LawyerProfile) {
    setEditingId(p.id);
    setEditPatch({ name: p.name, title: p.title ?? "", role: p.role, practiceAreas: [...(p.practiceAreas ?? [])], bio: p.bio ?? "", mode: p.mode });
  }

  async function saveEdit(id: string) {
    setBusy(true);
    try {
      await api.updateProfile(id, editPatch);
      setEditingId(null);
      onProfilesChange();
      notify("Profile updated");
    } catch (e) { notify((e as Error).message); } finally { setBusy(false); }
  }

  function togglePA(pa: string, current: string[] | undefined, setter: (v: string[]) => void) {
    const list = current ?? [];
    setter(list.includes(pa) ? list.filter((x) => x !== pa) : [...list, pa]);
  }

  const canEditProfile = (p: LawyerProfile) => isPartner || me?.profileId === p.id;

  return (
    <>
    <div className="modal-scrim" onClick={onClose}>
      <motion.div className="modal admin" onClick={(e) => e.stopPropagation()}
        initial={{ opacity: 0, y: 18, scale: 0.98 }} animate={{ opacity: 1, y: 0, scale: 1 }}
        transition={{ type: "spring", stiffness: 320, damping: 28 }}>
        <div className="modal-head">
          <h3>Admin</h3>
          <p>Manage users, practice areas, and system settings.</p>
        </div>

        <div className="tabs" style={{ margin: "0 26px" }}>
          <button className={`tab ${tab === "users" ? "active" : ""}`} onClick={() => setTab("users")}>
            Users {tab === "users" && <motion.span layoutId="adm-ul" className="tab-underline" />}
          </button>
          <button className={`tab ${tab === "settings" ? "active" : ""}`} onClick={() => setTab("settings")}>
            Settings {tab === "settings" && <motion.span layoutId="adm-ul" className="tab-underline" />}
          </button>
          {isPartner && (
            <button className={`tab ${tab === "cost" ? "active" : ""}`} onClick={() => setTab("cost")}>
              Cost {tab === "cost" && <motion.span layoutId="adm-ul" className="tab-underline" />}
            </button>
          )}
        </div>

        {tab === "users" && (
          <div className="modal-body">
            <div className="admin-section">
              <div className="admin-section-title">Users &amp; roles</div>
              <div className="lawyer-list">
                {profiles.map((p) => (
                  <div key={p.id}>
                    {editingId === p.id ? (
                      <div style={{ background: "var(--surface-2)", borderRadius: 8, padding: "12px 14px", marginBottom: 8 }}>
                        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10, marginBottom: 10 }}>
                          <div className="field" style={{ margin: 0 }}>
                            <label>Name</label>
                            <input value={editPatch.name ?? ""} onChange={(e) => setEditPatch({ ...editPatch, name: e.target.value })} />
                          </div>
                          <div className="field" style={{ margin: 0 }}>
                            <label>Title</label>
                            <input value={editPatch.title ?? ""} onChange={(e) => setEditPatch({ ...editPatch, title: e.target.value })} />
                          </div>
                        </div>
                        {isPartner && (
                          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10, marginBottom: 10 }}>
                            <div className="field" style={{ margin: 0 }}>
                              <label>Role</label>
                              <select value={editPatch.role ?? "lawyer"} onChange={(e) => setEditPatch({ ...editPatch, role: e.target.value as "lawyer" | "partner" })}>
                                <option value="lawyer">Lawyer</option>
                                <option value="partner">Partner</option>
                              </select>
                            </div>
                            <div className="field" style={{ margin: 0 }}>
                              <label>UX mode</label>
                              <select value={editPatch.mode ?? "full_flavour"} onChange={(e) => setEditPatch({ ...editPatch, mode: e.target.value as UserMode })}>
                                <option value="full_flavour">{MODE_LABEL.full_flavour}</option>
                                <option value="lite">{MODE_LABEL.lite}</option>
                              </select>
                            </div>
                          </div>
                        )}
                        <div className="field" style={{ margin: "0 0 10px" }}>
                          <label>Bio</label>
                          <textarea style={{ minHeight: 56 }} value={editPatch.bio ?? ""} onChange={(e) => setEditPatch({ ...editPatch, bio: e.target.value })} placeholder="Short bio…" />
                        </div>
                        <div className="field" style={{ margin: "0 0 10px" }}>
                          <label>Practice areas</label>
                          <div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginTop: 4 }}>
                            {PRACTICE_AREAS.map((pa) => {
                              const active = editPatch.practiceAreas?.includes(pa);
                              return (
                                <button key={pa} type="button"
                                  className={`pill sm ${active ? "blue" : ""}`}
                                  style={{ cursor: "pointer", opacity: active ? 1 : 0.55 }}
                                  onClick={() => togglePA(pa, editPatch.practiceAreas, (v) => setEditPatch({ ...editPatch, practiceAreas: v }))}>
                                  {pa}
                                </button>
                              );
                            })}
                          </div>
                        </div>
                        <div style={{ display: "flex", gap: 8 }}>
                          <button className="btn primary sm" disabled={busy} onClick={() => saveEdit(p.id)}>Save</button>
                          <button className="btn ghost sm" onClick={() => setEditingId(null)}>Cancel</button>
                        </div>
                      </div>
                    ) : (
                      <div className="lawyer-row">
                        <span className="avatar sm" style={{ background: p.color ?? "var(--gold-soft)" }}>
                          {p.name.split(/\s+/).slice(0, 2).map((w) => w[0]?.toUpperCase()).join("")}
                        </span>
                        <div style={{ flex: 1, minWidth: 0 }}>
                          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
                            <span className="lawyer-name">{p.name}</span>
                            <span className={`pill sm ${p.role === "partner" ? "gold" : ""}`}>{p.role}</span>
                            {p.mode && (
                              <span className="mode-chip" data-mode={p.mode}>{MODE_LABEL[p.mode]}</span>
                            )}
                          </div>
                          <div className="lawyer-email">{p.email}{p.title && ` · ${p.title}`}</div>
                          {p.practiceAreas && p.practiceAreas.length > 0 && (
                            <div style={{ display: "flex", flexWrap: "wrap", gap: 4, marginTop: 4 }}>
                              {p.practiceAreas.map((pa) => <span key={pa} className="pill sm blue">{pa}</span>)}
                            </div>
                          )}
                        </div>
                        {canEditProfile(p) && (
                          <button
                            className={`voice-btn${p.toneProfile ? " active" : ""}`}
                            onClick={() => setToneModalProfile(p)}
                            title={p.toneProfile ? "Voice fingerprint active — click to manage" : "Add voice fingerprint"}
                          >
                            <svg width="13" height="10" viewBox="0 0 13 10" fill="none">
                              {[3, 6, 9, 5, 10, 5, 9, 6, 3].map((h, i) => (
                                <rect key={i} x={i * 1.4} y={(10 - h) / 2} width={1} height={h} rx={0.5} fill="currentColor" />
                              ))}
                            </svg>
                            {p.toneProfile ? "Voice ●" : "Voice"}
                          </button>
                        )}
                        {canEditProfile(p) && (
                          <button className="btn ghost sm" onClick={() => startEdit(p)}>Edit</button>
                        )}
                        {isPartner && (
                          <button className="btn reject sm" onClick={() => removeLawyer(p.id)} title="Remove">✕</button>
                        )}
                      </div>
                    )}
                  </div>
                ))}
              </div>

              {isPartner && (
                <div style={{ marginTop: 16 }}>
                  <div className="admin-section-title" style={{ marginBottom: 10 }}>Add user</div>
                  <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10, marginBottom: 10 }}>
                    <div className="field" style={{ margin: 0 }}>
                      <label>Full name</label>
                      <input placeholder="Jane Smith" value={np.name} onChange={(e) => setNp({ ...np, name: e.target.value })} />
                    </div>
                    <div className="field" style={{ margin: 0 }}>
                      <label>Email</label>
                      <input placeholder="jane@firm.com" value={np.email} onChange={(e) => setNp({ ...np, email: e.target.value })} />
                    </div>
                    <div className="field" style={{ margin: 0 }}>
                      <label>Title</label>
                      <input placeholder="Senior Associate" value={np.title} onChange={(e) => setNp({ ...np, title: e.target.value })} />
                    </div>
                    <div className="field" style={{ margin: 0 }}>
                      <label>Role</label>
                      <select value={np.role} onChange={(e) => setNp({ ...np, role: e.target.value })}>
                        <option value="lawyer">Lawyer</option>
                        <option value="partner">Partner</option>
                      </select>
                    </div>
                  </div>
                  <div className="field" style={{ marginBottom: 10 }}>
                    <label>Practice areas</label>
                    <div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginTop: 4 }}>
                      {PRACTICE_AREAS.map((pa) => {
                        const active = np.practiceAreas.includes(pa);
                        return (
                          <button key={pa} type="button"
                            className={`pill sm ${active ? "blue" : ""}`}
                            style={{ cursor: "pointer", opacity: active ? 1 : 0.55 }}
                            onClick={() => togglePA(pa, np.practiceAreas, (v) => setNp({ ...np, practiceAreas: v }))}>
                            {pa}
                          </button>
                        );
                      })}
                    </div>
                  </div>
                  <button className="btn primary sm" disabled={busy} onClick={addLawyer}>＋ Add user</button>
                </div>
              )}
            </div>
          </div>
        )}

        {tab === "settings" && !s && (
          <div className="modal-body"><div className="placeholder">Loading settings…</div></div>
        )}
        {tab === "settings" && s && (
          <div className="modal-body">
            {/* ── Practice mode ───────────────────────────────────────────── */}
            <div className="admin-section">
              <div className="admin-section-title">Presentation</div>
              <div className="field">
                <label>Audience mode</label>
                <div className="wf-grid two">
                  <div className={`wf-chip ${s.presentation.mode === "lawyer" ? "sel" : ""}`} onClick={() => patch("presentation", "mode", "lawyer")}>
                    <div className="wf-name">Lawyer</div>
                    <div className="wf-desc">Full legal terminology &amp; citations</div>
                  </div>
                  <div className={`wf-chip ${s.presentation.mode === "plain" ? "sel" : ""}`} onClick={() => patch("presentation", "mode", "plain")}>
                    <div className="wf-name">Non-lawyer</div>
                    <div className="wf-desc">Plain-language framing</div>
                  </div>
                </div>
              </div>
              <div className="field">
                <label>Firm / organisation name</label>
                <input value={s.presentation.firmName} onChange={(e) => patch("presentation", "firmName", e.target.value)} placeholder="Shown in the header — optional" />
              </div>
            </div>

            {/* ── Orchestration (DyTopo) ──────────────────────────────────── */}
            <div className="admin-section">
              <div className="admin-section-title">Orchestration · DyTopo</div>
              <div className="admin-grid">
                <NumField label="Round depth (max rounds)" value={s.dytopo.maxRounds} min={1} max={30} onChange={(v) => patch("dytopo", "maxRounds", v)} />
                <NumField label="Max agents / round" value={s.dytopo.maxAgentsPerRound} min={1} max={48} onChange={(v) => patch("dytopo", "maxAgentsPerRound", v)} />
                <NumField label="Need/Offer match threshold" value={s.dytopo.similarityThreshold} min={0.1} max={0.99} step={0.01} onChange={(v) => patch("dytopo", "similarityThreshold", v)} />
              </div>
            </div>

            {/* ── Debate & verification ───────────────────────────────────── */}
            <div className="admin-section">
              <div className="admin-section-title">Debate &amp; verification</div>
              <div className="admin-grid">
                <NumField label="Verification passes" value={s.debate.verificationPasses} min={0} max={25} onChange={(v) => patch("debate", "verificationPasses", v)} />
                <NumField label="Human-gate confidence" value={s.debate.gateConfidenceThreshold} min={0} max={1} step={0.01} onChange={(v) => patch("debate", "gateConfidenceThreshold", v)} />
              </div>
              <label className="check"><input type="checkbox" checked={s.debate.adversarialEnabled} onChange={(e) => patch("debate", "adversarialEnabled", e.target.checked)} /> Adversarial challenge enabled</label>
              <label className="check"><input type="checkbox" checked={s.debate.citationRequired} onChange={(e) => patch("debate", "citationRequired", e.target.checked)} /> Require citations (CitationGate)</label>
            </div>

            {/* ── DocuSeal ────────────────────────────────────────────────── */}
            <div className="admin-section">
              <div className="admin-section-title">DocuSeal · e-signature</div>
              <label className="check"><input type="checkbox" checked={s.docuseal.enabled} onChange={(e) => patch("docuseal", "enabled", e.target.checked)} /> Enable e-signature tools</label>
              <div className="field">
                <label>DocuSeal URL</label>
                <input value={s.docuseal.url} onChange={(e) => patch("docuseal", "url", e.target.value)} placeholder="http://localhost:3000" />
              </div>
              <div className="field">
                <label>API key {s.docuseal.apiKeySet && <span className="key-set">● configured</span>}</label>
                <input type="password" value={apiKey} onChange={(e) => setApiKey(e.target.value)}
                  placeholder={s.docuseal.apiKeySet ? "•••••••• — leave blank to keep" : "X-Auth-Token"} />
              </div>
            </div>
          </div>
        )}

        {tab === "cost" && isPartner && (
          <div className="modal-body">
            <CostDashboard notify={notify} />
          </div>
        )}

        <div className="modal-foot">
          <button className="btn ghost" onClick={onClose}>Close</button>
          {tab === "settings" && (
            <button className="btn primary" disabled={busy || !s} onClick={save}>{busy ? "Saving…" : "Save settings"}</button>
          )}
        </div>
      </motion.div>
    </div>
    {toneModalProfile && (
      <ToneImportModal
        profile={toneModalProfile}
        onClose={() => setToneModalProfile(null)}
        onUpdated={(updated) => {
          setToneModalProfile(updated);
          onProfilesChange();
        }}
        notify={notify}
      />
    )}
    </>
  );
}

function NumField({ label, value, min, max, step, onChange }: {
  label: string; value: number; min: number; max: number; step?: number; onChange: (v: number) => void;
}) {
  return (
    <div className="field">
      <label>{label}</label>
      <input type="number" value={value} min={min} max={max} step={step ?? 1}
        onChange={(e) => onChange(step ? parseFloat(e.target.value) : parseInt(e.target.value))} />
    </div>
  );
}
