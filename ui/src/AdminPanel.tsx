import { useCallback, useEffect, useState } from "react";
import { motion } from "framer-motion";
import { api } from "./api";
import type { AppSettings, AuditEntry, Job, JobStatus, LawyerProfile, QueueStats, UserMode } from "./types";
import { PRACTICE_AREAS, MODE_LABEL } from "./types";
import { ToneImportModal } from "./ToneImportModal";
import { ErrorState } from "./Library";
import { timeAgo } from "./primitives";
import { tone, auditSummary } from "./AuditRail";

export function AdminPanel({ notify, isPartner, profiles, onProfilesChange, me }: {
  notify: (m: string) => void;
  isPartner: boolean; profiles: LawyerProfile[]; onProfilesChange: () => void;
  me?: { profileId: string } | null;
}) {
  const [s, setS] = useState<AppSettings | null>(null);
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [tab, setTab] = useState<"users" | "settings" | "jobs" | "audit">(isPartner ? "users" : "settings");
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
        clientVoice: s.clientVoice,
        models: s.models,
        drafting: s.drafting,
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
    <div className="page-scroll">
      <div className="page" style={{ maxWidth: 880 }}>
        <div className="page-head">
          <h1 className="page-title">Admin</h1>
          <p className="page-sub">Manage users, practice areas, system settings, and the background job queue.</p>
        </div>

        <div className="tabs">
          <button className={`tab ${tab === "users" ? "active" : ""}`} onClick={() => setTab("users")}>
            Users {tab === "users" && <motion.span layoutId="adm-ul" className="tab-underline" />}
          </button>
          <button className={`tab ${tab === "settings" ? "active" : ""}`} onClick={() => setTab("settings")}>
            Settings {tab === "settings" && <motion.span layoutId="adm-ul" className="tab-underline" />}
          </button>
          {isPartner && (
            <button className={`tab ${tab === "jobs" ? "active" : ""}`} onClick={() => setTab("jobs")}>
              Jobs {tab === "jobs" && <motion.span layoutId="adm-ul" className="tab-underline" />}
            </button>
          )}
          {isPartner && (
            <button className={`tab ${tab === "audit" ? "active" : ""}`} onClick={() => setTab("audit")}>
              Audit {tab === "audit" && <motion.span layoutId="adm-ul" className="tab-underline" />}
            </button>
          )}
        </div>

        {tab === "users" && (
          <div className="panel-body">
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
          <div className="panel-body"><div className="placeholder">Loading settings…</div></div>
        )}
        {tab === "settings" && s && (
          <div className="panel-body">
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

            {/* ── Remy · client voice ─────────────────────────────────────── */}
            <div className="admin-section">
              <div className="admin-section-title">Remy · client voice</div>
              <label className="check">
                <input type="checkbox" checked={s.clientVoice?.gateNotes ?? true}
                  onChange={(e) => patch("clientVoice", "gateNotes", e.target.checked)} />
                Attach Remy&apos;s client-advocacy notes to review gates
              </label>
              <label className="check">
                <input type="checkbox" checked={s.clientVoice?.matterNotifications ?? true}
                  onChange={(e) => patch("clientVoice", "matterNotifications", e.target.checked)} />
                Fan client-side notifications out to linked Teams/Slack channels
              </label>
              <p style={{ fontSize: 12, color: "var(--text-faint)", marginTop: 6 }}>
                Firm-wide switches. Notifications are always stored and audited — these only control alerts and gate hints.
                Each lawyer can also hide Remy&apos;s notes for themselves from the gate card.
              </p>
            </div>

            {/* ── Drafting ────────────────────────────────────────────────── */}
            <div className="admin-section">
              <div className="admin-section-title">Drafting</div>
              <label className="check">
                <input type="checkbox" checked={s.drafting?.dytopo ?? false}
                  onChange={(e) => patch("drafting", "dytopo", e.target.checked)} />
                Collaborative DyTopo drafting (writing huddles per section)
              </label>
              <p style={{ fontSize: 12, color: "var(--text-faint)", margin: "4px 0 8px" }}>
                On: each section is written by a small huddle (a lead drafter plus contributor
                agents that critique and add grounded specifics), run concurrently, then composed
                by the paged pass. Off: a single drafter per section.
              </p>
              <div className="grid2">
                <NumField label="Agents / section (huddle size)" value={s.drafting?.agentsPerSection ?? 2} min={1} max={5} onChange={(v) => patch("drafting", "agentsPerSection", v)} />
                <NumField label="Huddle rounds (draft → critique → revise)" value={s.drafting?.rounds ?? 2} min={1} max={4} onChange={(v) => patch("drafting", "rounds", v)} />
              </div>
            </div>

            {/* ── Models ──────────────────────────────────────────────────── */}
            <div className="admin-section">
              <div className="admin-section-title">Models</div>
              <div className="field">
                <label>Figure-extraction model</label>
                <select
                  value={s.models?.figureModel ?? ""}
                  onChange={(e) => patch("models", "figureModel", e.target.value)}
                >
                  <option value="">(default — tool model)</option>
                  {(s.models?.available ?? []).map((m) => (
                    <option key={m} value={m}>{m}</option>
                  ))}
                </select>
              </div>
              <p style={{ fontSize: 12, color: "var(--text-faint)", margin: "4px 0 8px" }}>
                A small model at temperature 0 runs the deterministic figure-extraction pass —
                a 7B-class model is plenty and keeps the pipeline efficient.
              </p>
              <div className="field">
                <label>Spine model (BELO conduct/allegation pass)</label>
                <select
                  value={s.models?.spineModel ?? ""}
                  onChange={(e) => patch("models", "spineModel", e.target.value)}
                >
                  <option value="">(default — bulk model)</option>
                  {(s.models?.available ?? []).map((m) => (
                    <option key={m} value={m}>{m}</option>
                  ))}
                </select>
              </div>
              <p style={{ fontSize: 12, color: "var(--text-faint)", margin: "4px 0 8px" }}>
                A few high-leverage calls discover the deliverable's section spine. Worth a
                stronger model (e.g. 14B) even on a small GPU — it's only ~7 calls.
              </p>
              <div className="field">
                <label>Synthesis model (drafting the deliverable)</label>
                <select
                  value={s.models?.synthesisModel ?? ""}
                  onChange={(e) => patch("models", "synthesisModel", e.target.value)}
                >
                  <option value="">(default — routed)</option>
                  {(s.models?.available ?? []).map((m) => (
                    <option key={m} value={m}>{m}</option>
                  ))}
                </select>
              </div>
              <p style={{ fontSize: 12, color: "var(--text-faint)", margin: "4px 0 8px" }}>
                The judged memo. Spending a stronger model here (e.g. 14B) buys the most quality
                per call; the high-volume research/extraction stays on the fast bulk model.
              </p>
              <div className="field">
                <label>Available models (the picker list)</label>
                <div style={{ display: "flex", flexWrap: "wrap", gap: 6, marginBottom: 6 }}>
                  {(s.models?.available ?? []).map((m) => (
                    <span key={m} className="wf-chip" style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                      {m}
                      <button
                        type="button"
                        title="Remove"
                        style={{ background: "none", border: "none", cursor: "pointer", color: "var(--text-faint)" }}
                        onClick={() => patch("models", "available", (s.models?.available ?? []).filter((x) => x !== m))}
                      >×</button>
                    </span>
                  ))}
                </div>
                <input
                  placeholder="Add a model id (e.g. qwen2.5:7b) and press Enter"
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      const v = (e.target as HTMLInputElement).value.trim();
                      const cur = s.models?.available ?? [];
                      if (v && !cur.includes(v)) patch("models", "available", [...cur, v]);
                      (e.target as HTMLInputElement).value = "";
                      e.preventDefault();
                    }
                  }}
                />
              </div>
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

            <div style={{ marginTop: 16 }}>
              <button className="btn primary" disabled={busy} onClick={save}>{busy ? "Saving…" : "Save settings"}</button>
            </div>
          </div>
        )}

        {tab === "jobs" && isPartner && (
          <div className="panel-body">
            <JobsPanel notify={notify} />
          </div>
        )}

        {tab === "audit" && isPartner && (
          <div className="panel-body">
            <AuditBrowserPanel profiles={profiles} />
          </div>
        )}
      </div>
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

