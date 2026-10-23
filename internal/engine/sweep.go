// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/docs"
)

// Visit is one unit of the sweep: a page of a top-level section of a document.
type Visit struct {
	DocumentID string `json:"documentId"`
	SectionID  string `json:"sectionId"`
	Offset     int    `json:"offset"`
}

func (v Visit) key() string { return fmt.Sprintf("%s|%s|%d", v.DocumentID, v.SectionID, v.Offset) }

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

// plan lists the sweep's visits over docIDs, in order: every page of every top-level section
// (pages as the page reader cuts them, so extract_facts reads exactly one each).
func (e *Engine) plan(ctx context.Context, scope, docIDs []string) ([]Visit, error) {
	var out []Visit
	for _, id := range docIDs {
		o, err := e.docs.Outline(ctx, scope, id)
		if err != nil {
			return nil, err
		}
		for _, s := range roots(o) {
			for off := 0; ; {
				out = append(out, Visit{id, s.SectionID, off})
				p, err := e.docs.Section(ctx, scope, id, s.SectionID, off, 0)
				if err != nil {
					return nil, err
				}
				if !p.Truncated {
					break
				}
				off = p.Next
			}
		}
	}
	return out, nil
}

// sweepTool is the retrieval floor (P8: idempotent): extract_facts over every visit not yet
// made, within the sweep's share; what the budget cannot reach is reported, never skipped silently.
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
		visits, err := e.plan(ctx, c.DocumentIDs, ids)
		if err != nil {
			return nil, agent.Usage{}, agent.Fail("sweep: a document is not one of this task's documents")
		}
		done, err := e.rows(ctx, c.TaskID, kVisit)
		if err != nil {
			return nil, agent.Usage{}, err
		}
		spent, err := e.classSpend(ctx, c.TaskID)
		if err != nil {
			return nil, agent.Usage{}, err
		}
		a, sw := Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD), spent["sweep"]
		b := agent.NewBudget(min(c.Budget.Calls, a.Sweep-sw.Calls), min(c.Budget.USD, a.dollars(a.Sweep)-sw.USD), 0)
		out := map[string]any{"visited": 0, "findings": []string{}, "incomplete": false, "uncovered": []map[string]string{}}
		fids, uncovered, broke := []string{}, []map[string]string{}, false
		q, _ := json.Marshal(r.t.Description)
		for _, v := range visits {
			if i := slices.IndexFunc(done, func(x row) bool { return x.Key == v.key() }); i >= 0 {
				var got []string
				_ = json.Unmarshal(done[i].Data, &got)
				fids = append(fids, got...)
				continue
			}
			if calls, _ := b.Remaining(); calls == 0 || broke {
				uncovered = append(uncovered, map[string]string{"documentId": v.DocumentID, "sectionId": v.SectionID})
				continue
			}
			in := fmt.Sprintf(`{"question":%s,"documentId":%q,"sectionId":%q,"offset":%d,"query":null,"issueId":null}`, q, v.DocumentID, v.SectionID, v.Offset)
			res, err := rt.Invoke(ctx, agent.Agent{Name: "sweep", Visible: []string{"extract_facts"}}, agent.Call{TaskID: c.TaskID, DocumentIDs: c.DocumentIDs}, "extract_facts", json.RawMessage(in), b, false, 0)
			if modelOrJournal(err) {
				return nil, b.Spent(), err
			} else if broke = errors.Is(err, agent.ErrBudget); broke { // its USD share is spent: the rest is uncovered, not tried
				uncovered = append(uncovered, map[string]string{"documentId": v.DocumentID, "sectionId": v.SectionID})
				continue
			}
			var got hookResult
			_ = json.Unmarshal(res.Output, &got)
			ids := []string{}
			for _, f := range got.Findings {
				ids = append(ids, f["fid"].(string))
			}
			if err == nil { // a failed visit (bad output, its grant spent) is read again by a later sweep
				if err := e.write(ctx, c.TaskID, kVisit, v.key(), ids, false); err != nil {
					return nil, b.Spent(), err
				}
			} else {
				uncovered = append(uncovered, map[string]string{"documentId": v.DocumentID, "sectionId": v.SectionID})
			}
			out["visited"] = out["visited"].(int) + 1
			fids = append(fids, ids...)
		}
		out["findings"], out["uncovered"], out["incomplete"] = fids, uncovered, len(uncovered) > 0
		if len(uncovered) > 0 {
			note := fmt.Sprintf("The evidence sweep ran out of budget: %d section %s not read: ", len(uncovered), plural(len(uncovered), "page was", "pages were"))
			for i, u := range uncovered {
				note += map[bool]string{true: "", false: "; "}[i == 0] + u["documentId"] + " " + u["sectionId"]
			}
			if err := e.write(ctx, c.TaskID, kNote, "sweep", note, true); err != nil {
				return nil, b.Spent(), err
			}
		} else if err := e.write(ctx, c.TaskID, kNote, "sweep", "", true); err != nil {
			return nil, b.Spent(), err
		}
		return out, b.Spent(), nil
	}}
}

// modelOrJournal: errors that end the run, never "no finding" (G6) or a lost step.
func modelOrJournal(err error) bool {
	return err != nil && (errors.Is(err, agent.ErrJournal) || modelFailed(err))
}
