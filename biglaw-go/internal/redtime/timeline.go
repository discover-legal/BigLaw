// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Timeline building — the analysis half of Redtime. For each version of a
// lineage the engine extracts that round's moves (tracked changes attributed
// via revparse when the version carries them; a formatting-insensitive word
// diff attributed to the version's side otherwise), folds in the
// respond_to_redline decision history, groups the moves into clause buckets
// (model classification when a provider is available, positional
// heading-based bucketing otherwise — the timeline always builds with zero
// model calls), and judges each clause's drift from the resolved playbook
// position. The JSON shape is render-ready for the UI.

package redtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/integrity"
	"github.com/discover-legal/biglaw-go/internal/negotiate"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/textdiff"
)

// ─── Timeline JSON shape ──────────────────────────────────────────────────────

// Timeline is the full per-clause negotiation history of one lineage.
type Timeline struct {
	LineageID   string           `json:"lineageId"`
	Rounds      int              `json:"rounds"`
	GeneratedAt string           `json:"generatedAt"`
	Versions    []VersionSummary `json:"versions"`
	Clauses     []ClauseTimeline `json:"clauses"`
}

// VersionSummary is one lineage node without its full text.
type VersionSummary struct {
	ID        string          `json:"id"`
	Round     int             `json:"round"`
	Source    string          `json:"source"`
	Author    string          `json:"author,omitempty"`
	CreatedAt string          `json:"createdAt,omitempty"`
	Path      string          `json:"path,omitempty"`
	Decisions json.RawMessage `json:"decisions,omitempty"`
}

// ClauseTimeline is the ordered event history of one clause bucket.
type ClauseTimeline struct {
	Clause      string        `json:"clause"`
	Events      []ClauseEvent `json:"events"`
	CurrentText string        `json:"currentText,omitempty"`
	Drift       *Drift        `json:"drift,omitempty"`
}

// ClauseEvent is one negotiation move on a clause.
type ClauseEvent struct {
	Round            int    `json:"round"`
	Actor            string `json:"actor"`
	Kind             string `json:"kind"` // insertion | deletion | substitution
	FromText         string `json:"fromText,omitempty"`
	ToText           string `json:"toText,omitempty"`
	ViaTrackedChange bool   `json:"viaTrackedChange"`
	// Decision carries the respond_to_redline disposition when negotiate ran
	// on the round that answered this move (accept | reject | counter | review).
	Decision     string `json:"decision,omitempty"`
	DecisionNote string `json:"decisionNote,omitempty"`
}

// Drift is how far the clause's current language sits from the firm's
// resolved playbook position.
type Drift struct {
	Status       string `json:"status"` // at_position | above | below | unknown
	Note         string `json:"note"`
	PlaybookTier string `json:"playbookTier,omitempty"`
}

// Drift statuses.
const (
	DriftAtPosition = "at_position"
	DriftAbove      = "above"
	DriftBelow      = "below"
	DriftUnknown    = "unknown"
)

// BuildOpts parameterises a timeline build. Everything is optional: with no
// provider the build makes zero model calls (positional clause buckets, drift
// unknown); with no playbook every clause's drift is unknown.
type BuildOpts struct {
	Playbook      *playbook.Store
	Resolve       playbook.ResolveOpts
	Provider      providers.Provider
	JudgeModel    string // resolved model ID for drift judgment
	ClassifyModel string // resolved model ID for clause classification
	TaskID        string // cost attribution
}

