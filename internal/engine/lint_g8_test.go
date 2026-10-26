// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
)

// lab-r4 r5 verifier repros (critique/biglaw-lab-r4-r5-verifier.md), in the tree: each failed at
// 0d42d7f.

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
		_, ok, low, _ := valueStated(mv("$500,000", "Money"), 0, rs, q)
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
	if _, ok, low, _ := valueStated(mv("$500,000", "Money"), 0, rs, q); ok {
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
	if len(fs) != 1 || len(fs[0].Values) != 0 || !strings.Contains(strings.Join(fs[0].ValueNotes, " "), "the figure check's call failed") {
		t.Fatalf("reader failing: %+v", fs)
	}
	out0 := deref(v.Output)
	if !strings.Contains(out0, "- 644,910 in the memo was not read by the figure check (the figure check's call failed): it traces only as written, digit for digit, to [F1], whose findings are gated") ||
		!slices.ContainsFunc(v.PendingGates, func(g Gate) bool { return g.GateID == "G-F1" }) && !slices.ContainsFunc(v.DecidedGates, func(g Gate) bool { return g.GateID == "G-F1" }) {
		t.Errorf("the memo's unread figure is not listed and gated (%s):\n%s", stop(v), out0)
	}
	secs, err := load[Section](context.Background(), f.e, v.ID, kSection)
	if err != nil || len(secs) != 1 || secs[0].Read == nil || !strings.Contains(secs[0].Read.Unread, "1 of its 1 figures were not read (the figure check's call failed)") {
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
	h := sha256.New() // the hash as it was computed before overrides (specHash over the embedded files): the built-ins keep their cached readings
	for _, name := range readerSpecFiles {
		data, err := specFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%s %d\n", name, len(data))
		h.Write(data)
	}
	builtinHash := builtinSpecs.hashPaths(readerSpecFiles)
	if hex.EncodeToString(h.Sum(nil)) != builtinHash {
		t.Fatal("the spec hash is not the embedded specs'")
	}
	for _, name := range readerSpecFiles {
		if edited := overrideOne(t, engineDir+name, " "); edited.hashPaths(readerSpecFiles) == builtinHash {
			t.Errorf("an override of %s leaves the cache key as it was", name)
		}
	}
	in := []passageIn{{ID: "q1", Kind: "quote", Text: "a fee of $5", Spans: []spanIn{{"q1.s1", "5"}}}}
	keys := map[string]bool{}
	for _, k := range []string{readKey("h", "m1", "matter:A", "", nil, in), readKey("h", "m2", "matter:A", "", nil, in), readKey("h", "m1", "matter:B", "", nil, in),
		readKey("h", "m1", "matter:A", "t1", nil, in), readKey("h", "m1", "matter:A", "t2", nil, in), readKey("h", "m1", "matter:A", "", []string{"Acme"}, in)} {
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
	if r := rs["q2.s1"]; !strings.Contains(r.Why, "too long for the figure check") || !r.Unclear {
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
			return strings.HasPrefix(r.Why, "not read") && r.Why != "not read: the budget could not fund the figure check"
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

// R6-H1 (unit): a scale's source only has to be whole words of the passage outside the span, so the
// quote's "$500" read "in thousands" from the word "fee" (or from another figure's "(in thousands)"
// header) is a clear 500000 at scale 1000; a typed "$500 thousand" read at scale 1000 from its own
// "thousand" then matches it CLEARLY (same scale string), so it is admitted verified, not gated.
func TestR6ScaleFromAnyWordVerifiesAScaledValue(t *testing.T) {
	for name, c := range map[string]struct{ q, from string }{
		"a word that is no scale": {"The filing fee is $500 per application.", "fee"},
		"another figure's header": {"The filing fee is $500 per application. Program costs (in thousands): 1,200.", "in thousands"},
	} {
		ps := []analysis.Passage{analysis.PassageOf("q1", c.q), analysis.PassageOf("v1", "$500 thousand")}
		rs, notes := analysis.ReadLabels(ps, []analysis.Label{
			lab("q1.s1", analysis.KindMoney, "USD", "1000", c.from, "", ""),
			lab("v1.s1", analysis.KindMoney, "USD", "1000", "thousand", "", ""),
		})
		if _, ok, low, _ := valueStated(mv("$500 thousand", "Money"), 0, rs, c.q); ok && !low {
			t.Errorf("%s: typed \"$500 thousand\" VERIFIED (not gated) from %q (quote read %+v, notes %v)", name, c.q, rs["q1.s1"], notes)
		}
	}
}

// R6-H1 (unit, G8): the same one wrong label on the quote traces a memo's "$500 thousand".
func TestR6ScaleFromAnyWordTracesAScaledMemoFigure(t *testing.T) {
	q := "The filing fee is $500 per application."
	qr, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", q)}, []analysis.Label{lab("q1.s1", analysis.KindMoney, "USD", "1000", "fee", "", "")})
	memo := "The filing fee is $500 thousand."
	mr, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("m", memo)}, []analysis.Label{lab("m.s1", analysis.KindMoney, "USD", "1000", "thousand", "", "")})
	read := map[int]analysis.Reading{}
	for _, r := range mr {
		read[r.Start] = r
	}
	if got := g8(memo, q, 0, nil, read, []analysis.Reading{qr["q1.s1"]}, nil, nil, nil, nil); len(got) == 0 {
		t.Errorf("G8 traces the memo's $500 thousand to a quote stating $500 (quote read %+v)", qr["q1.s1"])
	}
}

// R6-H1 (e2e): the extractor types "$500 thousand", the reader (one wrong label) scales the quote's
// $500 from "fee"; the value is admitted verified and the memo's "$500 thousand" is delivered traced.
func TestR6ScaleSmuggleWithAScaledValueReachesMemo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe filing fee is $500 per application.\n")
	f.reads = map[string]analysis.Label{"quote:500": lab("", analysis.KindMoney, "USD", "1000", "fee", "", "")}
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The filing fee is $500 thousand.", d1, "The filing fee is $500 per application", val("Company", "filing fee", "$500 thousand", "Money")))},
		"draft_section": {section("The filing fee is $500 thousand per application [F1].", "Fee [F1].")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) == 1 && len(fs[0].Values) == 1 && !fs[0].Low {
		t.Errorf("typed $500 thousand verified (not gated) from a quote stating $500: %+v", fs[0].Values[0].Fig)
	}
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(deref(v.Output), "$500 thousand") && !strings.Contains(fin, "not traced") && !strings.Contains(deref(v.Output), "written at another scale") {
		t.Errorf("memo states $500 thousand from a $500 quote; stop %s, no G8 line, no Limitations line", stop(v))
	}
}

// R6-M (unit, G8, choice (a)): the memo's "$5" read at 10^6 from any whole word of its own sentence
// ("share") is "limited" beside a cited $5,000,000, never flagged: a per-share figure the source never
// states is delivered with only a Limitations line.
func TestR6LimitedScaleFromAnyWord(t *testing.T) {
	memo := "Each holder received $5 per share."
	mr, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("m", memo)}, []analysis.Label{lab("m.s1", analysis.KindMoney, "USD", "1000000", "share", "", "")})
	read := map[int]analysis.Reading{}
	for _, r := range mr {
		read[r.Start] = r
	}
	known := []analysis.Reading{{Kind: analysis.KindMoney, Currency: "USD", Amount: "5000000", Text: "5,000,000"}}
	got := g8(memo, "The aggregate consideration is $5,000,000.", 0, nil, read, known, nil, nil, nil, nil)
	if len(got) == 1 && got[0].limited != "" {
		t.Errorf("the memo's \"$5 per share\" (scale from \"share\") is limited, not flagged: %+v", got)
	}
}

