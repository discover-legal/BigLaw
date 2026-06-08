// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/embeddings"
)

// Embedder is the frozen, pluggable sentence encoder seam [DT].
type Embedder interface {
	Embed(texts []string) [][]float64
}

// MockEmbedder is a deterministic bag-of-words hashing embedder for tests:
// cosine ~ shared-token overlap, enough to drive golden edge tests.
type MockEmbedder struct{ Dim int }

// NewMockEmbedder returns a MockEmbedder (default 128 dims).
func NewMockEmbedder() *MockEmbedder { return &MockEmbedder{Dim: 128} }

// Embed implements Embedder.
func (m *MockEmbedder) Embed(texts []string) [][]float64 {
	dim := m.Dim
	if dim <= 0 {
		dim = 128
	}
	out := make([][]float64, len(texts))
	for i, t := range texts {
		v := make([]float64, dim)
		for _, tok := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(tok))
			v[int(h.Sum32())%dim] += 1.0
		}
		out[i] = v
	}
	return out
}

// EmbeddingsAdapter wraps the project's embeddings.Client (live path).
type EmbeddingsAdapter struct{ c *embeddings.Client }

// NewEmbeddingsAdapter wraps an embeddings client.
func NewEmbeddingsAdapter(c *embeddings.Client) *EmbeddingsAdapter { return &EmbeddingsAdapter{c: c} }

// Embed implements Embedder over the live embeddings client.
func (e *EmbeddingsAdapter) Embed(texts []string) [][]float64 {
	res, err := e.c.EmbedBatch(texts)
	out := make([][]float64, len(texts))
	if err != nil {
		for i := range out {
			out[i] = []float64{}
		}
		return out
	}
	for i, r := range res {
		v := make([]float64, len(r.Embedding))
		for j, f := range r.Embedding {
			v[j] = float64(f)
		}
		out[i] = v
	}
	return out
}

// ── vector ops ──────────────────────────────────────────────────────────────
func normalize(v []float64) []float64 {
	var n float64
	for _, x := range v {
		n += x * x
	}
	n = math.Sqrt(n)
	if n == 0 {
		return v
	}
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

func dot(a, b []float64) float64 {
	var s float64
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		s += a[i] * b[i]
	}
	return s
}

// relevanceMatrix R[i][j] = cos(Q_i, K_j); edge j->i (j provides to i) keys on R[i][j].
func relevanceMatrix(qVecs, kVecs [][]float64) [][]float64 {
	n := len(qVecs)
	Q := make([][]float64, n)
	K := make([][]float64, n)
	for i := 0; i < n; i++ {
		Q[i] = normalize(qVecs[i])
		K[i] = normalize(kVecs[i])
	}
	R := make([][]float64, n)
	for i := 0; i < n; i++ {
		R[i] = make([]float64, n)
		for j := 0; j < n; j++ {
			R[i][j] = dot(Q[i], K[j])
		}
	}
	return R
}

// edge is a directed edge (src j provides to dst i) with relevance score.
type edge struct {
	Src   int
	Dst   int
	Score float64
}

// buildEdges: A[j->i]=1 if R[i][j]>tau and i!=j; enforce max in-degree k_in by
// keeping the top-k_in providers per recipient i by R[i][j].
func buildEdges(R [][]float64, tau float64, kIn int) []edge {
	n := len(R)
	var edges []edge
	for i := 0; i < n; i++ {
		type ps struct {
			j int
			s float64
		}
		var providers []ps
		for j := 0; j < n; j++ {
			if j != i && R[i][j] > tau {
				providers = append(providers, ps{j, R[i][j]})
			}
		}
		sort.Slice(providers, func(a, b int) bool {
			if providers[a].s != providers[b].s {
				return providers[a].s > providers[b].s
			}
			return providers[a].j < providers[b].j
		})
		for k := 0; k < len(providers) && k < kIn; k++ {
			edges = append(edges, edge{Src: providers[k].j, Dst: i, Score: providers[k].s})
		}
	}
	return edges
}

// topoOrCycleBreak returns a full permutation: topological sort if acyclic,
// else greedy cycle-break (place min restricted in-degree node).
func topoOrCycleBreak(n int, edges []edge) []int {
	succ := make([]map[int]bool, n)
	indeg := make([]int, n)
	for i := range succ {
		succ[i] = map[int]bool{}
	}
	for _, e := range edges {
		if !succ[e.Src][e.Dst] {
			succ[e.Src][e.Dst] = true
			indeg[e.Dst]++
		}
	}
	remaining := map[int]bool{}
	for i := 0; i < n; i++ {
		remaining[i] = true
	}
	cur := append([]int(nil), indeg...)
	var order []int
	for len(remaining) > 0 {
		var ready []int
		for x := range remaining {
			if cur[x] == 0 {
				ready = append(ready, x)
			}
		}
		if len(ready) == 0 {
			// cycle: greedily pick min restricted in-degree, smallest index
			pick := -1
			for x := range remaining {
				if pick == -1 || cur[x] < cur[pick] || (cur[x] == cur[pick] && x < pick) {
					pick = x
				}
			}
			ready = []int{pick}
		} else {
			sort.Ints(ready)
		}
		for _, node := range ready {
			order = append(order, node)
			delete(remaining, node)
			for s := range succ[node] {
				if remaining[s] && cur[s] > 0 {
					cur[s]--
				}
			}
		}
	}
	return order
}

// orderIncoming orders recipient i's providers by descending score (index tie-break).
func orderIncoming(i int, edges []edge) []int {
	type ps struct {
		j int
		s float64
	}
	var ins []ps
	for _, e := range edges {
		if e.Dst == i {
			ins = append(ins, ps{e.Src, e.Score})
		}
	}
	sort.Slice(ins, func(a, b int) bool {
		if ins[a].s != ins[b].s {
			return ins[a].s > ins[b].s
		}
		return ins[a].j < ins[b].j
	})
	out := make([]int, len(ins))
	for k, p := range ins {
		out[k] = p.j
	}
	return out
}