// OptsFromConfig assembles BuildOpts from the runtime config: the shared
// playbook store, the drafting/extraction-tier models, and their provider —
// each degrading to absent when unconfigured.
func OptsFromConfig(cfg *config.Config, provReg *providers.Registry, scope playbook.ResolveOpts, taskID string) BuildOpts {
	opts := BuildOpts{Resolve: scope, TaskID: taskID}
	if cfg == nil {
		return opts
	}
	pb := playbook.New(cfg.Persistence.PlaybooksFile)
	if cfg.Persistence.PlaybooksFile != "" {
		if err := pb.Init(); err != nil {
			slog.Warn("redtime: playbook store unavailable — drift will be unknown", "error", err)
		}
	}
	opts.Playbook = pb
	if provReg != nil {
		judge := routing.SelectModel(cfg, routing.SelectParams{TaskType: routing.TaskDrafting})
		classify := routing.SelectModel(cfg, routing.SelectParams{TaskType: routing.TaskExtraction})
		if prov, err := provReg.Get(judge); err == nil {
			opts.Provider = prov
			opts.JudgeModel = routing.ResolveModelID(judge)
			opts.ClassifyModel = routing.ResolveModelID(classify)
		}
	}
	return opts
}

// ─── Build ────────────────────────────────────────────────────────────────────

// eventTextCap bounds the from/to snippets carried on a timeline event so a
// whole-section rewrite doesn't bloat the JSON.
const eventTextCap = 400

// BuildTimeline assembles the per-clause timeline of one lineage.
func BuildTimeline(ctx context.Context, repo store.VersionRepository, lineageID string, opts BuildOpts) (*Timeline, error) {
	if repo == nil {
		return nil, ErrUnavailable
	}
	versions, err := repo.ListLineage(ctx, lineageID)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, ErrNotFound
	}

	tl := &Timeline{
		LineageID:   lineageID,
		Rounds:      versions[len(versions)-1].Round,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	for _, v := range versions {
		s := VersionSummary{ID: v.ID, Round: v.Round, Source: v.Source, Author: v.Author, Path: v.Path}
		if !v.CreatedAt.IsZero() {
			s.CreatedAt = v.CreatedAt.UTC().Format(time.RFC3339)
		}
		if len(v.Decisions) > 0 {
			s.Decisions = json.RawMessage(v.Decisions)
		}
		tl.Versions = append(tl.Versions, s)
	}

	events := collectEvents(versions)

	var engine *negotiate.Engine
	var vocab []string
	if opts.Provider != nil {
		engine = negotiate.New(opts.Provider, opts.JudgeModel, opts.ClassifyModel)
		vocab = negotiate.Vocabulary(opts.Playbook)
	}
	buckets, order := bucketEvents(events, engine, vocab, opts.TaskID)

	final := versions[len(versions)-1].Text
	tl.Clauses = make([]ClauseTimeline, 0, len(order))
	for _, label := range order {
		ct := ClauseTimeline{Clause: label}
		for _, ec := range buckets[label] {
			ct.Events = append(ct.Events, ec.ev)
		}
		ct.CurrentText = currentClauseText(final, buckets[label])
		ct.Drift = judgeDrift(label, ct.CurrentText, opts)
		tl.Clauses = append(tl.Clauses, ct)
	}
	return tl, nil
}

// ─── Event extraction ─────────────────────────────────────────────────────────

// eventCtx is a ClauseEvent plus the anchoring data bucketing needs: the
// surrounding untouched context (for the classifier) and an approximate
// offset into an anchor text (for positional bucketing), plus the untruncated
// replacement text (for locating the clause's current language).
type eventCtx struct {
	ev           ClauseEvent
	ctxBefore    string
	ctxAfter     string
	anchorText   string
	anchorOffset int
	fullTo       string
}

