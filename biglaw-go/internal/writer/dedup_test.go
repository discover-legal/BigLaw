// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package writer

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Two near-identical findings (the observed matter-run failure: the same $40,000
// TFSA-transfer conclusion re-emitted with minor rephrasing) plus one distinct one.
func dupFixture() []Finding {
	return []Finding{
		{ID: "f1", Source: "doc-a", Grounded: false, Evidence: "short",
			Content: "Marc transferred $40,000 from the joint TFSA to his personal account shortly before separation, which Danielle may trace and claim in equalization."},
		{ID: "f2", Source: "doc-b", Grounded: true, Evidence: "Statement showing the $40,000 withdrawal on April 2.",
			Content: "Marc transferred $40,000 from the joint TFSA to his personal account shortly before the separation, which Danielle may trace and claim in the equalization."},
		{ID: "f3", Source: "doc-c", Grounded: true, Evidence: "Vesting schedule for 1,200 RSUs.",
			Content: "Marc's disputed RSUs vested after the date of separation and their treatment in income for support purposes remains contested."},
	}
}

func TestMergeNearDuplicatesUnionsCitationsAndDropsCount(t *testing.T) {
	out := mergeNearDuplicates(dupFixture(), defaultDedupThreshold)
	if len(out) != 2 {
		t.Fatalf("expected 2 findings after merge, got %d", len(out))
	}
	rep := out[0]
	if rep.ID != "f1" {
		t.Fatalf("representative must keep the first occurrence's ID, got %q", rep.ID)
	}
	// Citations unioned, first-seen order.
	if rep.Source != "doc-a; doc-b" {
		t.Fatalf("expected unioned citations %q, got %q", "doc-a; doc-b", rep.Source)
	}
	// The grounded variant's content/evidence wins (highest-confidence proxy).
	if !rep.Grounded || !strings.Contains(rep.Evidence, "$40,000 withdrawal") {
		t.Fatalf("expected the grounded variant's substance to win, got grounded=%v evidence=%q", rep.Grounded, rep.Evidence)
	}
	// The distinct finding survives untouched.
	if out[1].ID != "f3" || out[1].Source != "doc-c" {
		t.Fatalf("distinct finding must survive unchanged, got %+v", out[1])
	}
}

func TestMergeNearDuplicatesDistinctSurvive(t *testing.T) {
	in := []Finding{
		{ID: "a", Content: "The matrimonial home at 41 Fernwood Crescent is valued at $1,640,000 with an RBC mortgage of $412,000.", Source: "s1"},
		{ID: "b", Content: "Danielle seeks occupancy of the home until June 2029 when Sam finishes elementary school.", Source: "s2"},
		{ID: "c", Content: "Harwood proposes monthly child support of $1,150 based on a shared-parenting set-off.", Source: "s3"},
	}
	out := mergeNearDuplicates(in, defaultDedupThreshold)
	if len(out) != 3 {
		t.Fatalf("distinct findings must all survive, got %d of 3", len(out))
	}
	for i := range in {
		if out[i].ID != in[i].ID || out[i].Source != in[i].Source {
			t.Fatalf("finding %d changed: %+v", i, out[i])
		}
	}
}

func TestMergeNearDuplicatesThresholdRespected(t *testing.T) {
	in := dupFixture()
	// A near-1 threshold refuses the merge (the two variants differ slightly).
	if out := mergeNearDuplicates(in, 0.99); len(out) != 3 {
		t.Fatalf("threshold 0.99 must not merge slight variants, got %d of 3", len(out))
	}
	// Disabled (≤ 0) → input returned as-is.
	if out := mergeNearDuplicates(in, -1); !reflect.DeepEqual(out, in) {
		t.Fatalf("threshold ≤ 0 must be a no-op")
	}
	// Exact duplicates merge even at a very high threshold.
	exact := []Finding{
		{ID: "x1", Content: in[0].Content, Source: "s1"},
		{ID: "x2", Content: in[0].Content, Source: "s2"},
	}
	if out := mergeNearDuplicates(exact, 0.99); len(out) != 1 || out[0].Source != "s1; s2" {
		t.Fatalf("exact duplicates must merge at any threshold ≤ 1, got %+v", out)
	}
}

func TestMergeNearDuplicatesDeterministicOrdering(t *testing.T) {
	first := mergeNearDuplicates(dupFixture(), defaultDedupThreshold)
	for i := 0; i < 10; i++ {
		again := mergeNearDuplicates(dupFixture(), defaultDedupThreshold)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("merge is not deterministic: run %d differs", i)
		}
	}
	// Representatives keep first-seen positions.
	if first[0].ID != "f1" || first[1].ID != "f3" {
		t.Fatalf("insertion order not preserved: %q, %q", first[0].ID, first[1].ID)
	}
}

