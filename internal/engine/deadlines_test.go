// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

const litigation = "../../eval/tasks/litigation/greystone-v-talbot-defense"

// handleDrafter is a drafter that writes the deadlines section from the worksheet handles its
// request carries, as a model given them would: one per computed date, found by its label.
type handleDrafter struct{ *model.Fake }

func (d handleDrafter) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	if req.Agent != "draft_section" {
		return d.Fake.Chat(ctx, req)
	}
	var in struct {
		Handles []struct{ Handle, Label string }
	}
	_ = json.Unmarshal([]byte(strings.TrimPrefix(fmt.Sprint(req.Messages[1].Content), "Request:\n")), &in)
	h := func(fn, ref string) string {
		for _, x := range in.Handles {
			if strings.HasPrefix(x.Label, fn+":") && strings.Contains(x.Label, ref) {
				return x.Handle
			}
		}
		return "(not computed)"
	}
	md := "Talbot was served on October 21, 2026 [F1]. Under Rule 12(a)(1)(A) Talbot must answer within 21 days [F8]; the raw date, November 11, 2026, is " +
		"Veterans Day, so under Rule 6(a)(1)(C) the answer is due " + h("date_add", "F1.v1") + " [F1]. The court set the initial status hearing for December 3, " +
		"2026 and ordered a joint initial status report no later than seven (7) days before it [F2]; seven days before is Thanksgiving, November 26, 2026, so " +
		"counting back under Rule 6(a)(5) [F7] the report is due " + h("date_add", "F2.v1") + " [F2]. Under Rule 7.1 Talbot must file its disclosure statement " +
		"with its first appearance [F6]. Greystone's Notice of Dispute of September 25, 2026 [F4] began the Section 14.2 negotiation period of thirty (30) days " +
		"[F5], which ran to " + h("date_add", "F4.v1") + "; Greystone sued on October 9, 2026 [F3], " + h("date_diff", "F4.v1") + " into that period."
	b, _ := json.Marshal(map[string]string{"markdown": md, "summary": "The deadlines [F1] [F2]."})
	return &model.Response{Model: "fake", Text: string(b)}, nil
}