// collectEvents walks the lineage in round order and extracts each version's
// moves. A version carrying tracked changes is attributed via revparse —
// minus the revisions already standing in the previous version, because
// unaccepted markup rides along between rounds and only the NEW revisions are
// that round's moves. A clean version is attributed to its side via a
// formatting-insensitive word diff against the previous version's text, so a
// formatting-only round produces no events.
func collectEvents(versions []store.DocumentVersion) []eventCtx {
	var out []eventCtx
	var prevComponents map[string]bool
	for i := range versions {
		cur := &versions[i]
		revs, baseline := docRevisions(cur.Path)
		switch {
		case len(revs) > 0:
			decisions := decisionsAfter(versions, i)
			for j, rv := range revs {
				if coveredBy(prevComponents, rv) {
					continue // standing markup from an earlier round
				}
				ev := ClauseEvent{
					Round:            cur.Round,
					Actor:            actorName(rv.Author, cur),
					Kind:             string(rv.Kind),
					FromText:         strutil.Truncate(rv.DeletedText, eventTextCap),
					ToText:           strutil.Truncate(rv.InsertedText, eventTextCap),
					ViaTrackedChange: true,
				}
				if d := matchDecision(decisions, j, rv); d != nil {
					ev.Decision = string(d.Disposition)
					ev.DecisionNote = d.Rationale
				}
				out = append(out, eventCtx{
					ev: ev, ctxBefore: rv.ContextBefore, ctxAfter: rv.ContextAfter,
					anchorText: baseline, anchorOffset: rv.BaselineStart,
					fullTo: rv.InsertedText,
				})
			}
		case i > 0:
			out = append(out, diffEvents(&versions[i-1], cur)...)
		}
		prevComponents = revComponents(revs)
	}
	return out
}

// diffEvents attributes a clean version's whole delta to its side: a
// word-level diff of the normalised texts, adjacent delete+insert pairs
// merged into substitutions.
func diffEvents(prev, cur *store.DocumentVersion) []eventCtx {
	a := integrity.NormalizeForCompare(prev.Text)
	b := integrity.NormalizeForCompare(cur.Text)
	hunks := textdiff.Diff(a, b)

	var out []eventCtx
	for k := 0; k < len(hunks); {
		h := hunks[k]
		if h.Kind == textdiff.Equal {
			k++
			continue
		}
		ev := ClauseEvent{Round: cur.Round, Actor: actorName("", cur)}
		ctxBefore, ctxAfter := h.ContextBefore, h.ContextAfter
		anchorOffset := h.TheirOffset
		fullTo := ""
		switch {
		case h.Kind == textdiff.Delete && k+1 < len(hunks) && hunks[k+1].Kind == textdiff.Insert:
			ins := hunks[k+1]
			ev.Kind = string(ooxml.RevSubstitution)
			ev.FromText = strutil.Truncate(h.OurText, eventTextCap)
			ev.ToText = strutil.Truncate(ins.TheirText, eventTextCap)
			ctxAfter = ins.ContextAfter
			anchorOffset = ins.TheirOffset
			fullTo = ins.TheirText
			k += 2
		case h.Kind == textdiff.Delete:
			ev.Kind = string(ooxml.RevDeletion)
			ev.FromText = strutil.Truncate(h.OurText, eventTextCap)
			k++
		default:
			ev.Kind = string(ooxml.RevInsertion)
			ev.ToText = strutil.Truncate(h.TheirText, eventTextCap)
			fullTo = h.TheirText
			k++
		}
		out = append(out, eventCtx{
			ev: ev, ctxBefore: ctxBefore, ctxAfter: ctxAfter,
			anchorText: b, anchorOffset: anchorOffset, fullTo: fullTo,
		})
	}
	return out
}

// docRevisions parses the tracked changes (and baseline text) out of a
// version's on-disk .docx; nil when the file is missing, unreadable, or not a
// .docx — the caller then falls back to the stored-text diff.
func docRevisions(path string) ([]ooxml.Revision, string) {
	if path == "" || !strings.EqualFold(filepath.Ext(path), ".docx") {
		return nil, ""
	}
	doc, err := ooxml.OpenFile(path)
	if err != nil {
		return nil, ""
	}
	return doc.ParseRevisions(), doc.BaselineText()
}

