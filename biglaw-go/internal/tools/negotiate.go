// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// respond_to_redline: the counter-redline loop. Opposing counsel returns a
// .docx with their tracked changes; every revision is parsed
// (ooxml.ParseRevisions), judged against the four-tier playbook cascade
// (internal/negotiate), and the response document is written next to the
// input as <stem>.response.docx — accepted opposing changes left standing,
// rejected/countered ones answered with NEW tracked changes authored by
// BigLaw that restore or replace the opposing language, each with a
// rationale card.

package tools

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/integrity"
	"github.com/discover-legal/biglaw-go/internal/negotiate"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
)

func (r *Registry) registerNegotiateTools() {
	r.Register(r.respondToRedlineTool())
}

// ─── respond_to_redline ───────────────────────────────────────────────────────

func (r *Registry) respondToRedlineTool() *ToolImpl {
	fail := func(msg string) map[string]interface{} {
		return map[string]interface{}{"ok": false, "error": msg}
	}
	return &ToolImpl{
		Name: "respond_to_redline",
		Schema: providers.ToolParam{
			Name:        "respond_to_redline",
			Description: "Respond to opposing counsel's marked-up .docx. Parses every tracked change, judges each against the firm's four-tier playbook cascade (client > matter > personal > firm), and writes a response document next to the input: accepted opposing changes are left standing; rejected or countered changes get new tracked changes (BigLaw as author) restoring or replacing the opposing language. Returns a per-change decision card with clause type, disposition, and rationale. When the document belongs to a Redtime lineage, each judgment also sees that clause's negotiation history from prior rounds (decision cards gain historyRounds, and escalation when a standoff pushed the judge to the playbook fallback or to lawyer review).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":               map[string]interface{}{"type": "string", "description": "Opposing counsel's marked-up .docx (absolute, or relative to the document output directory)"},
					"author":             map[string]interface{}{"type": "string", "description": "Author name for BigLaw's counter tracked changes (default \"Big Michael\")"},
					"matter_number":      map[string]interface{}{"type": "string", "description": "Matter number for playbook cascade scoping"},
					"client_number":      map[string]interface{}{"type": "string", "description": "Client number for playbook cascade scoping"},
					"owner_id":           map[string]interface{}{"type": "string", "description": "Lawyer profile ID for personal-playbook cascade scoping"},
					"instructions":       map[string]interface{}{"type": "string", "description": "Optional free-text negotiation guidance passed to the judgment step"},
					"prior_version_path": map[string]interface{}{"type": "string", "description": "Optional: the version we last sent (.docx or .txt). Enables unmarked-change detection — differences not accounted for by the opposing document's tracked changes."},
				},
				"required": []string{"path"},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			src, err := r.resolveDocxPath(strInput(input, "path"))
			if err != nil {
				return fail(err.Error()), nil
			}
			if !strings.EqualFold(filepath.Ext(src), ".docx") {
				return fail("only .docx files can be responded to"), nil
			}
			doc, err := ooxml.OpenFile(src)
			if err != nil {
				return fail(fmt.Sprintf("cannot open document: %v", err)), nil
			}
			revs := doc.ParseRevisions()

			// Integrity check — trust-but-verify on the inbound paper. The
			// obfuscation scan always runs; with a prior version supplied,
			// silent (untracked) edits are detected too. Integrity problems
			// never abort the negotiation — they inform it: the findings ride
			// on the result and a one-line warning reaches the judge below.
			obfuscation := integrity.ScanText(doc.Text())
			var unmarked *integrity.UnmarkedReport
			if p := strings.TrimSpace(strInput(input, "prior_version_path")); p != "" {
				sentText, perr := r.readPriorVersion(p)
				if perr != nil {
					return fail(perr.Error()), nil
				}
				rep := integrity.CompareVersions(sentText, doc)
				unmarked = &rep
			}
			clean := integrityClean(obfuscation, unmarked)
			instructions := strInput(input, "instructions")
			if !clean {
				instructions = strings.TrimSpace(instructions +
					"\nINTEGRITY WARNING: " + integritySummary(obfuscation, unmarked) +
					" Treat unmarked or obfuscated language with suspicion.")
			}

			// Playbook cascade — read-only, loaded from the shared store file.
			pbStore := playbook.New(r.cfg.Persistence.PlaybooksFile)
			if r.cfg.Persistence.PlaybooksFile != "" {
				if err := pbStore.Init(); err != nil {
					slog.Warn("respond_to_redline: playbook store unavailable — judging on market standard only", "error", err)
				}
			}

			// Judge memory — when this document belongs to a Redtime lineage
			// (found via the prior version we sent, or the inbound file
			// itself), each change's judge call sees that clause's prior
			// moves and decisions with escalation guidance. Best-effort: no
			// lineage means amnesiac judging, exactly as before.
			history := r.negotiationHistory(src, strInput(input, "prior_version_path"))

			judgeModel := routing.SelectModel(r.cfg, routing.SelectParams{TaskType: routing.TaskDrafting})
			classifyModel := routing.SelectModel(r.cfg, routing.SelectParams{TaskType: routing.TaskExtraction})
			prov, err := r.provReg.Get(judgeModel)
			if err != nil {
				return fail(err.Error()), nil
			}
			engine := negotiate.New(prov, routing.ResolveModelID(judgeModel), routing.ResolveModelID(classifyModel))
			decisions := engine.Decide(revs, pbStore, negotiate.Opts{
				MatterNumber: strInput(input, "matter_number"),
				ClientID:     strInput(input, "client_number"),
				ProfileID:    strInput(input, "owner_id"),
				Instructions: instructions,
				TaskID:       ctx.TaskID,
				History:      history,
			})

			author := strings.TrimSpace(strInput(input, "author"))
			if author == "" {
				author = defaultRedlineAuthor
			}
			rev := ooxml.NewRevisions(author, time.Now().UTC())
			applyCounterEdits(doc, revs, decisions, rev)

			stem := src[:len(src)-len(filepath.Ext(src))]
			outputPath := stem + ".response.docx"
			if err := doc.SaveFile(outputPath); err != nil {
				return fail(fmt.Sprintf("cannot write response document: %v", err)), nil
			}

			counts := map[string]int{"accepted": 0, "rejected": 0, "countered": 0, "review": 0}
			for _, d := range decisions {
				switch d.Disposition {
				case negotiate.DispositionAccept:
					counts["accepted"]++
				case negotiate.DispositionReject:
					counts["rejected"]++
				case negotiate.DispositionCounter:
					counts["countered"]++
				default:
					counts["review"]++
				}
			}
			var unmarkedOut interface{}
			if unmarked != nil {
				unmarkedOut = map[string]interface{}{
					"hunks": unmarked.Hunks,
					"count": unmarked.Count,
				}
			}

			// Redtime — the round accrues into the document lineage: the
			// inbound opposing draft ("theirs", chained onto the version we
			// last sent when prior_version_path identifies it) and the
			// response ("ours") carrying the decision summary. Best-effort:
			// version tracking never fails the negotiation; null means
			// tracking is unavailable.
			lineage := r.recordNegotiationVersions(src, outputPath,
				strInput(input, "prior_version_path"), author, revs, decisions)

			return map[string]interface{}{
				"ok":            true,
				"outputPath":    outputPath,
				"decisions":     decisions,
				"counts":        counts,
				"changesParsed": len(revs),
				"lineage":       lineage,
				"integrity": map[string]interface{}{
					"obfuscation":     obfuscation,
					"unmarkedChanges": unmarkedOut,
					"clean":           clean,
				},
			}, nil
		},
	}
}

