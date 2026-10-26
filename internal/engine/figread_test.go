// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// The figure reader (figures.go, lab-r4 r4 owner decision): number meaning is read_figures' labelling,
// Go only finds spans, checks labels and compares. These are the r4 verifier's HIGH and MEDIUM repros
// (critique/biglaw-lab-r4-r4-verifier.md) as scenarios: with the right labels the pipeline does the
// right thing, an unclear label gates instead of verifying, and no label makes a figure the source
// does not state.

func ls(kind, cur, scale, from string) analysis.Label { return lab("", kind, cur, scale, from, "", "") }

// H1: "Two Million (2,000,000) shares" read as a count never admits a typed $2,000,000, never
// becomes a claim's sum, and a memo's "$2,000,000" citing it is flagged (G8), with the reading named.
func TestR4V4SharesAreNoSum(t *testing.T) {
	for _, q := range []string{
		"The Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor.",
		"The Seller holds Five Hundred Thousand (500,000) Units.",
	} {
		for _, v := range []string{"$2,000,000", "$500,000"} {
			if valueInQuote(mv(v, "Money"), q) {
				t.Errorf("typed %s (US dollars) verified from %q", v, q)
			}
			if len(claimSumsIn("The Investor paid "+v+".", q, nil)) > 0 {
				t.Errorf("claimSums promoted %s from %q", v, q)
			}
		}
	}
	if !valueInQuote(mv("2,000,000", "Count"), "The Company shall issue Two Million (2,000,000) shares of Common Stock.") {
		t.Errorf("the share count itself dropped")
	}
}

func TestR4V4SharesNeverReachTheMemoAsDollars(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Issuance\nThe Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The Investor paid $2,000,000 for the shares.", d1, "The Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor",
			val("Investor", "price paid", "$2,000,000", "Money")))},
		"draft_section": {section("The Investor paid $2,000,000 for its shares [F1].", "Paid $2,000,000 [F1].")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || len(fs[0].Values) > 0 || !slices.ContainsFunc(fs[0].ValueNotes, func(n string) bool { return strings.Contains(n, "as a count figure") }) {
		t.Fatalf("a share count became a verified US-dollar value: %+v", fs)
	}
	if strings.Contains(deref(v.Output), "$2,000,000") {
		t.Errorf("the memo states $2,000,000 traced to a share count (%s)", stop(v))
	}
	if fin := strings.Join(f.outputs(v.ID, "finish"), "\n"); !strings.Contains(fin, "the figure 2000000 is not traced") || !strings.Contains(fin, "only as a count figure") {
		t.Errorf("G8 did not flag the memo's $2,000,000 with its reading: %s", fin)
	}
}