// revComponents indexes a revision list by its author+text components. A
// revision in the NEXT version whose components all appear here is standing
// markup, not a new move — including the case where a substitution's halves
// re-parse separately after the response wraps one half in a counter-edit.
func revComponents(revs []ooxml.Revision) map[string]bool {
	if len(revs) == 0 {
		return nil
	}
	set := make(map[string]bool, 2*len(revs))
	for _, rv := range revs {
		if rv.DeletedText != "" {
			set[rv.Author+"\x1fdel\x1f"+rv.DeletedText] = true
		}
		if rv.InsertedText != "" {
			set[rv.Author+"\x1fins\x1f"+rv.InsertedText] = true
		}
	}
	return set
}

// coveredBy reports whether every component of rv was already present in the
// previous version's revisions.
func coveredBy(prev map[string]bool, rv ooxml.Revision) bool {
	if len(prev) == 0 {
		return false
	}
	any := false
	if rv.DeletedText != "" {
		if !prev[rv.Author+"\x1fdel\x1f"+rv.DeletedText] {
			return false
		}
		any = true
	}
	if rv.InsertedText != "" {
		if !prev[rv.Author+"\x1fins\x1f"+rv.InsertedText] {
			return false
		}
		any = true
	}
	return any
}

// decisionsAfter returns the respond_to_redline decisions recorded on the
// version that ANSWERED versions[i] — decisions describe the parent round's
// tracked changes, so they annotate the parent's events.
func decisionsAfter(versions []store.DocumentVersion, i int) []negotiate.Decision {
	if i+1 >= len(versions) || len(versions[i+1].Decisions) == 0 {
		return nil
	}
	var out []negotiate.Decision
	if err := json.Unmarshal(versions[i+1].Decisions, &out); err != nil {
		return nil
	}
	return out
}

// matchDecision pairs one parsed revision with its decision card: by index
// when the alignment held (decisions are index-aligned with the revision list
// they were made from), by text match otherwise.
func matchDecision(decisions []negotiate.Decision, j int, rv ooxml.Revision) *negotiate.Decision {
	if len(decisions) == 0 {
		return nil
	}
	if j < len(decisions) &&
		decisions[j].DeletedText == rv.DeletedText && decisions[j].InsertedText == rv.InsertedText {
		return &decisions[j]
	}
	for i := range decisions {
		if decisions[i].DeletedText == rv.DeletedText && decisions[i].InsertedText == rv.InsertedText {
			return &decisions[i]
		}
	}
	return nil
}

// actorName resolves who moved: the tracked-change author when present, else
// the version's registered author, else its side.
func actorName(revAuthor string, v *store.DocumentVersion) string {
	if a := strings.TrimSpace(revAuthor); a != "" {
		return a
	}
	if v.Author != "" {
		return v.Author
	}
	if v.Source != "" {
		return v.Source
	}
	return SourceUpload
}

// ─── Clause bucketing ─────────────────────────────────────────────────────────

// bucketEvents groups events into clause buckets: the negotiate engine's
// classifier (with the playbook's clause vocabulary) when a provider is
// available, positional heading-based bucketing otherwise. Identical change
// texts classify once. Returns the buckets plus first-seen label order.
func bucketEvents(events []eventCtx, engine *negotiate.Engine, vocab []string, taskID string) (map[string][]eventCtx, []string) {
	buckets := map[string][]eventCtx{}
	var order []string
	cache := map[string]string{}
	for _, ec := range events {
		label := ""
		if engine != nil {
			key := ec.ev.FromText + "\x1f" + ec.ev.ToText
			if cached, ok := cache[key]; ok {
				label = cached
			} else {
				rv := ooxml.Revision{
					Kind:          ooxml.RevisionKind(ec.ev.Kind),
					DeletedText:   ec.ev.FromText,
					InsertedText:  ec.ev.ToText,
					ContextBefore: ec.ctxBefore,
					ContextAfter:  ec.ctxAfter,
				}
				if l, err := engine.ClassifyChange(rv, vocab, taskID); err == nil {
					label = strings.TrimSpace(l)
				} else {
					slog.Warn("redtime: clause classification failed — falling back to positional bucket", "error", err)
				}
				cache[key] = label
			}
		}
		if label == "" {
			label = positionalBucket(ec.anchorText, ec.anchorOffset)
		}
		if _, ok := buckets[label]; !ok {
			order = append(order, label)
		}
		buckets[label] = append(buckets[label], ec)
	}
	return buckets, order
}

