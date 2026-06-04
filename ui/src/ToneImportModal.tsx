// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

import { useRef, useState, useCallback, type DragEvent, type ChangeEvent } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { api } from "./api";
import type { LawyerProfile, ToneProfile } from "./types";

// ─── Source type config ────────────────────────────────────────────────────────

const SOURCES = [
  {
    id: "linkedin",
    label: "LinkedIn",
    desc: "Posts & Articles export",
    dropHint: "Drop your LinkedIn ZIP, Shares.csv, or Posts and Articles.csv",
    accept: ".zip,.csv",
    icon: (
      <svg width="17" height="17" viewBox="0 0 17 17" fill="none">
        <rect x="1" y="1" width="15" height="15" rx="2.5" stroke="currentColor" strokeWidth="1.4"/>
        <rect x="3.5" y="7" width="2" height="6.5" fill="currentColor"/>
        <circle cx="4.5" cy="4.5" r="1.15" fill="currentColor"/>
        <path d="M8.5 13.5V10c0-.9.45-1.8 1.4-1.8s1.4.9 1.4 1.8v3.5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"/>
        <path d="M8.5 7v6.5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"/>
      </svg>
    ),
  },
  {
    id: "document",
    label: "Work documents",
    desc: "Briefs, memos, opinions",
    dropHint: "Drop a Word (.docx) or PDF you've written",
    accept: ".docx,.pdf",
    icon: (
      <svg width="17" height="17" viewBox="0 0 17 17" fill="none">
        <path d="M3.5 2h7l3.5 3.5V15H3.5V2z" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round"/>
        <path d="M10.5 2v3.5H14" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round"/>
        <line x1="6" y1="8.5" x2="11" y2="8.5" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round"/>
        <line x1="6" y1="11" x2="11" y2="11" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round"/>
        <line x1="6" y1="13.5" x2="9" y2="13.5" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round"/>
      </svg>
    ),
  },
  {
    id: "samples",
    label: "Writing samples",
    desc: "CSV, plain text, or Markdown",
    dropHint: "Drop a .csv, .txt, or .md with your writing",
    accept: ".csv,.txt,.md,.text",
    icon: (
      <svg width="17" height="17" viewBox="0 0 17 17" fill="none">
        <rect x="1.5" y="1.5" width="14" height="14" rx="2" stroke="currentColor" strokeWidth="1.4"/>
        <line x1="1.5" y1="6" x2="15.5" y2="6" stroke="currentColor" strokeWidth="1.2"/>
        <line x1="6.5" y1="1.5" x2="6.5" y2="15.5" stroke="currentColor" strokeWidth="1.2"/>
        <line x1="1.5" y1="10.5" x2="15.5" y2="10.5" stroke="currentColor" strokeWidth="1.2"/>
      </svg>
    ),
  },
] as const;

type SourceId = (typeof SOURCES)[number]["id"];

// ─── Decorative waveform ───────────────────────────────────────────────────────
// Derives bar heights from injectionSnippet — each lawyer's wave is unique.

function profileWave(snippet: string, count = 13): number[] {
  return Array.from({ length: count }, (_, i) => {
    const code = snippet.charCodeAt(Math.floor((i * snippet.length) / count)) || 72;
    return 4 + ((code * 31 + i * 17) % 18); // 4–21 px
  });
}

function WaveDisplay({ profile, size = 48 }: { profile: ToneProfile; size?: number }) {
  const bars = profileWave(profile.injectionSnippet);
  const maxH = Math.max(...bars);
  return (
    <svg width={bars.length * 5 - 1} height={size} style={{ display: "block" }}>
      {bars.map((h, i) => {
        const scaledH = (h / maxH) * size;
        return (
          <rect
            key={i}
            x={i * 5}
            y={(size - scaledH) / 2}
            width={3}
            height={scaledH}
            rx={1.5}
            fill="var(--gold)"
            opacity={0.55 + (h / maxH) * 0.45}
          />
        );
      })}
    </svg>
  );
}

// ─── Trait dots ────────────────────────────────────────────────────────────────