// R6-L (accounting, choice (f)): the coverage pass's redraft runs its section's figure reader too; its
// calls are added to kRead and subtracted from the draft class's calls although the cover step's
// calls were never the draft class's, so P4 sees fewer drafter calls than the lead made.
func TestR6CoverReaderCallsLeaveTheDraftShare(t *testing.T) {
	t.Parallel()
	f := newFixture(t, referralText)
	d1 := f.docs[0].ID
	_, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Is the 2021 conduct time-barred?", "Requirement"))), calls(extract(d1, "2021", "I1")), calls(draft("I1", "F1")),
			calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The Firm allocated profitable trades to Oceanic in 2021.", d1, "In 2021 the Firm allocated profitable trades to Oceanic Fund I LP"))},
		"analyze_issue": {analysed(limitsRule, "The 2021 allocations [F1] fall outside five years.", limitsConc)},
		"draft_section": {section("In 2021 the Firm allocated profitable trades to Oceanic [F1].", "s"),
			section("In 2021 the Firm allocated profitable trades to Oceanic [F1]; penalties for it may be time-barred.", "s")},
		"prosecute": {supported, supported}, "reproduce": {noAnswers, noAnswers}, "audit_gaps": {noGaps, noGaps},
	}), Config{})
	task := f.submit(0)
	f.step(context.Background())
	spent, err := f.e.classSpend(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	steps := f.steps(task.ID)
	drafts := 0
	for _, c := range fake.Calls() {
		if c.Agent == "draft_section" {
			drafts++
		}
	}
	if drafts != 2 {
		t.Fatalf("want the lead's draft and the coverage pass's redraft, got %d drafter calls\n%s", drafts, steps)
	}
	if spent["draft"].Calls != 1 {
		t.Errorf("the lead's one draft_section drafter call shows as %d draft-class calls (cover's reader calls subtracted)\n%s", spent["draft"].Calls, steps)
	}
}

// R6-M (pre-existing, widened use): a worksheet of literals only ("sum of 9000000000 and 0", no ref to
// any verified value) traces any memo figure equal to its result: G8 puts every worksheet's result in
// ground and matches a clear reading against every sheet, and the "calculation from typed inputs"
// Limitations line fires only when the section writes the worksheet's {W…} handle. The eval oracle
// (eval/proofruns_test.go) now uses exactly this to pass G8 on its answers' figures.
func TestR6LiteralWorksheetLaundersAFigure(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Settlement\nThe settlement payment is $9,000.\n")
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What is the settlement?", "Conduct"))), calls(extract(d1, "settlement", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "sum", "operands": []m{op("9000000000", "USD"), op("0", "USD")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The settlement payment is $9,000.", d1, "The settlement payment is $9,000", val("Company", "settlement payment", "$9,000", "Money")))},
		"draft_section": {section("The settlement is $9,000,000,000 [F1].", "Settlement [F1].")},
	}), Config{})
	out := deref(v.Output)
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(out, "$9,000,000,000") && !strings.Contains(fin, "not traced") && !strings.Contains(out, "typed inputs") {
		t.Errorf("a literal-only worksheet traced the memo's $9,000,000,000 against a $9,000 quote, undisclosed; stop %s\n%s", stop(v), f.outputs(v.ID, "numbers"))
	}
}

// R6 M2: the cited finding's quote is never read (it states no typed value, so it is read with the
// section that cites it, and the reader's answer labels the memo's span but not the quote's), the
// section's own figure is: the memo's clear "$2,000,000 paid" traces only as written to the quote's
// unread "2,000,000 shares": disclosed in Limitations, and the finding is gated for a lawyer (G-F1).
func TestR6UnreadQuoteTracesAsWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Issuance\nThe Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor.\n")
	d1 := f.docs[0].ID
	e, fake := f.engine(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The Company shall issue 2,000,000 shares to the Investor.", d1, "The Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor"))},
		"draft_section": {section("The Investor paid $2,000,000 for its shares [F1].", "Paid [F1].")},
	}), Config{})
	fake.Respond = func(req model.Request) (model.Reply, bool) {
		if req.Agent != "read_figures" {
			return model.Reply{}, false
		}
		r, ok := readerOracle(req)
		var in readRequest
		requestOf(req, &in)
		memo := map[string]bool{}
		for _, f := range in.Findings {
			for _, p := range f.Passages {
				for _, s := range p.Spans {
					memo[s.ID] = p.Kind == "memo"
				}
			}
		}
		var out struct {
			Labels []analysis.Label `json:"labels"`
		}
		_ = json.Unmarshal([]byte(r.Text), &out)
		out.Labels = slices.DeleteFunc(out.Labels, func(l analysis.Label) bool { return !memo[l.Span] }) // the quote's spans left unlabelled
		data, _ := json.Marshal(out)
		r.Text = string(data)
		return r, ok
	}
	_ = e
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	fs := ledgerFindings(t, f, v.ID)
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	out := deref(v.Output)
	if strings.Contains(out, "$2,000,000") && !strings.Contains(fin, "not traced") && !(len(fs) == 1 && fs[0].Low) {
		t.Errorf("delivered %s with the memo's $2,000,000 traced only as written and nothing gated", stop(v))
	}
	if !strings.Contains(out, "traced only as written") || !slices.ContainsFunc(v.PendingGates, func(g Gate) bool { return g.GateID == "G-F1" }) && !slices.ContainsFunc(v.DecidedGates, func(g Gate) bool { return g.GateID == "G-F1" }) {
		t.Errorf("the finding a memo figure traces to only as written is not disclosed and gated (gates %+v):\n%s", v.PendingGates, out)
	}
}

// L3: a complete answer that reads nothing clearly (unclear throughout) is never cached: a later run,
// or a stronger candidate model, reads those figures again.
func TestR6AllUnclearAnswerIsNotCached(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	u := lab("", analysis.KindUnclear, "unclear", "", "", "", "unclear")
	unclear := scriptedLabels(func() []analysis.Label {
		var ls []analysis.Label
		for _, id := range []string{"1.q1.s1", "1.v1.s1", "1.c.s1"} {
			l := u
			l.Span = id
			ls = append(ls, l)
		}
		return ls
	}()...)
	f.run(f.with(map[string][]model.Reply{"read_figures": {unclear, unclear, unclear}}), Config{})
	var n int
	if err := f.st.DB().QueryRow(`SELECT count(*) FROM figure_reads`).Scan(&n); err != nil || n != 0 {
		t.Errorf("an all-unclear answer was cached: %d rows (%v)", n, err)
	}
}

// lab-r4 r9 verifier repros (critique/biglaw-lab-r4-r9-verifier.md H1, H2, M1), kept as regression
// tests: one wrong reader label, or a memo period with right labels, never traces a wrong figure
// unseen.

// H1: "Section 30" read as a 30-day duration with no words beside it that state a unit is no period:
// the label is unclear (ReadLabels), and a reading with no cited unit words feeds no worksheet.
func TestMislabelledSectionIsNoPeriod(t *testing.T) {
	q := "Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days."
	days := "days"
	l := lab("q1.s1", analysis.KindDuration, "", "1", "", "", "")
	l.Unit, l.UnitFrom = &days, &days
	rs, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", q)}, []analysis.Label{l})
	if r := rs["q1.s1"]; !r.Unclear {
		t.Errorf("\"Section 30\" read as 30 days citing \"days\": %+v", r)
	}
	known := []analysis.Reading{
		{Kind: analysis.KindDuration, Unit: "days", Amount: "30", Text: "30"}, // a duration no words state
		{Kind: analysis.KindDuration, Unit: "days", UnitFrom: "days", Amount: "21", Text: "21"},
	}
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"30", "days"})
	if got := tracedSheets([]analysis.Worksheet{w}, q+"\n", map[string]bool{"F1": true}, known); len(got) == 1 {
		t.Errorf("date_add(2024-03-01, 30 days) = %s traced from \"Section 30\"", w.Result)
	}
}