// H2: a table header's scale is the reader's, per quote: the header quote's "4,000" reads as
// $4,000,000, the other quote's "$500" as $500, so a typed $500,000 never verifies from it; an
// "except" in the header the reader cannot place is unclear, and gates the finding instead of verifying.
func TestR4V4HeaderScaleIsTheReadersPerQuote(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Revenue\nRevenue (in thousands) 4,000\n\n2. Fees\nThe filing fee is $500 per application.\n")
	f.reads = map[string]analysis.Label{"quote:4,000": ls(analysis.KindMoney, "USD", "1000", "in thousands")}
	d1 := f.docs[0].ID
	x := fd("Revenue was $4,000,000 and the filing fee is $500,000.", d1, "Revenue (in thousands) 4,000",
		val("Company", "revenue", "$4,000,000", "Money"), val("Company", "filing fee", "$500,000", "Money"), val("Company", "filing fee", "$500", "Money"))
	x["quotes"] = append(x["quotes"].([]m), m{"documentId": d1, "quote": "The filing fee is $500 per application", "locator": nil})
	v := f.run(f.with(map[string][]model.Reply{"extract_facts": {facts(x)}}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 {
		t.Fatalf("%+v", fs)
	}
	var got []string
	for _, v := range fs[0].Values {
		got = append(got, v.Value)
	}
	if !slices.Contains(got, "$4,000,000") || !slices.Contains(got, "$500") || slices.Contains(got, "$500,000") {
		t.Errorf("values %v, want $4,000,000 (the header's scale) and $500, never $500,000", got)
	}
}

func TestR4V4UnclearReadingGates(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Results\n(in thousands, except per share data)\nEPS\t1.25\n")
	f.reads = map[string]analysis.Label{"quote:1.25": lab("", analysis.KindUnclear, "unclear", "1", "", "", "")}
	d1 := f.docs[0].ID
	ff := fd("Earnings per share were $1.25.", d1, "EPS\t1.25", val("Company", "earnings per share", "$1.25", "Money"))
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(ff)},
		"draft_section": {section("Earnings per share were {Zephyr} [F1].", "EPS [F1].")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || len(fs[0].Values) != 1 || !fs[0].Low || fs[0].Values[0].Fig == nil || !fs[0].Values[0].Fig.Unclear ||
		!slices.ContainsFunc(fs[0].ValueNotes, func(n string) bool { return strings.Contains(n, "low confidence, gated") }) {
		t.Fatalf("an unclear reading verified, or was dropped: %+v", fs)
	}
	if stop(v) != "awaiting_gate/awaiting_gate" || len(v.PendingGates) != 1 || v.PendingGates[0].GateID != "G-F1" {
		t.Fatalf("the finding is not gated for a lawyer: %s %+v", stop(v), v.PendingGates)
	}
	if _, c, _, ok := analysis.Canon(fs[0].Values[0]); ok {
		t.Errorf("an unclear reading is compared (canon %s)", c)
	}
}

// M2: a range is the reader's: "$5-10 million" read as two ends of one range, each scaled; the
// typed $5 million verifies, a negative one never does, and the memo's "$5 million to $10 million"
// traces.
func TestR4V4RangeIsTheReaders(t *testing.T) {
	q := "Estimated damages: $5-10 million"
	from := "million"
	r5 := lab("q1.s1", analysis.KindMoney, "USD", "1000000", from, "", "")
	r5.IsRange, r5.RangeWith = true, &[]string{"q1.s2"}[0]
	rs, notes := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", q)}, []analysis.Label{r5, lab("q1.s2", analysis.KindMoney, "USD", "1000000", from, "", "")})
	if len(notes) > 0 || rs["q1.s1"].Amount != "5000000" || rs["q1.s1"].RangeWith != "q1.s2" || rs["q1.s2"].Amount != "10000000" {
		t.Fatalf("readings %+v %q", rs, notes)
	}
	for _, c := range []struct {
		v  string
		ok bool
	}{{"$5 million", true}, {"-$5 million", false}, {"$10 million", true}} {
		v := mv(c.v, "Money")
		own := oracleReadings("", nil, []analysis.Value{v})
		for k, r := range own {
			rs[k] = r
		}
		if _, ok, _, _ := valueStated(v, 0, rs, q); ok != c.ok {
			t.Errorf("%s admitted %v from %q, want %v", c.v, ok, q, c.ok)
		}
	}
	memo := oracleSections("Damages are estimated at $5 million to $10 million [F1].")
	use := map[string]Finding{"F1": {Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: q}}, Figures: []analysis.Reading{rs["q1.s1"], rs["q1.s2"]}}}
	if got := lint("I1", memo[0], use, nil); len(got) > 0 {
		t.Errorf("G8 flags a correct range: %q", got)
	}
}

// The reader may label only the spans Go gave it, and no label makes a figure the source does not
// state: a label for a span it was not given is rejected (and noted on the finding); a scale that
// is no power of ten, or one from words the passage does not hold, is unclear; so a typed $7,777
// never verifies from a quote stating $500, whatever the labels.
func TestR4V4ReaderCannotInventSpansOrFigures(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe filing fee is $500 per application.\n")
	d1 := f.docs[0].ID
	labels := scriptedLabels(
		lab("1.q1.s1", analysis.KindMoney, "USD", "15.554", "fee", "", ""), // 500 × 15.554 = 7,777
		lab("1.q1.s7", analysis.KindMoney, "USD", "1", "", "", ""),         // no such span
		lab("1.v1.s1", analysis.KindMoney, "USD", "1", "", "", ""),
		lab("1.v2.s1", analysis.KindMoney, "USD", "1000", "thousands", "", ""), // the value says no "thousands"
	)
	x := fd("The filing fee is $7,777.", d1, "The filing fee is $500 per application", val("Court", "filing fee", "$7,777", "Money"), val("Court", "filing fee", "$500", "Money"))
	v := f.run(f.with(map[string][]model.Reply{"extract_facts": {facts(x)}, "read_figures": {labels, labels}}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 {
		t.Fatalf("%+v", fs)
	}
	for _, v := range fs[0].Values {
		if v.Value == "$7,777" || v.Value == "$500" && (v.Fig == nil || !v.Fig.Unclear) {
			t.Errorf("a figure the quote does not state, or one read from an unchecked label, verified: %+v", v)
		}
	}
	if !slices.ContainsFunc(fs[0].ValueNotes, func(n string) bool { return strings.Contains(n, `"q1.s7" was rejected`) }) {
		t.Errorf("the label for a span not given was not rejected on the record: %q", fs[0].ValueNotes)
	}
	if !slices.ContainsFunc(fs[0].ValueNotes, func(n string) bool { return strings.Contains(n, `"$7,777"`) && strings.Contains(n, "not admitted") }) {
		t.Errorf("$7,777 not noted as dropped: %q", fs[0].ValueNotes)
	}
}

// The reader is called once per admission (the call's findings batched), and its answer is cached by
// what it read: the same finding read again (a rerun, another task over the same documents) gets the
// same labels and makes no call.
func TestR4V4ReadingIsCachedByContent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	two := facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices", val("Greystone", "damages", "$644,910", "Money")),
		fd("The Notice of Dispute was delivered on September 25, 2026.", d1, "The Notice of Dispute was delivered on September 25, 2026", val("Greystone", "notice date", "September 25, 2026", "Date")))
	reads := func() (int, int) {
		_, fake := f.engine(f.with(map[string][]model.Reply{"extract_facts": {two}}), Config{})
		task := f.submit(0)
		f.step(context.Background())
		n, findings := 0, 0
		for _, c := range fake.Calls() {
			if c.Agent == "read_figures" {
				n++
				var in readRequest
				requestOf(c, &in)
				findings += len(in.Findings)
			}
		}
		if v := f.get(task.ID); v.Output == nil {
			t.Fatalf("%s %v", stop(v), v.Error)
		}
		return n, findings
	}
	if n, items := reads(); n != 1 || items != 2 {
		t.Fatalf("first run: %d reader calls for %d findings, want one call for both", n, items)
	}
	if n, _ := reads(); n != 0 {
		t.Fatalf("the second run read the same findings again: %d calls", n)
	}
	var stored int
	if err := f.st.DB().QueryRow(`SELECT count(*) FROM figure_reads`).Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("figure_reads holds %d rows (%v), want one per finding", stored, err)
	}
}

