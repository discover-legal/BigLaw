// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

// lab-r4 r5 verifier repros (critique/biglaw-lab-r4-r5-verifier.md), in the tree: each failed at
// 0d42d7f.

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// H1 (unit): a scale label passes ReadLabels whenever scaleFrom is ANY substring of the passage
// ("fee", "the", "e") or ANY span id of the item (the typed value's own "v1.s1"): "$500" becomes a
// clear $500,000 and admits a typed $500,000 verified, not gated.
func TestR5ScaleFromAnyText(t *testing.T) {
	q := "The filing fee is $500 per application."
	for _, from := range []string{"fee", "the", "e", "v1.s1"} {
		ps := []analysis.Passage{analysis.PassageOf("q1", q), analysis.PassageOf("v1", "$500,000")}
		rs, notes := analysis.ReadLabels(ps, []analysis.Label{
			lab("q1.s1", analysis.KindMoney, "USD", "1000", from, "", ""),
			lab("v1.s1", analysis.KindMoney, "USD", "", "", "", ""),
		})
		_, ok, low, _ := valueStated(mv3("$500,000", "Money"), 0, rs, q)
		if ok && !low {
			t.Errorf("scaleFrom %q: typed $500,000 VERIFIED (not gated) from %q (refusal notes %v; reading %+v)", from, q, notes, rs["q1.s1"])
		}
	}
}

// H1 (unit, G8): a memo's "$5 billion" read with scale 10^6 "from" the word "billion" traces to a cited
// "$5 million": the scale's value is never tied to its source words.
func TestR5MemoScaleMisreadTraces(t *testing.T) {
	memo := "The settlement is $5 billion."
	p := analysis.PassageOf("m", memo)
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, []analysis.Label{lab("m.s1", analysis.KindMoney, "USD", "1000000", "billion", "", "")})
	read := map[int]analysis.Reading{}
	for _, r := range rs {
		read[r.Start] = r
	}
	known := []analysis.Reading{{Kind: analysis.KindMoney, Currency: "USD", Amount: "5000000", Text: "5"}}
	if got := g8(memo, "The settlement is $5 million.", 0, nil, read, known, nil, nil, nil, nil); len(got) == 0 {
		t.Errorf("G8 traces a memo's $5 billion to a quote's $5 million (reading %+v)", rs["m.s1"])
	}
}

// H1 (e2e): one label with scaleFrom "fee" turns the quote's $500 into a verified $500,000 value, and
// the memo's "$500,000" is delivered with no G8 line and no gate.
func TestR5ScaleSmuggleReachesMemo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe filing fee is $500 per application.\n")
	f.reads = map[string]analysis.Label{"quote:500": lab("", analysis.KindMoney, "USD", "1000", "fee", "", "")}
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The filing fee is $500,000.", d1, "The filing fee is $500 per application", val("Company", "filing fee", "$500,000", "Money")))},
		"draft_section": {section("The filing fee is $500,000 per application [F1].", "Fee $500,000 [F1].")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) == 1 && len(fs[0].Values) == 1 && !fs[0].Low {
		t.Errorf("typed $500,000 verified (not gated) from a quote stating $500: %+v", fs[0].Values[0].Fig)
	}
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n") // flagged, or (r7 M4: the scaled-cited direction as the other) limited, disclosed and gated
	if strings.Contains(deref(v.Output), "$500,000") && !strings.Contains(fin, "not traced") && !strings.Contains(deref(v.Output), "written at another scale") {
		t.Errorf("memo states $500,000 from a $500 quote; stop %s, no G8 line, no Limitations line", stop(v))
	}
}

// M (unit): a scale with NO source is marked unclear but still applied: "$500" reads 500000 (unclear)
// and a typed $500,000 is admitted (gated) instead of refused; the scale should fall back to 1 as the
// non-power-of-ten branch does.
func TestR5ScaleFromNothingStillApplied(t *testing.T) {
	q := "The filing fee is $500 per application."
	ps := []analysis.Passage{analysis.PassageOf("q1", q), analysis.PassageOf("v1", "$500,000")}
	rs, _ := analysis.ReadLabels(ps, []analysis.Label{
		lab("q1.s1", analysis.KindMoney, "USD", "1000", "", "", ""),
		lab("v1.s1", analysis.KindMoney, "USD", "", "", "", ""),
	})
	if r := rs["q1.s1"]; r.Amount == "500000" {
		t.Errorf("a scale from nothing is applied anyway: %+v", r)
	}
	if _, ok, low, _ := valueStated(mv3("$500,000", "Money"), 0, rs, q); ok {
		t.Errorf("typed $500,000 admitted (low=%v) from a quote stating $500 with no scale in it", low)
	}
}

