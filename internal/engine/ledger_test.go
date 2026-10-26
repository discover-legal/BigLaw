// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// TestSpecsLoadAndValidate: every subagent spec and Go tool spec registers (strict schemas,
// descriptions with three sentences, a "Do not use" line and validating examples, allowed tools
// registered), every subagent has its prompt, and the memo lead's list fits the cap.
func TestSpecsLoadAndValidate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine(nil, Config{})
	r := &run{e: f.e, t: Task{ID: "00000000-0000-0000-0000-000000000000", WorkflowType: "memo", ToolBandit: true}}
	rt, err := f.e.runtime(r)
	if err != nil {
		t.Fatal(err)
	}
	names := rt.Reg.Names()
	want := []string{"analyze_issue", "audit_gaps", "check_deviation", "committee", "cover", "draft_section", "exact_calc", "extract_facts", "figure_context", "finish", "join_discrepancies", "judge_figures",
		"ledger", "numbers", "prosecute", "read_figures", "reproduce", "retrieve", "sweep", "use_skill", "verify_quote"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("registry %v", names)
	}
	if _, err := rt.Reg.Defs(MemoVisible); err != nil || len(MemoVisible) != 12 {
		t.Fatalf("memo list: %v", err)
	}
	files, _ := fs.Glob(specFS, "specs/agents/*.json")
	for _, f := range files {
		if _, err := specFS.ReadFile(strings.TrimSuffix(f, ".json") + ".md"); err != nil {
			t.Errorf("%s has no prompt", f)
		}
	}
	for _, n := range []string{"lead", "meta"} {
		if strings.TrimSpace(prompt(context.Background(), n)) == "" {
			t.Errorf("no %s prompt", n)
		}
	}
	// No matter data in any schema or example: the proof-run parties never appear.
	for _, n := range names {
		s, _ := rt.Reg.Spec(n)
		for _, party := range []string{"Greystone", "Talbot", "Kestrel", "Quillon", "Lumen", "Altamira"} {
			if strings.Contains(string(s.In)+string(s.Out)+s.Description, party) {
				t.Errorf("%s's spec names %s", n, party)
			}
		}
	}
}

// TestExtractFactsTypedValues (trap): a value its own quote does not state is dropped; one it does
// is kept, typed.
func TestExtractFactsTypedValues(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1"))},
		"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices",
			val("Greystone", "damages", "$644,910", "Money"), val("Greystone", "interest", "$12,000", "Money"), val("Greystone", "filed", "October 9, 2026", "Date")))},
	}), Config{})
	fs, _ := f.e.Findings(context.Background(), v.ID)
	if len(fs) != 1 || len(fs[0].Values) != 1 || fs[0].Values[0].Value != "$644,910" {
		t.Fatalf("values %+v", fs)
	}
	// The lead sees each kept value under the ref numbers takes (bench: it guessed "F30.d1").
	if ex := f.outputs(v.ID, "extract_facts"); len(ex) != 1 || !strings.Contains(ex[0], `"values":[{"ref":"F1.v1","value":"$644,910"}]`) {
		t.Fatalf("extract_facts result %q", ex)
	}
}

// TestLedgerBucketsFindingsByIssue: a finding belongs to the issue whose call produced it, and
// ledger op findings filters by it.
func TestLedgerBucketsFindingsByIssue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Damages?", "Conduct"), issue("I2", "Service?", "Requirement"))), calls(extract(d1, "damages", "I1"), extract(d2, "served", "I2")),
			calls(ledgerOp("findings", "issueId", "I2", "detail", "brief"))},
		"extract_facts": {facts(fd("Damages claimed.", d1, "Greystone claims damages of $644,910 for unpaid invoices")),
			facts(fd("Talbot was served on 10/21/2026.", d2, "Talbot was served on 10/21/2026 via its registered agent"))},
	}), Config{})
	if len(v.Findings) != 2 || v.Findings[0].IssueID != "I1" || v.Findings[1].IssueID != "I2" || v.Findings[1].Question != "Service?" {
		t.Fatalf("%+v", v.Findings)
	}
	out := f.outputs(v.ID, "ledger")
	if len(out) != 2 || !strings.Contains(out[1], `"fid":"F2"`) || strings.Contains(out[1], `"fid":"F1"`) {
		t.Fatalf("bucket: %q", out)
	}
}