func TestMergeCloseGroups(t *testing.T) {
	near := &group{centroid: []float32{1, 0.02, 0}, ids: []string{"a"}, items: []Finding{{ID: "a"}}}
	nearer := &group{centroid: []float32{0.99, 0, 0.01}, ids: []string{"b"}, items: []Finding{{ID: "b"}}}
	far := &group{centroid: []float32{0, 0, 1}, ids: []string{"c"}, items: []Finding{{ID: "c"}}}
	out := mergeCloseGroups([]*group{near, nearer, far}, defaultClusterMergeThreshold)
	if len(out) != 2 {
		t.Fatalf("expected the two near-parallel clusters to merge into one (2 total), got %d", len(out))
	}
	if got := out[0].ids; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("merged cluster must absorb in order, got %v", got)
	}
	// Orthogonal cluster survives.
	if out[1].ids[0] != "c" {
		t.Fatalf("distinct cluster must survive, got %v", out[1].ids)
	}
	// Disabled → unchanged.
	if out := mergeCloseGroups([]*group{near, nearer, far}, -1); len(out) != 3 {
		t.Fatalf("mergeThreshold ≤ 0 must be a no-op, got %d groups", len(out))
	}
	// Threshold respected: nearly-identical centroids do NOT merge at ≥ their cosine.
	if out := mergeCloseGroups([]*group{near, far}, 0.999); len(out) != 2 {
		t.Fatalf("clusters below the merge threshold must stay apart")
	}
}

func TestNewDefaultsAndDisableKnob(t *testing.T) {
	w := New(nil, nil, "m", Options{})
	if w.opt.DedupThreshold != defaultDedupThreshold || w.opt.ClusterMergeThreshold != defaultClusterMergeThreshold {
		t.Fatalf("defaults not applied: %v %v", w.opt.DedupThreshold, w.opt.ClusterMergeThreshold)
	}
	off := New(nil, nil, "m", Options{DedupDisabled: true})
	if off.opt.DedupThreshold > 0 || off.opt.ClusterMergeThreshold > 0 {
		t.Fatalf("DedupDisabled must switch both passes off: %v %v", off.opt.DedupThreshold, off.opt.ClusterMergeThreshold)
	}
	if out := mergeNearDuplicates(dupFixture(), off.opt.DedupThreshold); len(out) != 3 {
		t.Fatalf("disabled dedup must keep all findings, got %d", len(out))
	}
}

// foldTinyGroups: single-finding clusters must not become one-asset sections.
func TestFoldTinyGroups(t *testing.T) {
	mk := func(n int, c []float32) *group {
		g := &group{centroid: c}
		for i := 0; i < n; i++ {
			g.items = append(g.items, Finding{ID: fmt.Sprintf("f%d-%p", i, g)})
			g.ids = append(g.ids, g.items[len(g.items)-1].ID)
		}
		return g
	}
	big1 := mk(5, []float32{1, 0})
	big2 := mk(4, []float32{0, 1})
	tinyNearBig2 := mk(1, []float32{0.1, 0.9})
	tinyNoVec := mk(2, nil)
	out := foldTinyGroups([]*group{big1, big2, tinyNearBig2, tinyNoVec}, 3)
	if len(out) != 2 {
		t.Fatalf("want 2 kept groups, got %d", len(out))
	}
	if len(out[1].items) != 5 { // big2 absorbed the near tiny (4+1)
		t.Fatalf("nearest-by-centroid fold failed: big2 has %d items", len(out[1].items))
	}
	if len(out[0].items) != 7 { // big1 got the no-centroid tiny (5+2)
		t.Fatalf("no-centroid tiny should fold into first kept group, got %d", len(out[0].items))
	}
	// All-tiny input is left alone.
	allTiny := []*group{mk(1, []float32{1, 0}), mk(2, []float32{0, 1})}
	if got := foldTinyGroups(allTiny, 3); len(got) != 2 {
		t.Fatalf("all-tiny matter must keep its groups, got %d", len(got))
	}
}

// The no-model fallback must declare itself as undigested extracts — never
// render as paragraphs that read as the memo's own analysis.
func TestFallbackSectionIsLabeledExtracts(t *testing.T) {
	w := &Writer{}
	ix := NewFindingIndex(nil, []Finding{
		{ID: "a", Content: "The employer asserts the termination was for cause.", Grounded: true},
		{ID: "b", Content: "Commissions of $48,300 remain unpaid.", Grounded: false},
	})
	out := w.fallbackSection(section{FindingIDs: []string{"a", "b"}}, ix)
	if !strings.Contains(out, "Drafting unavailable for this section") {
		t.Fatalf("fallback must carry the extracts banner, got:\n%s", out)
	}
	if !strings.Contains(out, "- The employer asserts") {
		t.Fatalf("fallback items must render as bullets, got:\n%s", out)
	}
	if !strings.Contains(out, "(unverified — requires confirmation)") {
		t.Fatal("ungrounded extract must stay marked")
	}
}

// Echo drafts (findings copied back as the "draft") must route to the labeled
// fallback, not ship as unlabeled memo body.
func TestIsEchoDraft(t *testing.T) {
	w := &Writer{}
	fs := []Finding{
		{ID: "a", Content: "Commissions of $48,300 were earned on paid invoices under the 2026 Commission Plan."},
		{ID: "b", Content: "The severance offer of four weeks' pay remains open until 2 September 2026 conditioned on a release."},
	}
	ix := NewFindingIndex(nil, fs)
	s := section{FindingIDs: []string{"a", "b"}}
	echo := "- " + fs[0].Content + "\n- " + fs[1].Content
	if !w.isEchoDraft(echo, s, ix) {
		t.Fatal("verbatim finding bullets must be detected as an echo")
	}
	drafted := "The commission claim is strong: the plan itself concedes the amounts were earned on paid invoices, and the employer's only defence is the payment-date condition.\n" +
		"On severance, counsel should respond before the September deadline rather than accept the release as drafted."
	if w.isEchoDraft(drafted, s, ix) {
		t.Fatal("genuine prose engaging the same facts must NOT be flagged as echo")
	}
}
