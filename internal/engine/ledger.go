// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
)

// The matter ledger: JSON rows per (kind, key) in the ledger table. A tool call's writes are
// buffered in its step and committed with it (steps.go); reads see the step's own writes.
const (
	kIssue    = "issue"    // key: issue id; Issue
	kFinding  = "finding"  // key: fid; Finding
	kCritic   = "critic"   // key: critic finding id; Critic
	kSection  = "section"  // key: issue id; Section
	kSheet    = "sheet"    // key: worksheet id; analysis.Worksheet
	kGate     = "gate"     // key: gate id; Gate
	kVisit    = "visit"    // key: document|section|offset; the fids it produced
	kCycle    = "cycle"    // key: cycle number; Cycle
	kNote     = "note"     // key: a fixed name; a Limitations line Go wrote (plan fallback, uncovered)
	kEscalate = "escalate" // key: cycle number; P7 fired for it
	kHandle   = "handle"   // key: codename; the Fig it names for the whole run
	kAnalysis = "analysis" // key: issue id; Analysis, analyze_issue's latest for the issue
	kCover    = "cover"    // key: "pass"; Cover, the coverage pass's outcome (it runs once)
	kSwept    = "swept"    // key: "sweep"; []SweptDoc, each document's sweep coverage (the latest sweep's)
	kAlias    = "alias"    // key: a party's folded name; analysis.Alias, the names join_discrepancies was told it goes by
	kRead     = "figread"  // key: "draft"; the calls drafted sections' figure readers made (not the drafters')
	kReadStat = "figstats" // key: "reader"; ReaderStats, the figure reader's calls, failures and the sweep's reader spend
	kVerdict  = "verdict"  // key: judgeKey of a candidate pair's content and the run's aliases; analysis.Verdict, the figure judge's (judge.go)
)

// Issue is one plan issue (the deliverable's spine), typed by a BELO-lite issue class.
type Issue struct {
	analysis.Issue
	Practice string `json:"practice"`
}

// Citation is one verified (or not) quote of a finding: the ladder's result.
type Citation struct {
	DocumentID string  `json:"documentId"`
	Quote      string  `json:"quote"`
	Locator    string  `json:"locator"`
	SectionID  string  `json:"sectionId"`
	Method     string  `json:"method"`
	Confidence float64 `json:"confidence"`
	Match      string  `json:"match"`
	Note       string  `json:"note,omitempty"`
	Low        bool    `json:"lowConfidence,omitempty"` // a low-agreement transcript page (G11)
}

// Finding is a ledger finding: analysis's shape plus its citations and who produced it.
type Finding struct {
	analysis.Finding
	Citations []Citation `json:"citations"`
	Severity  string     `json:"severity"`
	Arm       agent.Arm  `json:"arm"` // the tool and model that produced it (delayed critic credit)
	// Check is a deviation's independent section check (deviation.go checkSection); Low marks a
	// finding admitted at low confidence (the check could not tell, or read a cut section): it is
	// gated for a lawyer's review like a low-confidence transcript.
	Check *SectionCheck `json:"sectionCheck,omitempty"`
	Low   bool          `json:"lowConfidence,omitempty"`
	// ValueNotes are the typed values admission dropped, and why (valueStated): the committee and
	// coverage see them (lab-r4 r3: a dropped value is never silent).
	ValueNotes []string `json:"valueNotes,omitempty"`
	// HookBlocked is the configurable hook that quarantined it (finding_admitted block, or a check
	// failing closed): never in the memo nor a drafter's or subagent's read, disclosed in Limitations,
	// and never counted as a quote that failed verification (G4 counts only Verified).
	HookBlocked string `json:"hookBlocked,omitempty"`
	// Figures are its verified quotes' numbers as the figure reader read them (figures.go): what G8
	// traces a memo figure that cites it to.
	Figures []analysis.Reading `json:"figures,omitempty"`
	// Unread marks a finding whose figures the reader has not read yet: one that states no typed
	// value is read only when a drafted section first cites it (saveSection), and a sweep's findings
	// are read together at the sweep's end (readPending). Until then its quotes' numbers are unread
	// to G8: a memo figure traces to them only as written, limited and gated (lab-r4 r6 M2).
	Unread bool `json:"figuresUnread,omitempty"`
}

