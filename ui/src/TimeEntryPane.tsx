import { useCallback, useEffect, useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { api } from "./api";
import type { TimeEntry, OcgSuggestion } from "./types";
import { timeAgo } from "./primitives";

type ViewTab = "pending" | "all";

function fmtDuration(billingUnits: number, durationMs: number): string {
  if (billingUnits > 0) return `${(billingUnits * 0.1).toFixed(1)}h`;
  const h = durationMs / 3_600_000;
  return h > 0 ? `${h.toFixed(2)}h` : "—";
}

function SeverityBadge({ severity }: { severity: "hard" | "soft" }) {
  return severity === "hard"
    ? <span className="pill sm red" style={{ fontSize: 10, padding: "1px 6px" }}>HARD</span>
    : <span className="pill sm gold" style={{ fontSize: 10, padding: "1px 6px" }}>SOFT</span>;
}

function CategoryChip({ category }: { category: string }) {
  const label = category.replace(/_/g, " ");
  return <span className="pill sm blue" style={{ fontSize: 10, textTransform: "capitalize" }}>{label}</span>;
}

interface EntryItemProps {
  entry: TimeEntry;
  selected: boolean;
  onClick: () => void;
}

function EntryItem({ entry, selected, onClick }: EntryItemProps) {
  const pending = entry.ocgSuggestions?.filter((s) => s.status === "pending").length ?? 0;
  const allReviewed = (entry.ocgSuggestions?.length ?? 0) > 0 && pending === 0;

  return (
    <motion.button
      className={`task-card ${selected ? "active" : ""}`}
      style={{ textAlign: "left", position: "relative" }}
      onClick={(e) => { e.stopPropagation(); onClick(); }}
      layout
      initial={{ opacity: 0, x: -8 }}
      animate={{ opacity: 1, x: 0 }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 6, marginBottom: 2 }}>
        {pending > 0 && <span className="pill sm gold" style={{ fontSize: 10 }}>{pending} issue{pending !== 1 ? "s" : ""}</span>}
        {allReviewed && <span className="dot complete" style={{ width: 7, height: 7, flexShrink: 0 }} />}
        <span style={{ fontWeight: 600, fontSize: 12.5, flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {entry.description.slice(0, 60)}{entry.description.length > 60 ? "…" : ""}
        </span>
      </div>
      <div style={{ color: "var(--text-faint)", fontSize: 11.5, display: "flex", gap: 8 }}>
        <span>{entry.startedAt ? new Date(entry.startedAt).toLocaleDateString() : "—"}</span>
        <span>{entry.profileName}</span>
        <span>{fmtDuration(entry.billingUnits, entry.durationMs)}</span>
      </div>
      {(entry.clientNumber || entry.matterNumber) && (
        <div style={{ color: "var(--text-faint)", fontSize: 11, marginTop: 2 }}>
          {entry.clientNumber && <span>Client {entry.clientNumber}</span>}
          {entry.clientNumber && entry.matterNumber && " · "}
          {entry.matterNumber && <span>{entry.matterNumber}</span>}
        </div>
      )}
    </motion.button>
  );
}

interface SuggestionCardProps {
  suggestion: OcgSuggestion;
  onAccept: () => void;
  onDismiss: () => void;
  busy: boolean;
}

function SuggestionCard({ suggestion, onAccept, onDismiss, busy }: SuggestionCardProps) {
  if (suggestion.status === "accepted") {
    return (
      <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "10px 12px", background: "rgba(96,200,96,0.07)", borderRadius: 8, marginBottom: 8, opacity: 0.7 }}>
        <span style={{ color: "var(--green)", fontSize: 14 }}>✓</span>
        <span style={{ fontSize: 12.5, color: "var(--text-dim)" }}>Accepted — description updated</span>
      </div>
    );
  }
  if (suggestion.status === "dismissed") {
    return (
      <div style={{ display: "flex", alignItems: "center", gap: 8, padding: "10px 12px", background: "rgba(120,120,120,0.07)", borderRadius: 8, marginBottom: 8, opacity: 0.6 }}>
        <span style={{ color: "var(--text-faint)", fontSize: 14 }}>✕</span>
        <span style={{ fontSize: 12.5, color: "var(--text-faint)" }}>Dismissed</span>
      </div>
    );
  }

  return (
    <motion.div
      initial={{ opacity: 0, y: 4 }}
      animate={{ opacity: 1, y: 0 }}
      style={{ border: "1px solid var(--border)", borderRadius: 10, padding: "12px 14px", marginBottom: 10 }}
    >
      <div style={{ display: "flex", gap: 6, alignItems: "center", marginBottom: 8 }}>
        <CategoryChip category={suggestion.category} />
        <SeverityBadge severity={suggestion.severity} />
        <span style={{ fontSize: 12.5, color: "var(--text-dim)", flex: 1 }}>{suggestion.issue}</span>
      </div>
      <div style={{ fontSize: 12, color: "var(--text-faint)", marginBottom: 6 }}>Rule: {suggestion.ruleText}</div>
      <div style={{ background: "rgba(100,200,100,0.08)", border: "1px solid rgba(100,200,100,0.25)", borderRadius: 6, padding: "8px 10px", marginBottom: 10, fontSize: 12.5 }}>
        <div style={{ fontWeight: 600, fontSize: 11, color: "var(--green)", marginBottom: 3 }}>SUGGESTED</div>
        {suggestion.suggestedDescription}
      </div>
      <div style={{ display: "flex", gap: 8 }}>
        <button className="btn primary sm" disabled={busy} onClick={onAccept}>
          ✓ Accept
        </button>
        <button className="btn ghost sm" disabled={busy} onClick={onDismiss}>
          Dismiss
        </button>
      </div>
    </motion.div>
  );
}

