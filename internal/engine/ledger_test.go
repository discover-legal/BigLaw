// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
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
	want := []string{"analyze_issue", "audit_gaps", "check_deviation", "committee", "draft_section", "extract_facts", "finish", "join_discrepancies",
		"ledger", "numbers", "prosecute", "reproduce", "retrieve", "sweep", "verify_quote"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("registry %v", names)
	}
	if _, err := rt.Reg.Defs(MemoVisible); err != nil || len(MemoVisible) != 11 {
		t.Fatalf("memo list: %v", err)
	}
	files, _ := fs.Glob(specFS, "specs/agents/*.json")
	for _, f := range files {
		if _, err := specFS.ReadFile(strings.TrimSuffix(f, ".json") + ".md"); err != nil {
			t.Errorf("%s has no prompt", f)
		}
	}
	for _, n := range []string{"lead", "meta"} {
		if strings.TrimSpace(prompt(n)) == "" {
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

// TestLedgerNearDuplicateMerged: a restated finding in the same issue merges into the first, its
// citation added (v1 dedup vectors below).
func TestLedgerNearDuplicateMerged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Damages?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(extract(d1, "invoices", "I1"))},
		"extract_facts": {facts(fd("Greystone claims damages of $644,910 for unpaid invoices from Talbot.", d1, "Greystone claims damages of $644,910 for unpaid invoices")),
			facts(fd("Greystone claims damages of $644,910 for the unpaid invoices from Talbot.", d1, "damages of $644,910"))},
	}), Config{})
	if len(v.Findings) != 1 || len(v.Findings[0].Citations) != 2 {
		t.Fatalf("%+v", v.Findings)
	}
}

const (
	dupA = "Marc transferred $40,000 from the joint TFSA to his personal account shortly before separation, which Danielle may trace and claim in equalization."
	dupB = "Marc transferred $40,000 from the joint TFSA to his personal account shortly before the separation, which Danielle may trace and claim in the equalization."
	dupC = "Marc's disputed RSUs vested after the date of separation and their treatment in income for support purposes remains contested."
)

// Carried (v1 writer/dedup_test.go): the shingle Jaccard merges restatements, keeps distinct facts
// apart, merges exact copies at any threshold, and is deterministic.
func TestMergeNearDuplicatesUnionsCitationsAndDropsCount(t *testing.T) {
	if !nearDuplicate(dupA, dupB) || nearDuplicate(dupA, dupC) {
		t.Fatalf("%.2f %.2f", similarity(dupA, dupB), similarity(dupA, dupC))
	}
}

func TestMergeNearDuplicatesDistinctSurvive(t *testing.T) {
	in := []string{"The matrimonial home at 41 Fernwood Crescent is valued at $1,640,000 with an RBC mortgage of $412,000.",
		"Danielle seeks occupancy of the home until June 2029 when Sam finishes elementary school.",
		"Harwood proposes monthly child support of $1,150 based on a shared-parenting set-off."}
	for i := range in {
		for j := i + 1; j < len(in); j++ {
			if nearDuplicate(in[i], in[j]) {
				t.Errorf("%d and %d merged", i, j)
			}
		}
	}
}

func TestMergeNearDuplicatesThresholdRespected(t *testing.T) {
	if s := similarity(dupA, dupB); s >= 0.99 || s < dupThreshold {
		t.Fatalf("slight variants %.3f", s)
	}
	if similarity(dupA, dupA) != 1 {
		t.Fatal("exact copies must score 1")
	}
}

func TestMergeNearDuplicatesDeterministicOrdering(t *testing.T) {
	first := similarity(dupA, dupB)
	for range 10 {
		if similarity(dupA, dupB) != first || similarity(dupB, dupA) != first {
			t.Fatal("similarity is not deterministic or symmetric")
		}
	}
}

// TestSweepVisitsEverySectionOnce: the sweep runs extract_facts once per page of every top-level
// section of every document, in order (with the carried sectionChunks vectors).
func TestSweepVisitsEverySectionOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	visits, err := f.e_(t).plan(context.Background(), f.ids(), f.ids())
	if err != nil {
		t.Fatal(err)
	}
	if len(visits) != 4 { // complaint: three sections; docket: one
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
				DocumentID, SectionID string
				Offset                int
			}
			jsonDecode(c.Messages[1].Content.(string), &in)
			got = append(got, Visit{in.DocumentID, in.SectionID, in.Offset})
		}
	}
	if !reflect.DeepEqual(got, visits) || !strings.Contains(strings.Join(f.outputs(v.ID, "sweep"), ""), `"visited":4`) {
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
		p, err := f.d.Section(context.Background(), f.ids(), v.DocumentID, v.SectionID, v.Offset, 0)
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

// TestSweepIdempotent (P8): a second sweep reads nothing again and returns the same findings.
func TestSweepIdempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, complaintText)
	d1 := f.docs[0].ID
	sweep := tc("sweep", m{"over": "sections", "documentIds": nil})
	v := f.run(f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "Damages?", "Conduct"))), calls(sweep), calls(sweep)},
		"extract_facts": {facts(fd("Suit filed.", d1, "sued Talbot Fresh Foods, Inc. on October 9, 2026")), facts(fd("Damages claimed.", d1, "Greystone claims damages of $644,910")), facts()},
	}), Config{})
	out := f.outputs(v.ID, "sweep")
	if len(out) != 2 || !strings.Contains(out[1], `"visited":0`) || !strings.Contains(out[1], `"findings":["F1","F2"]`) {
		t.Fatalf("sweeps %q", out)
	}
	n := 0
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		n += btoi(c.Agent == "extract_facts")
	}
	if n != 3 {
		t.Fatalf("%d extract_facts calls, want 3", n)
	}
}

// TestSweepReportsIncompleteWhenBudgetShort (trap): when the sweep share runs out, sweep says
// incomplete, lists the uncovered sections, and Limitations names them; never silently partial.
func TestSweepReportsIncompleteWhenBudgetShort(t *testing.T) {
	t.Parallel()
	f := newFixture(t, complaintText, docketText)
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
	b := &barrier{Fake: model.NewFake(f.happy()), all: make(chan struct{})}
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