// Analysis is analyze_issue's answer on one issue (rule, application, conclusion): it goes to the
// issue's drafter, and it is what makes a Conduct or Requirement issue analysed (cover.go).
type Analysis struct {
	Rule        string `json:"rule"`
	Application string `json:"application"`
	Conclusion  string `json:"conclusion"`
}

// Cover is the coverage pass's outcome: the touched issues it analysed and those still missing, the
// salient findings still left out of the memo after it, and the figure conflicts still unaddressed.
type Cover struct {
	Analysed    []string `json:"analysed"`
	Missing     []string `json:"missing"`
	LeftOut     []string `json:"leftOut"`
	Unaddressed int      `json:"unaddressed"`
	Unadmitted  []string `json:"unadmitted,omitempty"` // usable findings with a typed value admission dropped (Finding.ValueNotes)
}

// Critic is one committee finding, open until the lead resolves it (fixed or disclosed).
type Critic struct {
	ID       string   `json:"id"`
	Cycle    int      `json:"cycle"`
	Critic   string   `json:"critic"`
	Severity string   `json:"severity"` // HIGH, MEDIUM or LOW
	FID      string   `json:"fid,omitempty"`
	FIDs     []string `json:"fids,omitempty"` // an admission line's findings (each raised once per run)
	Text     string   `json:"text"`
	Status   string   `json:"status"` // open, fixed or disclosed
	Note     string   `json:"note,omitempty"`
	Resolved int      `json:"resolved,omitempty"` // the committee cycles run when it was resolved
}

// Fig is a figure handle as given to a drafter: kept with its section, so the renderer substitutes
// exactly the set the drafter wrote against (codenames shift as the ledger grows).
type Fig struct {
	Handle string `json:"handle"`
	FID    string `json:"fid"`
	Value  string `json:"value"`
}

// Section is one drafted section of the deliverable; Summary goes to later drafters, never out.
type Section struct {
	IssueID  string `json:"issueId"`
	Markdown string `json:"markdown"`
	Summary  string `json:"summary"`
	Handles  []Fig  `json:"handles"`
	Version  int    `json:"version"`
	// Read is the figure reader's reading of the numbers the drafter wrote (not Go's handle values),
	// over the text G8 reads (memoBody), by its hash: a changed text is read again or read literally.
	Read *SectionRead `json:"read,omitempty"`
}

// SectionRead is a section's figure reading: the hash of the text read and its readings (offsets in
// it), and why the reader did not read all of it ("" when it did): Limitations discloses that, and
// G8 flags every figure it did not read (lab-r4 r5 L4).
type SectionRead struct {
	Hash   string             `json:"hash"`
	Figs   []analysis.Reading `json:"figs"`
	Unread string             `json:"unread,omitempty"`
}

// Cycle is one committee run: on which draft version, after how many calls, finding what.
type Cycle struct {
	N       int `json:"n"`
	Version int `json:"version"`
	Calls   int `json:"calls"` // calls spent by the task when it returned
}

// Gate is a human gate: the deliverable, or one finding.
type Gate struct {
	GateID    string          `json:"gateId"`
	Kind      string          `json:"kind"` // deliverable, finding or hook (a configurable hook's approve: a call waits for a lawyer)
	FindingID string          `json:"findingId,omitempty"`
	Hook      string          `json:"hook,omitempty"`     // a hook gate's rule
	Subject   string          `json:"subject,omitempty"`  // the tool or subagent its call is of
	Detail    string          `json:"detail,omitempty"`   // the rule's message
	Input     json.RawMessage `json:"input,omitempty"`    // the call's whole input: what the lawyer approves, exactly
	Used      bool            `json:"used,omitempty"`     // a decided hook gate that decided its one execution
	Noted     bool            `json:"noted,omitempty"`    // its decision was in a note to the lead
	Decision  string          `json:"decision,omitempty"` // approved or rejected; "" while open
	Reason    string          `json:"reason,omitempty"`
	DecidedAt string          `json:"decidedAt,omitempty"`
	DecidedBy *Person         `json:"decidedBy,omitempty"` // who decided it (sign-in's user, or the local user without sign-in)
}