// H1: "Section 12" read as a bare number is no multiplier: a number needs the sign beside it.
func TestMislabelledSectionIsNoMultiplier(t *testing.T) {
	quote := "Under Section 12 of the Agreement, the Buyer shall pay $1,000,000 at Closing."
	sec := "Section"
	l := lab("q1.s1", analysis.KindNumber, "", "1", "", "", "")
	l.UnitFrom = &sec
	if rs, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", quote)}, []analysis.Label{l}); !rs["q1.s1"].Unclear {
		t.Errorf("\"Section 12\" read as a multiple of \"Section\": %+v", rs["q1.s1"])
	}
	known := []analysis.Reading{{Kind: analysis.KindNumber, Amount: "12", Text: "12"}, {Kind: analysis.KindMoney, Currency: "USD", Amount: "1000000", Text: "1,000,000"}}
	cited := map[string]bool{"F1": true}
	w := sheet(t, "product", [2]string{"12", ""}, [2]string{"1,000,000", "USD"})
	memo := "The Buyer pays $12,000,000 at Closing."
	sheets := tracedSheets([]analysis.Worksheet{w}, quote+"\n", cited, known)
	if got := g8(memo, quote+"\n", 0, nil, memoRead(memo, lab("m.s1", analysis.KindMoney, "USD", "1", "", "", "")), known, nil, sheets, cited, nil); len(got) == 0 {
		t.Errorf("%q traced by %s = %s %s", memo, w.WID, w.Result, w.Unit)
	}
}

// H1 (r9, r10): "21 business days" read in calendar days (its words "days" are beside it) still
// traces a calendar deadline, so every figure of a section that traces only through a worksheet over
// typed inputs is in the Computed figures block, whatever form the memo writes it in (G8 itself
// decides: lintLimited traces the section again without those worksheets), with the words its inputs
// rest on, the unit whose words are not its name marked.
func TestComputedFigureDisclosedWithItsWords(t *testing.T) {
	quote := "The complaint was served on March 1, 2024. An answer is due within 21 business days."
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: quote, Method: "exact"}},
		Figures: []analysis.Reading{{Span: "q1.s3", Text: "21", Start: 60, End: 62, Kind: analysis.KindDuration, Unit: "days", UnitFrom: "business days", Amount: "21"}}} // the wrong unit
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "days"})
	use := map[string]Finding{"F1": f1}
	for _, memo := range []string{"The answer is due March 22, 2024 [F1].", "The answer is due March 22 [F1].", "The answer is due March 22nd, 2024 [F1].", "The answer is due 22 March 2024 [F1].",
		"The answer is due {" + w.WID + "} [F1]."} {
		sec := readSection(Section{Markdown: memo, Handles: []Fig{{"{" + w.WID + "}", "", sheetValue(w.Result, w.Unit)}}})
		lines, _, _, computed := lintLimited("I1", sec, use, []analysis.Worksheet{w})
		if len(lines) == 0 && (len(computed) != 1 || !computed[0].Mismatch || !strings.HasPrefix(computed[0].Text, "⚠ March 22, 2024 = March 1, 2024 [F1] “") || !strings.Contains(computed[0].Text, "” + 21 days [F1] “…2024. An answer is due within 21 business days.”") ||
			!strings.HasSuffix(computed[0].Text, ", read in days from “business days”: the cited words “business days” are not its unit (days); the findings are gated") || computed[0].Gate["F1"] != limitedUnit) {
			t.Errorf("%q: traced, but not disclosed with its words: %+v", memo, computed)
		}
	}
	if _, _, _, computed := lintLimited("I1", readSection(Section{Markdown: "The complaint was served on March 1, 2024 [F1]."}), use, []analysis.Worksheet{w}); len(computed) != 0 {
		t.Errorf("a section that states no computed figure lists one: %+v", computed)
	}
}

// H1 (unchanged): a period in the wrong unit is never an operand: 21 days feeds no 21 months.
func TestWrongUnitOperandRefused(t *testing.T) {
	ground := "Served on March 1, 2024. An answer is due within 21 days.\n"
	known := []analysis.Reading{{Kind: analysis.KindDuration, Unit: "days", UnitFrom: "days", Amount: "21", Text: "21"}}
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "months"})
	if got := tracedSheets([]analysis.Worksheet{w}, ground, map[string]bool{"F1": true}, known); len(got) != 0 {
		t.Errorf("date_add 21 months traced from a 21-day reading: %s", w.Result)
	}
}

// M1: a month on from January 31, 2024 is February 29, 2024, and a year on from February 29, 2024 is
// February 28, 2025 (the last day of the shorter month), traced from the cited "one (1) month".
func TestMonthEndClampTraces(t *testing.T) {
	ground := "Notice of default was given on January 31, 2024. The Borrower may cure within one (1) month after notice.\n"
	known := []analysis.Reading{{Kind: analysis.KindDuration, Unit: "months", UnitFrom: "month", Amount: "1", Text: "1"}}
	w := sheet(t, "date_add", [2]string{"2024-01-31", "date"}, [2]string{"1", "months"})
	if got := tracedSheets([]analysis.Worksheet{w}, ground, map[string]bool{"F1": true}, known); w.Result != "2024-02-29" || len(got) != 1 {
		t.Errorf("date_add(2024-01-31, 1 month) = %s (traced %v), want 2024-02-29", w.Result, len(got) == 1)
	}
	if y := sheet(t, "date_add", [2]string{"2024-02-29", "date"}, [2]string{"1", "years"}); y.Result != "2025-02-28" {
		t.Errorf("date_add(2024-02-29, 1 year) = %s, want 2025-02-28", y.Result)
	}
}

// H2: with every label right, a memo's "within 30 days" traces only to a cited period of 30 days (or
// the days between two cited dates), never to the "30" of "Section 30"; the record's 21 days traces.
func TestMemoPeriodNeedsACitedPeriod(t *testing.T) {
	quote := "Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days. The hearing is on March 31, 2024."
	known := []analysis.Reading{{Kind: analysis.KindOther, Amount: "30", Text: "30"}, {Kind: analysis.KindDuration, Unit: "days", UnitFrom: "days", Amount: "21", Text: "21"}}
	period := func(memo string) []untraced {
		days := "days"
		l := lab("m.s1", analysis.KindDuration, "", "1", "", "", "")
		l.Unit, l.UnitFrom = &days, &days
		return g8(memo, quote+"\n", 0, nil, memoRead(memo, l), known, nil, nil, map[string]bool{"F1": true}, nil)
	}
	if got := period("An answer is due within 31 days of service."); len(got) != 1 {
		t.Errorf("31 days (no cited period, nor two cited dates 31 days apart) traced: %+v", got)
	}
	if got := period("An answer is due within 21 days of service."); len(got) != 0 {
		t.Errorf("the cited 21 days flagged: %+v", got)
	}
	if got := period("The hearing is 30 days after service."); len(got) != 0 { // March 1 to March 31: two cited dates
		t.Errorf("the days between two cited dates flagged: %+v", got)
	}
	quote = strings.Replace(quote, " The hearing is on March 31, 2024.", "", 1)
	if got := period("An answer is due within 30 days of service."); len(got) != 1 || !strings.Contains(got[0].hint, "no period of 30 days") {
		t.Errorf("30 days traced to \"Section 30\": %+v", got)
	}
}

// H1 e2e: one wrong label ("Section 30" read as 30 days) delivers no traced, undisclosed deadline.
func TestMislabelledSectionDeadlineNotDelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nUnder Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days.\n")
	days := "days"
	wrong := lab("", analysis.KindDuration, "", "1", "", "", "")
	wrong.Unit, wrong.UnitFrom = &days, &days
	f.reads = map[string]analysis.Label{"quote:30": wrong}
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-03-01", "date"), op("30", "days")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 days.", d1,
			"Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days"))},
		"draft_section": {section("The answer is due March 31, 2024 [F1].", "Due [F1].")},
	}), Config{})
	if out, fin := deref(v.Output), strings.Join(f.outputs(v.ID, "finish"), "\n"); strings.Contains(out, "March 31, 2024") && !strings.Contains(fin, "not traced") && !strings.Contains(out, "### Computed figures") {
		t.Errorf("\"March 31, 2024\" delivered traced and undisclosed; stop %s", stop(v))
	}
}

