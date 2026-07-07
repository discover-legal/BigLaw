// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package writer

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/bm25"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
)

// Cluster is a topic group of findings plus a cheap keyword label derived from
// their text (no model call). It seeds one outline section / one section drafter.
type Cluster struct {
	Label string
	Items []Finding
}

// cluster groups findings by embedding similarity (greedy, deterministic in
// insertion order): each finding joins the nearest existing cluster whose centroid
// cosine ≥ threshold, else starts a new cluster, up to maxClusters (after which it
// joins the nearest). Without embeddings it returns a single cluster — the writer's
// size-based batching then splits it. Each cluster gets a keyword label.
func cluster(ix *FindingIndex, threshold float64, maxClusters int) []Cluster {
	all := ix.All()
	if len(all) == 0 {
		return nil
	}
	var groups []*group
	for _, f := range all {
		v := ix.vec(f.ID)
		if len(v) == 0 {
			// No embedding: fold into the first group (size-batching handles it).
			if len(groups) == 0 {
				groups = append(groups, &group{})
			}
			groups[0].items = append(groups[0].items, f)
			groups[0].ids = append(groups[0].ids, f.ID)
			continue
		}
		best, bestSim := -1, threshold
		for i, g := range groups {
			if len(g.centroid) == 0 {
				continue
			}
			if s := embeddings.CosineSimilarity(v, g.centroid); s >= bestSim {
				best, bestSim = i, s
			}
		}
		if best < 0 && len(groups) < maxClusters {
			groups = append(groups, &group{centroid: append([]float32(nil), v...)})
			best = len(groups) - 1
		} else if best < 0 {
			best = nearestGroupIdx(v, groups)
		}
		g := groups[best]
		g.items = append(g.items, f)
		g.ids = append(g.ids, f.ID)
		g.centroid = runningMean(g.centroid, v, len(g.items))
	}
	items := make([][]Finding, 0, len(groups))
	for _, g := range groups {
		if len(g.items) > 0 {
			items = append(items, g.items)
		}
	}
	labels := labelClusters(items) // distinctive per-cluster labels (TF-IDF)
	out := make([]Cluster, 0, len(items))
	for i, it := range items {
		out = append(out, Cluster{Label: labels[i], Items: it})
	}
	// Largest clusters first — the document leads with its biggest themes.
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Items) > len(out[j].Items) })
	return out
}

func nearestGroupIdx(v []float32, groups []*group) int {
	best, bestSim := 0, -2.0
	for i, g := range groups {
		if len(g.centroid) == 0 {
			continue
		}
		if s := embeddings.CosineSimilarity(v, g.centroid); s > bestSim {
			best, bestSim = i, s
		}
	}
	return best
}

// group is a forming cluster: its findings and the running centroid of their
// embeddings.
type group struct {
	ids      []string
	items    []Finding
	centroid []float32
}

// runningMean updates a centroid to include the n-th vector (incremental average).
func runningMean(centroid, v []float32, n int) []float32 {
	if len(centroid) == 0 {
		return append([]float32(nil), v...)
	}
	out := make([]float32, len(centroid))
	fn := float32(n)
	for i := range centroid {
		var add float32
		if i < len(v) {
			add = v[i]
		}
		out[i] = centroid[i] + (add-centroid[i])/fn
	}
	return out
}

// labelClusters derives a SHORT, DISTINCTIVE label per cluster using TF-IDF over
// the cluster set: a term scores high when it is frequent inside its cluster but
// rare across the others. This stops globally-common words (e.g. "violations",
// "referral") from labelling every cluster the same — the failure mode of naive
// per-cluster top-terms. Labels are Title-Cased and de-duplicated.
func labelClusters(clusters [][]Finding) []string {
	n := len(clusters)
	if n == 0 {
		return nil
	}
	tfs := make([]map[string]int, n)
	df := map[string]int{} // number of clusters containing the term
	for i, items := range clusters {
		tf := map[string]int{}
		for _, f := range items {
			for _, tok := range bm25.Tokenize(f.Content) {
				if len(tok) >= 4 {
					tf[tok]++
				}
			}
		}
		tfs[i] = tf
		for t := range tf {
			df[t]++
		}
	}
	labels := make([]string, n)
	seen := map[string]int{}
	for i, tf := range tfs {
		type kv struct {
			w string
			s float64
		}
		terms := make([]kv, 0, len(tf))
		for w, f := range tf {
			idf := math.Log(1.0 + float64(n)/float64(df[w]))
			terms = append(terms, kv{w, float64(f) * idf})
		}
		sort.Slice(terms, func(a, b int) bool {
			if terms[a].s != terms[b].s {
				return terms[a].s > terms[b].s
			}
			return terms[a].w < terms[b].w
		})
		var top []string
		for j := 0; j < len(terms) && j < 4; j++ {
			top = append(top, titleCase(terms[j].w))
		}
		label := strings.Join(top, " ")
		if label == "" {
			label = "General"
		}
		if seen[label] > 0 { // disambiguate identical labels
			label = fmt.Sprintf("%s (%d)", label, seen[label]+1)
		}
		seen[strings.SplitN(label, " (", 2)[0]]++
		labels[i] = label
	}
	return labels
}

func titleCase(w string) string {
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + w[1:]
}
