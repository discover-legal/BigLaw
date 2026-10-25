// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
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
			if valueInQuote(mv3(v, "Money"), q) {
				t.Errorf("typed %s (US dollars) verified from %q", v, q)
			}
			if len(claimSumsIn("The Investor paid "+v+".", q, nil)) > 0 {
				t.Errorf("claimSums promoted %s from %q", v, q)
			}
		}
	}
	if !valueInQuote(mv3("2,000,000", "Count"), "The Company shall issue Two Million (2,000,000) shares of Common Stock.") {
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
		v := mv3(c.v, "Money")
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
	s := spec("read_figures")
	if s.Role != model.Critic || !slices.Equal(s.Allowed, []string{"figure_context", "exact_calc"}) {
		t.Fatalf("read_figures spec %+v", s)
	}
	it := findingReading(Finding{Finding: analysis.Finding{Claim: "Paid $5."}}, []analysis.Quote{{DocumentID: "d", Quote: "a fee of $5 and 3 items"}}, nil)
	data, _ := json.Marshal(it.in)
	if !strings.Contains(string(data), `"id":"q1.s1","text":"5"`) || !strings.Contains(string(data), `"id":"q1.s2","text":"3"`) || !strings.Contains(string(data), `"id":"c.s1"`) {
		t.Fatalf("request passages %s", data)
	}
	calc := exactCalc{}
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
	tool := f.e.figureContextTool()
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