// H2 (unit): G8 skips a memo's single-digit figure whenever the reader did not read it clearly, so
// "$9 billion" (no reading: the reader unfunded, failed, or said unclear/other) traces against "$9,000".
func TestR5G8SkipsUnreadSingleDigit(t *testing.T) {
	memo, ground := "The settlement is $9 billion.", "The settlement payment is $9,000."
	cases := map[string]map[int]analysis.Reading{
		"not read": {},
		"unclear":  {18: {Kind: analysis.KindUnclear, Unclear: true, Text: "9", Start: 18, End: 19}},
		"other":    {18: {Kind: analysis.KindOther, Amount: "9", Text: "9", Start: 18, End: 19}},
	}
	for name, read := range cases {
		if got := g8(memo, ground, 0, nil, read, nil, nil, nil, nil, nil); len(got) == 0 {
			t.Errorf("%s: G8 traces the memo's $9 billion against %q", name, ground)
		}
	}
	for _, m := range []string{"The fee is 9%.", "Damages are $7 million.", "The Company owes $3."} {
		if got := g8(m, "Nothing numeric here.", 0, nil, map[int]analysis.Reading{}, nil, nil, nil, nil, nil); len(got) == 0 {
			t.Errorf("unread: G8 traces %q against a ground with no numbers", m)
		}
	}
}

// H2 (e2e): the reader reads the memo's "9" as unclear: "$9 billion" is delivered, untraced, ungated.
func TestR5UnclearMemoDigitIsDelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Settlement\nThe settlement payment is $9,000.\n")
	f.reads = map[string]analysis.Label{"memo:9": lab("", analysis.KindUnclear, "unclear", "1", "", "", "unclear")}
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The settlement payment is $9,000.", d1, "The settlement payment is $9,000", val("Company", "settlement payment", "$9,000", "Money")))},
		"draft_section": {section("The settlement is $9 billion [F1].", "Settlement $9 billion [F1].")},
	}), Config{})
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(deref(v.Output), "$9 billion") && !strings.Contains(fin, "not traced") {
		t.Errorf("the memo's $9 billion (reader: unclear) delivered with no G8 line; stop %s", stop(v))
	}
}

// M (e2e): a quote span the reader read as unclear lets a memo's clear "$2,000,000" trace "as written"
// (unread), and nothing gates the finding (no typed value, so f.Low is never set): an unclear reading
// verifies a memo figure.
func TestR5UnclearQuoteTracesUngated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Issuance\nThe Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor.\n")
	f.reads = map[string]analysis.Label{"quote:2,000,000": lab("", analysis.KindUnclear, "unclear", "1", "", "", "unclear")}
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The Company shall issue 2,000,000 shares to the Investor.", d1, "The Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor"))},
		"draft_section": {section("The Investor paid $2,000,000 for its shares [F1].", "Paid $2,000,000 [F1].")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(deref(v.Output), "$2,000,000") && !strings.Contains(fin, "not traced") && !(len(fs) == 1 && fs[0].Low) {
		t.Errorf("memo's $2,000,000 traced to an UNCLEAR reading and nothing is gated (stop %s, Low=%v)", stop(v), len(fs) == 1 && fs[0].Low)
	}
}

// M (e2e): an answer that parses but labels nothing (here {"labels": []}) is cached for ever under
// the content key; a later run on the same documents with a working reader still reads nothing.
func TestR5EmptyAnswerCachedForever(t *testing.T) {
	t.Parallel()
	empty := model.Reply{Text: `{"labels": []}`}
	f := newFixture(t)
	v1 := f.run(f.with(map[string][]model.Reply{"read_figures": {empty, empty, empty}}), Config{})
	fs1 := ledgerFindings(t, f, v1.ID)
	v2 := f.run(f.happy(), Config{}) // the oracle reader answers properly now
	fs2 := ledgerFindings(t, f, v2.ID)
	if len(fs2) == 1 && len(fs2[0].Values) == 0 {
		t.Errorf("run 2 (working reader) still drops $644,910: the empty answer of run 1 was cached (run1 values %d, run2 notes %v)", len(fs1[0].Values), fs2[0].ValueNotes)
	}
}