// TestLedgerExactDuplicateMerged: a finding that repeats an admitted one in the same issue (the
// same quote, the same values) merges into the first, its citation kept once; a restatement in other
// words (here, a part of the first's quote) stays a finding of its own (lab-r4 verifier H2).
func TestLedgerExactDuplicateMerged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Damages?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(extract(d1, "invoices", "I1")), calls(extract(d1, "part", "I1"))},
		"extract_facts": {facts(fd("Greystone claims damages of $644,910 for unpaid invoices from Talbot.", d1, "Greystone claims damages of $644,910 for unpaid invoices")),
			facts(fd("Greystone claims damages of $644,910 for the unpaid invoices from Talbot.", d1, "Greystone claims damages of $644,910 for unpaid invoices")),
			facts(fd("Greystone claims damages of $644,910 for the unpaid invoices from Talbot.", d1, "damages of $644,910"))},
	}), Config{})
	if len(v.Findings) != 2 || len(v.Findings[0].Citations) != 1 {
		t.Fatalf("%+v", v.Findings)
	}
}

// TestSweepVisitsEverySectionOnce: the sweep runs extract_facts once per page of every top-level
// section of every document, short sections batched (with the carried sectionChunks vectors);
// visits run in parallel, so they are compared as a set (TestSweepReadsInParallelInPlanOrder: the
// ledger is in plan order).
func TestSweepVisitsEverySectionOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	visits, err := f.e_(t).plan(context.Background(), f.ids(), f.ids())
	if err != nil {
		t.Fatal(err)
	}
	if len(visits) != 2 { // complaint: its three short sections in one visit; docket: one
		t.Fatalf("visits %+v", visits)
	}
	reps := []model.Reply{}
	for range visits {
		reps = append(reps, facts())
	}
	sweep := tc("sweep", m{"over": "sections", "documentIds": nil})
	v := f.run(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Damages?", "Conduct"))), calls(sweep)}, "extract_facts": reps}), Config{})
	var got []Visit
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "extract_facts" {
			var in struct {
				DocumentID, SectionID, Through string
				Offset                         int
			}
			jsonDecode(c.Messages[1].Content.(string), &in)
			got = append(got, Visit{DocumentID: in.DocumentID, SectionID: in.SectionID, Offset: in.Offset, Through: in.Through})
		}
	}
	key := func(a, b Visit) int { return strings.Compare(a.key(), b.key()) }
	slices.SortFunc(got, key)
	slices.SortFunc(visits, key)
	for i := range visits {
		visits[i].text = ""
	}
	if !reflect.DeepEqual(got, visits) || !strings.Contains(strings.Join(f.outputs(v.ID, "sweep"), ""), `"visited":2`) {
		t.Fatalf("visited %+v, want %+v", got, visits)
	}
}

func (f *fixture) e_(t *testing.T) *Engine {
	if f.e == nil {
		f.engine(nil, Config{})
	}
	return f.e
}

// Carried (v1 orchestrator/figures_test.go sectionChunks): deterministic, a full cover of the text
// (the pages, joined, are the document), section-aligned (each page starts a section or continues
// one).
func TestSectionChunks_Deterministic(t *testing.T) {
	t.Parallel()
	f := newFixture(t, strings.Repeat("1. Long\n"+strings.Repeat("word ", 3000)+"\n", 2))
	a, _ := f.e_(t).plan(context.Background(), f.ids(), f.ids())
	b, _ := f.e.plan(context.Background(), f.ids(), f.ids())
	if !reflect.DeepEqual(a, b) || len(a) < 4 {
		t.Fatalf("%+v vs %+v", a, b)
	}
}