// Person is who did something: a signed-in user's issuer subject, display name and email, or the
// local user ({local, Local user}) on a server without sign-in.
type Person struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type row struct {
	Key  string
	Data json.RawMessage
}

// rows reads one kind of a task's ledger in insertion order, with the running step's writes.
func (e *Engine) rows(ctx context.Context, taskID, kind string) ([]row, error) {
	q, err := e.db.QueryContext(ctx, `SELECT key, data FROM ledger WHERE task_id = $1 AND kind = $2 ORDER BY seq`, taskID, kind)
	if err != nil {
		return nil, fmt.Errorf("engine: ledger: %w", err)
	}
	defer q.Close()
	var out []row
	for q.Next() {
		var r row
		if err := q.Scan(&r.Key, &r.Data); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := q.Err(); err != nil {
		return nil, err
	}
	if r := e.active(taskID); r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, w := range r.buf {
			if w.kind != kind {
				continue
			}
			if i := slices.IndexFunc(out, func(x row) bool { return x.Key == w.key }); i < 0 {
				out = append(out, row{w.key, w.data})
			} else if w.update {
				out[i].Data = w.data
			}
		}
	}
	return out, nil
}

// write records a ledger row: buffered in the running step (committed with it), or at once when
// no step runs (a gate decision). update replaces an existing row; otherwise the first write wins.
func (e *Engine) write(ctx context.Context, taskID, kind, key string, v any, update bool) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if r := e.active(taskID); r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.buf = append(r.buf, op{kind: kind, key: key, data: data, update: update})
		return nil
	}
	_, err = e.db.ExecContext(ctx, upsertSQL(update), taskID, kind, key, data, 0)
	return err
}

func upsertSQL(update bool) string {
	return `INSERT INTO ledger (task_id, kind, key, data, step) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (task_id, kind, key) DO ` +
		map[bool]string{true: "UPDATE SET data = EXCLUDED.data", false: "NOTHING"}[update]
}

// Findings is the task's findings as analysis reads them (numbers, join_discrepancies).
func (e *Engine) Findings(ctx context.Context, taskID string) ([]analysis.Finding, error) {
	fs, err := load[Finding](ctx, e, taskID, kFinding)
	out := []analysis.Finding{}
	for _, f := range fs {
		if f.HookBlocked == "" { // quarantined by a hook: no analysis reads it (#58 r2 N2)
			out = append(out, f.Finding)
		}
	}
	return out, err
}

// usable is the verified findings no lawyer rejected at a finding gate (G3, G7).
func (e *Engine) usable(ctx context.Context, taskID string) ([]Finding, error) {
	fs, err := load[Finding](ctx, e, taskID, kFinding)
	gs, gerr := load[Gate](ctx, e, taskID, kGate)
	return slices.DeleteFunc(fs, func(f Finding) bool {
		return !f.ok() || slices.ContainsFunc(gs, func(g Gate) bool { return g.FindingID == f.FID && g.Decision == "rejected" })
	}), cmpErr(err, gerr)
}

// ok is whether a finding may reach a drafter, a read or the memo: its quotes verified (G3) and no
// configurable hook quarantined it.
func (f Finding) ok() bool { return f.Verified && f.HookBlocked == "" }