// L4: with the figure reader failing, every typed quantity is dropped (noted), the memo's figures are
// never traced by a reading (G8 fails closed): one its cited quote states as written is listed
// check-first and its finding gated (bench7: a blocking lint line drove figure-stripping redrafts),
// and Limitations says the section's figures were not read.
func TestR5NoReaderBudget(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{"read_figures": {},
		"lead": {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(draft("I1", "F1")), calls(committee()),
			calls(finishCall("submit")), calls(finishCall("submit")), calls(finishCall("submit"))},
		"draft_section": {section("Greystone claims damages of $644,910 for unpaid invoices [F1].", "Damages [F1].")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || len(fs[0].Values) != 0 || !strings.Contains(strings.Join(fs[0].ValueNotes, " "), "the figure reader's call failed") {
		t.Fatalf("reader failing: %+v", fs)
	}
	out0 := deref(v.Output)
	if !strings.Contains(out0, "- 644,910 in the memo was not read by the figure reader (the figure reader's call failed): it traces only as written, digit for digit, to [F1], whose findings are gated") ||
		!slices.ContainsFunc(v.PendingGates, func(g Gate) bool { return g.GateID == "G-F1" }) && !slices.ContainsFunc(v.DecidedGates, func(g Gate) bool { return g.GateID == "G-F1" }) {
		t.Errorf("the memo's unread figure is not listed and gated (%s):\n%s", stop(v), out0)
	}
	secs, err := load[Section](context.Background(), f.e, v.ID, kSection)
	if err != nil || len(secs) != 1 || secs[0].Read == nil || !strings.Contains(secs[0].Read.Unread, "1 of its 1 figures were not read (the figure reader's call failed)") {
		t.Fatalf("section read %+v %v", secs, err)
	}
	out := deref(v.Output)
	if !strings.Contains(out, "Figures: in the section on I1, 1 of its 1 figures were not read") || strings.Contains(out, "Delivered with an unresolved check") {
		t.Errorf("Limitations does not disclose the unread section (%s):\n%s", stop(v), out)
	}
}

// LOW: one span longer than the reader schema's 64 characters (a long reference number) makes the
// whole reader call invalid: every finding batched with it is unread and its values dropped.
func TestR5LongSpanSinksTheChunk(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("1234567890", 7)
	f := newFixture(t, "2. Damages\nGreystone claims damages of $644,910 for unpaid invoices under reference "+long+".\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices under reference "+long, val("Greystone", "damages", "$644,910", "Money")))},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) == 1 && len(fs[0].Values) == 0 {
		t.Errorf("a 70-digit reference in the quote dropped the $644,910 value: %v", fs[0].ValueNotes)
	}
}

// M (e2e): a quote number the reader read as NOT a quantity (a section number, kind other) is put in
// lint's "unread" list, so a memo's clear money figure with the same digits traces to it as written.
func TestR5SectionNumberTracesMoney(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Filing\nUnder Section 500 of the Code, the Company must file an annual report.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The Company must file an annual report under Section 500.", d1, "Under Section 500 of the Code, the Company must file an annual report"))},
		"draft_section": {section("The Company owes a $500 filing penalty [F1].", "Penalty $500 [F1].")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(deref(v.Output), "$500") && !strings.Contains(fin, "not traced") {
		t.Errorf("memo's $500 traced to 'Section 500' (read as %+v), stop %s", fs[0].Figures, stop(v))
	}
}

// M3: only a complete answer is cached: an empty one, one labelling only some spans, one whose ids
// lack the call's item prefix, or one with a label for a span not given is this run's only. A run
// after it with a working reader reads the figures and admits the value.
func TestR5CacheStoresOnlyCompleteAnswers(t *testing.T) {
	t.Parallel()
	for name, bad := range map[string]model.Reply{
		"partial":     scriptedLabels(lab("1.q1.s1", analysis.KindMoney, "USD", "", "", "Greystone", "")),
		"unprefixed":  scriptedLabels(lab("q1.s1", analysis.KindMoney, "USD", "", "", "Greystone", ""), lab("v1.s1", analysis.KindMoney, "USD", "", "", "Greystone", ""), lab("c.s1", analysis.KindMoney, "USD", "", "", "Greystone", "")),
		"a span more": scriptedLabels(lab("1.q1.s1", analysis.KindMoney, "USD", "", "", "Greystone", ""), lab("1.v1.s1", analysis.KindMoney, "USD", "", "", "Greystone", ""), lab("1.c.s1", analysis.KindMoney, "USD", "", "", "Greystone", ""), lab("1.q1.s9", analysis.KindMoney, "USD", "", "", "", "")),
	} {
		f := newFixture(t)
		f.run(f.with(map[string][]model.Reply{"read_figures": {bad, bad, bad}}), Config{})
		var n int
		if err := f.st.DB().QueryRow(`SELECT count(*) FROM figure_reads`).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows cached (%v)", name, n, err)
		}
		v := f.run(f.happy(), Config{})
		if fs := ledgerFindings(t, f, v.ID); len(fs) != 1 || len(fs[0].Values) != 1 {
			t.Errorf("%s: a later run with a working reader did not admit the value: %+v", name, fs)
		}
	}
}