function TraitRow({ label, value, score, max = 3 }: { label: string; value: string; score: number; max?: number }) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "6px 0", borderBottom: "1px solid var(--border)" }}>
      <span style={{ flex: 1, fontSize: 11, color: "var(--text-dim)", fontFamily: "var(--font-mono)", letterSpacing: "0.04em", textTransform: "uppercase" }}>{label}</span>
      <span style={{ display: "flex", gap: 4, alignItems: "center" }}>
        {Array.from({ length: max }, (_, i) => (
          <span key={i} style={{
            width: 8, height: 8, borderRadius: "50%", display: "inline-block",
            background: i < score ? "var(--gold)" : "transparent",
            border: i < score ? "none" : "1.5px solid var(--border-2)",
            transition: "background 0.2s, border-color 0.2s",
          }} />
        ))}
      </span>
      <span style={{ fontSize: 12, color: "var(--text)", minWidth: 110, textAlign: "right" }}>{value}</span>
    </div>
  );
}

function ProfileDisplay({ profile, onReimport, onClear, loading }: {
  profile: ToneProfile; onReimport: () => void; onClear: () => void; loading: boolean;
}) {
  const FORMALITY = { formal: 3, "semi-formal": 2, conversational: 1 } as const;
  const SENTENCE  = { "long-complex": 3, mixed: 2, "short-punchy": 1 } as const;
  const VOCAB     = { "technical-heavy": 3, balanced: 2, "plain-language": 1 } as const;
  const RHETORIC  = { assertive: 4, analytical: 3, collaborative: 2, hedging: 1 } as const;

  const ago = (() => {
    const ms = Date.now() - new Date(profile.generatedAt).getTime();
    const days = Math.floor(ms / 86_400_000);
    if (days === 0) return "today";
    if (days === 1) return "yesterday";
    if (days < 30) return `${days}d ago`;
    return `${Math.floor(days / 30)}mo ago`;
  })();

  return (
    <motion.div key="profile" initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -6 }} transition={{ duration: 0.22 }}>
      {/* Header wave */}
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 18 }}>
        <WaveDisplay profile={profile} size={40} />
        <div style={{ textAlign: "right" }}>
          <div style={{ fontSize: 10, fontFamily: "var(--font-mono)", color: "var(--text-faint)", textTransform: "uppercase", letterSpacing: "0.08em" }}>
            {profile.sourceType === "linkedin_export" ? "LinkedIn" : "Writing samples"} · {profile.sampleCount} samples · {ago}
          </div>
          <div style={{ fontSize: 11, color: "var(--green)", marginTop: 2, fontFamily: "var(--font-mono)" }}>● Voice active</div>
        </div>
      </div>

      {/* Traits */}
      <div style={{ marginBottom: 18 }}>
        <TraitRow label="Formality"  value={profile.formality}      score={FORMALITY[profile.formality] ?? 2} />
        <TraitRow label="Sentences"  value={profile.sentenceStyle}  score={SENTENCE[profile.sentenceStyle] ?? 2} />
        <TraitRow label="Vocabulary" value={profile.vocabulary}     score={VOCAB[profile.vocabulary] ?? 2} />
        <TraitRow label="Rhetoric"   value={profile.rhetoricalStyle} score={RHETORIC[profile.rhetoricalStyle] ?? 2} max={4} />
      </div>

      {/* Signature patterns */}
      {profile.signaturePatterns.length > 0 && (
        <div style={{ marginBottom: 16 }}>
          <div style={{ fontSize: 10, fontFamily: "var(--font-mono)", letterSpacing: "0.1em", textTransform: "uppercase", color: "var(--text-faint)", marginBottom: 8 }}>Signature patterns</div>
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            {profile.signaturePatterns.map((p, i) => (
              <div key={i} style={{
                fontSize: 12, color: "var(--text-dim)", lineHeight: 1.5,
                borderLeft: "2px solid var(--border-gold)", paddingLeft: 10,
                fontStyle: "italic",
              }}>{p}</div>
            ))}
          </div>
        </div>
      )}

      {/* Actions */}
      <div style={{ display: "flex", gap: 8, marginTop: 6 }}>
        <button className="btn ghost sm" onClick={onReimport} disabled={loading}>Re-import</button>
        <button className="btn reject sm" onClick={onClear} disabled={loading}>
          {loading ? <span className="spinner" style={{ width: 11, height: 11 }} /> : "Clear voice"}
        </button>
      </div>
    </motion.div>
  );
}

// ─── Drop zone ─────────────────────────────────────────────────────────────────