func TestSectionChunks_FullCover(t *testing.T) {
	t.Parallel()
	text := "Preface line.\n" + strings.Repeat("1. Long\n"+strings.Repeat("word ", 3000)+"\n1.1 Sub\nsub text\n", 2) + "2. Short\nend.\n"
	f := newFixture(t, text)
	visits, _ := f.e_(t).plan(context.Background(), f.ids(), f.ids())
	var b strings.Builder
	for _, v := range visits {
		if v.Through != "" {
			run, ok := f.e.runText(context.Background(), f.ids(), v.DocumentID, v.SectionID, v.Through)
			if !ok {
				t.Fatalf("%+v is not a run", v)
			}
			b.WriteString(run)
			continue
		}
		p, err := f.d.Section(context.Background(), f.ids(), v.DocumentID, v.SectionID, v.Offset, sweepTokens)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(p.Text)
	}
	if b.String() != text {
		t.Fatalf("the pages do not rebuild the document (%d vs %d bytes)", b.Len(), len(text))
	}
}

func TestSectionChunks_SectionAligned(t *testing.T) {
	t.Parallel()
	f := newFixture(t, complaintText)
	visits, _ := f.e_(t).plan(context.Background(), f.ids(), f.ids())
	out, _ := f.d.Outline(context.Background(), f.ids(), f.ids()[0])
	for _, v := range visits {
		if v.Offset == 0 && !slices.ContainsFunc(out, func(o docsEntry) bool { return o.SectionID == v.SectionID }) {
			t.Fatalf("%+v starts no section", v)
		}
	}
}

// TestSweepBatchesShortSections (real-model bench, SEC referral): a document numbered by paragraph
// (a hundred top-level sections of a few hundred characters) is read in batches of consecutive
// paragraphs of at most sweepTokens, not one subagent run per paragraph; each batched visit's
// sectionText holds its run verbatim, the batches rebuild the document, and the sweep completes.
func TestSweepBatchesShortSections(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 1; i <= 40; i++ { // ~29 tokens each: about 1,160 in all
		fmt.Fprintf(&b, "%d. The Division alleges fact number %d about the trade allocations of the Firm in the review period.\n\n", i, i)
	}
	f := newFixture(t, b.String())
	visits, err := f.e_(t).plan(context.Background(), f.ids(), f.ids())
	if err != nil {
		t.Fatal(err)
	}
	if len(visits) != 2 || visits[0].Through == "" || visits[1].Through == "" {
		t.Fatalf("40 short paragraphs planned as %d visits (want 2 batches of at most %d tokens): %+v", len(visits), sweepTokens, visits)
	}
	sweep := tc("sweep", m{"over": "sections", "documentIds": nil})
	v := f.run(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Allegations?", "Conduct"))), calls(sweep)}, "extract_facts": {facts(), facts()}}), Config{})
	var texts []string
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "extract_facts" {
			var in struct{ SectionText string }
			jsonDecode(c.Messages[1].Content.(string), &in)
			texts = append(texts, in.SectionText)
		}
	}
	slices.SortFunc(texts, func(x, y string) int { return strings.Index(b.String(), x) - strings.Index(b.String(), y) }) // read in parallel
	out := strings.Join(f.outputs(v.ID, "sweep"), "")
	if len(texts) != 2 || strings.Join(texts, "") != b.String() || !strings.Contains(out, `"visited":2`) || !strings.Contains(out, `"incomplete":false`) {
		t.Fatalf("%d extract_facts runs (want 2 that rebuild the document); sweep %s", len(texts), out)
	}
}