// H1 e2e: "21 business days" read in calendar days: the computed deadline the drafter writes out is
// delivered only with its computation and the words it rests on in Limitations.
func TestBusinessDaysReadAsDaysDisclosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	days, from := "days", "business days"
	wrong := lab("", analysis.KindDuration, "", "1", "", "", "")
	wrong.Unit, wrong.UnitFrom = &days, &from
	f.reads = map[string]analysis.Label{"quote:21": wrong}
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-03-01", "date"), op("21", "days")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days"))},
		"draft_section": {section("The answer is due March 22, 2024 [F1].", "Due [F1].")},
	}), Config{})
	if out := deref(v.Output); !strings.Contains(out, "March 22, 2024") || !strings.Contains(out, "⚠ March 22, 2024 = March 1, 2024 [F1]") || !strings.Contains(out, "the cited words “business days” are not its unit (days)") {
		t.Errorf("the calendar deadline from \"21 business days\" is delivered without its computation; stop %s\n%s", stop(v), out)
	}
}

// M1 e2e: a month on from January 31, 2024 is February 29; the memo's overflowed "March 2, 2024" is flagged.
func TestMonthEndDeadlineFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Default\nNotice of default was given on January 31, 2024. The Borrower may cure within one (1) month after notice.\n")
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When does the cure period end?", "Conduct"))), calls(extract(d1, "cure", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-01-31", "date"), op("1", "months")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("Notice of default was given on January 31, 2024; the cure period is one month.", d1,
			"Notice of default was given on January 31, 2024. The Borrower may cure within one (1) month after notice"))},
		"draft_section": {section("The cure period ends March 2, 2024 [F1].", "Cure [F1].")},
	}), Config{})
	if fin := strings.Join(f.outputs(v.ID, "finish"), "\n"); !strings.Contains(fin, "the figure 2024-03-02 is not traced") {
		t.Errorf("\"March 2, 2024\" (a month on from Jan 31 is Feb 29) not flagged; stop %s\n%s", stop(v), fin)
	}
}

// H2 e2e: with the stand-in's own labels, the memo's "within 30 days" (the record: 21 days) is flagged.
func TestMemoPeriodFromSectionNumberFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nUnder Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 days.", d1,
			"Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days"))},
		"draft_section": {section("The answer is due within 30 days of service [F1].", "Due [F1].")},
	}), Config{})
	if fin := strings.Join(f.outputs(v.ID, "finish"), "\n"); !strings.Contains(fin, "the figure 30 days is not traced") {
		t.Errorf("\"within 30 days\" (the record: 21 days) not flagged; stop %s\n%s", stop(v), fin)
	}
}

// lab-r4 r10 verifier repros (critique/biglaw-lab-r4-r10-verifier.md H1, M1, L2, L5), kept as
// regression tests.

// M1: a memo period is the cited one only when their unit words agree as written: "21 business days"
// read in days traces no "within 21 days" (limited: listed first, its finding gated).
func TestMemoDaysFromBusinessDaysLimited(t *testing.T) {
	quote := "The complaint was served on March 1, 2024. An answer is due within 21 business days."
	known := []analysis.Reading{{Kind: analysis.KindDuration, Unit: "days", UnitFrom: "business days", Amount: "21", Text: "21"}}
	memo := "An answer is due within 21 days of service."
	days := "days"
	l := lab("m.s1", analysis.KindDuration, "", "1", "", "", "")
	l.Unit, l.UnitFrom = &days, &days
	got := g8(memo, quote+"\n", 0, nil, memoRead(memo, l), known, nil, nil, map[string]bool{"F1": true}, nil)
	if len(got) != 1 || got[0].limited != limitedUnit {
		t.Errorf("%q traced to \"21 business days\" read in days: %+v", memo, got)
	}
}

// M1, L2: "Section 30 of" read as 30 days citing "of" (a word beside it the reader may cite: Go checks
// only where and its shape) never traces the memo's "within 30 days": the words differ.
func TestSectionOfIsNoMemoPeriod(t *testing.T) {
	quote := "Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days."
	days, of := "days", "of"
	l := lab("q1.s1", analysis.KindDuration, "", "1", "", "", "")
	l.Unit, l.UnitFrom = &days, &of
	rs, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", quote)}, []analysis.Label{l})
	memo := "An answer is due within 30 days of service."
	ml := lab("m.s1", analysis.KindDuration, "", "1", "", "", "")
	ml.Unit, ml.UnitFrom = &days, &days
	if got := g8(memo, quote+"\n", 0, nil, memoRead(memo, ml), []analysis.Reading{rs["q1.s1"]}, nil, nil, map[string]bool{"F1": true}, nil); len(got) != 1 {
		t.Errorf("%q traced to \"Section 30 of\": %+v", memo, got)
	}
}

// L2: a letter in parentheses after the digits ("Rule 12(b)") is no multiple's sign, nor a unit.
func TestRuleLetterIsNoMultiple(t *testing.T) {
	q := "Under Rule 12(b) the Buyer shall pay $1,000,000 within 10 days."
	b, days := "b", "days"
	for _, l := range []analysis.Label{lab("q1.s1", analysis.KindNumber, "", "1", "", "", ""), lab("q1.s1", analysis.KindDuration, "", "1", "", "", "")} {
		l.UnitFrom = &b
		if l.Kind == analysis.KindDuration {
			l.Unit = &days
		}
		if rs, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", q)}, []analysis.Label{l}); !rs["q1.s1"].Unclear {
			t.Errorf("\"Rule 12(b)\" read as a %s of %q: %+v", l.Kind, b, rs["q1.s1"])
		}
	}
}

// L5: a memo number the reader read as an identifier traces as written, but when a cited quantity
// states the same digits it is listed for the lawyer to check first.
func TestOtherWithAQuantitysDigitsListed(t *testing.T) {
	quote := "The complaint was served on March 1, 2024. An answer is due within 30 days."
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: quote, Method: "exact"}},
		Figures: []analysis.Reading{{Span: "q1.s4", Text: "30", Start: 60, End: 62, Kind: analysis.KindDuration, Unit: "days", UnitFrom: "days", Amount: "30"}}}
	sec := Section{Markdown: "An answer is due within 30 days [F1]."} // the memo's "30" mislabelled an identifier
	body, _ := memoBody(sec)
	at := strings.Index(body, "30")
	sec.Read = &SectionRead{Hash: textHash(body), Figs: []analysis.Reading{{Span: "m.s1", Text: "30", Start: at, End: at + 2, Kind: analysis.KindOther, Amount: "30"}}}
	lines, _, _, computed := lintLimited("I1", sec, map[string]Finding{"F1": f1}, nil)
	if len(lines) != 0 || len(computed) != 1 || !computed[0].Check || !strings.Contains(computed[0].Text, "“30” in the memo is read as an identifier and traces as written, but [F1] “") {
		t.Errorf("lint %q, computed %+v", lines, computed)
	}
}

// H1 e2e: the deadline written without its year ("March 22") is in the block too (G8 decides what a
// section states, whatever the form).
func TestBusinessDaysDeadlineNoYearDisclosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	days, from := "days", "business days"
	wrong := lab("", analysis.KindDuration, "", "1", "", "", "")
	wrong.Unit, wrong.UnitFrom = &days, &from
	f.reads = map[string]analysis.Label{"quote:21": wrong}
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-03-01", "date"), op("21", "days")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days"))},
		"draft_section": {section("The answer is due March 22 [F1].", "Due [F1].")},
	}), Config{})
	if out := deref(v.Output); !strings.Contains(out, "The answer is due March 22 [F1].") || !strings.Contains(out, "⚠ March 22, 2024 = March 1, 2024 [F1]") {
		t.Errorf("\"March 22\" (21 calendar days; the record: 21 business days) delivered without its computation; stop %s\n%s", stop(v), out)
	}
}

// M1 e2e: the memo's "within 21 days" for the record's "21 business days" read in days: listed first
// and its finding gated, never traced silently.
func TestMemoDaysFromBusinessDaysDisclosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	days, from := "days", "business days"
	wrong := lab("", analysis.KindDuration, "", "1", "", "", "")
	wrong.Unit, wrong.UnitFrom = &days, &from
	f.reads = map[string]analysis.Label{"quote:21": wrong}
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days"))},
		"draft_section": {section("The answer is due within 21 days of service [F1].", "Due [F1].")},
	}), Config{})
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	if out := deref(v.Output); strings.Contains(out, "within 21 days of service") && (!strings.Contains(out, "⚠ 21 days in the memo (its words “days”) is the cited period [F1]") || !gated) {
		t.Errorf("\"within 21 days\" (the record: 21 business days) delivered without the mismatch, or ungated (gated %v); stop %s\n%s", gated, stop(v), out)
	}
}