// The reader's request is what its schema says, its span ids are stable, and figure_context and
// exact_calc are its tools only: the lead's tool list never offers them.
func TestR4V4ReaderToolsAreItsOwn(t *testing.T) {
	if slices.Contains(MemoVisible, "read_figures") || slices.Contains(MemoVisible, "figure_context") || slices.Contains(MemoVisible, "exact_calc") {
		t.Fatal("the lead is offered the figure reader or its tools")
	}
	s := spec(context.Background(), "read_figures")
	if s.Role != model.Critic || !slices.Equal(s.Allowed, []string{"figure_context", "exact_calc"}) {
		t.Fatalf("read_figures spec %+v", s)
	}
	it := findingReading(Finding{Finding: analysis.Finding{Claim: "Paid $5."}}, []analysis.Quote{{DocumentID: "d", Quote: "a fee of $5 and 3 items"}}, nil)
	data, _ := json.Marshal(it.in)
	if !strings.Contains(string(data), `"id":"q1.s1","text":"5"`) || !strings.Contains(string(data), `"id":"q1.s2","text":"3"`) || !strings.Contains(string(data), `"id":"c.s1"`) {
		t.Fatalf("request passages %s", data)
	}
	calc := exactCalc{builtinSpecs.Spec("exact_calc")}
	for _, c := range []struct{ op, a, b, want string }{{"mul", "4217", "1000", "4217000"}, {"add", "0.1", "0.2", "0.3"}, {"div", "1", "3", "1/3"}, {"cmp", "92600", "103800.00", "-1"}, {"sub", "-1250.50", "1", "-1251.5"}} {
		in, _ := json.Marshal(m{"op": c.op, "a": c.a, "b": c.b})
		res, err := calc.Call(context.Background(), agentCall(in))
		var out struct{ Result string }
		if json.Unmarshal(res.Output, &out); err != nil || out.Result != c.want {
			t.Errorf("%s %s %s = %q %v, want %s", c.a, c.op, c.b, out.Result, err, c.want)
		}
	}
	in, _ := json.Marshal(m{"op": "add", "a": "4,217", "b": "1"})
	if _, err := calc.Call(context.Background(), agentCall(in)); err == nil {
		t.Error("exact_calc read a grouped number")
	}
}

// figure_context reads the task's document around a quote, before or after it, a bounded page at a
// time, and never a document outside the task.
func TestR4V4FigureContextReadsAroundTheQuote(t *testing.T) {
	t.Parallel()
	head := "Statement of operations (in thousands, except per share data)\n"
	f := newFixture(t, head+strings.Repeat("Line item\t1,000\n", 200)+"Revenue\t4,000\n"+"Note: amounts are unaudited.\n")
	f.engine(nil, Config{})
	tool := f.e.figureContextTool(builtinSpecs)
	call := func(quote, side string, off *int) (string, *int, error) {
		in, _ := json.Marshal(m{"documentId": f.docs[0].ID, "quote": quote, "side": side, "offset": off})
		res, err := tool.Call(context.Background(), agent.Call{DocumentIDs: f.ids(), Input: in})
		var out struct {
			Text string
			Next *int
		}
		_ = json.Unmarshal(res.Output, &out)
		return out.Text, out.Next, err
	}
	after, next, err := call("Revenue\t4,000", "after", nil)
	if err != nil || !strings.Contains(after, "unaudited") || next != nil {
		t.Fatalf("after: %q %v %v", after, next, err)
	}
	text, next, err := call("Revenue\t4,000", "before", nil)
	for k := 0; err == nil && next != nil && !strings.Contains(text, "except per share") && k < 10; k++ {
		text, next, err = call("Revenue\t4,000", "before", next)
	}
	if err != nil || !strings.Contains(text, "except per share") || len(text) > contextBytes {
		t.Fatalf("the header was not reached page by page: %q %v", text, err)
	}
	if _, _, err := call("a quote that is nowhere", "after", nil); err == nil {
		t.Error("a quote the document does not hold was read around")
	}
	in, _ := json.Marshal(m{"documentId": f.docs[0].ID, "quote": "Revenue", "side": "after", "offset": nil})
	if _, err := tool.Call(context.Background(), agent.Call{DocumentIDs: nil, Input: in}); err == nil {
		t.Error("a document outside the task's scope was read")
	}
}

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