// hookQuarantined is the Limitations line for findings hooks quarantined: how many, by which rules.
func hookQuarantined(fs []Finding) string {
	by := map[string]int{}
	for _, f := range fs {
		if f.HookBlocked != "" {
			by[f.HookBlocked]++
		}
	}
	if len(by) == 0 {
		return ""
	}
	var parts []string
	n := 0
	for _, r := range slices.Sorted(maps.Keys(by)) {
		parts, n = append(parts, fmt.Sprintf("%s: %d", r, by[r])), n+by[r]
	}
	return fmt.Sprintf("%d %s quarantined by configurable hooks (%s) and kept out of this memo; %s quotes verified.", n, plural(n, "finding was", "findings were"),
		strings.Join(parts, ", "), map[bool]string{true: "its", false: "their"}[n == 1])
}

func cmpErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func nextID(prefix string, n int) string { return prefix + strconv.Itoa(n+1) }

// ledgerTool is the lead's matter memory: plan, findings, get, resolve, draft_get, relations,
// critics. A subagent may only read (plan and resolve are the lead's). Every list and a section's text
// is one page of agent.PageBytes with next (bench4, SEC: 892 findings in full were 682 KB in one
// result, past the lead's context; owner decision: bounded by type, never trimmed).
func (e *Engine) ledgerTool(sp *Specs) agent.Tool {
	return agent.GoTool{S: sp.Spec("ledger"), Fn: func(ctx context.Context, c agent.Call) (any, agent.Usage, error) {
		var in struct {
			Op, IssueID, FID, Detail, CriticID, Resolution, Note, Entity string
			Issues                                                       []Issue
			Offset                                                       int
		}
		if err := agent.Decode(c, &in); err != nil {
			return nil, agent.Usage{}, err
		}
		out, pg := map[string]any{"ok": nil, "findings": nil, "finding": nil, "markdown": nil, "relations": nil, "critics": nil, "next": nil}, agent.NewPage()
		page := func(n, total int) { // a list's page from in.Offset: next names the rest
			if in.Offset+n < total {
				out["next"] = in.Offset + n
			}
		}
		if c.Depth > 0 && (in.Op == "plan" || in.Op == "resolve") {
			return nil, agent.Usage{}, agent.Fail("ledger: op " + in.Op + " is the lead's; a subagent may only read (findings, get, draft_get, relations)")
		}
		fs, err := load[Finding](ctx, e, c.TaskID, kFinding)
		if err != nil {
			return nil, agent.Usage{}, err
		}
		if c.Depth > 0 { // G3: subagents never see quarantined findings
			fs = slices.DeleteFunc(fs, func(f Finding) bool { return !f.ok() })
		}
		brief := func(f Finding) any { // the spec's closed shape; full adds citations and value refs
			b := map[string]any{"fid": f.FID, "issueId": f.IssueID, "claim": f.Claim, "verified": f.Verified, "citations": nil, "values": nil}
			if in.Detail == "full" {
				cs, vs := []map[string]any{}, []map[string]string{}
				for _, c := range f.Citations {
					cs = append(cs, map[string]any{"documentId": c.DocumentID, "quote": c.Quote, "locator": c.Locator, "method": c.Method, "confidence": c.Confidence})
				}
				for i, v := range f.Values {
					vs = append(vs, map[string]string{"ref": fmt.Sprintf("%s.v%d", f.FID, i+1), "entity": v.Entity, "metric": v.Metric, "value": v.Value, "unit": v.Unit, "date": v.Date})
				}
				b["citations"], b["values"] = cs, vs
			}
			return b
		}
		switch in.Op {
		case "plan":
			if len(in.Issues) == 0 {
				return nil, agent.Usage{}, agent.Fail("ledger: op plan needs issues[] (id, question, class, practice): the questions the deliverable must answer")
			}
			for _, is := range in.Issues {
				if err := e.write(ctx, c.TaskID, kIssue, is.ID, is, true); err != nil {
					return nil, agent.Usage{}, err
				}
			}
			out["ok"] = true
		case "findings":
			list := []any{}
			for _, f := range fs {
				if in.IssueID == "" || f.IssueID == in.IssueID {
					list = append(list, brief(f))
				}
			}
			in.Offset = min(in.Offset, len(list))
			out["findings"] = agent.Take(pg, list[in.Offset:])
			page(len(out["findings"].([]any)), len(list))
		case "get":
			i := slices.IndexFunc(fs, func(f Finding) bool { return f.FID == in.FID })
			if i < 0 {
				return nil, agent.Usage{}, agent.Fail("ledger: no finding " + in.FID + " in this task; list them with op findings")
			}
			out["finding"] = brief(fs[i])
		case "resolve":
			cs, err := load[Critic](ctx, e, c.TaskID, kCritic)
			i := slices.IndexFunc(cs, func(x Critic) bool { return x.ID == in.CriticID })
			switch {
			case err != nil:
				return nil, agent.Usage{}, err
			case i < 0:
				return nil, agent.Usage{}, agent.Fail("ledger: no critic finding " + in.CriticID + "; committee's output lists them (C1, C2, …)")
			case in.Resolution != "fixed" && in.Resolution != "disclosed" || strings.TrimSpace(in.Note) == "":
				return nil, agent.Usage{}, agent.Fail("ledger: op resolve needs resolution fixed or disclosed and a note saying what was done (a disclosed note goes into Limitations)")
			}
			cyc, err := load[Cycle](ctx, e, c.TaskID, kCycle)
			if err != nil {
				return nil, agent.Usage{}, err
			}
			cs[i].Status, cs[i].Note, cs[i].Resolved = in.Resolution, in.Note, len(cyc)
			out["ok"] = true
			return out, agent.Usage{}, e.write(ctx, c.TaskID, kCritic, cs[i].ID, cs[i], true)
		case "draft_get":
			ss, err := load[Section](ctx, e, c.TaskID, kSection)
			i := slices.IndexFunc(ss, func(s Section) bool { return s.IssueID == in.IssueID })
			if err != nil {
				return nil, agent.Usage{}, err
			} else if i < 0 {
				return nil, agent.Usage{}, agent.Fail("not_drafted: issue " + in.IssueID + " has no finished section yet; draft it with draft_section first")
			}
			md := ss[i].Markdown
			if in.Offset > len(md) || in.Offset < len(md) && !utf8.RuneStart(md[in.Offset]) {
				return nil, agent.Usage{}, agent.Fail(fmt.Sprintf("ledger: offset %d is not a page start of this %d-byte section; pass 0 or the result's next", in.Offset, len(md)))
			}
			text := agent.TakeText(pg, md[in.Offset:])
			out["markdown"] = text
			page(len(text), len(md))
		case "critics":
			cs, err := load[Critic](ctx, e, c.TaskID, kCritic)
			if err != nil {
				return nil, agent.Usage{}, err
			}
			list := []map[string]any{}
			for _, x := range cs {
				list = append(list, map[string]any{"id": x.ID, "cycle": x.Cycle, "critic": x.Critic, "severity": x.Severity, "fid": nilIf(x.FID), "text": x.Text, "status": x.Status})
			}
			in.Offset = min(in.Offset, len(list))
			out["critics"] = agent.Take(pg, list[in.Offset:])
			page(len(out["critics"].([]map[string]any)), len(list))
		default: // relations
			rels := []map[string]string{}
			for _, f := range fs {
				kept, _ := analysis.Relations(f.Entities, f.Relations)
				for _, r := range kept {
					if f.ok() && (strings.EqualFold(r.Subject, in.Entity) || strings.EqualFold(r.Object, in.Entity)) {
						rels = append(rels, map[string]string{"subject": r.Subject, "predicate": r.Predicate, "object": r.Object, "fid": f.FID})
					}
				}
			}
			in.Offset = min(in.Offset, len(rels))
			out["relations"] = agent.Take(pg, rels[in.Offset:])
			page(len(out["relations"].([]map[string]string)), len(rels))
		}
		return out, agent.Usage{}, nil
	}}
}