// lab-r4 r11 verifier repros (critique/biglaw-lab-r4-r11-verifier.md H, M, L1, L3, L4, L5), kept as
// regression tests.

const businessDaysQuote = "The complaint was served on March 1, 2024. An answer is due within 21 business days."

// businessDaysFinding is F1 over businessDaysQuote with its "21" read in days from "business days"
// (the one wrong unit).
func businessDaysFinding(t *testing.T) Finding {
	t.Helper()
	p := analysis.PassageOf("q1", businessDaysQuote)
	var ls []analysis.Label
	days, from := "days", "business days"
	for i, s := range p.Spans {
		l := lab(analysis.SpanID("q1", i), analysis.KindDate, "", "1", "", "", "")
		if s.Text == "21" {
			l.Kind, l.Unit, l.UnitFrom = analysis.KindDuration, &days, &from
		}
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	var figs []analysis.Reading
	for i := range p.Spans {
		figs = append(figs, rs[analysis.SpanID("q1", i)])
	}
	return Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: businessDaysQuote, Method: "exact"}}, Figures: figs}
}

// labelledSection is a section whose memo spans are all read with the labels label gives each.
func labelledSection(md string, label func(analysis.Span) analysis.Label) Section {
	sec := Section{Markdown: md}
	body, _ := memoBody(sec)
	p := analysis.PassageOf("m", body)
	var ls []analysis.Label
	for i, s := range p.Spans {
		l := label(s)
		l.Span = analysis.SpanID("m", i)
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	sec.Read = &SectionRead{Hash: textHash(body)}
	for _, r := range rs {
		sec.Read.Figs = append(sec.Read.Figs, r)
	}
	return sec
}

func asDate(analysis.Span) analysis.Label { return lab("", analysis.KindDate, "", "1", "", "", "") }

// r10 H1 held across memo forms: every form of the computed deadline March 22, 2024 is flagged or
// listed, never silent.
func TestComputedDateEveryForm(t *testing.T) {
	f1 := businessDaysFinding(t)
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "days"})
	for _, memo := range []string{
		"The answer is due March 22 [F1].", "The answer is due March 22nd, 2024 [F1].", "The answer is due March 22nd [F1].",
		"The answer is due 22 March [F1].", "The answer is due 22 March 2024 [F1].", "The answer is due 22nd March 2024 [F1].",
		"The answer is due Mar. 22, 2024 [F1].", "The answer is due Mar 22 [F1].", "The answer is due 3/22/2024 [F1].",
		"The answer is due 3/22 [F1].", "The answer is due 2024-03-22 [F1].", "The answer is due on the 22nd [F1].",
		"The answer is due on the 22nd of March [F1].", "Served March 1, 2024, the answer is due on the 22nd [F1].",
		"The answer is due on March 22 (a Friday) [F1].", "The answer is due MARCH 22 [F1].", "The answer is due March 22–23 [F1].",
		"The answer is due by 22/3/2024 [F1].",
	} {
		lines, _, _, computed := lintLimited("I1", labelledSection(memo, asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
		if len(lines) == 0 && len(computed) == 0 {
			t.Errorf("%q: no lint line and no computed line", memo)
		}
	}
}

// A memo period traced through a typed worksheet's result, or the days between its dates, is listed.
func TestPeriodFromTypedSheetResultListed(t *testing.T) {
	f1 := businessDaysFinding(t)
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "days"})
	days := "days"
	for _, memo := range []string{"From March 1, 2024 to March 22, 2024 is 21 days [F1].", "The answer is due 21 days after service [F1]."} {
		sec := labelledSection(memo, func(s analysis.Span) analysis.Label {
			l := asDate(s)
			if s.Text == "21" {
				l.Kind, l.Unit, l.UnitFrom = analysis.KindDuration, &days, &days
			}
			return l
		})
		if lines, _, _, computed := lintLimited("I1", sec, map[string]Finding{"F1": f1}, []analysis.Worksheet{w}); len(lines) == 0 && len(computed) == 0 {
			t.Errorf("%q silent", memo)
		}
	}
}