// bench7 r1 (verifier 1): the section's reader call SUCCEEDS but gives the memo span no label. The memo's
// "$2,000,000 paid" (money) cites a finding whose quote the reader read clearly as a 2,000,000
// share COUNT. At cfa9fcb this was a blocking G8 line ("not traced"); at ef9e6e4 the omitted
// label is "not read: the figure reader gave it no label", which matches limitedMemoUnread's
// HasPrefix("not read") and the figure is delivered, traced as written by digits, with
// Limitations text claiming the reader's call "could not be funded or failed".
func TestUnlabelledMemoSpanIsNoCallFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Issuance\nThe Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor.\n")
	d1 := f.docs[0].ID
	_, fake := f.engine(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The Company shall issue 2,000,000 shares to the Investor.", d1, "The Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor",
			val("Company", "shares issued", "2,000,000", "Count")))},
		"draft_section": {section("The Investor paid $2,000,000 for its shares [F1].", "Paid [F1].")},
	}), Config{})
	onReader(fake, readerOracle, func(in readRequest) (model.Reply, bool) { // answered, but labelled nothing
		return model.Reply{Text: `{"labels":[]}`}, len(in.Findings) > 0 && in.Findings[0].Passages[0].Kind == "memo"
	})
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	fs := ledgerFindings(t, f, v.ID)
	out := deref(v.Output)
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	t.Logf("stop %s; F1 figures %+v", stop(v), fs[0].Figures)
	t.Logf("finish:\n%s", fin)
	t.Logf("output:\n%s", out)
	if strings.Contains(out, "$2,000,000") && !strings.Contains(fin, "not traced") {
		t.Errorf("memo money figure with no label delivered, traced by digits to a share count (stop %s); gated=%v",
			stop(v), slices.ContainsFunc(v.PendingGates, func(g Gate) bool { return g.GateID == "G-F1" }))
	}
	if strings.Contains(out, "its call could not be funded or failed") {
		t.Errorf("disclosure says the call could not be funded or failed, but it answered")
	}
}

// bench7 r1 (verifier 2): bench5 F388 again, but stating no typed value (the very shape claimSums exists for: the
// $103,800.00 sum in its claim and quote, typed nowhere). At ef9e6e4 such a finding is admitted
// unread and read only when a section cites it, so its claim sum never exists and finish's join
// never pairs $92,600 with $103,800.00 unless the memo happens to cite F2.
func TestValuelessClaimSumReachesTheJoin(t *testing.T) {
	t.Parallel()
	f := newFixture(t, compRef, compRows)
	f.parties = map[string]string{"92,600": "Marcus T. Bellini", "103,800.00": "Marcus T. Bellini"}
	f.measures = map[string]string{"92,600": "undisclosed compensation paid for Bellini", "103,800.00": "undisclosed compensation paid for Bellini"}
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	f752 := fd("Bellini received undisclosed compensation from Ostrowski totaling approximately $92,600 in connection with a directed brokerage arrangement with Lakeshore Trading.", d1,
		"The total undisclosed compensation received by Bellini, in the form of payments made by Ostrowski to Bayshore Palms Golf & Country Club for Bellini's benefit, was approximately $92,600 ($45,000 initiation fee + $47,600 in monthly dues = $92,600).",
		val("Bellini", "total undisclosed compensation", "$92,600", "Money"))
	f388 := fd("Kevin Ostrowski's personal checking account at Coastal Commerce Bank was used to make payments totaling $103,800.00 to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini between May 3, 2022 and January 2, 2024.", d2,
		"Total of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini | 05/03/2022 – 01/02/2024 | 22 | | $103,800.00")
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What compensation did Bellini receive?", "RedFlag"))), calls(extract(d1, "compensation", "I1")), calls(extract(d2, "payments", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("check"))},
		"extract_facts": {facts(f752), facts(f388)},
		"draft_section": {section("Bellini received about $92,600 [F1].", "s")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	fin := strings.Join(f.outputs(v.ID, "finish"), "")
	t.Logf("F2 unread=%v values=%v", fs[1].Unread, fs[1].Values)
	if !strings.Contains(fin, "[F1] $92,600 vs [F2] $103,800.00") {
		t.Errorf("the bench5 conflict is no longer proposed when F388 states no typed value (F2 unread=%v)", fs[1].Unread)
	}
}

// bench7 r1 (verifier 3): free-text "decimal" now admits "" (the schema dropped its enum, no minLength); Go's
// pre-existing branch reads "" as "none", so a label that never says which mark is the decimal
// point reads "1.200" as one thousand two hundred, clearly.
func TestEmptyDecimalIsUnclear(t *testing.T) {
	p := analysis.PassageOf("q1", "The fee is 1.200 per unit.")
	l := lab("q1.s1", analysis.KindCount, "", "1", "", "", "")
	l.Decimal = ""
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, []analysis.Label{l})
	r := rs["q1.s1"]
	t.Logf("%+v", r)
	if !r.Unclear {
		t.Errorf("decimal \"\" read clearly as amount %s", r.Amount)
	}
}