export function TimeEntryPane({
  onClose,
  notify,
  isPartner,
}: {
  onClose: () => void;
  notify: (m: string) => void;
  isPartner: boolean;
}) {
  const [tab, setTab] = useState<ViewTab>("pending");
  const [entries, setEntries] = useState<TimeEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [filterClient, setFilterClient] = useState("");
  const [filterMatter, setFilterMatter] = useState("");
  const [checkBusy, setCheckBusy] = useState(false);
  const [suggBusy, setSuggBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      if (tab === "pending") {
        const data = await api.listTimeEntrySuggestions({
          clientNumber: filterClient || undefined,
          matterNumber: filterMatter || undefined,
        });
        setEntries(data);
      } else {
        const data = await api.listTimeEntries({
          clientNumber: filterClient || undefined,
          matterNumber: filterMatter || undefined,
        });
        setEntries(data);
      }
    } catch (e) {
      notify((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [tab, filterClient, filterMatter, notify]);

  useEffect(() => { load(); }, [load]);

  const selected = entries.find((e) => e.id === selectedId) ?? null;

  async function runCheck() {
    if (!isPartner) return;
    setCheckBusy(true);
    try {
      const res = await api.runOcgCheck({
        clientNumber: filterClient || undefined,
        matterNumber: filterMatter || undefined,
        limit: 100,
      });
      notify(`OCG check complete — ${res.checked} entries checked, ${res.withSuggestions} with issues`);
      await load();
    } catch (e) {
      notify((e as Error).message);
    } finally {
      setCheckBusy(false);
    }
  }

  async function runCheckForSelected() {
    if (!isPartner || !selected?.clientNumber) return;
    setCheckBusy(true);
    try {
      const res = await api.runOcgCheck({ clientNumber: selected.clientNumber, limit: 50 });
      notify(`OCG check — ${res.checked} entries checked, ${res.withSuggestions} with issues`);
      await load();
    } catch (e) {
      notify((e as Error).message);
    } finally {
      setCheckBusy(false);
    }
  }

  async function accept(entryId: string, ruleId: string) {
    setSuggBusy(true);
    try {
      const updated = await api.acceptSuggestion(entryId, ruleId);
      setEntries((prev) => prev.map((e) => (e.id === updated.id ? updated : e)));
      if (tab === "pending") {
        // Remove from list if no more pending suggestions
        const stillPending = updated.ocgSuggestions?.some((s) => s.status === "pending");
        if (!stillPending) {
          setEntries((prev) => prev.filter((e) => e.id !== updated.id));
          setSelectedId(null);
        }
      }
      notify("Suggestion accepted — description updated");
    } catch (e) {
      notify((e as Error).message);
    } finally {
      setSuggBusy(false);
    }
  }

  async function dismiss(entryId: string, ruleId: string) {
    setSuggBusy(true);
    try {
      const updated = await api.dismissSuggestion(entryId, ruleId);
      setEntries((prev) => prev.map((e) => (e.id === updated.id ? updated : e)));
      if (tab === "pending") {
        const stillPending = updated.ocgSuggestions?.some((s) => s.status === "pending");
        if (!stillPending) {
          setEntries((prev) => prev.filter((e) => e.id !== updated.id));
          setSelectedId(null);
        }
      }
      notify("Suggestion dismissed");
    } catch (e) {
      notify((e as Error).message);
    } finally {
      setSuggBusy(false);
    }
  }

  return (
    <div className="modal-scrim" onClick={onClose}>
      <motion.div
        className="modal admin"
        style={{ maxWidth: 860 }}
        onClick={(e) => e.stopPropagation()}
        initial={{ opacity: 0, y: 18, scale: 0.98 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        transition={{ type: "spring", stiffness: 320, damping: 28 }}
      >
        <div className="modal-head">
          <h3>Time entries &amp; OCG review</h3>
          <p>Review billing entries for Outside Counsel Guidelines compliance.</p>
        </div>

        <div className="tabs" style={{ margin: "0 26px" }}>
          <button className={`tab ${tab === "pending" ? "active" : ""}`} onClick={() => setTab("pending")}>
            Pending review {tab === "pending" && <motion.span layoutId="tep-ul" className="tab-underline" />}
          </button>
          <button className={`tab ${tab === "all" ? "active" : ""}`} onClick={() => setTab("all")}>
            All entries {tab === "all" && <motion.span layoutId="tep-ul" className="tab-underline" />}
          </button>
        </div>

        <div className="modal-body">
          {/* Filter row */}
          <div style={{ display: "flex", gap: 10, marginBottom: 14, alignItems: "center" }}>
            <input
              style={{ width: 140 }}
              placeholder="Client number"
              value={filterClient}
              onChange={(e) => setFilterClient(e.target.value)}
            />
            <input
              style={{ width: 140 }}
              placeholder="Matter number"
              value={filterMatter}
              onChange={(e) => setFilterMatter(e.target.value)}
            />
            {isPartner && (
              <button className="btn primary sm" disabled={checkBusy} onClick={runCheck}>
                {checkBusy ? "Checking…" : "Run OCG check"}
              </button>
            )}
          </div>

          {/* Split pane */}
          <div style={{ display: "flex", gap: 16, height: 420 }}>
            {/* Entry list */}
            <div style={{ width: 260, flexShrink: 0, overflowY: "auto", display: "flex", flexDirection: "column", gap: 4 }}>
              {loading && <div className="placeholder">Loading…</div>}
              {!loading && !entries.length && (
                <div className="placeholder">
                  {tab === "pending" ? "No entries with pending OCG issues." : "No time entries found."}
                </div>
              )}
              <AnimatePresence>
                {entries.map((e) => (
                  <EntryItem key={e.id} entry={e} selected={e.id === selectedId} onClick={() => setSelectedId(e.id)} />
                ))}
              </AnimatePresence>
            </div>

            {/* Entry detail */}
            <div style={{ flex: 1, overflowY: "auto", paddingLeft: 8 }}>
              {!selected ? (
                <div className="placeholder" style={{ paddingTop: 48 }}>Select an entry to review suggestions.</div>
              ) : (
                <div>
                  <div style={{ marginBottom: 14 }}>
                    <div style={{ fontWeight: 700, fontSize: 15, marginBottom: 4 }}>{selected.description}</div>
                    <div style={{ color: "var(--text-dim)", fontSize: 12.5, display: "flex", gap: 12 }}>
                      <span>{selected.startedAt ? new Date(selected.startedAt).toLocaleDateString() : "—"}</span>
                      <span>{selected.profileName}</span>
                      <span>{fmtDuration(selected.billingUnits, selected.durationMs)}</span>
                      {selected.matterNumber && <span>Matter: {selected.matterNumber}</span>}
                      {selected.clientNumber && <span>Client: {selected.clientNumber}</span>}
                    </div>
                    {selected.ocgCheckedAt && (
                      <div style={{ color: "var(--text-faint)", fontSize: 11.5, marginTop: 4 }}>
                        Last checked: {timeAgo(selected.ocgCheckedAt)}
                      </div>
                    )}
                  </div>

                  <div style={{ fontSize: 11, fontWeight: 600, color: "var(--text-faint)", textTransform: "uppercase", letterSpacing: "0.08em", marginBottom: 10 }}>
                    OCG suggestions · {selected.ocgSuggestions?.length ?? 0}
                  </div>

                  {!selected.ocgSuggestions?.length && (
                    <div className="placeholder">No OCG issues found for this entry.</div>
                  )}

                  <AnimatePresence>
                    {selected.ocgSuggestions?.map((s) => (
                      <SuggestionCard
                        key={s.ruleId}
                        suggestion={s}
                        busy={suggBusy}
                        onAccept={() => accept(selected.id, s.ruleId)}
                        onDismiss={() => dismiss(selected.id, s.ruleId)}
                      />
                    ))}
                  </AnimatePresence>
                </div>
              )}
            </div>
          </div>
        </div>

        <div className="modal-foot">
          <button className="btn ghost" onClick={onClose}>Close</button>
          {isPartner && selected?.clientNumber && (
            <button className="btn primary sm" disabled={checkBusy} onClick={runCheckForSelected}>
              {checkBusy ? "Checking…" : "Run OCG check for this client"}
            </button>
          )}
        </div>
      </motion.div>
    </div>
  );
}