// M3, L3: the cache key is derived from the reader's spec, prompt and tools' specs (an edit reads
// every figure again, never by a hand-kept version), the model, the matter, and for a drafted
// section the task; a task's rows go with it, and a document's with the document.
func TestR5CacheKeyAndScope(t *testing.T) {
	fsys := fstest.MapFS{}
	for _, name := range readerSpecFiles {
		data, err := specFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fsys[name] = &fstest.MapFile{Data: data}
	}
	if specHash(fsys, readerSpecFiles) != readerSpec {
		t.Fatal("the spec hash is not the embedded specs'")
	}
	for _, name := range readerSpecFiles {
		edited := maps.Clone(fsys)
		edited[name] = &fstest.MapFile{Data: append(slices.Clone(fsys[name].Data), ' ')}
		if specHash(edited, readerSpecFiles) == readerSpec {
			t.Errorf("editing %s leaves the cache key as it was", name)
		}
	}
	in := []passageIn{{ID: "q1", Kind: "quote", Text: "a fee of $5", Spans: []spanIn{{"q1.s1", "5"}}}}
	keys := map[string]bool{}
	for _, k := range []string{readKey("m1", "matter:A", "", nil, in), readKey("m2", "matter:A", "", nil, in), readKey("m1", "matter:B", "", nil, in),
		readKey("m1", "matter:A", "t1", nil, in), readKey("m1", "matter:A", "t2", nil, in), readKey("m1", "matter:A", "", []string{"Acme"}, in)} {
		keys[k] = true
	}
	if len(keys) != 6 {
		t.Errorf("model, matter, task or parties do not change the key: %d distinct of 6", len(keys))
	}

	t.Run("deleted with the task and the document", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		written := map[string][]model.Reply{"draft_section": {section("Greystone claims damages of $644,910 for unpaid invoices [F1].", "Damages [F1].")}}
		v := f.run(f.with(written), Config{})
		count := func() (n int) {
			if err := f.st.DB().QueryRow(`SELECT count(*) FROM figure_reads`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		var memo int
		if err := f.st.DB().QueryRow(`SELECT count(*) FROM figure_reads WHERE task_id = $1 AND matter_number = 'M-1'`, v.ID).Scan(&memo); err != nil || memo != count() || memo < 2 {
			t.Fatalf("rows %d of %d scoped to the task (%v)", memo, count(), err)
		}
		v2 := f.run(f.with(written), Config{})
		calls := 0
		for _, c := range f.e.chat.(*model.Fake).Calls() {
			if c.Agent == "read_figures" {
				var in readRequest
				requestOf(c, &in)
				for _, it := range in.Findings {
					if it.Passages[0].Kind != "memo" {
						t.Errorf("run 2 read a finding the matter's cache holds: %+v", it)
					}
					calls++
				}
			}
		}
		if calls != 1 { // the section is the task's own reading
			t.Errorf("run 2 read %d items, want its section only", calls)
		}
		if _, err := f.st.DB().Exec(`DELETE FROM tasks WHERE id = $1`, v2.ID); err != nil {
			t.Fatal(err)
		}
		if n := count(); n != memo {
			t.Errorf("deleting run 2 left %d rows, want run 1's %d", n, memo)
		}
		if err := f.d.Delete(context.Background(), f.docs[0].ID); err != nil {
			t.Fatal(err)
		}
		var docRows int
		if err := f.st.DB().QueryRow(`SELECT count(*) FROM figure_reads WHERE cardinality(document_ids) > 0`).Scan(&docRows); err != nil || docRows != 0 {
			t.Errorf("the document's readings outlived it: %d (%v)", docRows, err)
		}
	})
}

// L1: an item over the call bounds is read in parts (a passage's spans split between them), a span
// too long for the reader's schema or a passage too long for one call is not sent (unread, unclear,
// with why), and nothing else batched with them is lost.
func TestR5OversizeInputIsSplit(t *testing.T) {
	var many []string
	for i := range 150 { // a quote of 300 numbers, within the extractor's 4,000-character quote
		many = append(many, fmt.Sprintf("item %d costs $%d,%03d", i, i+1, i))
	}
	quote := strings.Join(many, "; ")
	long := strings.Repeat("1234567890", 7)
	it := findingReading(Finding{Finding: analysis.Finding{Claim: "Fees."}}, []analysis.Quote{{DocumentID: "d", Quote: quote}, {DocumentID: "d", Quote: "reference " + long + " and $5"}, {DocumentID: "d", Quote: strings.Repeat("x ", maxReadBytes) + "$7"}}, nil)
	parts := it.parts()
	n := 0
	for _, pt := range parts {
		if len(pt.ids) > maxReadSpans || len(pt.in) > maxReadPassages {
			t.Errorf("a part over the bounds: %d spans, %d passages", len(pt.ids), len(pt.in))
		}
		n += len(pt.ids)
	}
	if len(parts) < 4 || n != 300+1 { // 150 items' numbers and prices; the second quote's $5 (its long reference not sent)
		t.Errorf("%d parts holding %d spans", len(parts), n)
	}
	rs, _ := it.readings()
	if r := rs["q2.s1"]; !strings.Contains(r.Why, "too long for the figure reader") || !r.Unclear {
		t.Errorf("the long reference: %+v", r)
	}
	if r := rs["q3.s1"]; !strings.Contains(r.Why, "passage is too long") {
		t.Errorf("the long passage: %+v", r)
	}

	t.Run("e2e", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, "1. Schedule\n"+quote+".\n\n2. Damages\nGreystone claims damages of $644,910 for unpaid invoices.\n")
		d1 := f.docs[0].ID
		v := f.run(f.with(map[string][]model.Reply{"extract_facts": {facts(fd("The schedule prices 150 items.", d1, quote),
			fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices", val("Greystone", "damages", "$644,910", "Money")))}}), Config{})
		fs := ledgerFindings(t, f, v.ID)
		if len(fs) != 2 {
			t.Fatalf("%d findings (%s)\n%s\n%q", len(fs), stop(v), f.steps(v.ID), f.outputs(v.ID, "extract_facts"))
		}
		// the long schedule states no typed value: admission reads only the damages finding, never
		// sunk by it; the section citing the schedule reads it in calls of at most maxCallSpans spans,
		// and what its grant cannot fund stays unread with why (bench7)
		unfunded := !slices.ContainsFunc(fs[0].Figures, func(r analysis.Reading) bool {
			return strings.HasPrefix(r.Why, "not read") && r.Why != "not read: the budget could not fund the figure reader"
		})
		if len(fs[1].Values) != 1 || fs[1].Unread || fs[0].Unread || len(fs[0].Figures) != 300 || !unfunded {
			t.Fatalf("an oversized finding sank its call: %d findings, values %v, %d figures; notes %q %q", len(fs), fs[min(1, len(fs)-1)].Values, len(fs[0].Figures), fs[0].ValueNotes, fs[1].ValueNotes)
		}
	})
}

// L4: the figure reader's call for a drafted section is held back from the drafter's grant before it
// runs: a drafter that uses every turn it has still has its figures read.
func TestR5ReaderReservedBeforeDrafting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	read := ledgerOp("findings", "detail", "full")
	v := f.run(f.with(map[string][]model.Reply{
		"draft_section": {calls(read), calls(read), section("Greystone claims damages of $644,910 for unpaid invoices [F1].", "Damages [F1].")},
	}), Config{})
	secs, err := load[Section](context.Background(), f.e, v.ID, kSection)
	if err != nil || len(secs) != 1 || secs[0].Read == nil || secs[0].Read.Unread != "" || len(secs[0].Read.Figs) != 1 || !secs[0].Read.Figs[0].Clear() {
		t.Fatalf("a drafter using its three turns left its figures unread: %+v %v (%s)", secs, err, stop(v))
	}
	if goTurns("draft_section") != 1 {
		t.Error("draft_section's grant holds no call for the reader")
	}
}