// bench7 r1 (verifier 6): the span's-own-zeros scale rule only ever lowers to scale 1; ×1000 needs words outside the span.
func TestOwnZerosScaleNeverMultiplies(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, c := range []struct {
		text, from, scale, want string
		unclear                 bool
	}{
		{"The fee was $5,000.", "000", "1000", "5000", false},
		{"The fee was $5,000.", ",000", "1000000", "5000", false},
		{"The fee was $5,000.", "5,000", "1000", "5000", true},
		{"The fee was $5,000 000 total.", "000", "1000", "", false},  // a second 000 outside the span: Go cannot tell
		{"Amounts ($000)\nFee\t$5,000", "000", "1000", "5000", true}, // the header states "000" too: which it read Go cannot tell, so unclear
		{"The fee was $15,000.", "000", "1000", "15000", false},
	} {
		p := passageQ1(c.text)
		id := p.ID + ".s1"
		for i, sp := range p.Spans { // the fee's own span (a header's "$000" is a span too)
			if strings.HasSuffix(sp.Text, ",000") {
				id = analysis.SpanID(p.ID, i)
				break
			}
		}
		l := lab(id, analysis.KindMoney, "USD", c.scale, "", "", "")
		l.ScaleFrom = str(c.from)
		rs, _ := analysis.ReadLabels([]analysis.Passage{p}, []analysis.Label{l})
		r := rs[id]
		t.Logf("%q from %q x%s -> amount %s scale %q unclear %v (%s)", c.text, c.from, c.scale, r.Amount, r.Scale, r.Unclear, r.Why)
		if c.want != "" && (r.Amount != c.want || r.Unclear != c.unclear) {
			t.Errorf("%q: %+v", c.text, r)
		}
	}
}

func passageQ1(text string) analysis.Passage { return analysis.PassageOf("q1", text) }

// killAtCall stops the process at the at-th call of one agent (it never reaches the Fake).
type killAtCall struct {
	*model.Fake
	mu    sync.Mutex
	agent string
	n, at int
	kill  func()
}

func (k *killAtCall) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	k.mu.Lock()
	k.n += btoi(req.Agent == k.agent)
	hit := req.Agent == k.agent && k.n == k.at && k.kill != nil
	k.mu.Unlock()
	if hit {
		k.kill()
		return nil, context.Canceled
	}
	return k.Fake.Chat(ctx, req)
}

// bench7 r1 (verifier 5a): a process killed at the sweep's end-of-sweep batched reader call commits none of the
// sweep; the resumed run's findings (values, figures, unread flags) and reader stats equal an
// uninterrupted run's.
func TestResumeAtSweepEndRead(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&doc, "%d. Lot %d\nThe supplier shall be paid $%d,000 for lot %d.\n%s\n\n", i, i, 100+i, i, filler)
	}
	runIt := func(killAt int) (*fixture, Task) {
		f := newFixture(t, doc.String())
		d1 := f.docs[0].ID
		sc := f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "What is the supplier paid?", "Conduct"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
			"draft_section": {section("Lot 1 costs $101,000 [F1].", "s")}})
		for range 2 {
			for i := 1; i <= 6; i++ {
				q := fmt.Sprintf("The supplier shall be paid $%d,000 for lot %d", 100+i, i)
				k := fmt.Sprintf("lot%d", i)
				sc[k] = append(sc[k], facts(on("I1", fd(fmt.Sprintf("Lot %d costs $%d,000.", i, 100+i), d1, q, val("Supplier", fmt.Sprintf("lot %d price", i), fmt.Sprintf("$%d,000", 100+i), "Money")))))
			}
		}
		k := &killAtCall{Fake: newFake(sc), agent: "read_figures", at: killAt}
		k.Route = func(req model.Request) string {
			if req.Agent != "extract_facts" {
				return req.Agent
			}
			if m := regexp.MustCompile(`for lot (\d+)\.`).FindStringSubmatch(sweepReq(req).SectionText); m != nil {
				return "lot" + m[1]
			}
			return req.Agent
		}
		e := f.reopen(k, nil)
		task := f.submit(0)
		if killAt > 0 {
			k.kill = func() { e.active(task.ID).cancel() }
			f.step(context.Background())
			if st := f.steps(task.ID); !strings.HasSuffix(st, "4:tool_call:sweep:started") {
				t.Fatalf("first process: %s", st)
			}
			k.kill = nil
			f.reopen(k, nil)
			f.stale(task.ID)
		}
		f.step(context.Background())
		return f, task
	}
	ref, want := runIt(0)
	f, task := runIt(1)
	norm := func(f *fixture, id string) string {
		var b strings.Builder
		for _, r := range f.ledger(id) {
			if r.Kind == kFinding || r.Kind == kReadStat {
				d := r.Data
				if r.Kind == kReadStat { // usage may differ by lost-call accounting; keep counts
					var st ReaderStats
					_ = json.Unmarshal([]byte(d), &st)
					d = fmt.Sprintf("calls=%d failed=%d unfunded=%d spans=%d unread=%d sweepCalls=%d", st.Calls, st.Failed, st.Unfunded, st.Spans, st.Unread, st.Sweep.Calls)
				}
				b.WriteString(r.Kind + " " + r.Key + " " + d + "\n")
			}
		}
		return strings.ReplaceAll(b.String(), f.docs[0].ID, "D1")
	}
	v := f.get(task.ID)
	a, w := norm(f, task.ID), norm(ref, want.ID)
	if stop(v) != stop(ref.get(want.ID)) || a != w {
		t.Fatalf("%s vs %s: resumed ledger\n%s\nwant\n%s", stop(v), stop(ref.get(want.ID)), a, w)
	}
	s1, _ := f.e.classSpend(context.Background(), task.ID)
	s0, _ := ref.e.classSpend(context.Background(), want.ID)
	var lost json.RawMessage
	f.st.DB().QueryRow(`SELECT lost FROM tasks WHERE id = $1`, task.ID).Scan(&lost)
	t.Logf("resumed: sweep %+v read %+v evidence %+v; lost %s", s1["sweep"], s1["read"], s1["evidence"], lost)
	t.Logf("ref:     sweep %+v read %+v evidence %+v", s0["sweep"], s0["read"], s0["evidence"])
}