// refFinding is F1 over quote with values (each read by its label: a ref's amount is the figure
// reader's reading of it), its quote's spans read by quoteLabel, and the worksheet fn over refs.
func refFinding(t *testing.T, quote string, quoteLabel func(analysis.Span) analysis.Label, fn string, values ...analysis.Value) (Finding, analysis.Worksheet) {
	t.Helper()
	p := analysis.PassageOf("q1", quote)
	var ls []analysis.Label
	for i, s := range p.Spans {
		l := quoteLabel(s)
		l.Span = analysis.SpanID("q1", i)
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	f := Finding{Finding: analysis.Finding{FID: "F1", Verified: true, Values: values}, Citations: []Citation{{Quote: quote, Method: "exact"}}}
	for i := range p.Spans {
		f.Figures = append(f.Figures, rs[analysis.SpanID("q1", i)])
	}
	var ops []analysis.Operand
	for i := range values {
		r := fmt.Sprintf("F1.v%d", i+1)
		ops = append(ops, analysis.Operand{Ref: &r})
	}
	w, err := analysis.Calc([]analysis.Finding{f.Finding}, fn, ops, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f, w
}

// H (one wrong scale): two values read ×10⁶ from "billion" summed by ref (r12 L3: their Figs as
// ReadLabels gives them, the amounts shifted). However the memo states the total, written out
// ("$8,000,000"), by its handle, or at another scale's words ("$8 million"), it traces only through
// the worksheet, whose inputs rest on the reader's scale: ⚠, and the finding is gated, as
// limitedScale's would be (r12 L1).
func TestRefScaledSumWrittenOutGated(t *testing.T) {
	quote := "The first instalment is $5 billion. The second instalment is $3 billion."
	wrong := func(analysis.Span) analysis.Label {
		return lab("", analysis.KindMoney, "USD", "1000000", "billion", "", "")
	}
	million := func(n, text string) *analysis.Reading {
		return &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: n, Scale: "1000000", ScaleFrom: "billion", Text: text}
	}
	f1, w := refFinding(t, quote, wrong, "sum",
		analysis.Value{Entity: "Company", Metric: "first instalment", Value: "$5 billion", Class: "Money", Fig: million("5000000", "5")},
		analysis.Value{Entity: "Company", Metric: "second instalment", Value: "$3 billion", Class: "Money", Fig: million("3000000", "3")})
	if w.LiteralOperands || w.Result != "8000000" {
		t.Fatalf("a sum over refs: %+v", w)
	}
	scaled := func(analysis.Span) analysis.Label {
		return lab("", analysis.KindMoney, "USD", "1000000", "million", "", "")
	}
	for _, c := range []struct {
		sec  Section
		want string
	}{
		{labelledSection("The instalments total $8,000,000 [F1].", func(analysis.Span) analysis.Label { return lab("", analysis.KindMoney, "USD", "1", "", "", "") }), "the memo writes it digit for digit"},
		{readSection(Section{Markdown: "The instalments total {" + w.WID + "} [F1].", Handles: []Fig{{"{" + w.WID + "}", "", sheetValue(w.Result, w.Unit)}}}), "the memo writes it digit for digit"},
		{labelledSection("The instalments total $8 million [F1].", scaled), "the memo writes it at another scale's words"},
	} {
		lines, _, gate, computed := lintLimited("I1", c.sec, map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
		if len(lines) != 0 || len(computed) != 1 || !computed[0].Mismatch || !strings.HasPrefix(computed[0].Text, "⚠ $8,000,000 = $5,000,000 [F1] “") ||
			!strings.Contains(computed[0].Text, ", scaled by “billion”") || !strings.Contains(computed[0].Text, c.want) || gate["F1"] != limitedScale {
			t.Errorf("%q: $8 million (the record: $8 billion) by a ref-only sum: lint %q, gate %v, computed %+v", c.sec.Markdown, lines, gate, computed)
		}
	}
}

// r12 L1: a memo scale that agrees with its scaled inputs' words ("3,120 million" over "2810" and
// "310" read in millions from "EUR million") is the reading at its word: listed, not gated.
func TestConsistentScaleListedNotGated(t *testing.T) {
	quote := "Altamira Inc.,Turnover,Worldwide,2810,EUR million\nSorell B.V.,Turnover,Worldwide,310,EUR million"
	eur := func(analysis.Span) analysis.Label {
		return lab("", analysis.KindMoney, "EUR", "1000000", "million", "", "")
	}
	m := func(n, text string) *analysis.Reading {
		return &analysis.Reading{Kind: analysis.KindMoney, Currency: "EUR", Amount: n, Scale: "1000000", ScaleFrom: "million", Text: text}
	}
	f1, w := refFinding(t, quote, eur, "sum",
		analysis.Value{Entity: "Altamira", Metric: "turnover", Value: "2810", Unit: "EUR", Class: "Money", Fig: m("2810000000", "2810")},
		analysis.Value{Entity: "Sorell", Metric: "turnover", Value: "310", Unit: "EUR", Class: "Money", Fig: m("310000000", "310")})
	lines, _, gate, computed := lintLimited("I1", labelledSection("Combined worldwide turnover is EUR 3,120 million [F1].", eur), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) != 0 || len(gate) != 0 || len(computed) != 1 || computed[0].Mismatch || !computed[0].Check ||
		!strings.Contains(computed[0].Text, "; written in the memo as 3,120 million") || !strings.Contains(computed[0].Text, "“Altamira Inc.,Turnover,Worldwide,2810,EUR million…”") {
		t.Errorf("lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// H: the same sum written out by refs the reader read at scale 1 is plain arithmetic: listed, not
// gated.
func TestRefSumOfWrittenOutAmountsIsArithmetic(t *testing.T) {
	quote := "The first instalment is $5,000,000. The second instalment is $3,000,000."
	money := func(analysis.Span) analysis.Label { return lab("", analysis.KindMoney, "USD", "1", "", "", "") }
	f1, w := refFinding(t, quote, money, "sum",
		analysis.Value{Entity: "Company", Metric: "first instalment", Value: "$5,000,000", Class: "Money", Fig: &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "5000000", Text: "5,000,000"}},
		analysis.Value{Entity: "Company", Metric: "second instalment", Value: "$3,000,000", Class: "Money", Fig: &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "3000000", Text: "3,000,000"}})
	sec := labelledSection("The instalments total $8,000,000 [F1].", money)
	lines, _, gate, computed := lintLimited("I1", sec, map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) != 0 || len(gate) != 0 || len(computed) != 1 || computed[0].Check || !strings.HasPrefix(computed[0].Text, "$8,000,000 = $5,000,000 [F1] “…first instalment is $5,000,000.") ||
		!strings.HasSuffix(computed[0].Text, " + $3,000,000 [F1] “…second instalment is $3,000,000.”") {
		t.Errorf("lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// H (one wrong kind): a value the reader read as a count, used by ref as a period of days, is the
// period only by the value's unit: ⚠, and the finding is gated.
func TestRefCountUsedAsPeriodGated(t *testing.T) {
	quote := "The complaint was served on March 1, 2024. An answer is due within 21 days."
	label := func(s analysis.Span) analysis.Label {
		return lab("", cmpStr(s.Text == "21", analysis.KindCount, analysis.KindDate), "", "1", "", "", "")
	}
	f1, w := refFinding(t, quote, label, "date_add",
		analysis.Value{Entity: "Plaintiff", Metric: "service date", Value: "March 1, 2024", Class: "Date"},
		analysis.Value{Entity: "Defendant", Metric: "answer period", Value: "21", Unit: "days", Class: "Count", Fig: &analysis.Reading{Kind: analysis.KindCount, Amount: "21", Text: "21"}})
	lines, _, gate, computed := lintLimited("I1", labelledSection("The answer is due March 22, 2024 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) == 0 && (len(computed) != 1 || !computed[0].Mismatch || !strings.Contains(computed[0].Text, "21 days [F1] “") ||
		!strings.Contains(computed[0].Text, ": the figure check read it as a count figure, not a period in days") || gate["F1"] != limitedUnit) {
		t.Errorf("lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// H: a value whose unit is business days never feeds date_add's calendar days by ref (resolve keeps
// the unit); business_days counts it.
func TestBusinessDaysRefIsNoCalendarPeriod(t *testing.T) {
	f := analysis.Finding{FID: "F1", Verified: true, Values: []analysis.Value{
		{Entity: "Plaintiff", Metric: "service date", Value: "March 1, 2024", Class: "Date"},
		{Entity: "Defendant", Metric: "answer period", Value: "21", Unit: "business days", Class: "Count", Fig: &analysis.Reading{Kind: analysis.KindCount, Amount: "21", Text: "21"}}}}
	r1, r2 := "F1.v1", "F1.v2"
	ops := []analysis.Operand{{Ref: &r1}, {Ref: &r2}}
	if w, err := analysis.Calc([]analysis.Finding{f}, "date_add", ops, nil); err == nil {
		t.Errorf("date_add over 21 business days: %+v", w)
	}
	if w, err := analysis.Calc([]analysis.Finding{f}, "business_days", ops, nil); err != nil || w.Result != "2024-04-01" {
		t.Errorf("business_days over 21 business days: %+v, %v", w, err)
	}
}

// L1: a deadline computed from a literal whose cited words are not its unit gates its finding, as a
// memo period with those words does.
func TestMismatchedLiteralGatesItsFinding(t *testing.T) {
	f1 := businessDaysFinding(t)
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "days"})
	_, _, gate, computed := lintLimited("I1", labelledSection("The answer is due March 22, 2024 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(computed) != 1 || !computed[0].Mismatch || gate["F1"] != limitedUnit {
		t.Errorf("gate %v, computed %+v", gate, computed)
	}
}

// L3: a difference of counts read with no unit words (section numbers, perhaps) is checked first,
// and a negative input is written "− (−45)".
func TestCountInputsCheckedFirst(t *testing.T) {
	quote := "Sections 10.1 and 2.1 apply; notice is due forty-five (45) days before, or -45 days."
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: quote, Method: "exact"}}}
	p := analysis.PassageOf("q1", quote)
	var ls []analysis.Label
	for i, sp := range p.Spans {
		l := lab(analysis.SpanID("q1", i), analysis.KindCount, "", "1", "", "", cmpStr(strings.HasPrefix(sp.Text, "-") || sp.Start > 0 && quote[sp.Start-1] == '-', "negative", ""))
		if strings.Contains(sp.Text, ".") {
			l.Decimal = "."
		}
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	for i := range p.Spans {
		if r := rs[analysis.SpanID("q1", i)]; !r.Unclear {
			f1.Figures = append(f1.Figures, r)
		}
	}
	number := func(analysis.Span) analysis.Label { return lab("", analysis.KindCount, "", "1", "", "", "") }
	for _, c := range []struct {
		w    analysis.Worksheet
		memo string
		want string
	}{
		{sheet(t, "diff", [2]string{"10.1", ""}, [2]string{"2.1", ""}), "The difference is 8 [F1].", "8 = 10.1 [F1] “"},
		{sheet(t, "diff", [2]string{"45", ""}, [2]string{"-45", ""}), "The difference is 90 [F1].", " − (−45) [F1] “"},
	} {
		_, _, _, computed := lintLimited("I1", labelledSection(c.memo, number), map[string]Finding{"F1": f1}, []analysis.Worksheet{c.w})
		if len(computed) != 1 || !computed[0].Check || !strings.Contains(computed[0].Text, c.want) || !strings.Contains(computed[0].Text, ", read as a count figure") {
			t.Errorf("%q: %+v", c.memo, computed)
		}
	}
}

// L4: a snippet of a CSV or table row starts at the row's first cell, so its party stays.
func TestSnippetKeepsTheRowsFirstCell(t *testing.T) {
	q := "Party,Measure,Country,Amount,Unit\nVeltris Medical B.V.,Turnover,Netherlands,48,EUR million\nHeron Inc.,Turnover,Netherlands,90,EUR million"
	for _, c := range []struct{ num, party string }{{"48", "Veltris Medical B.V."}, {"90", "Heron Inc."}} {
		i := strings.Index(q, ","+c.num+",") + 1
		if s := snippet(q, i, i+len(c.num)); !strings.Contains(s, c.party) {
			t.Errorf("%s: %q drops %q", c.num, s, c.party)
		}
	}
	prose := "The purchase price of $212,500,000 is payable at closing, and the existing 8% stake is valued at $16,000,000 as of today."
	i := strings.Index(prose, "16,000,000")
	if s := snippet(prose, i, i+10); strings.HasPrefix(s, "The purchase") || !strings.Contains(s, "16,000,000") {
		t.Errorf("prose snippet %q", s)
	}
}

// L4: a date input the quote writes another way ("10/21/2026") still gets the quote's words.
func TestDateInputQuotedWhereverWritten(t *testing.T) {
	quote := "The summons was served on 10/21/2026 and an answer is due within 21 days."
	days := "days"
	p := analysis.PassageOf("q1", quote)
	var ls []analysis.Label
	for i, s := range p.Spans {
		l := lab(analysis.SpanID("q1", i), analysis.KindDate, "", "1", "", "", "")
		if s.Text == "21" {
			l.Kind, l.Unit, l.UnitFrom = analysis.KindDuration, &days, &days
		}
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: quote, Method: "exact"}}}
	for i := range p.Spans {
		f1.Figures = append(f1.Figures, rs[analysis.SpanID("q1", i)])
	}
	w := sheet(t, "date_add", [2]string{"2026-10-21", "date"}, [2]string{"21", "days"})
	_, _, _, computed := lintLimited("I1", labelledSection("The answer is due November 11, 2026 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(computed) != 1 || !strings.Contains(computed[0].Text, "= October 21, 2026 [F1] “…summons was served on 10/21/2026") {
		t.Errorf("%+v", computed)
	}
}

// L5: a worksheet's result the memo writes at a scale ("$228.5 million") is one item of the block,
// with how the memo writes it, not also a separate scale line.
func TestScaledMemoFigureListedWithItsWorksheet(t *testing.T) {
	quote := "The purchase price is $212,500,000. The existing stake is valued at $16,000,000."
	money := func(analysis.Span) analysis.Label { return lab("", analysis.KindMoney, "USD", "1", "", "", "") }
	f1, w := refFinding(t, quote, money, "sum",
		analysis.Value{Entity: "Buyer", Metric: "price", Value: "$212,500,000", Class: "Money", Fig: &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "212500000", Text: "212,500,000"}},
		analysis.Value{Entity: "Buyer", Metric: "stake", Value: "$16,000,000", Class: "Money", Fig: &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "16000000", Text: "16,000,000"}})
	sec := labelledSection("The total consideration is $228.5 million [F1].", func(analysis.Span) analysis.Label {
		l := lab("", analysis.KindMoney, "USD", "1000000", "million", "", "")
		l.Decimal = "."
		return l
	})
	lines, limited, _, computed := lintLimited("I1", sec, map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) != 0 || len(limited[limitedScale]) != 0 || len(computed) != 1 || !computed[0].Check ||
		!strings.Contains(computed[0].Text, "$228,500,000 = $212,500,000 [F1]") || !strings.Contains(computed[0].Text, "; written in the memo as $228.5 million") {
		t.Errorf("lint %q, limited %v, computed %+v", lines, limited, computed)
	}
}

// M: the double trace on a large section (n paragraphs each stating a computed deadline and a fee,
// n typed worksheets, n cited quotes). At 60c5bc8, n=80 took 2.3 s on the verifier's machine (1.5 s
// here); at 1c50231, 24.6 s. lintLimited parses each ground once and attributes in one pass.
func largeSection(t testing.TB, n int) (Section, map[string]Finding, []analysis.Worksheet) {
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}}
	var sheets []analysis.Worksheet
	var paras []string
	days := "days"
	for i := range n {
		d := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i*3)
		q := fmt.Sprintf("Notice %d was served on %s. A response is due within %d days of notice %d, and the fee is $%d,000.", i, d.Format("January 2, 2006"), 10+i, i, 100+i)
		f1.Citations = append(f1.Citations, Citation{Quote: q, Method: "exact"})
		id := fmt.Sprintf("q%d", i+1)
		p := analysis.PassageOf(id, q)
		var ls []analysis.Label
		for k, s := range p.Spans {
			l := lab(analysis.SpanID(id, k), analysis.KindDate, "", "1", "", "", "")
			switch {
			case strings.HasPrefix(q[s.End:], " days"):
				l.Kind, l.Unit, l.UnitFrom = analysis.KindDuration, &days, &days
			case s.Start > 0 && q[s.Start-1] == '$':
				l = lab(analysis.SpanID(id, k), analysis.KindMoney, "USD", "1", "", "", "")
			case s.Text == fmt.Sprint(i):
				l.Kind = analysis.KindOther
			}
			ls = append(ls, l)
		}
		rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
		for k := range p.Spans {
			f1.Figures = append(f1.Figures, rs[analysis.SpanID(id, k)])
		}
		var os []analysis.Operand
		for _, o := range [][2]string{{d.Format("2006-01-02"), "date"}, {fmt.Sprint(10 + i), "days"}} {
			os = append(os, analysis.Operand{Literal: &o[0], Unit: &o[1]})
		}
		w, err := analysis.Calc(nil, "date_add", os, nil)
		if err != nil {
			t.Fatal(err)
		}
		sheets = append(sheets, w)
		r, _ := analysis.ParseDate(w.Result)
		paras = append(paras, fmt.Sprintf("Response %d is due %s, and response %d's fee is $%d,000 [F1].", i, time.Date(r.Year, time.Month(r.Month), r.Day, 0, 0, 0, 0, time.UTC).Format("January 2, 2006"), i, 100+i))
	}
	return labelledSection(strings.Join(paras, "\n\n"), asDate), map[string]Finding{"F1": f1}, sheets
}

func TestDoubleTraceLargeSection(t *testing.T) {
	sec, use, sheets := largeSection(t, 80)
	start := time.Now()
	_, _, _, computed := lintLimited("I1", sec, use, sheets)
	el := time.Since(start)
	t.Logf("80 paragraphs × 80 worksheets: %v, %d computed", el, len(computed))
	if len(computed) == 0 {
		t.Error("no computed deadline listed")
	}
	if !docs.RaceEnabled && el > 2300*time.Millisecond {
		t.Errorf("80 paragraphs × 80 worksheets took %v (60c5bc8: 2.3 s)", el)
	}
}

func BenchmarkLintLimitedLargeSection(b *testing.B) {
	sec, use, sheets := largeSection(b, 80)
	for b.Loop() {
		lintLimited("I1", sec, use, sheets)
	}
}

// L4: a snippet never cuts a character, always holds its span, and stays short.
func FuzzSnippetBounds(f *testing.F) {
	f.Add("…Inc.,Turnover,Netherlands,90,EUR million", 30, 32)
	f.Add("Kaufpreis von 5 Mrd. € für die Übernahme durch die Gesellschaft", 15, 16)
	f.Add("a,b\nVeltris Medical B.V.,Turnover,Netherlands,48,EUR million", 48, 50)
	f.Fuzz(func(t *testing.T, q string, a, b int) {
		if q == "" || !utf8.ValidString(q) || a < 0 || b > len(q) || a > b || a < len(q) && !utf8.RuneStart(q[a]) || b < len(q) && !utf8.RuneStart(q[b]) {
			return
		}
		s := snippet(q, a, b)
		if !utf8.ValidString(s) {
			t.Fatalf("snippet(%q,%d,%d) = %q: invalid UTF-8", q, a, b, s)
		}
		if !strings.Contains(s, q[a:b]) {
			t.Fatalf("snippet(%q,%d,%d) = %q: lost the span %q", q, a, b, s, q[a:b])
		}
		if len(s) > (b-a)+64+24+2*len("…")+2*utf8.UTFMax {
			t.Fatalf("snippet(%q,%d,%d) = %q: %d bytes", q, a, b, s, len(s))
		}
	})
}

// H e2e (one wrong kind): the "21" of "21 business days" read as a count feeds date_add by ref; the
// value's unit keeps "business days", so date_add refuses it (the scripted lead then runs out), and
// the memo's calendar "March 22, 2024" (the record: April 1) is never delivered silently.
func TestRefOnlyBusinessDaysDeadlineNeverSilent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	f.reads = map[string]analysis.Label{"quote:21": lab("", analysis.KindCount, "", "1", "", "", ""), "value:21": lab("", analysis.KindCount, "", "1", "", "", "")}
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	period := m{"entity": "Defendant", "metric": "answer period", "value": "21", "unit": "business days", "date": nil, "class": "Count"}
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days",
			val("Defendant", "service date", "March 1, 2024", "Date"), period))},
		"draft_section": {section("The answer is due March 22, 2024 [F1].", "Due [F1].")},
	}), Config{})
	if ns := f.outputs(v.ID, "numbers"); len(ns) != 1 || !strings.Contains(ns[0], "date_add counts calendar days, and this period is in business days") {
		t.Errorf("date_add over a value in business days was not refused: %q", ns)
	}
	if out := deref(v.Output); strings.Contains(out, "The answer is due March 22, 2024") && !strings.Contains(out, "March 22, 2024 is not traced") && !strings.Contains(out, "⚠ March 22, 2024 =") {
		t.Errorf("a calendar deadline from \"21 business days\" (the record: April 1) delivered neither flagged nor listed ⚠; stop %s\n%s", stop(v), out)
	}
}