// ─── Counter-edit application ─────────────────────────────────────────────────

// applyCounterEdits applies reject/counter decisions to the document as fresh
// tracked changes, last-to-first so earlier visible-text offsets stay valid
// while later spans are rewritten. accept and review leave the opposing
// revision standing. A failed application downgrades that decision to
// "review" (with the error on its rationale card) rather than aborting.
func applyCounterEdits(doc *ooxml.Document, revs []ooxml.Revision, decisions []negotiate.Decision, rev *ooxml.Revisions) {
	order := make([]int, len(revs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return revs[order[a]].VisibleStart > revs[order[b]].VisibleStart
	})
	for _, i := range order {
		d := &decisions[i]
		var err error
		switch d.Disposition {
		case negotiate.DispositionReject:
			err = applyRevert(doc, revs[i], revs[i].DeletedText, rev)
		case negotiate.DispositionCounter:
			err = applyRevert(doc, revs[i], d.CounterText, rev)
		default:
			continue
		}
		if err != nil {
			d.Disposition = negotiate.DispositionReview
			d.Rationale = strings.TrimSpace(d.Rationale +
				" (counter-edit could not be applied — opposing change left standing: " + err.Error() + ")")
		}
	}
}

// applyRevert answers one opposing revision with replacement text: the
// opposing insertion's visible span (if any) is tracked-deleted first and the
// replacement inserted at the join point as a separate step, so BigLaw's
// <w:ins> lands beside — never nested inside — the opposing <w:ins>. For an
// opposing pure deletion the visible span is zero-width and the replacement
// is simply inserted at the deletion point. Rejecting restores the baseline
// (text = the opposing revision's deleted text); countering substitutes the
// firm's language.
func applyRevert(doc *ooxml.Document, rv ooxml.Revision, text string, rev *ooxml.Revisions) error {
	if rv.VisibleEnd > rv.VisibleStart {
		if err := doc.ApplyTracked(rv.VisibleStart, rv.VisibleEnd, "", rev); err != nil {
			return err
		}
	} else if text == "" {
		return fmt.Errorf("revision has no visible span and no replacement text")
	}
	if text == "" {
		return nil
	}
	return doc.ApplyTracked(rv.VisibleStart, rv.VisibleStart, text, rev)
}