// bench7 r1 (verifier 5b): killed at the lazy read a drafted section makes of a cited finding that states no typed
// value (its reader call holds the memo and the finding): the resumed run equals an uninterrupted one.
func TestResumeAtLazySectionRead(t *testing.T) {
	t.Parallel()
	runIt := func(killAt int) (*fixture, Task) {
		f := newFixture(t)
		d1 := f.docs[0].ID
		sc := f.with(map[string][]model.Reply{
			"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices"))},
			"draft_section": {section("Greystone claims damages of $644,910 for unpaid invoices [F1].", "Damages [F1].")},
		})
		sc["draft_section"] = append(sc["draft_section"], sc["draft_section"]...)
		sc["lead"] = append(sc["lead"], sc["lead"]...)
		k := &killAtCall{Fake: newFake(sc), agent: "read_figures", at: killAt}
		e := f.reopen(k, nil)
		task := f.submit(0)
		if killAt > 0 {
			k.kill = func() { e.active(task.ID).cancel() }
			f.step(context.Background())
			t.Logf("first process: %s", f.steps(task.ID))
			k.kill = nil
			f.reopen(k, nil)
			f.stale(task.ID)
		}
		f.step(context.Background())
		return f, task
	}
	ref, want := runIt(0)
	f, task := runIt(1)
	norm := func(f *fixture, id string) string {
		var b strings.Builder
		for _, r := range f.ledger(id) {
			if r.Kind == kFinding || r.Kind == kSection {
				b.WriteString(r.Kind + " " + r.Key + " " + r.Data + "\n")
			}
		}
		return strings.ReplaceAll(b.String(), f.docs[0].ID, "D1")
	}
	v := f.get(task.ID)
	if a, w := norm(f, task.ID), norm(ref, want.ID); stop(v) != stop(ref.get(want.ID)) || a != w {
		t.Fatalf("%s vs %s:\n%s\nwant\n%s", stop(v), stop(ref.get(want.ID)), a, w)
	}
	t.Logf("ok %s", stop(v))
}