// TestSweepIdempotent (P8): a second sweep reads nothing again and returns the same findings.
func TestSweepIdempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, complaintText)
	d1 := f.docs[0].ID
	sweep := tc("sweep", m{"over": "sections", "documentIds": nil})
	v := f.run(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "Damages?", "Conduct"))), calls(sweep), calls(sweep)},
		"extract_facts": {facts(on("I1", fd("Suit filed.", d1, "sued Talbot Fresh Foods, Inc. on October 9, 2026")), on("I1", fd("Damages claimed.", d1, "Greystone claims damages of $644,910")))},
	}), Config{})
	out := f.outputs(v.ID, "sweep")
	if len(out) != 2 || !strings.Contains(out[1], `"visited":0`) || !strings.Contains(out[1], `"findings":["F1","F2"]`) {
		t.Fatalf("sweeps %q", out)
	}
	n := 0
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		n += btoi(c.Agent == "extract_facts")
	}
	if n != 1 { // the complaint's three short sections are one visit
		t.Fatalf("%d extract_facts calls, want 1", n)
	}
}

// TestSweepReportsIncompleteWhenBudgetShort (trap): when the sweep share runs out, sweep says
// incomplete, lists the uncovered sections, and Limitations names them; never silently partial.
func TestSweepReportsIncompleteWhenBudgetShort(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&doc, "%d. Clause %d\nThe supplier shall deliver lot %d on time.\n%s\n\n", i, i, i, filler)
	}
	f := newFixture(t, doc.String(), docketText)
	sweep := tc("sweep", m{"over": "sections", "documentIds": nil})
	v := f.run(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "Damages?", "Conduct"))), calls(sweep)},
		"extract_facts": {facts(), facts(), facts(), facts()},
	}), Config{BudgetCalls: 20, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}) // 3 calls free for the sweep (15 kept for later phases)
	out := strings.Join(f.outputs(v.ID, "sweep"), "")
	notes, _ := f.e.rows(context.Background(), v.ID, kNote)
	if !strings.Contains(out, `"incomplete":true`) || !strings.Contains(out, f.docs[1].ID) || len(notes) == 0 || !strings.Contains(string(notes[0].Data), "not read") {
		t.Fatalf("sweep %s notes %v", out, notes)
	}
}

// barrier is test plumbing around model.Fake: the critics' calls wait until all three are in
// flight at once (or fail after a second): proof they run in parallel.
type barrier struct {
	*model.Fake
	mu      sync.Mutex
	waiting int
	all     chan struct{}
}

func (b *barrier) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	if slices.Contains(critics, req.Agent) {
		b.mu.Lock()
		if b.waiting++; b.waiting == 3 {
			close(b.all)
		}
		b.mu.Unlock()
		select {
		case <-b.all:
		case <-time.After(time.Second):
			return nil, errors.New("critics ran one at a time")
		}
	}
	return b.Fake.Chat(ctx, req)
}

// TestCommitteeRunsThreeInParallel and TestCommitteeCriticsContextFree: the three critics run at
// once, each on its own request only: no lead transcript, and reproduce never sees the draft.
func TestCommitteeRunsThreeInParallel(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	b := &barrier{Fake: newFake(f.happy()), all: make(chan struct{})}
	f.reopen(b, nil)
	task := f.submit(0)
	f.step(context.Background())
	if v := f.get(task.ID); stop(v) != "awaiting_gate/finished" {
		t.Fatalf("%s %v", stop(v), v.Error)
	}
}

func TestCommitteeCriticsContextFree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v := f.run(f.happy(), Config{})
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if !slices.Contains(critics, c.Agent) {
			continue
		}
		first := c.Messages[1].Content.(string)
		if len(c.Messages) > 2 && c.Messages[2].Role == "user" || strings.Contains(first, "Request from the lawyer") || strings.Contains(first, "Defense intake memo") {
			t.Errorf("%s saw the lead's context: %s", c.Agent, first)
		}
		if c.Agent == "reproduce" && (strings.Contains(first, "unpaid invoices") || strings.Contains(first, "[F1]")) {
			t.Errorf("reproduce saw the draft: %s", first)
		}
	}
	if stop(v) != "awaiting_gate/finished" {
		t.Fatal(stop(v))
	}
}

