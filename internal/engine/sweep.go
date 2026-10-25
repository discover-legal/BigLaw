// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/docs"
)

// Visit is one unit of the sweep: a page of a top-level section of a document, or (Through set) a
// run of consecutive short top-level sections, SectionID to Through, that fits in one page.
type Visit struct {
	DocumentID string `json:"documentId"`
	SectionID  string `json:"sectionId"`
	Offset     int    `json:"offset"`
	Through    string `json:"through,omitempty"`
	text       string // what it reads: the sweep orders and skips visits by it
}

// unread is the visit as sweep reports it (not read, or skipped): a batch names its whole run
// (through), any other visit has through null (sweep.json: strict-shaped, every field present).
func (v Visit) unread() map[string]any {
	u := map[string]any{"documentId": v.DocumentID, "sectionId": v.SectionID, "through": nil}
	if v.Through != "" {
		u["through"] = v.Through
	}
	return u
}

func (v Visit) key() string {
	k := fmt.Sprintf("%s|%s|%d", v.DocumentID, v.SectionID, v.Offset)
	if v.Through != "" {
		k += "|" + v.Through
	}
	return k
}

// roots are a document's top-level sections in order: those no other section contains (a level-0
// preface contains nothing). The section tree is byte-exact, so the roots cover the text once.
func roots(outline []docs.OutlineEntry) []docs.OutlineEntry {
	var out []docs.OutlineEntry
	var open []int // levels of the sections the walk is inside
	for _, s := range outline {
		for len(open) > 0 && open[len(open)-1] >= s.Level {
			open = open[:len(open)-1]
		}
		if len(open) == 0 {
			out = append(out, s)
		}
		if s.Level > 0 {
			open = append(open, s.Level)
		}
	}
	return out
}

// sweepTokens bounds what one extract_facts call reads: a batch of short top-level sections, or a
// page of a long one (the lead's direct section reads too). It is set by the answer, not the page:
// a subagent writes about five to seven tokens of findings per token of dense text (Haiku 4.5 on
// the SEC referral), so a full page (2,000) overflowed the 8,192-token answer and most of its
// visits failed; 600 keeps the answer near 4,000. A table's page is also at most
// docs.TablePageRows rows (a row is a trade or a payment: a finding each).
const sweepTokens = 600

// plan lists the sweep's visits over docIDs, in document order, each with its text: every page of
// every top-level section (pages of sweepTokens as the page reader cuts them, so extract_facts reads
// exactly one each). Consecutive top-level sections shorter than sweepTokens are batched into one
// visit of at most that: a document numbered by paragraph has a hundred roots of a few hundred
// characters each, and one visit (a subagent run) per paragraph spent the sweep's share on a quarter
// of what it planned.
func (e *Engine) plan(ctx context.Context, scope, docIDs []string) ([]Visit, error) {
	var out []Visit
	for _, id := range docIDs {
		o, err := e.docs.Outline(ctx, scope, id)
		if err != nil {
			return nil, err
		}
		var run []docs.OutlineEntry // short roots not yet planned
		var text strings.Builder
		size := 0
		flush := func() {
			if len(run) > 0 {
				v := Visit{DocumentID: id, SectionID: run[0].SectionID, text: text.String()}
				if len(run) > 1 {
					v.Through = run[len(run)-1].SectionID
				}
				out, run, size = append(out, v), nil, 0
				text.Reset()
			}
		}
		for _, s := range roots(o) {
			if s.Tokens < sweepTokens {
				if size+s.Tokens > sweepTokens {
					flush()
				}
				p, err := e.docs.Section(ctx, scope, id, s.SectionID, 0, sweepTokens)
				if err != nil {
					return nil, err
				}
				run, size = append(run, s), size+s.Tokens
				text.WriteString(p.Text)
				continue
			}
			flush()
			for off := 0; ; {
				p, err := e.docs.Section(ctx, scope, id, s.SectionID, off, sweepTokens)
				if err != nil {
					return nil, err
				}
				out = append(out, Visit{DocumentID: id, SectionID: s.SectionID, Offset: off, text: p.Header + "\n" + p.Text})
				if !p.Truncated {
					break
				}
				off = p.Next
			}
		}
		flush()
	}
	return out, nil
}