function DropZone({ accept, hint, file, onFile }: {
  accept: string; hint: string; file: File | null; onFile: (f: File) => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);

  const handleDrop = useCallback((e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    const f = e.dataTransfer.files[0];
    if (f) onFile(f);
  }, [onFile]);

  const handleChange = useCallback((e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    if (f) onFile(f);
  }, [onFile]);

  const ext = (f: File) => f.name.split(".").pop()?.toUpperCase() ?? "FILE";

  return (
    <div
      className={`tone-drop${dragging ? " over" : ""}${file ? " has-file" : ""}`}
      onDragOver={(e) => { e.preventDefault(); setDragging(true); }}
      onDragLeave={() => setDragging(false)}
      onDrop={handleDrop}
      onClick={() => !file && inputRef.current?.click()}
    >
      <input ref={inputRef} type="file" accept={accept} style={{ display: "none" }} onChange={handleChange} />

      <AnimatePresence mode="wait">
        {file ? (
          <motion.div key="file" initial={{ opacity: 0, scale: 0.95 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: 0.95 }} transition={{ duration: 0.15 }}
            style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 8 }}>
            <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
              <span style={{
                fontFamily: "var(--font-mono)", fontSize: 10, fontWeight: 700, letterSpacing: "0.06em",
                color: "var(--gold)", background: "var(--gold-soft)", border: "1px solid var(--border-gold)",
                borderRadius: 5, padding: "3px 7px",
              }}>{ext(file)}</span>
              <span style={{ fontSize: 13, color: "var(--text)", maxWidth: 240, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{file.name}</span>
            </div>
            <button className="btn ghost sm" style={{ fontSize: 11 }}
              onClick={(e) => { e.stopPropagation(); onFile(null as unknown as File); inputRef.current && (inputRef.current.value = ""); }}>
              Change file
            </button>
          </motion.div>
        ) : (
          <motion.div key="empty" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.12 }}
            style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 10, cursor: "pointer" }}>
            <svg width="32" height="32" viewBox="0 0 32 32" fill="none" style={{ opacity: 0.4 }}>
              <path d="M16 22V10M16 10l-5 5M16 10l5 5" stroke="var(--text)" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"/>
              <path d="M8 24h16" stroke="var(--text)" strokeWidth="1.6" strokeLinecap="round" opacity="0.5"/>
            </svg>
            <span style={{ fontSize: 13, color: "var(--text-dim)", textAlign: "center", lineHeight: 1.5 }}>{hint}</span>
            <span style={{ fontSize: 11, color: "var(--text-faint)", fontFamily: "var(--font-mono)" }}>or click to browse</span>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
}

// ─── Analysing state ───────────────────────────────────────────────────────────

function AnalysingState({ name }: { name: string }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 20, padding: "30px 0" }}>
      <div style={{ display: "flex", alignItems: "flex-end", gap: 4, height: 36 }}>
        {[0, 1, 2, 3, 4, 5, 6, 7].map((i) => (
          <div key={i} className="tone-bar" style={{ animationDelay: `${i * 0.1}s` }} />
        ))}
      </div>
      <div style={{ textAlign: "center" }}>
        <div style={{ fontFamily: "var(--font-display)", fontSize: 17, color: "var(--text)", marginBottom: 4 }}>Reading {name}'s voice…</div>
        <div style={{ fontSize: 12, color: "var(--text-faint)" }}>Chunked analysis in progress — this takes a moment.</div>
      </div>
    </div>
  );
}

// ─── Main modal ────────────────────────────────────────────────────────────────