var (
	headingWordRe = regexp.MustCompile(`(?i)^(article|section|schedule|exhibit|clause|part|annex|appendix)\b`)
	// Numbered headings: multi-level numbering ("7.2 Term"), or a single
	// number that carries list punctuation ("3. Indemnity") — a bare year or
	// amount starting a sentence does not qualify.
	numberedHeadingRe = regexp.MustCompile(`^(\d+(\.\d+)+|\d+[.):])\s+\S`)
)

// positionalBucket is the zero-model-call fallback: the nearest heading-like
// paragraph at or above the change, else the paragraph ordinal.
func positionalBucket(text string, offset int) string {
	if text == "" {
		return "General"
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	para, heading := 0, ""
	lineStart := 0
	for lineStart < len(text) && lineStart <= offset {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		line := strings.TrimSpace(text[lineStart:lineEnd])
		if line != "" {
			para++
			if isHeadingLine(line) {
				heading = line
			}
		}
		lineStart = lineEnd + 1
	}
	if heading != "" {
		return strutil.Truncate(heading, 80)
	}
	return fmt.Sprintf("¶%d", para)
}

// isHeadingLine recognises contract headings: short lines that open with a
// structural word, carry outline numbering, or are set in all caps.
func isHeadingLine(line string) bool {
	if line == "" || len(line) > 90 {
		return false
	}
	if headingWordRe.MatchString(line) || numberedHeadingRe.MatchString(line) {
		return true
	}
	hasLetter := false
	for _, r := range line {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsLetter(r) {
			hasLetter = true
		}
	}
	return hasLetter
}

// ─── Current language ─────────────────────────────────────────────────────────

// currentTextCap bounds the per-clause current-language snippet.
const currentTextCap = 500

// currentClauseText locates the clause's language in the final version: the
// paragraph containing the most recent replacement text, falling back to that
// text itself when the paragraph cannot be located (reworded or deleted
// since).
func currentClauseText(final string, events []eventCtx) string {
	normFinal := integrity.NormalizeForCompare(final)
	for i := len(events) - 1; i >= 0; i-- {
		needle := events[i].fullTo
		if needle == "" {
			continue
		}
		if idx := strings.Index(final, needle); idx >= 0 {
			return strutil.Truncate(paragraphAround(final, idx), currentTextCap)
		}
		if idx := strings.Index(normFinal, integrity.NormalizeForCompare(needle)); idx >= 0 {
			return strutil.Truncate(paragraphAround(normFinal, idx), currentTextCap)
		}
	}
	if last := events[len(events)-1].ev; last.ToText != "" {
		return last.ToText
	}
	return ""
}

// paragraphAround expands a byte offset to its surrounding paragraph
// (documents extracted via ooxml carry one paragraph per line).
func paragraphAround(s string, idx int) string {
	start := strings.LastIndexByte(s[:idx], '\n') + 1
	end := strings.IndexByte(s[idx:], '\n')
	if end < 0 {
		end = len(s)
	} else {
		end += idx
	}
	return strings.TrimSpace(s[start:end])
}

// ─── Drift ────────────────────────────────────────────────────────────────────

// judgeDrift compares a clause's current language against the resolved
// playbook position — one model call per clause when a provider is present,
// "unknown" otherwise. It never blocks the timeline: every failure path
// degrades to an unknown status with an explanatory note.
func judgeDrift(clause, currentText string, opts BuildOpts) *Drift {
	if opts.Playbook == nil {
		return &Drift{Status: DriftUnknown, Note: "no playbook available"}
	}
	resolved := opts.Playbook.Resolve(clause, opts.Resolve)
	if resolved == nil {
		return &Drift{Status: DriftUnknown, Note: "no playbook position resolved for this clause"}
	}
	tier := string(resolved.ResolvedFrom)
	if opts.Provider == nil {
		return &Drift{Status: DriftUnknown, PlaybookTier: tier,
			Note: "playbook position found, but drift judgment requires a model"}
	}
	if strings.TrimSpace(currentText) == "" {
		return &Drift{Status: DriftUnknown, PlaybookTier: tier,
			Note: "the clause's current language could not be located in the latest version"}
	}
	status, note, err := driftCall(opts, clause, currentText, resolved)
	if err != nil {
		return &Drift{Status: DriftUnknown, PlaybookTier: tier, Note: "drift judgment failed: " + err.Error()}
	}
	return &Drift{Status: status, Note: note, PlaybookTier: tier}
}

const driftSystemPrompt = `You compare the CURRENT negotiated language of one contract clause against the firm's playbook position and judge the drift.

Decide exactly one status:
- "at_position" — the current language substantively matches the firm's standard (or fallback) position.
- "above"       — the current language is MORE favourable to the firm than its standard position.
- "below"       — the current language falls short of the firm's position: weaker than the fallback, or crossing a red line.

Return a JSON object: {"status":"at_position|above|below","note":"one sentence explaining the judgment"}`

func driftCall(opts BuildOpts, clause, currentText string, resolved *playbook.ResolvedClause) (string, string, error) {
	var b strings.Builder
	b.WriteString("CLAUSE TYPE: " + clause + "\n")
	b.WriteString("CURRENT LANGUAGE:\n" + strutil.Truncate(currentText, 1500) + "\n")
	en := resolved.EffectiveEntry
	b.WriteString(fmt.Sprintf("\nPLAYBOOK POSITION (resolved from the %s tier):\n", resolved.ResolvedFrom))
	b.WriteString("STANDARD POSITION: " + strutil.Truncate(en.StandardPosition, 600) + "\n")
	if en.FallbackPosition != "" {
		b.WriteString("FALLBACK POSITION: " + strutil.Truncate(en.FallbackPosition, 400) + "\n")
	}
	if len(en.RedLines) > 0 {
		b.WriteString("RED LINES: " + strings.Join(en.RedLines, "; ") + "\n")
	}

	start := time.Now()
	resp, err := opts.Provider.Chat(providers.ChatParams{
		Model:       opts.JudgeModel,
		MaxTokens:   400,
		System:      driftSystemPrompt,
		CacheSystem: true,
		JSONMode:    true,
		Messages:    []providers.Message{{Role: "user", Content: b.String()}},
	})
	if err != nil {
		return "", "", err
	}
	recordCost(opts.JudgeModel, resp, time.Since(start).Milliseconds(), opts.TaskID)

	var parsed struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := negotiate.ParseJSONObject(chatText(resp), &parsed); err != nil {
		return "", "", fmt.Errorf("drift judgment returned no usable JSON: %w", err)
	}
	switch status := strings.ToLower(strings.TrimSpace(parsed.Status)); status {
	case DriftAtPosition, DriftAbove, DriftBelow:
		return status, parsed.Note, nil
	default:
		return "", "", fmt.Errorf("drift judgment returned unrecognised status %q", parsed.Status)
	}
}

func chatText(resp *providers.ChatResponse) string {
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			return blk.Text
		}
	}
	return ""
}

func recordCost(model string, resp *providers.ChatResponse, dms int64, taskID string) {
	cw, cr := 0, 0
	if resp.Usage.CacheWriteTokens != nil {
		cw = *resp.Usage.CacheWriteTokens
	}
	if resp.Usage.CacheReadTokens != nil {
		cr = *resp.Usage.CacheReadTokens
	}
	costUSD := cost.CalcCostUSD(model, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	cost.Default.Record(cost.RecordRequest{
		Model: model, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens, CacheReadTokens: resp.Usage.CacheReadTokens,
		CostUSD: costUSD, DurationMs: dms,
		Context: "redtime", TaskID: taskID,
	})
}