// TestDeadlinesMeetTheAnswerKey (bench: all three litigation deadline criteria were missed): the
// lead's numbers calls in the forms a model sends (F1.d1, a bare F4, "21 days", a negative count)
// compute the deadlines with Rule 6(a) rolls, the drafter writes them as worksheet handles, the
// rule numbers pass the figure lint, finish accepts at once, and the memo meets every deadline
// criterion of the litigation matter's answer key (eval/tasks).
func TestDeadlinesMeetTheAnswerKey(t *testing.T) {
	t.Parallel()
	var texts []string
	for _, n := range []string{"02-packaging-supply-agreement.md", "03-key-correspondence.md", "04-docket-sheet.md", "05-federal-calendar-and-rules-excerpt.md"} {
		b, err := os.ReadFile(filepath.Join(litigation, "documents", n))
		if err != nil {
			t.Fatal(err)
		}
		texts = append(texts, string(b))
	}
	f := newFixture(t, texts...)
	agr, cor, dkt, rules := f.docs[0].ID, f.docs[1].ID, f.docs[2].ID, f.docs[3].ID
	holidays := []string{"2026-10-12", "2026-11-11", "2026-11-26", "2026-12-25"}
	calc := func(fn string, holidays []string, ops ...m) model.ToolCall {
		return tc("numbers", m{"op": "calc", "fn": fn, "operands": ops, "holidays": holidays, "fids": nil, "salientTop": nil})
	}
	op := func(ref, lit, unit any) m { return m{"ref": ref, "literal": lit, "unit": unit} }
	lead := []model.Reply{
		calls(plan(issue("I1", "What deadlines does Talbot face?", "Requirement"))),
		calls(extract(dkt, "served answer status report", "I1"), extract(cor, "notice of dispute", "I1"), extract(agr, "negotiation", "I1"), extract(rules, "rules", "I1")),
		calls(calc("date_add", holidays, op("F1.d1", nil, nil), op(nil, "21 days", nil)), calc("date_add", holidays, op("F2.v1", nil, nil), op(nil, "-7", "days")),
			calc("date_add", nil, op("F4", nil, nil), op(nil, "30", "days")), calc("date_diff", nil, op("F4.v1", nil, nil), op("[F3].v1", nil, nil))),
		calls(draft("I1", "F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8")),
		calls(committee()),
		calls(finishCall("submit")),
	}
	q := func(claim, doc, quote string, vals ...m) m { return fd(claim, doc, quote, vals...) }
	scripts := f.with(map[string][]model.Reply{
		"lead": lead,
		"extract_facts": {
			facts(q("Talbot was served on 10/21/2026.", dkt, "served on 10/21/2026 via its registered agent", val("Talbot", "served", "10/21/2026", "Date")),
				q("A joint status report is due seven days before the December 3, 2026 hearing.", dkt, "Initial status hearing set for December 3, 2026 at 9:30 a.m. in Courtroom 1925. The parties shall file a joint initial status report no later than seven (7) days before the initial status hearing",
					val("court", "initial status hearing", "December 3, 2026", "Date")),
				q("The complaint was filed on 10/09/2026.", dkt, "Date Filed: 10/09/2026", val("Greystone", "filed", "10/09/2026", "Date"))),
			facts(q("Greystone's Notice of Dispute is dated September 25, 2026.", cor, "Greystone Notice of Dispute, September 25, 2026", val("Greystone", "notice of dispute", "September 25, 2026", "Date"))),
			facts(q("Section 14.2 provides a thirty-day negotiation period.", agr, "For thirty (30) days after delivery of a Notice of Dispute, senior executives of the parties with authority to settle shall meet and negotiate in good faith to resolve the dispute")),
			facts(q("Rule 7.1 requires a disclosure statement with the first appearance.", rules, "A nongovernmental corporate party must file a disclosure statement with its first appearance"),
				q("Rule 6(a)(5) counts backward for a period before an event.", rules, "continuing to count forward when the period is measured after an event and backward when measured before an event"),
				q("An answer is due 21 days after service.", rules, "A defendant must serve an answer within 21 days after being served with the summons and complaint")),
		},
	})
	fake := newFake(scripts)
	f.e = New(f.st, f.d, handleDrafter{fake}, testRoles, nil, Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	out := deref(v.Output)
	if fin := f.outputs(v.ID, "finish"); stop(v) != "awaiting_gate/finished" || len(fin) != 1 || !strings.Contains(fin[0], `"lint":[]`) {
		t.Fatalf("%s %q\n%s\n%q", stop(v), fin, f.steps(v.ID), f.outputs(v.ID, "numbers"))
	}
	if !strings.Contains(out, "November 12, 2026 is a calculation from typed inputs as well as verified values (date_add: 1 F1.v1 = 10/21/2026 date; 2 21 days;") {
		t.Errorf("a calculation over typed inputs is not disclosed")
	}
	var key struct {
		Facts []struct {
			ID, Category string
			Detect       struct{ All [][]string }
		}
	}
	b, err := os.ReadFile(filepath.Join(litigation, "answer_key.json"))
	if err == nil {
		err = json.Unmarshal(b, &key)
	}
	if err != nil {
		t.Fatal(err)
	}
	got, _ := analysis.Literal(out)
	checked := 0
	for _, c := range key.Facts {
		if c.Category != "deadline" {
			continue
		}
		checked++
		for _, group := range c.Detect.All {
			if !slices.ContainsFunc(group, func(term string) bool {
				if d, ok := strings.CutPrefix(term, "$date:"); ok {
					return slices.Contains(got, d)
				}
				return strings.Contains(strings.ToLower(out), strings.ToLower(term))
			}) {
				t.Errorf("%s: none of %q in the memo", c.ID, group)
			}
		}
	}
	if checked != 4 {
		t.Fatalf("the answer key has %d deadline criteria, want 4", checked)
	}
	if t.Failed() {
		t.Logf("memo:\n%s", out)
	}
}

// TestRawDeadlineNeedsItsRoll (bench r3, gate A: the memo said the answer was due November 11,
// Veterans Day, though the lead's worksheet rolled it to November 12, while TestDeadlinesMeetTheAnswerKey
// passed): a drafter computed the date again with no holidays and wrote the raw date, and G8 traced
// it to that worksheet and to the rolled worksheet's raw-date step. A deadline's raw date now traces
// only in a paragraph that states the rolled deadline, and the lint names the handle to write.
func TestRawDeadlineNeedsItsRoll(t *testing.T) {
	served, rule := "served on 10/21/2026 via its registered agent", "A defendant must serve an answer within 21 days after being served" // the period a day count traces to (r7 M1)
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true, Values: []analysis.Value{{Entity: "Talbot", Metric: "served", Value: "10/21/2026", Class: "Date"}}},
		Citations: []Citation{{Quote: served, Method: "exact"}, {Quote: rule, Method: "exact"}}, Figures: []analysis.Reading{{Span: "q2.s1", Text: "21", Kind: analysis.KindOther, Amount: "21"}}}
	fs := []analysis.Finding{f1.Finding}
	ops := func() []analysis.Operand {
		ref, n, u := "F1.v1", "21", "days"
		return []analysis.Operand{{Ref: &ref}, {Literal: &n, Unit: &u}}
	}
	rolled, err := analysis.Calc(fs, "date_add", ops(), []string{"2026-11-11"})
	raw, err2 := analysis.Calc(fs, "date_add", ops(), nil) // the drafter's own, with no holidays
	if err != nil || err2 != nil || rolled.Result != "2026-11-12" || raw.Result != "2026-11-11" {
		t.Fatalf("worksheets %+v %+v (%v %v)", rolled, raw, err, err2)
	}
	figs := []Fig{{"{" + rolled.WID + "}", "", sheetValue(rolled.Result, rolled.Unit)}, {"{" + raw.WID + "}", "", sheetValue(raw.Result, raw.Unit)}}
	use := map[string]Finding{"F1": f1}
	for _, c := range []struct {
		md   string
		lint bool
	}{
		{"The raw deadline falls on November 11, 2026, which is Veterans Day [F1]. Talbot must serve its answer by November 11, 2026. [F1]", true}, // the bench memo
		{"Talbot was served on 10/21/2026 [F1] and must answer by {" + raw.WID + "}.", true},
		{"Talbot was served on 10/21/2026 [F1]; the raw date, November 11, 2026, is Veterans Day, so the answer is due {" + rolled.WID + "}.", false},
		{"Talbot was served on 10/21/2026 [F1] and must answer by {" + rolled.WID + "}.\n\nNovember 11, 2026 is Veterans Day.", true}, // the raw date apart from its roll
	} {
		got := lint("I1", Section{Markdown: c.md, Handles: figs}, use, []analysis.Worksheet{rolled, raw})
		if c.lint != (len(got) > 0) || c.lint && !strings.Contains(strings.Join(got, ""), "the deadline is {"+rolled.WID+"} (November 12, 2026)") {
			t.Errorf("%q: lint %q", c.md, got)
		}
	}
}
