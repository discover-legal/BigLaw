// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// M4 — Semantic matching: embed->cosine->threshold->top-k; ordering.
package topoflow

import (
	"reflect"
	"testing"
)

func edgePairs(edges []edge) map[[2]int]bool {
	m := map[[2]int]bool{}
	for _, e := range edges {
		m[[2]int{e.Src, e.Dst}] = true
	}
	return m
}

func TestBuildEdgesThresholdNoSelf(t *testing.T) {
	R := [][]float64{
		{1.0, 0.6, 0.1},
		{0.2, 1.0, 0.9},
		{0.4, 0.05, 1.0},
	}
	p := edgePairs(buildEdges(R, 0.3, 3))
	if !p[[2]int{1, 0}] || !p[[2]int{2, 1}] || !p[[2]int{0, 2}] {
		t.Fatalf("expected edges missing: %v", p)
	}
	for k := range p {
		if k[0] == k[1] {
			t.Fatal("no self-edges allowed")
		}
	}
}

func TestInDegreeCap(t *testing.T) {
	R := [][]float64{
		{1.0, 0.9, 0.8, 0.7},
		{0.0, 1.0, 0.0, 0.0},
		{0.0, 0.0, 1.0, 0.0},
		{0.0, 0.0, 0.0, 1.0},
	}
	edges := buildEdges(R, 0.3, 2)
	var inc0 []int
	for _, e := range edges {
		if e.Dst == 0 {
			inc0 = append(inc0, e.Src)
		}
	}
	if len(inc0) != 2 {
		t.Fatalf("in-degree not capped: %v", inc0)
	}
}

func TestDeveloperTesterMutualEdge(t *testing.T) {
	names := []string{"Developer", "Researcher", "Tester", "Designer"}
	q := map[string]string{
		"Developer": "tests testing coverage", "Researcher": "facts references",
		"Tester": "code implementation", "Designer": "layout palette",
	}
	k := map[string]string{
		"Developer": "code implementation", "Researcher": "facts references",
		"Tester": "tests testing coverage", "Designer": "layout palette",
	}
	emb := NewMockEmbedder()
	qv := emb.Embed([]string{q["Developer"], q["Researcher"], q["Tester"], q["Designer"]})
	kv := emb.Embed([]string{k["Developer"], k["Researcher"], k["Tester"], k["Designer"]})
	edges := buildEdges(relevanceMatrix(qv, kv), 0.3, 3)
	idx := map[string]int{"Developer": 0, "Researcher": 1, "Tester": 2, "Designer": 3}
	p := edgePairs(edges)
	if !p[[2]int{idx["Tester"], idx["Developer"]}] {
		t.Error("expected Tester->Developer")
	}
	if !p[[2]int{idx["Developer"], idx["Tester"]}] {
		t.Error("expected Developer->Tester")
	}
	_ = names
}

func TestTopoSortDAG(t *testing.T) {
	edges := []edge{{0, 1, 1}, {0, 2, 1}, {1, 3, 1}, {2, 3, 1}}
	order := topoOrCycleBreak(4, edges)
	if len(order) != 4 {
		t.Fatalf("not a full permutation: %v", order)
	}
	pos := map[int]int{}
	for i, n := range order {
		pos[n] = i
	}
	for _, e := range edges {
		if pos[e.Src] >= pos[e.Dst] {
			t.Errorf("provider %d not before recipient %d", e.Src, e.Dst)
		}
	}
}

func TestCycleBreakValidPermutation(t *testing.T) {
	edges := []edge{{0, 1, 1}, {1, 2, 1}, {2, 0, 1}}
	order := topoOrCycleBreak(3, edges)
	seen := map[int]bool{}
	for _, n := range order {
		seen[n] = true
	}
	if len(seen) != 3 {
		t.Fatalf("not a valid permutation: %v", order)
	}
}

func TestOrderIncomingDescending(t *testing.T) {
	edges := []edge{{1, 0, 0.5}, {2, 0, 0.9}, {3, 0, 0.7}}
	if got := orderIncoming(0, edges); !reflect.DeepEqual(got, []int{2, 3, 1}) {
		t.Errorf("orderIncoming=%v want [2 3 1]", got)
	}
}