// Paged drafting (revision 5).
func pagedScripts(f *fixture) map[string][]model.Reply {
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	return f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Damages?", "Conduct"), issue("I2", "Service?", "Requirement"))),
			calls(extract(d1, "damages", "I1"), extract(d2, "served", "I2")),
			calls(draft("I2", "F2")), calls(draft("I1", "F1")), calls(ledgerOp("draft_get", "issueId", "I3")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("Damages claimed.", d1, "Greystone claims damages of $644,910 for unpaid invoices", val("Greystone", "damages", "$644,910", "Money"))),
			facts(fd("Talbot was served on 10/21/2026.", d2, "Talbot was served on 10/21/2026 via its registered agent"))},
		"draft_section": {section("Talbot was served on 10/21/2026 [F2].", "SUMMARY-I2: service date [F2]."), section("Greystone claims {Zephyr} [F1], served later (see service).", "SUMMARY-I1: damages {Zephyr} [F1].")},
	})
}

// TestPagedDraftingLossless (carried TestPagedBoard_CompactPagingIsLossless): the memo joins every
// section's full text, in plan order.
func TestPagedDraftingLossless(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	out := deref(f.run(pagedScripts(f), Config{}).Output)
	i, j := strings.Index(out, "Greystone claims $644,910 [F1], served later (see service)."), strings.Index(out, "Talbot was served on 10/21/2026 [F2].")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("sections lost or out of plan order:\n%s", out)
	}
}

// TestPriorSummariesPassedInOrder: a later section's drafter gets the earlier sections' summaries.
func TestPriorSummariesPassedInOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.run(pagedScripts(f), Config{})
	var reqs []string
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "draft_section" {
			reqs = append(reqs, c.Messages[1].Content.(string))
		}
	}
	if len(reqs) != 2 || strings.Contains(reqs[0], "SUMMARY") || !strings.Contains(reqs[1], "SUMMARY-I2") {
		t.Fatalf("requests %q", reqs)
	}
}

// TestDraftGetOnlyFinishedSections (trap): expanding an undrafted issue is a typed not_drafted
// error, never an empty section.
func TestDraftGetOnlyFinishedSections(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v := f.run(pagedScripts(f), Config{})
	out := f.outputs(v.ID, "ledger")
	if len(out) != 2 || !strings.HasPrefix(out[1], "failed") || !strings.Contains(out[1], "not_drafted") {
		t.Fatalf("draft_get %q", out)
	}
}

// TestSummaryNeverRendered: summaries go to drafters only.
func TestSummaryNeverRendered(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if out := deref(f.run(pagedScripts(f), Config{}).Output); strings.Contains(out, "SUMMARY") {
		t.Fatalf("a summary was rendered:\n%s", out)
	}
}

// TestBanditStatsPersistAcrossTasks: one task's tool results are in the record a later task reads.
func TestBanditStatsPersistAcrossTasks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v := f.run(f.happy(), Config{})
	sig := agent.Signature("memo", "", "US", "lead-m", 2)
	r := &run{e: f.e, t: Task{ID: v.ID}}
	st, err := stats{r}.Load(context.Background(), sig)
	if err != nil || st[agent.Arm{Tool: "extract_facts", Model: "lead-m"}].N != 1 || st[agent.Arm{Tool: "ledger"}].N != 1 {
		t.Fatalf("%v %+v", err, st)
	}
	f.engine(f.happy(), Config{})
	task := f.submit(0)
	f.step(context.Background())
	var hint string
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "lead" && len(c.Messages) > 3 {
			hint = fmt.Sprint(c.Messages[len(c.Messages)-1].Content)
			break
		}
	}
	if !strings.Contains(hint, "extract_facts 1.50 (1 calls)") {
		t.Fatalf("the second task's hint does not use the first's record: %s (task %s)", hint, task.ID)
	}
}