// runText is the text of the run of top-level sections from first to through of one document,
// verbatim (roots are contiguous, so the run is one span of the text); ok is false when the run
// is not one (through before first, or either is not a top-level section), and the caller reads
// first alone.
func (e *Engine) runText(ctx context.Context, scope []string, docID, first, through string) (text string, ok bool) {
	o, err := e.docs.Outline(ctx, scope, docID)
	if err != nil {
		return "", false
	}
	rs := roots(o)
	i := slices.IndexFunc(rs, func(s docs.OutlineEntry) bool { return s.SectionID == first })
	j := slices.IndexFunc(rs, func(s docs.OutlineEntry) bool { return s.SectionID == through })
	if i < 0 || j < i {
		return "", false
	}
	size := 0
	for _, s := range rs[i : j+1] {
		size += s.Tokens
	}
	if size > docs.MaxSectionTokens {
		return "", false
	}
	var b strings.Builder
	for _, s := range rs[i : j+1] {
		p, err := e.docs.Section(ctx, scope, docID, s.SectionID, 0, docs.MaxSectionTokens)
		if err != nil || p.Truncated {
			return "", false
		}
		b.WriteString(p.Text)
	}
	return b.String(), true
}

// sweepWorkers is how many visits a sweep reads at once (bench: 51 visits one at a time took 40 to
// 55 minutes of a run).
const sweepWorkers = 4

// turnKey carries a parallel sweep's turn (sweepVisits): extract_facts admits its findings only
// once the channel is closed, when every earlier visit has ended.
type turnKey struct{}