// bench7 r1 (verifier 8): a sweep whose later visit hits a model error returns that error before its end-of-sweep
// read; the step's buffered writes still commit (Record commits any non-started status), so the
// earlier visits' findings stay in the ledger admitted Unread with their typed values never checked
// (valueStated never ran). What does the run do next, and what does a reader of the ledger see?
func TestFailedSweepDropsUncheckedValues(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&doc, "%d. Lot %d\nThe supplier shall be paid $%d,000 for lot %d.\n%s\n\n", i, i, 100+i, i, filler)
	}
	f := newFixture(t, doc.String())
	d1 := f.docs[0].ID
	sc := f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "What is the supplier paid?", "Conduct"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil})),
		calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"draft_section": {section("Lot 1 costs $999,000 [F1].", "s")}})
	for i := 1; i <= 4; i++ {
		q := fmt.Sprintf("The supplier shall be paid $%d,000 for lot %d", 100+i, i)
		k := fmt.Sprintf("lot%d", i)
		// lot 1's analyst typed a value its quote does NOT state ($999,000): valueStated would drop it
		v := fmt.Sprintf("$%d,000", 100+i)
		if i == 1 {
			v = "$999,000"
		}
		r := facts(on("I1", fd(fmt.Sprintf("Lot %d costs %s.", i, v), d1, q, val("Supplier", fmt.Sprintf("lot %d price", i), v, "Money"))))
		if i == 4 {
			r = model.Reply{Err: model.ErrUnavailable}
		}
		sc[k] = []model.Reply{r, r, r, r}
	}
	fake := newFake(sc)
	fake.Route = func(req model.Request) string {
		if req.Agent != "extract_facts" {
			return req.Agent
		}
		if mm := regexp.MustCompile(`for lot (\d+)\.`).FindStringSubmatch(sweepReq(req).SectionText); mm != nil {
			return "lot" + mm[1]
		}
		return req.Agent
	}
	f.e = New(f.st, f.d, fake, testRoles, nil, Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	t.Logf("stop %s; steps %s; error %v", stop(v), f.steps(task.ID), v.Error)
	fs, _ := load[Finding](context.Background(), f.e, task.ID, kFinding)
	for _, x := range fs {
		t.Logf("%s unread=%v values=%v figures=%d notes=%q", x.FID, x.Unread, x.Values, len(x.Figures), x.ValueNotes)
		if x.Unread && len(x.Values) > 0 {
			t.Errorf("%s committed Unread with unchecked typed values %v", x.FID, x.Values)
		}
	}
	t.Logf("output: %s", deref(v.Output))
	for _, fv := range v.Findings {
		t.Logf("view %+v", fv)
	}
}

// bench7 r1 (verifier 9): limitedMemoUnread when the only ground stating the memo's unread figure is a worksheet's
// result (no cited finding's quote or value writes it): what is listed, what is gated?
func TestUnreadMemoWorksheetOnlyIsFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe first instalment is $5,000. The second instalment is $3,000.\n")
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	_, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What are the fees?", "Conduct"))), calls(extract(d1, "instalment", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "sum", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The instalments are $5,000 and $3,000.", d1,
			"The first instalment is $5,000. The second instalment is $3,000",
			val("Company", "first instalment", "$5,000", "Money"), val("Company", "second instalment", "$3,000", "Money")))},
		"draft_section": {section("The instalments total $8,000 [F1].", "Fees [F1].")},
	}), Config{})
	onReader(fake, readerOracle, func(in readRequest) (model.Reply, bool) {
		return model.Reply{Text: "not json"}, len(in.Findings) > 0 && in.Findings[0].Passages[0].Kind == "memo"
	})
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	fin, out := strings.Join(f.outputs(v.ID, "finish"), "\n"), deref(v.Output)
	t.Logf("stop %s gated %v\nfinish %s\n%s", stop(v), gated, fin, out)
	if strings.Contains(out, "to []") || strings.Contains(out, "total $8,000") && !strings.Contains(out, "is not traced") || !strings.Contains(fin, "the figure 8000 is not traced (G8)") {
		t.Errorf("an unread memo figure only a worksheet states is not flagged, or is listed as gated by no finding (stop %s):\n%s\n%s", stop(v), fin, out)
	}
}

// bench7 r1 (verifier 10): r11 H (TestRefOnlyScaledSumDisclosedAndGated) with the section's reader call failing: one
// wrong scale ("$5 billion" and "$3 billion" read x10^6 from "billion") summed by ref, the memo's
// "$8,000,000". At cfa9fcb the unread memo figure was a blocking G8 line. At ef9e6e4 it matches the
// worksheet's result as written, so it is limitedMemoUnread, which `continue`s past the worksheet
// listing: no ⚠ line with inputs, and F1 is NOT gated ("to [], whose findings are gated").
func TestUnreadMemoScaledSumNeverUngated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe first instalment is $5 billion. The second instalment is $3 billion.\n")
	wrong := lab("", analysis.KindMoney, "USD", "1000000", "billion", "", "")
	f.reads = map[string]analysis.Label{"quote:5": wrong, "quote:3": wrong, "value:5": wrong, "value:3": wrong}
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	_, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What are the fees?", "Conduct"))), calls(extract(d1, "instalment", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "sum", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The instalments are $5 billion and $3 billion.", d1,
			"The first instalment is $5 billion. The second instalment is $3 billion",
			val("Company", "first instalment", "$5 billion", "Money"), val("Company", "second instalment", "$3 billion", "Money")))},
		"draft_section": {section("The instalments total $8,000,000 [F1].", "Fees [F1].")},
	}), Config{})
	oracle := fake.Respond
	onReader(fake, oracle, func(in readRequest) (model.Reply, bool) { // the section's reader call fails
		return model.Reply{Text: "not json"}, len(in.Findings) > 0 && in.Findings[0].Passages[0].Kind == "memo"
	})
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	out := deref(v.Output)
	t.Logf("stop %s gated %v\n%s", stop(v), gated, out)
	if strings.Contains(out, "total $8,000,000") && (!strings.Contains(out, "⚠ $8,000,000 = $5,000,000 [F1]") || !gated) {
		t.Errorf("$8,000,000 (the record: $8 billion) by a ref-only sum delivered, not listed ⚠ or not gated (gated %v); stop %s", gated, stop(v))
	}
}