// H e2e (one wrong scale): "$5 billion" and "$3 billion" read ×10⁶ from "billion", summed by ref;
// the memo's "$8,000,000" is listed ⚠ and the finding gated, never traced silently.
func TestRefOnlyScaledSumDisclosedAndGated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe first instalment is $5 billion. The second instalment is $3 billion.\n")
	wrong := lab("", analysis.KindMoney, "USD", "1000000", "billion", "", "")
	f.reads = map[string]analysis.Label{"quote:5": wrong, "quote:3": wrong, "value:5": wrong, "value:3": wrong}
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What are the fees?", "Conduct"))), calls(extract(d1, "instalment", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "sum", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The instalments are $5 billion and $3 billion.", d1,
			"The first instalment is $5 billion. The second instalment is $3 billion",
			val("Company", "first instalment", "$5 billion", "Money"), val("Company", "second instalment", "$3 billion", "Money")))},
		"draft_section": {section("The instalments total $8,000,000 [F1].", "Fees [F1].")},
	}), Config{})
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	if out := deref(v.Output); strings.Contains(out, "total $8,000,000") && (!strings.Contains(out, "⚠ $8,000,000 = $5,000,000 [F1]") || !gated) {
		t.Errorf("$8,000,000 (the record: $8 billion) by a ref-only sum, not listed ⚠ or not gated (gated %v); stop %s\n%s", gated, stop(v), out)
	}
}