// TestBanditDelayedCriticCredit: the prosecutor's verdict adds ±0.5 to the arm that produced the
// finding, without counting a call.
func TestBanditDelayedCriticCredit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v := f.run(f.happy(), Config{})
	sig := agent.Signature("memo", "", "US", "lead-m", 2)
	st, _ := stats{&run{e: f.e, t: Task{ID: v.ID}}}.Load(context.Background(), sig)
	if a := st[agent.Arm{Tool: "extract_facts", Model: "lead-m"}]; a.N != 1 || a.Reward != 1.5 {
		t.Fatalf("extract_facts arm %+v (want reward 1 + 0.5 credit over 1 call)", a)
	}
}

// Packs.
func TestPackSelectionByJurisdictionPrefix(t *testing.T) {
	codes := func(ps []Pack) (out []string) {
		for _, p := range ps {
			out = append(out, p.Code)
		}
		return out
	}
	for j, want := range map[string][]string{"US-NY": {"us", "us-ny"}, "us": {"us"}, "US-CA": {"us"}, "UK": nil, "USA": nil} {
		if got := codes(SelectPacks(j, "")); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", j, got, want)
		}
	}
	if got := codes(SelectPacks("US", "litigation")); !reflect.DeepEqual(got, []string{"us", "litigation"}) {
		t.Errorf("practice: %v", got)
	}
}

func TestCandidatePackDisclosed(t *testing.T) {
	if s := candidates(SelectPacks("US-NY", "litigation")); !strings.Contains(s, "us, us-ny, litigation") || !strings.Contains(s, "no named lawyer has reviewed") {
		t.Fatalf("%q", s)
	}
	if candidates([]Pack{{Code: "x", Status: "reviewed"}}) != "" {
		t.Fatal("a reviewed pack was disclosed")
	}
}

// TestSweepUncoveredNamesTheWholeBatch (r2 M-1, r3): a batched visit that is not read is disclosed as
// its whole run of sections, not as its first section alone, through the real output schema check
// (bench r3: sweep.json lacked through, so every incomplete sweep failed validation and the lead
// never saw what was unread).
func TestSweepUncoveredNamesTheWholeBatch(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "%d. The Division alleges fact number %d about the trade allocations of the Firm in the review period.\n\n", i, i)
	}
	f := newFixture(t, b.String())
	visits, err := f.e_(t).plan(context.Background(), f.ids(), f.ids())
	if err != nil || len(visits) != 2 || visits[0].Through == "" {
		t.Fatalf("plan %+v %v", visits, err)
	}
	bad := model.Reply{Text: "not json"}
	sweep := tc("sweep", m{"over": "sections", "documentIds": nil})
	v := f.run(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Allegations?", "Conduct"))), calls(sweep)},
		"extract_facts": {bad, bad, bad, bad}}), Config{})
	out := strings.Join(f.outputs(v.ID, "sweep"), "")
	notes, _ := f.e.rows(context.Background(), v.ID, kNote)
	note := ""
	for _, n := range notes {
		note += string(n.Data)
	}
	seen := "" // what the lead read of the sweep (r3: the output failed its schema on through, so it read only the failure)
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "lead" {
			seen = fmt.Sprint(c.Messages[len(c.Messages)-1].Content)
		}
	}
	if !strings.HasPrefix(out, "committed") || !strings.Contains(seen, `"uncovered":[`) {
		t.Fatalf("the incomplete sweep's output did not pass its schema: %s; the lead read %q", out, seen)
	}
	for _, x := range visits {
		if !strings.Contains(out, `"through":"`+x.Through+`"`) || !strings.Contains(seen, `"through":"`+x.Through+`"`) || !strings.Contains(note, x.SectionID+" to "+x.Through) {
			t.Fatalf("unread batch %s–%s not disclosed whole: sweep %s note %s", x.SectionID, x.Through, out, note)
		}
	}
}