// r1 LOW-5: finish's judge keeps a floor when the root is below the drafting share's keep.
func TestFinishJudgeHasAFloor(t *testing.T) {
	if g := judgeGrant(agent.Grant{Calls: 40, USD: 2}, agent.Usage{Calls: 10, USD: 0.4}); g.Calls != 7 || g.USD != judgeShare*1.6 {
		t.Errorf("beside the keep: %+v", g)
	}
	if g := judgeGrant(agent.Grant{Calls: 6, USD: 0.2}, agent.Usage{Calls: 20, USD: 1}); g.Calls != 1 || g.USD != judgeShare*0.2 {
		t.Errorf("below the keep: %+v, want one call", g)
	}
	if g := judgeGrant(agent.Grant{Calls: 1, USD: 0.1}, agent.Usage{Calls: 20, USD: 1}); g.Calls != 0 {
		t.Errorf("one call left: %+v", g)
	}
}

// r1 LOW-2: a batched reader call that fails is read again in halves, once, so one failure does not
// drop every finding in it.
func TestFailedReaderBatchSplitInHalves(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, fake := f.engine(nil, Config{})
	onReader(fake, readerOracle, func(in readRequest) (model.Reply, bool) { // the batch of four fails; its halves do not
		return model.Reply{Text: "not json"}, len(in.Findings) > 2
	})
	task := f.submit(0)
	rt, err := f.e.runtime(&run{e: f.e, t: task})
	if err != nil {
		t.Fatal(err)
	}
	var items []*reading
	for i := range 4 {
		items = append(items, findingReading(Finding{Finding: analysis.Finding{Claim: "Fee."}}, []analysis.Quote{{DocumentID: f.docs[0].ID, Quote: fmt.Sprintf("Lot %d costs $%d,000.", i, 100+i)}}, nil))
	}
	if err := f.e.readFigures(context.Background(), rt, agent.Call{TaskID: task.ID, DocumentIDs: task.DocumentIDs}, agent.NewBudget(50, 50, 0), "test", items); err != nil {
		t.Fatal(err)
	}
	for i, it := range items {
		if rs, _ := it.readings(); !it.read || slices.ContainsFunc(slices.Collect(maps.Values(rs)), func(r analysis.Reading) bool { return r.Unclear }) {
			t.Errorf("item %d not read after the split: %+v", i, rs)
		}
	}
}

// r1 MEDIUM-3: claims the reader could not read before the join (the valueless F2, and F1 whose read failed) are counted in
// Limitations, never silent.
func TestUnreadClaimsDisclosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, compRef, compRows)
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	f752 := fd("Bellini received undisclosed compensation of about $92,600.", d1,
		"The total undisclosed compensation received by Bellini, in the form of payments made by Ostrowski to Bayshore Palms Golf & Country Club for Bellini's benefit, was approximately $92,600 ($45,000 initiation fee + $47,600 in monthly dues = $92,600).",
		val("Bellini", "total undisclosed compensation", "$92,600", "Money"))
	f388 := fd("Ostrowski's account paid $103,800.00 to Bayshore Palms on behalf of Bellini.", d2,
		"Total of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini | 05/03/2022 – 01/02/2024 | 22 | | $103,800.00")
	v := f.run(f.with(map[string][]model.Reply{"read_figures": {},
		"lead": {calls(plan(issue("I1", "What compensation did Bellini receive?", "RedFlag"))), calls(extract(d1, "compensation", "I1")), calls(extract(d2, "payments", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit")), calls(finishCall("submit")), calls(finishCall("submit"))},
		"extract_facts": {facts(f752), facts(f388)},
		"draft_section": {section("Bellini received about $92,600 [F1].", "s")},
	}), Config{})
	task, err := f.e.task(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := finish{e: f.e, r: &run{e: f.e, t: task}}.check(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Join(rep.limits, "\n")
	if !strings.Contains(out, "Figures: 2 findings whose claims state a figure were never read (the figure check ran out of budget or failed), so the cross-document comparison could not see their claims' sums: F1, F2.") {
		t.Errorf("the unread claim is not disclosed (%s):\n%s", stop(v), out)
	}
}

// onReader answers a read_figures call with answer when it says so (its request decoded), and
// every other call, or one answer leaves, with next.
func onReader(fake *model.Fake, next func(model.Request) (model.Reply, bool), answer func(in readRequest) (model.Reply, bool)) {
	fake.Respond = func(req model.Request) (model.Reply, bool) {
		if req.Agent != "read_figures" {
			return model.Reply{}, false
		}
		var in readRequest
		requestOf(req, &in)
		if r, ok := answer(in); ok {
			return r, true
		}
		return next(req)
	}
}