// lab-r4 r12 verifier repros (critique/biglaw-lab-r4-r12-verifier.md M1, LOW-4), kept as regression
// tests.

// countAt21 reads the "21" of a quote as a count (one wrong kind: "21 business days" is a period),
// its other spans as dates.
func countAt21(s analysis.Span) analysis.Label {
	return lab("", cmpStr(s.Text == "21", analysis.KindCount, analysis.KindDate), "", "1", "", "", "")
}

// M1: a value with no unit, read as a count, as date_add's day count (Calc takes a unitless number
// as days): a period only by Calc's default, never the reader's: ⚠, and its finding gated.
func TestRefCountNoUnitUsedAsPeriodGated(t *testing.T) {
	f1, w := refFinding(t, businessDaysQuote, countAt21, "date_add",
		analysis.Value{Entity: "Plaintiff", Metric: "service date", Value: "March 1, 2024", Class: "Date"},
		analysis.Value{Entity: "Defendant", Metric: "answer period", Value: "21", Class: "Count", Fig: &analysis.Reading{Kind: analysis.KindCount, Amount: "21", Text: "21"}})
	lines, _, gate, computed := lintLimited("I1", labelledSection("The answer is due March 22, 2024 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) != 0 || len(computed) != 1 || !computed[0].Mismatch || !strings.Contains(computed[0].Text, ": the figure check read it as a count figure, not a period in days") || gate["F1"] != limitedUnit {
		t.Errorf("a count used as date_add's days (no unit): lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// M1: the same through a literal "21" with no unit whose cited reading is a count.
func TestLiteralCountNoUnitUsedAsPeriodGated(t *testing.T) {
	p := analysis.PassageOf("q1", businessDaysQuote)
	var ls []analysis.Label
	for i, s := range p.Spans {
		l := countAt21(s)
		l.Span = analysis.SpanID("q1", i)
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: businessDaysQuote, Method: "exact"}}}
	for i := range p.Spans {
		f1.Figures = append(f1.Figures, rs[analysis.SpanID("q1", i)])
	}
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", ""})
	lines, _, gate, computed := lintLimited("I1", labelledSection("The answer is due March 22, 2024 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) != 0 || len(computed) != 1 || !computed[0].Mismatch || !strings.Contains(computed[0].Text, ": the figure check read it as a count figure, not a period in days") || gate["F1"] != limitedUnit {
		t.Errorf("a count literal used as date_add's days (no unit): lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// LOW-4: a ref's snippet is its own span's (admission's match, Reading.Stated), not the first span
// of the same amount.
func TestRefSnippetIsItsOwnSpan(t *testing.T) {
	quote := "The deposit is $5,000,000, refundable. The holdback, also $5,000,000, is released at closing."
	money := func(analysis.Span) analysis.Label { return lab("", analysis.KindMoney, "USD", "1", "", "", "") }
	fig := func(span string) *analysis.Reading {
		return &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "5000000", Text: "5,000,000", Stated: span}
	}
	f1, w := refFinding(t, quote, money, "sum",
		analysis.Value{Entity: "Buyer", Metric: "holdback", Value: "$5,000,000", Class: "Money", Fig: fig("q1.s2")},
		analysis.Value{Entity: "Buyer", Metric: "deposit", Value: "$5,000,000", Class: "Money", Fig: fig("q1.s1")})
	_, _, _, computed := lintLimited("I1", labelledSection("Together they are $10,000,000 [F1].", money), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(computed) != 1 || !strings.HasPrefix(computed[0].Text, "$10,000,000 = $5,000,000 [F1] “…The holdback, also $5,000,000, is released at…” + $5,000,000 [F1] “The deposit is $5,000,000, refundable. The…”") {
		t.Errorf("%+v", computed)
	}
}

// M1 e2e: the extractor's value has no unit ("unit": null), the reader reads the "21" of "21 business
// days" as a count, and date_add takes it as calendar days: the memo's March 22 (the record: April
// 1) is listed ⚠ and F1 gated.
func TestRefCountNoUnitDeadlineGated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	f.reads = map[string]analysis.Label{"quote:21": lab("", analysis.KindCount, "", "1", "", "", ""), "value:21": lab("", analysis.KindCount, "", "1", "", "", "")}
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	period := m{"entity": "Defendant", "metric": "answer period", "value": "21", "unit": nil, "date": nil, "class": "Count"}
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days",
			val("Defendant", "service date", "March 1, 2024", "Date"), period))},
		"draft_section": {section("The answer is due March 22, 2024 [F1].", "Due [F1].")},
	}), Config{})
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	out := deref(v.Output)
	if !strings.Contains(out, "The answer is due March 22, 2024") || !strings.Contains(out, "⚠ March 22, 2024 = March 1, 2024 [F1]") ||
		!strings.Contains(out, "not a period in days") || !gated {
		t.Errorf("calendar March 22 from a count used as days: not delivered ⚠ and gated (gated %v); stop %s\n%s", gated, stop(v), out)
	}
}