// awaitTurn waits for the caller's turn to admit findings, if a sweep gave it one.
func awaitTurn(ctx context.Context) error {
	if prev, ok := ctx.Value(turnKey{}).(chan struct{}); ok {
		select {
		case <-prev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// visitRec is a visit's record (kVisit): the fids it produced and the plan's issues it was read for.
// When the plan gains issues a page is read again, for the new ones only (lab-r4 verifier M3: a fact
// on an issue the lead added after the sweep was dropped at the visit, and the page never read again).
type visitRec struct {
	FIDs   []string `json:"fids"`
	Issues []string `json:"issues"`
}

// decodeVisit reads a visit record; one kept before records held issues (the fids alone) counts as
// read for the plan's issues cur.
func decodeVisit(data json.RawMessage, cur []string) visitRec {
	var r visitRec
	if json.Unmarshal(data, &r) != nil {
		_ = json.Unmarshal(data, &r.FIDs)
		r.Issues = cur
	}
	return r
}

// missing is the issues of cur that read does not hold, in cur's order.
func missing(cur, read []string) []string {
	var out []string
	for _, id := range cur {
		if !slices.Contains(read, id) {
			out = append(out, id)
		}
	}
	return out
}

// issuesKey carries the plan issues one visit is read for, when only some (a page read again for
// the issues added since): extract_facts is sent only those and admits only findings naming one.
type issuesKey struct{}

// visitRun is one visit's outcome: the fids it produced, whether it ran, and its error.
type visitRun struct {
	ids                 []string
	notes               []string // admission's notes (a dropped value, a merge), kept for the lead
	ran, broke, refused bool     // ran: it made a model call; broke: all that was left ran out on it; refused: a worker's share could not fund it
	err                 error
}

// sweepVisits reads visits with up to sweepWorkers at once, within b, and returns each one's
// outcome, in the order given (the reading order: order). It is deterministic and the shares hold exactly, as one at a time:
//   - grants: a visit starts with a full grant (the subagent's turns and a worker's share of the
//     USD) while one is free, else only when no visit is in flight, with what is left (as a
//     sequential sweep would give it); so a visit's grant never depends on timing;
//   - order: findings are admitted (fids, near-duplicate merges) and visits recorded in reading
//     order: each visit waits, after its subagent ran, for the one before it to end;
//   - serial fallback: a visit a worker's share could not fund (a call's worst case over what the
//     share had left) is read again once nothing is in flight, one at a time with all that is left,
//     as a sequential sweep would, so parallelism never reads less (its findings are admitted after
//     the parallel visits', still in reading order among themselves);
//   - stops: when the money runs out on a visit that had all that was left, or a model or journal
//     failure ends the run (the in-flight visits are cancelled; the earliest error is returned).
//
// A visit with a record (was[i].Issues set) is read for the issues of cur it was not read for, and its
// record then holds both.
func (e *Engine) sweepVisits(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, visits []Visit, todo []bool, was []visitRec, cur []string, question string) ([]visitRun, error) {
	out := make([]visitRun, len(visits))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ef := spec("extract_facts")
	turns, share := ef.MaxTurns, b.Value().USD/sweepWorkers
	model := rt.Bandit.PickModel(ctx, ef.Name, rt.Candidates(ef.Role)) // one pick for the sweep, so no visit's model depends on timing
	fin := make(chan int)
	prev := make(chan struct{})
	close(prev)
	inflight, stop, failed := 0, false, -1
	settle := func() { // one visit ended
		i := <-fin
		inflight--
		switch r := out[i]; {
		case errors.Is(r.err, context.Canceled) && ctx.Err() == nil: // cancelled by a failure settled before it
		case modelOrJournal(r.err):
			if failed < 0 || i < failed {
				failed = i
			}
			stop = true
			cancel()
		case r.broke:
			stop = true
		}
	}
	q, _ := json.Marshal(question)
	launch := func(i int, v Visit, serial bool) error {
		var calls int
		var usd float64
		for !stop {
			fc, fu := b.Remaining()
			if serial && inflight == 0 {
				calls, usd = min(turns, fc), fu
				break
			}
			if full := fc >= turns && fu >= share; !serial && inflight < sweepWorkers && (full || inflight == 0) {
				calls, usd = min(turns, fc), min(share, fu)
				break
			}
			settle()
		}
		if stop || calls == 0 {
			stop = true
			return nil
		}
		g, err := b.Grant(calls, usd, agent.LevelEvidence)
		if err != nil {
			return err
		}
		through := "null"
		if v.Through != "" {
			through = fmt.Sprintf("%q", v.Through)
		}
		in := fmt.Sprintf(`{"question":%s,"documentId":%q,"sectionId":%q,"through":%s,"offset":%d,"query":null,"issueId":null}`, q, v.DocumentID, v.SectionID, through, v.Offset)
		next, tail := make(chan struct{}), calls < turns || usd < share || serial
		inflight++
		vctx, read := context.WithValue(cctx, turnKey{}, prev), cur
		if was[i].Issues != nil {
			read = missing(cur, was[i].Issues)
			vctx = context.WithValue(vctx, issuesKey{}, read)
		}
		go func(wait chan struct{}) {
			res, err := rt.Invoke(vctx, agent.Agent{Name: "sweep", Visible: []string{"extract_facts"}},
				agent.Call{TaskID: c.TaskID, DocumentIDs: c.DocumentIDs, Model: model, NoTools: true}, "extract_facts", json.RawMessage(in), g, false, 0)
			g.Close()
			select { // the visit before this one has ended (its findings and its record are in)
			case <-wait:
			case <-cctx.Done():
			}
			r := visitRun{ids: []string{}, ran: res.Usage.Calls > 0, err: err}
			if errors.Is(err, agent.ErrBudget) { // tail: the money left is spent, the rest is uncovered; else the serial fallback reads it
				r.broke, r.refused = tail, !tail
			}
			var got hookResult
			_ = json.Unmarshal(res.Output, &got)
			for _, f := range got.Findings {
				if id, ok := f["fid"].(string); ok { // a failed visit's output may be its raw answer
					r.ids = append(r.ids, id)
				}
			}
			if err == nil {
				r.notes = got.Notes
			}
			if err == nil && cctx.Err() == nil { // a failed visit (bad output, its grant spent) is read again by a later sweep
				rec := visitRec{FIDs: append(slices.Clone(was[i].FIDs), r.ids...), Issues: append(slices.Clone(was[i].Issues), read...)}
				if rec.Issues == nil {
					rec.Issues = []string{}
				}
				r.err = e.write(ctx, c.TaskID, kVisit, v.key(), rec, true)
			}
			out[i] = r
			close(next)
			fin <- i
		}(prev)
		prev = next
		return nil
	}
	for i, v := range visits {
		if todo[i] {
			if err := launch(i, v, false); err != nil {
				return nil, err
			}
		}
	}
	for inflight > 0 {
		settle()
	}
	for i, v := range visits { // the serial fallback, in reading order
		if out[i].refused {
			if err := launch(i, v, true); err != nil {
				return nil, err
			}
		}
	}
	for inflight > 0 {
		settle()
	}
	if failed >= 0 {
		return out, out[failed].err
	}
	return out, nil
}

// sweepTool is the retrieval floor (P8: idempotent): extract_facts over every visit not yet
// made, within the sweep's share, most relevant to the plan's issues first (order); a duplicate or
// boilerplate page is skipped (skipWhy). What the budget cannot reach is reported, never skipped
// silently, and each document's coverage is recorded (kSwept) for the cross-document checks.
func (e *Engine) sweepTool(rt *agent.Runtime, r *run) agent.Tool {
	return agent.GoTool{S: spec("sweep"), Fn: func(ctx context.Context, c agent.Call) (any, agent.Usage, error) {
		var in struct{ DocumentIDs []string }
		if err := agent.Decode(c, &in); err != nil {
			return nil, agent.Usage{}, err
		}
		ids := in.DocumentIDs
		if len(ids) == 0 {
			ids = c.DocumentIDs
		}
		planned, err := e.plan(ctx, c.DocumentIDs, ids)
		if err != nil {
			return nil, agent.Usage{}, agent.Fail("sweep: a document is not one of this task's documents")
		}
		done, err := e.rows(ctx, c.TaskID, kVisit)
		issues, ierr := load[Issue](ctx, e, c.TaskID, kIssue)
		spent, serr := e.classSpend(ctx, c.TaskID)
		if err = cmpErr(err, ierr, serr); err != nil {
			return nil, agent.Usage{}, err
		}
		qs, cur := []string{}, []string{}
		for _, is := range issues {
			qs, cur = append(qs, is.Question), append(cur, is.ID)
		}
		visits := make([]Visit, 0, len(planned))
		for _, i := range order(planned, qs) {
			visits = append(visits, planned[i])
		}
		a, sw := Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD), spent["sweep"]
		b := agent.NewBudget(min(c.Budget.Calls, a.Sweep-sw.Calls), min(c.Budget.USD, a.dollars(a.Sweep)-sw.USD), 0)
		todo, was, why, src := make([]bool, len(visits)), make([]visitRec, len(visits)), make([]string, len(visits)), make([]int, len(visits))
		var kept []Visit // the read (or to be read) visits so far, in reading order: what a later one may repeat
		var keptAt []int // their indexes in visits
		for i, v := range visits {
			src[i] = -1
			if j := slices.IndexFunc(done, func(x row) bool { return x.Key == v.key() }); j >= 0 {
				was[i] = decodeVisit(done[j].Data, cur)
				if was[i].Issues == nil {
					was[i].Issues = []string{}
				}
				todo[i] = len(missing(cur, was[i].Issues)) > 0 // read again for the issues added since
			} else if why[i], src[i] = skipWhy(v, kept); why[i] == "" {
				todo[i] = true
			} else if src[i] >= 0 {
				src[i] = keptAt[src[i]]
			}
			if why[i] == "" {
				kept, keptAt = append(kept, v), append(keptAt, i)
			}
		}
		runs, err := e.sweepVisits(ctx, rt, c, b, visits, todo, was, cur, r.t.Description)
		if err != nil {
			return nil, b.Spent(), err
		}
		fids, notes, uncovered, skipped, visited := []string{}, []string{}, []map[string]any{}, []map[string]any{}, 0
		cov := map[string]*SweptDoc{}
		var docOrder []string
		for i, v := range visits {
			d := cov[v.DocumentID]
			if d == nil {
				d = &SweptDoc{DocumentID: v.DocumentID}
				cov[v.DocumentID], docOrder = d, append(docOrder, v.DocumentID)
			}
			d.Planned++
			fids = append(fids, was[i].FIDs...)
			unread := func(j int) bool { return todo[j] && (!runs[j].ran || runs[j].err != nil) }
			switch x := runs[i]; {
			case why[i] != "" && src[i] >= 0 && unread(src[i]): // a copy of a page the sweep did not read is not read either (lab-r3 verifier M2)
				uncovered = append(uncovered, v.unread())
			case why[i] != "":
				u := v.unread()
				u["reason"] = why[i]
				skipped, d.Skipped = append(skipped, u), d.Skipped+1
			case !todo[i]:
				d.Read++
			case unread(i):
				uncovered = append(uncovered, v.unread())
			default:
				visited, d.Read = visited+1, d.Read+1
				fids, notes = append(fids, x.ids...), append(notes, x.notes...)
			}
		}
		swept, unread := []SweptDoc{}, []map[string]any{}
		for _, id := range docOrder {
			d := *cov[id]
			if dd, err := e.docs.Get(ctx, c.DocumentIDs, id); err == nil {
				d.Name = dd.Name
			}
			swept = append(swept, d)
			if d.Read+d.Skipped < d.Planned {
				unread = append(unread, map[string]any{"documentId": id, "read": d.Read, "planned": d.Planned})
			}
		}
		if was, err := load[[]SweptDoc](ctx, e, c.TaskID, kSwept); err != nil {
			return nil, b.Spent(), err
		} else if len(was) > 0 { // a sweep over some documents keeps the others' coverage
			for _, d := range was[0] {
				if !slices.Contains(docOrder, d.DocumentID) {
					swept = append(swept, d)
				}
			}
		}
		if err := e.write(ctx, c.TaskID, kSwept, "sweep", swept, true); err != nil {
			return nil, b.Spent(), err
		}
		out := sweepPage(visited, fids, notes, uncovered, skipped, unread)
		note := ""
		if len(uncovered) > 0 {
			note = fmt.Sprintf("The evidence sweep ran out of budget: %d of %d pages were not read (it read those most relevant to the plan's issues first): ", len(uncovered), len(visits))
			k := 0
			for _, d := range swept {
				var pages []string
				for _, u := range uncovered {
					if u["documentId"] == d.DocumentID {
						pages = append(pages, fmt.Sprint(u["sectionId"])+cmpStr(u["through"] != nil, fmt.Sprint(" to ", u["through"]), ""))
					}
				}
				if len(pages) > 0 {
					more := cmpStr(len(pages) > 8, fmt.Sprintf(" and %d more", len(pages)-8), "")
					note += cmpStr(k > 0, "; ", "") + fmt.Sprintf("%s, %d of %d pages (%s%s)", cmpStr(d.Name != "", d.Name, d.DocumentID), len(pages), d.Planned, strings.Join(pages[:min(8, len(pages))], ", "), more)
					k++
				}
			}
			note += ". Facts and figures on those pages are not in the findings, so the cross-document comparison could not see them."
		}
		if err := e.write(ctx, c.TaskID, kNote, "sweep", note, true); err != nil {
			return nil, b.Spent(), err
		}
		if err := e.write(ctx, c.TaskID, kNote, "sweep-skipped", skippedNote(swept), true); err != nil {
			return nil, b.Spent(), err
		}
		return out, b.Spent(), nil
	}}
}

// sweepPage is sweep's answer: one page, its lists in order of use (truncated: all the findings are
// in the ledger, and a later sweep reads the uncovered pages).
func sweepPage(visited int, fids, notes []string, uncovered, skipped, unread []map[string]any) map[string]any {
	pg := agent.NewPage()
	out := map[string]any{"visited": visited, "incomplete": len(uncovered) > 0, "unreadDocuments": agent.Take(pg, unread), "uncovered": agent.Take(pg, uncovered),
		"findings": agent.Take(pg, fids), "skipped": agent.Take(pg, skipped), "notes": agent.Take(pg, notes)}
	out["truncated"] = pg.Truncated
	return out
}

// skippedNote is the Limitations line for the pages the sweeps skipped (lab-r3 verifier M3: the
// lawyer is told, not only the lead): how many, and in which documents.
func skippedNote(swept []SweptDoc) string {
	n, docs := 0, []string{}
	for _, d := range swept {
		if d.Skipped > 0 {
			n += d.Skipped
			docs = append(docs, fmt.Sprintf("%s (%d of %d)", cmpStr(d.Name != "", d.Name, d.DocumentID), d.Skipped, d.Planned))
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("The evidence sweep did not read %d %s it judged exact duplicates of pages it read or boilerplate (no text, or a table of contents): %s.",
		n, plural(n, "page", "pages"), strings.Join(docs, "; "))
}

// staleLine is the Limitations line for the pages a sweep read before the plan gained issues and no
// sweep read again for them ("" when none): facts there on only those issues may be missing.
func (e *Engine) staleLine(ctx context.Context, taskID string, issues []Issue) (string, error) {
	rs, err := e.rows(ctx, taskID, kVisit)
	if err != nil {
		return "", err
	}
	var cur []string
	for _, is := range issues {
		cur = append(cur, is.ID)
	}
	n, gone := 0, map[string]bool{}
	for _, r := range rs {
		if m := missing(cur, decodeVisit(r.Data, cur).Issues); len(m) > 0 {
			n++
			for _, id := range m {
				gone[id] = true
			}
		}
	}
	if n == 0 {
		return "", nil
	}
	ids := slices.DeleteFunc(slices.Clone(cur), func(id string) bool { return !gone[id] })
	list := strings.Join(ids, ", ")
	return fmt.Sprintf("Coverage: %d %s the evidence sweep read before %s %s joined the plan %s not read again for %s, so facts there that bear only on %s may be missing.",
		n, plural(n, "page", "pages"), plural(len(ids), "issue", "issues"), list, plural(n, "was", "were"), plural(len(ids), "it", "them"), list), nil
}

// SweptDoc is one document's sweep coverage: pages planned, read and skipped (duplicate or boilerplate).
type SweptDoc struct {
	DocumentID string `json:"documentId"`
	Name       string `json:"name"`
	Planned    int    `json:"planned"`
	Read       int    `json:"read"`
	Skipped    int    `json:"skipped"`
}

// unswept is the documents a sweep left partly unread (none when no sweep ran).
func (e *Engine) unswept(ctx context.Context, taskID string) ([]SweptDoc, error) {
	rs, err := load[[]SweptDoc](ctx, e, taskID, kSwept)
	var out []SweptDoc
	for _, r := range rs {
		for _, d := range r {
			if d.Read+d.Skipped < d.Planned {
				out = append(out, d)
			}
		}
	}
	return out, err
}

// modelOrJournal: errors that end the run, never "no finding" (G6) or a lost step.
func modelOrJournal(err error) bool {
	return err != nil && (errors.Is(err, agent.ErrJournal) || errors.Is(err, agent.ErrPanic) || modelFailed(err))
}

// Unread is the documents a sweep left partly unread (analysis.Unswept, for join_discrepancies).
func (e *Engine) Unread(ctx context.Context, taskID string) ([]string, error) {
	ds, err := e.unswept(ctx, taskID)
	out := []string{}
	for _, d := range ds {
		out = append(out, d.DocumentID)
	}
	return out, err
}