// ─── Background job queue ─────────────────────────────────────────────────────

const JOB_STATUSES: JobStatus[] = ["pending", "running", "done", "failed", "dead_letter"];

const JOB_STATUS_PILL: Record<JobStatus, string> = {
  pending: "", running: "gold", done: "green", failed: "red", dead_letter: "red",
};

function JobsPanel({ notify }: { notify: (m: string) => void }) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [stats, setStats] = useState<QueueStats | null>(null);
  const [statusFilter, setStatusFilter] = useState<JobStatus | "">("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [retrying, setRetrying] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    Promise.all([
      api.listJobs({ status: statusFilter || undefined, limit: 100 }),
      api.jobStats(),
    ])
      .then(([j, st]) => { setJobs(j); setStats(st); })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [statusFilter]);

  useEffect(() => { load(); }, [load]);

  async function retry(id: string) {
    setRetrying(id);
    try {
      await api.retryJob(id);
      notify("Job re-queued");
      load();
    } catch (e) { notify((e as Error).message); }
    finally { setRetrying(null); }
  }

  return (
    <div>
      {stats && (
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap", marginBottom: 14 }}>
          {JOB_STATUSES.map((st) => (
            <button key={st} className={`pill ${statusFilter === st ? "gold" : ""}`}
              style={{ cursor: "pointer", background: statusFilter === st ? undefined : "transparent" }}
              onClick={() => setStatusFilter((cur) => (cur === st ? "" : st))}>
              {st.replace("_", " ")} · {stats[st]}
            </button>
          ))}
          <button className="btn ghost sm" onClick={load}>↻ Refresh</button>
        </div>
      )}

      {loading && <div className="placeholder">Loading job queue…</div>}
      {error && <ErrorState message={error} onRetry={load} />}
      {!loading && !error && jobs.length === 0 && (
        <div className="placeholder">No jobs{statusFilter ? ` with status "${statusFilter}"` : " in the queue"}.</div>
      )}
      {!loading && !error && jobs.length > 0 && (
        <div className="grid-wrap">
          <div className="grid-scroll">
            <table className="grid">
              <thead>
                <tr><th>Type</th><th>Status</th><th>Created</th><th>Retries</th><th>Error</th><th></th></tr>
              </thead>
              <tbody>
                {jobs.map((j) => (
                  <tr key={j.id}>
                    <td>
                      <div style={{ fontFamily: "var(--font-mono)", fontSize: 12 }}>{j.type}</div>
                      <div className="grid-meta" style={{ marginTop: 3 }}>{j.id}</div>
                    </td>
                    <td><span className={`pill sm ${JOB_STATUS_PILL[j.status]}`}>{j.status.replace("_", " ")}</span></td>
                    <td style={{ whiteSpace: "nowrap", color: "var(--text-dim)" }}>{timeAgo(j.createdAt)}</td>
                    <td style={{ fontFamily: "var(--font-mono)" }}>{j.retries}/{j.maxRetries}</td>
                    <td style={{ maxWidth: 280, color: "var(--red)", fontSize: 12, wordBreak: "break-word" }}>{j.error ?? "—"}</td>
                    <td>
                      {(j.status === "failed" || j.status === "dead_letter") && (
                        <button className="btn ghost sm" disabled={retrying === j.id} onClick={() => retry(j.id)}>
                          {retrying === j.id ? "…" : "↻ Retry"}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}

// ─── Audit browser ────────────────────────────────────────────────────────────
// Partner-only firm-wide audit log: browsable and filterable. (Each user's
// personal feed lives in the right-hand activity rail.)

function fullTs(iso: string): string {
  const d = new Date(iso);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function AuditBrowserPanel({ profiles }: { profiles: LawyerProfile[] }) {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [event, setEvent] = useState("");
  const [actorId, setActorId] = useState("");
  const [taskId, setTaskId] = useState("");
  const [limit, setLimit] = useState(200);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    api.recentAudit(limit, {
      event: event.trim() || undefined,
      actorId: actorId || undefined,
      taskId: taskId.trim() || undefined,
    })
      .then((list) => setEntries([...list].reverse())) // server is oldest-first
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [event, actorId, taskId, limit]);

  useEffect(() => { load(); }, [load]);

  const profileName = (id: string) =>
    profiles.find((p) => p.id === id)?.name ?? id;

  return (
    <div>
      <div style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "flex-end", marginBottom: 14 }}>
        <div className="field" style={{ minWidth: 180 }}>
          <label>Event prefix</label>
          <input value={event} placeholder="e.g. task. / gate. / settings." onChange={(e) => setEvent(e.target.value)} />
        </div>
        <div className="field" style={{ minWidth: 170 }}>
          <label>Actor</label>
          <select value={actorId} onChange={(e) => setActorId(e.target.value)}>
            <option value="">All actors</option>
            <option value="system">system</option>
            <option value="anonymous">anonymous</option>
            {profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        </div>
        <div className="field" style={{ minWidth: 160 }}>
          <label>Task ID</label>
          <input value={taskId} placeholder="filter by task" onChange={(e) => setTaskId(e.target.value)} />
        </div>
        <div className="field" style={{ width: 90 }}>
          <label>Limit</label>
          <select value={limit} onChange={(e) => setLimit(Number(e.target.value))}>
            {[100, 200, 500, 1000].map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        </div>
        <button className="btn ghost sm" style={{ marginBottom: 6 }} onClick={load}>↻ Refresh</button>
      </div>

      {loading && <div className="placeholder">Loading audit log…</div>}
      {error && <ErrorState message={error} onRetry={load} />}
      {!loading && !error && entries.length === 0 && (
        <div className="placeholder">No audit entries match these filters.</div>
      )}
      {!loading && !error && entries.length > 0 && (
        <div className="grid-wrap">
          <div className="grid-scroll">
            <table className="grid">
              <thead>
                <tr><th>Time</th><th>Event</th><th>Actor</th><th>Task</th><th>Details</th></tr>
              </thead>
              <tbody>
                {entries.map((e) => (
                  <tr key={e.id}>
                    <td style={{ whiteSpace: "nowrap", fontFamily: "var(--font-mono)", fontSize: 12, color: "var(--text-dim)" }}>{fullTs(e.ts)}</td>
                    <td style={{ whiteSpace: "nowrap" }}>
                      <span style={{ color: tone(e.event), fontFamily: "var(--font-mono)", fontSize: 12 }}>{e.event}</span>
                    </td>
                    <td style={{ whiteSpace: "nowrap", fontSize: 12 }}>{e.actorId ? profileName(e.actorId) : "—"}</td>
                    <td style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--text-dim)" }}>{e.taskId ? e.taskId.slice(0, 8) : "—"}</td>
                    <td style={{ fontSize: 12, color: "var(--text-dim)", maxWidth: 380, wordBreak: "break-word" }}>{auditSummary(e) || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
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
