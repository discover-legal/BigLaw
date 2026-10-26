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
	"github.com/discover-legal/BigLaw/internal/model"
)

// bench7 (SEC referral, a real model, $11/900 calls): the figure reader starved the sweep (164 of its
// 268 calls), finish judged with its whole grant before the memo was read, 28 of 110 reader calls
// failed with nothing logged. These pin the fixes.

// readerRequests are the fake's read_figures requests: each one's span count.
func readerRequests(fake *model.Fake) []int {
	var out []int
	for _, c := range fake.Calls() {
		if c.Agent != "read_figures" {
			continue
		}
		var in readRequest
		requestOf(c, &in)
		n := 0
		for _, f := range in.Findings {
			for _, p := range f.Passages {
				n += len(p.Spans)
			}
		}
		out = append(out, n)
	}
	return out
}

// A sweep's findings are read together at its end, from the reader's own share: twelve pages, one
// reader call, and the sweep's own spend is its visits' alone.
func TestSweepReadsItsFindingsTogether(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&doc, "%d. Lot %d\nThe supplier shall be paid $%d,000 for lot %d.\n%s\n\n", i, i, 100+i, i, filler)
	}
	f := newFixture(t, doc.String())
	d1 := f.docs[0].ID
	var ex []model.Reply
	for i := 1; i <= 12; i++ {
		q := fmt.Sprintf("The supplier shall be paid $%d,000 for lot %d", 100+i, i)
		ex = append(ex, facts(on("I1", fd(fmt.Sprintf("Lot %d costs $%d,000.", i, 100+i), d1, q, val("Supplier", fmt.Sprintf("lot %d price", i), fmt.Sprintf("$%d,000", 100+i), "Money")))))
	}
	_, fake := f.engine(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "What is the supplier paid?", "Conduct"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"extract_facts": ex,
	}), Config{})
	task := f.submit(0)
	f.step(context.Background())
	sw := strings.Join(f.outputs(task.ID, "sweep"), "")
	if !strings.Contains(sw, `"visited":12,`) {
		t.Fatalf("sweep %s", sw)
	}
	if rq := readerRequests(fake); len(rq) != 1 || rq[0] > maxCallSpans {
		t.Errorf("twelve pages' findings read in %d calls (%v spans), want one", len(rq), rq)
	}
	fs := ledgerFindings(t, f, task.ID)
	for _, x := range fs {
		if len(x.Values) != 1 || x.Unread || len(x.Figures) == 0 {
			t.Errorf("%s not read with the others: values %v unread %v notes %q", x.FID, x.Values, x.Unread, x.ValueNotes)
		}
	}
	spent, err := f.e.classSpend(context.Background(), task.ID)
	if err != nil || spent["read"].Calls != 1 || spent["sweep"].Calls != 12 {
		t.Errorf("the reader's call charged to the sweep: sweep %+v read %+v (%v)", spent["sweep"], spent["read"], err)
	}
}

// One reader call never holds more spans than its answer can label within the subagent's answer cap
// (bench7: calls of 106 and 123 spans were cut off and failed, dropping every value they held).
func TestReaderCallSpansBounded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine(nil, Config{})
	task := f.submit(0)
	rt, err := f.e.runtime(&run{e: f.e, t: task})
	if err != nil {
		t.Fatal(err)
	}
	var items []*reading
	for i := range 5 {
		var ps []string
		for k := range 40 {
			ps = append(ps, fmt.Sprintf("lot %d-%d costs $%d", i, k, 1000+k))
		}
		items = append(items, findingReading(Finding{Finding: analysis.Finding{Claim: "Prices."}}, []analysis.Quote{{DocumentID: f.docs[0].ID, Quote: strings.Join(ps, "; ")}}, nil))
	}
	if err := f.e.readFigures(context.Background(), rt, agent.Call{TaskID: task.ID, DocumentIDs: task.DocumentIDs}, agent.NewBudget(50, 50, 0), "test", items); err != nil {
		t.Fatal(err)
	}
	rq := readerRequests(rt.Chat.(*model.Fake))
	total := 0
	for _, n := range rq {
		total += n
		if n > maxCallSpans {
			t.Errorf("a reader call of %d spans (cap %d)", n, maxCallSpans)
		}
	}
	if total != 5*120 {
		t.Errorf("%d spans sent in %d calls, want 600", total, len(rq))
	}
}

// A finding that states no typed value is not read at admission; a section citing it reads it in the
// section's own call, so G8 traces the memo's figure to it by a reading, and one never cited stays
// unread (bench7: 326 of 494 findings stated no value).
func TestUncitedFindingReadWhenCited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices"),
			fd("Greystone sued Talbot.", d1, "Greystone Packaging Co. sued Talbot Fresh Foods, Inc. on October 9, 2026"))},
		"draft_section": {section("Greystone claims damages of $644,910 for unpaid invoices [F1].", "Damages [F1].")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 2 || fs[0].Unread || len(fs[0].Figures) == 0 || !fs[1].Unread || len(fs[1].Figures) != 0 {
		t.Fatalf("cited read, uncited unread: %+v", fs)
	}
	if fin := strings.Join(f.outputs(v.ID, "finish"), "\n"); strings.Contains(fin, "not traced") || stop(v) != "awaiting_gate/finished" {
		t.Errorf("%s: %s", stop(v), fin)
	}
}

