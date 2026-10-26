// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/engine/figtest"
	"github.com/discover-legal/BigLaw/internal/model"
)

var goldenID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// TestFigureLabellersGolden pins what the figure reader and the figure judge send and keep, byte for
// byte (run ids aside): every reader and judge request in order (model, batch, prompt and input),
// every figure_reads row (model, matter, documents, the stored answer), and what each item read
// directly ends with (read, why, unread spans, labels) for a batch that fails and is split, an
// unfunded grant and no model. Two runs of one matter show the cross-run cache's reuse.
// GOLDEN_UPDATE=1 rewrites testdata/labellers.golden.
func TestFigureLabellersGolden(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	ids := map[string]string{}
	norm := func(s string) string {
		return goldenID.ReplaceAllStringFunc(s, func(id string) string {
			if _, ok := ids[id]; !ok {
				ids[id] = "<id" + strconv.Itoa(len(ids)+1) + ">"
			}
			return ids[id]
		})
	}
	trace := func(title string, fake *model.Fake) {
		fmt.Fprintf(&out, "## %s\n", title)
		for _, c := range fake.Calls() {
			if c.Agent != "read_figures" && c.Agent != "judge_figures" {
				continue
			}
			msgs, _ := json.Marshal(c.Messages)
			fmt.Fprintf(&out, "- %s model=%q role=%q max=%d\n  %s\n", c.Agent, c.Model, c.Role, c.MaxTokens, norm(string(msgs)))
		}
	}
	rows := func(f *fixture) {
		r, err := f.st.DB().Query(`SELECT model, matter_number, task_id::text, document_ids::text, labels::text FROM figure_reads`)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var all []string
		for r.Next() {
			var m, matter, task, docs, labels string
			if err := r.Scan(&m, &matter, &task, &docs, &labels); err != nil {
				t.Fatal(err)
			}
			all = append(all, fmt.Sprintf("- model=%s matter=%s task=%s docs=%s\n  %s", m, matter, task, docs, labels))
		}
		slices.SortStableFunc(all, func(a, b string) int { // by content, run ids aside
			return strings.Compare(goldenID.ReplaceAllString(a, "<id>"), goldenID.ReplaceAllString(b, "<id>"))
		})
		fmt.Fprintf(&out, "## figure_reads\n%s\n", norm(strings.Join(all, "\n")))
	}

	// Two runs of the commissions matter: the reader reads the findings and the section, the judge
	// decides the pair (yes); the second run reuses the cached readings and verdict.
	f := newFixture(t, feeLetter, feeSchedule)
	f.judge = func(a, b figtest.JudgeSide) string { return "yes" }
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	for i := range 2 {
		_, fake := f.engine(f.with(map[string][]model.Reply{
			"lead": {calls(plan(issue("I1", "What commissions were paid?", "RedFlag"))), calls(extract(d1, "commissions", "I1")), calls(extract(d2, "commissions", "I1")),
				calls(draft("I1", "F1")), calls(committee()), calls(finishCall("check"))},
			"extract_facts": {facts(fd("The Adviser paid commissions of $291,400 to Lakeshore Trading LLC.", d1, "The Adviser paid commissions of $291,400 to Lakeshore Trading LLC in 2023",
				val("Lakeshore Trading LLC", "commissions", "$291,400", "Money"))),
				facts(fd("The Adviser paid Lakeshore Trading LLC commissions of $310,000.", d2, "The Adviser paid Lakeshore Trading LLC commissions of $310,000 in 2023",
					val("Lakeshore Trading LLC", "commissions", "$310,000", "Money")))},
			"draft_section": {section("The Adviser paid $291,400 in commissions [F1].", "s")},
		}), Config{})
		task := f.submit(0)
		f.step(context.Background())
		trace(fmt.Sprintf("commissions run %d", i+1), fake)
		j, err := f.e.joined(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "## joined run %d\n%s\nconflicts %d unclear %d unjudged %d\n", i+1, strings.Join(j.lines(), "\n"), len(j.all), len(j.unclear), j.unjudged)
		stats, err := f.e.readerStats(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		st, _ := json.Marshal(stats)
		fmt.Fprintf(&out, "## reader stats run %d\n%s\n", i+1, st)
	}
	rows(f)

	// Direct reads: a batch of four that fails (read again in halves), the same items on a grant that
	// funds nothing, and with no model.
	g := newFixture(t)
	_, fake := g.engine(nil, Config{})
	fake.Respond = func(req model.Request) (model.Reply, bool) {
		if req.Agent != "read_figures" {
			return model.Reply{}, false
		}
		var in readRequest
		requestOf(req, &in)
		if len(in.Findings) > 2 {
			return model.Reply{Text: "not json"}, true
		}
		return readerOracle(req)
	}
	task := g.submit(0)
	rt, err := g.e.runtime(&run{e: g.e, t: task})
	if err != nil {
		t.Fatal(err)
	}
	items := func(tag string) []*reading {
		var its []*reading
		for i := range 4 {
			q := fmt.Sprintf("%s lot %d costs $%d,000 and %d units.", tag, i, 100+i, 3+i*7)
			if i == 2 {
				q = tag + " fee schedule lists " + strings.Repeat("$1,000, ", 30) + "and $2,000."
			}
			its = append(its, findingReading(Finding{Finding: analysis.Finding{Claim: "Fee."}}, []analysis.Quote{{DocumentID: g.docs[0].ID, Quote: q}}, nil))
		}
		return its
	}
	show := func(title string, its []*reading) {
		fmt.Fprintf(&out, "## %s\n", title)
		for i, it := range its {
			var unread []string
			for _, k := range slices.Sorted(maps.Keys(it.unread)) {
				unread = append(unread, k+"="+it.unread[k])
			}
			labels, _ := json.Marshal(it.labels)
			fmt.Fprintf(&out, "- item %d read=%v why=%q unread=[%s]\n  %s\n", i, it.read, it.why, strings.Join(unread, "; "), labels)
		}
	}
	c := agent.Call{TaskID: task.ID, DocumentIDs: task.DocumentIDs}
	split := items("First")
	if err := g.e.readFigures(context.Background(), rt, c, agent.NewBudget(50, 50, 0), "test", split); err != nil {
		t.Fatal(err)
	}
	show("split", split)
	trace("split calls", fake)
	poor := items("Second")
	if err := g.e.readFigures(context.Background(), rt, c, agent.NewBudget(0, 0, 0), "test", poor); err != nil {
		t.Fatal(err)
	}
	show("unfunded", poor)
	none := items("Third")
	if err := g.e.readFigures(context.Background(), nil, c, agent.NewBudget(50, 50, 0), "test", none); err != nil {
		t.Fatal(err)
	}
	show("no model", none)
	stats, err := g.e.readerStats(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := json.Marshal(stats)
	fmt.Fprintf(&out, "## direct reader stats\n%s\n", st)
	rows(g)

	got := out.String()
	path := filepath.Join("testdata", "labellers.golden")
	if os.Getenv("GOLDEN_UPDATE") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (GOLDEN_UPDATE=1 writes it)", err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := range max(len(gl), len(wl)) {
			if i >= len(gl) || i >= len(wl) || gl[i] != wl[i] {
				t.Fatalf("%s changed at line %d (GOLDEN_UPDATE=1 rewrites it; review the diff):\nwant %.400s\n got %.400s", path, i+1, goldenAt(wl, i), goldenAt(gl, i))
			}
		}
	}
}

func goldenAt(l []string, i int) string {
	if i < len(l) {
		return l[i]
	}
	return "<end>"
}