// TestExtractFactsRefusesABadThrough (r2 M-2): a through that is not a run of top-level sections
// from sectionId fails the call; it never reads sectionId alone as if it had read the run.
func TestExtractFactsRefusesABadThrough(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "%d. The Division alleges fact number %d about the trade allocations of the Firm in the review period.\n\n", i, i)
	}
	f := newFixture(t, b.String())
	visits, err := f.e_(t).plan(context.Background(), f.ids(), f.ids())
	if err != nil || len(visits) != 2 {
		t.Fatalf("plan %+v %v", visits, err)
	}
	back := tc("extract_facts", m{"question": "q", "documentId": f.docs[0].ID, "sectionId": visits[1].SectionID, "through": visits[0].SectionID, "offset": nil, "query": nil, "issueId": "I1"})
	v := f.run(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Allegations?", "Conduct"))), calls(back)}, "extract_facts": {facts()}}), Config{})
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "extract_facts" {
			t.Fatalf("a backwards through ran extract_facts on sectionId alone: %s", strings.Join(f.outputs(v.ID, "extract_facts"), ""))
		}
	}
	if out := strings.Join(f.outputs(v.ID, "extract_facts"), ""); !strings.Contains(out, "through") {
		t.Fatalf("extract_facts %s", out)
	}
}

// The lab-r3 verifier's repros (H1–H3, M1–M3), kept as regression tests: each failed on 1814a6a.

// TestLedgerFindingsPage (bench4, SEC: ledger op findings detail full returned all 892 findings,
// 682 KB, in one result, which overflowed the lead's context): the list comes a page at a time
// (ledgerPage bytes), next names the offset of the following page, and the pages hold every
// finding once, by issue too.
func TestLedgerFindingsPage(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Terms\nThe Seller shall deliver the goods.\n")
	f.engine(map[string][]model.Reply{}, Config{})
	task := f.submit(0)
	quote := strings.Repeat("The Seller shall deliver the goods on time and in full under the Agreement. ", 8)
	for i := range 900 {
		x := Finding{Finding: analysis.Finding{FID: fmt.Sprintf("F%d", i+1), IssueID: fmt.Sprintf("I%d", i%3+1), Claim: "The Seller must deliver.", Verified: true}}
		x.Citations = []Citation{{DocumentID: f.docs[0].ID, Quote: quote, Method: "exact", Confidence: 1}}
		if err := f.e.write(context.Background(), task.ID, kFinding, x.FID, x, true); err != nil {
			t.Fatal(err)
		}
	}
	page := func(issue any, offset any) (fids []string, next *int, size int) {
		in, _ := json.Marshal(m{"op": "findings", "issues": nil, "issueId": issue, "fid": nil, "detail": "full", "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": offset})
		res, err := f.e.ledgerTool(builtinSpecs).Call(context.Background(), agent.Call{TaskID: task.ID, Input: in})
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Findings []struct{ FID string }
			Next     *int
		}
		_ = json.Unmarshal(res.Output, &out)
		for _, x := range out.Findings {
			fids = append(fids, x.FID)
		}
		return fids, out.Next, len(res.Output)
	}
	for _, issue := range []any{nil, "I2"} {
		seen, pages := map[string]int{}, 0
		var off any
		for {
			fids, next, size := page(issue, off)
			if pages++; size > agent.ShrunkResult-1000 || len(fids) == 0 || pages > 100 {
				t.Fatalf("issue %v page %d: %d bytes, %d findings", issue, pages, size, len(fids))
			}
			for _, x := range fids {
				seen[x]++
			}
			if next == nil {
				break
			}
			off = *next
		}
		if want := map[bool]int{true: 900, false: 300}[issue == nil]; len(seen) != want || pages < 2 {
			t.Errorf("issue %v: %d findings over %d pages, want %d over several", issue, len(seen), pages, want)
		}
		for x, n := range seen {
			if n != 1 {
				t.Errorf("%s listed %d times", x, n)
			}
		}
	}
}