// A failed reader call is counted and why (the task's cost shows it), and its findings' values say
// so; the cost's reader line has its failure rate (bench7: 28 of 110 failed, none recorded).
func TestReaderFailuresCounted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{"read_figures": {},
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices", val("Greystone", "damages", "$644,910", "Money")))},
		"draft_section": {section("Greystone claims damages of $644,910 for unpaid invoices [F1].", "Damages [F1].")},
	}), Config{})
	c, err := f.e.Cost(context.Background(), v.ID)
	if err != nil || c.Reader == nil || c.Reader.Calls < 2 || c.Reader.Failed != c.Reader.Calls || c.Reader.FailureRate != 1 || c.Reader.Reasons["the figure check's call failed"] != c.Reader.Calls {
		data, _ := json.Marshal(c.Reader)
		t.Errorf("reader line %s (%v)", data, err)
	}
}

// finish reads first: a section whose reading the budget could not fund is read again from finish's
// grant before the judge spends any of it.
func TestFinishReadsAStaleSectionFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices", val("Greystone", "damages", "$644,910", "Money")))},
		"draft_section": {section("Greystone claims damages of $644,910 for unpaid invoices [F1].", "Damages [F1].")},
	}), Config{})
	ctx := context.Background()
	secs, err := load[Section](ctx, f.e, v.ID, kSection)
	if err != nil || len(secs) != 1 || secs[0].Read == nil {
		t.Fatalf("%+v %v", secs, err)
	}
	s := secs[0]
	s.Read.Figs, s.Read.Unread = nil, "1 of its 1 figures were not read (the budget could not fund the figure check)"
	if err := f.e.write(ctx, v.ID, kSection, s.IssueID, s, true); err != nil {
		t.Fatal(err)
	}
	fs, _ := load[Finding](ctx, f.e, v.ID, kFinding)
	if !f.e.stale(s, fs) {
		t.Fatal("an unfunded reading is not stale")
	}
	rt, err := f.e.runtime(&run{e: f.e, t: Task{ID: v.ID, DocumentIDs: f.ids()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.readSections(ctx, rt, agent.Call{TaskID: v.ID, DocumentIDs: f.ids()}, agent.NewBudget(5, 5, 0)); err != nil {
		t.Fatal(err)
	}
	secs, _ = load[Section](ctx, f.e, v.ID, kSection)
	if len(secs[0].Read.Figs) != 1 || secs[0].Read.Unread != "" || secs[0].Read.Figs[0].Unclear {
		t.Errorf("not read again: %+v", secs[0].Read)
	}
}

// figure_context finds a quote that verified tolerantly (curly quotes, white space), so the reader's
// turn is not spent on a refusal (bench7: 219 of its 469 calls failed).
func TestFigureContextFindsATolerantQuote(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "Schedule (in thousands)\nThe “Base Fee”  is\npayable in full: 4,000.\nEnd of schedule.\n")
	f.engine(nil, Config{})
	in, _ := json.Marshal(m{"documentId": f.docs[0].ID, "quote": `The "Base Fee" is payable in full: 4,000.`, "side": "before", "offset": nil})
	res, err := f.e.figureContextTool(builtinSpecs).Call(context.Background(), agent.Call{DocumentIDs: f.ids(), Input: in})
	if err != nil || !strings.Contains(string(res.Output), "in thousands") {
		t.Errorf("%s %v", res.Output, err)
	}
}

// A count the reader read as a period ("18 months") is admitted as that period (bench7: 15 values
// dropped "read it as a duration figure").
func TestCountValueReadAsDurationAdmitted(t *testing.T) {
	quote := "The business continuity plan was last updated 18 months ago."
	p := analysis.PassageOf("q1", quote)
	months, from := "months", "months"
	l := lab(analysis.SpanID("q1", 0), analysis.KindDuration, "", "1", "", "", "")
	l.Unit, l.UnitFrom = &months, &from
	vp := analysis.PassageOf("v1", "18 months")
	vl := lab(analysis.SpanID("v1", 0), analysis.KindDuration, "", "1", "", "", "")
	vl.Unit, vl.UnitFrom = &months, &from
	rs, _ := analysis.ReadLabels([]analysis.Passage{p, vp}, []analysis.Label{l, vl})
	fig, ok, low, why := valueStated(analysis.Value{Value: "18 months", Class: "Count", Metric: "time since update"}, 0, rs, quote+"\n")
	if !ok || low || fig == nil || fig.Kind != analysis.KindDuration || fig.Unit != "months" {
		t.Errorf("%+v %v %v %q", fig, ok, low, why)
	}
}
