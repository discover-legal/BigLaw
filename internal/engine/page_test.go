// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
)

// pageOK checks out is one valid page of tool: its output schema, at most agent.PageBytes, truncated.
func pageOK(t *testing.T, tool string, out any) {
	t.Helper()
	b, _ := json.Marshal(out)
	sch, err := agent.Compile(spec(tool).Out)
	var p struct{ Truncated bool }
	if err != nil || sch.Validate(b) != nil || len(b) > agent.PageBytes || json.Unmarshal(b, &p) != nil || !p.Truncated {
		t.Errorf("%s: %d bytes (page %d), truncated %v, schema %v", tool, len(b), agent.PageBytes, p.Truncated, cmpErr(err, sch.Validate(b)))
	}
}

// TestReportsPageByType (owner decision, lab-r3 r4: tool outputs are bounded by type, never trimmed):
// finish, committee and sweep, filled past their budget, answer one valid page that says truncated.
func TestReportsPageByType(t *testing.T) {
	long := strings.Repeat("Smith & Jones <LLP> \"é\" ", 20)
	var rep report
	var found []Critic
	var fids []string
	var pages []map[string]any
	for i := range 400 {
		rep.Errors = append(rep.Errors, finishErr{"lint", fmt.Sprint(i, long)})
		rep.Lint, rep.MissingIssues, rep.OpenPreconditions = append(rep.Lint, long), append(rep.MissingIssues, fmt.Sprint("I", i)), append(rep.OpenPreconditions, long)
		rep.UncoveredParties = append(rep.UncoveredParties, analysis.PartyGap{Name: long, FIDs: []string{"F1"}, Note: long})
		rep.MissingFigures = append(rep.MissingFigures, analysis.Handle{Handle: "{Zephyr}", FID: "F1", Ref: "F1.v1", Label: long})
		found = append(found, Critic{ID: fmt.Sprint("C", i+1), Critic: "audit_gaps", Severity: "MEDIUM", Text: long})
		fids = append(fids, fmt.Sprint("F", i+1))
		pages = append(pages, map[string]any{"documentId": "3f6c2a9e-0b1d-4c2e-9a7f-5d8e1b2c4a6f", "sectionId": fmt.Sprint("s", i), "through": nil})
	}
	if p := rep.page(); len(p.Errors) == 0 || len(p.Errors) == 400 {
		t.Errorf("finish kept %d of 400 errors", len(p.Errors))
	}
	pageOK(t, "finish", rep.page())
	pageOK(t, "committee", criticPage(1, found))
	skipped := []map[string]any{}
	for _, p := range pages {
		skipped = append(skipped, map[string]any{"documentId": p["documentId"], "sectionId": p["sectionId"], "through": nil, "reason": "duplicate"})
	}
	pageOK(t, "sweep", sweepPage(3, fids, fids, pages, skipped, []map[string]any{{"documentId": "d", "read": 1, "planned": 400}}))
}

// TestLedgerPagesByType: relations, critics and draft_get past their budget come a page at a time
// with next, valid against ledger's schema, and the pages join to the whole (draft_get byte for byte).
func TestLedgerPagesByType(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Terms\nThe Seller shall deliver the goods.\n")
	f.engine(nil, Config{})
	task := f.submit(0)
	ctx := context.Background()
	long := strings.Repeat("Smith & Jones <LLP> \"é\" ", 20)
	for i := range 300 {
		x := Finding{Finding: analysis.Finding{FID: fmt.Sprintf("F%d", i+1), IssueID: "I1", Claim: "c", Verified: true,
			Entities:  []analysis.Entity{{Name: fmt.Sprint("Scheme ", i, " ", long), Class: "Conduct"}, {Name: "Gerald Whitmore", Class: "Person"}},
			Relations: []analysis.Relation{{Subject: fmt.Sprint("Scheme ", i, " ", long), Predicate: "committed by", Object: "Gerald Whitmore"}}}}
		c := Critic{ID: fmt.Sprint("C", i+1), Cycle: 1, Critic: "audit_gaps", Severity: "LOW", Text: long, Status: "open"}
		if err := cmpErr(f.e.write(ctx, task.ID, kFinding, x.FID, x, true), f.e.write(ctx, task.ID, kCritic, c.ID, c, true)); err != nil {
			t.Fatal(err)
		}
	}
	md := strings.Repeat("## Terms\n"+long+"\n", 300)
	if err := f.e.write(ctx, task.ID, kSection, "I1", Section{IssueID: "I1", Markdown: md}, true); err != nil {
		t.Fatal(err)
	}
	sch, _ := agent.Compile(spec("ledger").Out)
	for _, op := range []string{"relations", "critics", "draft_get"} {
		var items, pages int
		var text strings.Builder
		for off := 0; ; pages++ {
			in, _ := json.Marshal(m{"op": op, "issues": nil, "issueId": "I1", "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": "Gerald Whitmore", "offset": off})
			res, err := f.e.ledgerTool().Call(ctx, agent.Call{TaskID: task.ID, Input: in})
			var out struct {
				Relations, Critics []json.RawMessage
				Markdown           *string
				Next               *int
			}
			if err != nil || sch.Validate(res.Output) != nil || len(res.Output) > agent.PageBytes || json.Unmarshal(res.Output, &out) != nil || pages > 100 {
				t.Fatalf("%s at %d: %d bytes, err %v, schema %v", op, off, len(res.Output), err, sch.Validate(res.Output))
			}
			items += len(out.Relations) + len(out.Critics)
			if out.Markdown != nil {
				text.WriteString(*out.Markdown)
			}
			if out.Next == nil {
				break
			}
			off = *out.Next
		}
		if pages < 2 || op != "draft_get" && items != 300 || op == "draft_get" && text.String() != md {
			t.Errorf("%s: %d items over %d pages; text joins: %v", op, items, pages+1, text.String() == md)
		}
	}
}