export function ToneImportModal({ profile, onClose, onUpdated, notify }: {
  profile: LawyerProfile;
  onClose: () => void;
  onUpdated: (p: LawyerProfile) => void;
  notify: (m: string) => void;
}) {
  const [view, setView] = useState<"profile" | "import">(profile.toneProfile ? "profile" : "import");
  const [source, setSource] = useState<SourceId>("linkedin");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [clearBusy, setClearBusy] = useState(false);

  const currentSource = SOURCES.find((s) => s.id === source)!;

  async function handleImport() {
    if (!file) return;
    setBusy(true);
    try {
      const result = await api.toneImport(profile.id, file);
      onUpdated({ ...profile, toneProfile: result.toneProfile });
      notify(`Voice profile built — ${result.samplesAnalysed} samples analysed`);
      setView("profile");
    } catch (e) {
      notify((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function handleClear() {
    if (!window.confirm(`Clear ${profile.name}'s voice profile?`)) return;
    setClearBusy(true);
    try {
      const updated = await api.clearTone(profile.id);
      onUpdated(updated);
      notify("Voice profile cleared");
      setView("import");
    } catch (e) {
      notify((e as Error).message);
    } finally {
      setClearBusy(false);
    }
  }

  // Sync view when profile is externally updated
  const activeTone = profile.toneProfile;

  return (
    <div className="modal-scrim" style={{ zIndex: 60 }} onClick={onClose}>
      <motion.div
        className="modal"
        style={{ maxWidth: 520 }}
        onClick={(e) => e.stopPropagation()}
        initial={{ opacity: 0, y: 20, scale: 0.97 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        exit={{ opacity: 0, y: 12, scale: 0.98 }}
        transition={{ type: "spring", stiffness: 340, damping: 30 }}
      >
        {/* ── Head ── */}
        <div className="modal-head" style={{ paddingBottom: 18 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 11 }}>
            <div style={{
              width: 36, height: 36, borderRadius: "var(--r-sm)",
              background: "var(--gold-soft)", border: "1px solid var(--border-gold)",
              display: "flex", alignItems: "center", justifyContent: "center",
              color: "var(--gold)", flexShrink: 0,
            }}>
              <svg width="18" height="18" viewBox="0 0 18 18" fill="none">
                {[2,4,6,3,8,3,6,4,2].map((h, i) => (
                  <rect key={i} x={1 + i * 1.8} y={(18 - h * 1.5) / 2} width={1.2} height={h * 1.5} rx={0.6} fill="currentColor" opacity={0.6 + (i === 4 ? 0.4 : 0)} />
                ))}
              </svg>
            </div>
            <div>
              <h3 style={{ fontSize: 18 }}>Voice Fingerprint</h3>
              <p style={{ marginTop: 1 }}>{profile.name}</p>
            </div>
          </div>
        </div>

        {/* ── Body ── */}
        <div className="modal-body" style={{ gap: 16 }}>
          <AnimatePresence mode="wait">
            {busy ? (
              <AnalysingState key="busy" name={profile.name.split(" ")[0]} />
            ) : view === "profile" && activeTone ? (
              <ProfileDisplay
                key="profile"
                profile={activeTone}
                onReimport={() => { setFile(null); setView("import"); }}
                onClear={handleClear}
                loading={clearBusy}
              />
            ) : (
              <motion.div key="import" initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -6 }} transition={{ duration: 0.2 }}>
                <p style={{ fontSize: 13, color: "var(--text-dim)", lineHeight: 1.6, marginBottom: 16 }}>
                  Upload writing samples to fingerprint {profile.name.split(" ")[0]}'s style.
                  The voice profile is injected into all drafting agents and final synthesis.
                </p>

                {/* Source selector */}
                <div className="wf-grid" style={{ marginBottom: 14 }}>
                  {SOURCES.map((s) => (
                    <div key={s.id} className={`wf-chip ${source === s.id ? "sel" : ""}`}
                      onClick={() => { setSource(s.id); setFile(null); }}>
                      <div style={{ display: "flex", justifyContent: "center", marginBottom: 5, opacity: source === s.id ? 1 : 0.5, transition: "opacity 0.15s" }}>{s.icon}</div>
                      <div className="wf-name">{s.label}</div>
                      <div className="wf-desc">{s.desc}</div>
                    </div>
                  ))}
                </div>

                {/* Drop zone */}
                <DropZone
                  key={source}
                  accept={currentSource.accept}
                  hint={currentSource.dropHint}
                  file={file}
                  onFile={setFile}
                />

                {source === "linkedin" && !file && (
                  <p style={{ fontSize: 11, color: "var(--text-faint)", marginTop: 8, lineHeight: 1.6 }}>
                    Get your export:{" "}
                    <a href="https://www.linkedin.com/mypreferences/d/download-my-data" target="_blank" rel="noreferrer"
                      style={{ color: "var(--gold)", textDecoration: "none" }} onClick={(e) => e.stopPropagation()}>
                      LinkedIn → Settings → Data privacy → Get a copy of your data
                    </a>
                    {" "}→ select Posts &amp; Articles.
                  </p>
                )}
              </motion.div>
            )}
          </AnimatePresence>
        </div>

        {/* ── Foot ── */}
        {!busy && view === "import" && (
          <div className="modal-foot">
            {activeTone && (
              <button className="btn ghost" style={{ marginRight: "auto" }}
                onClick={() => setView("profile")}>
                ← Back to profile
              </button>
            )}
            <button className="btn ghost" onClick={onClose}>Cancel</button>
            <button className="btn primary" disabled={!file} onClick={handleImport}>
              Analyse {source === "linkedin" ? "posts" : source === "document" ? "document" : "samples"}
            </button>
          </div>
        )}
        {!busy && view === "profile" && (
          <div className="modal-foot">
            <button className="btn ghost" onClick={onClose}>Done</button>
          </div>
        )}
      </motion.div>
    </div>
  );
}
