// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package writer

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
// joins the nearest). A post-pass merges clusters whose centroids sit within
// mergeThreshold cosine of each other (≤ 0 disables) — greedy insertion order can
// split one topic across several clusters, which is how a single issue ends up
// re-litigated in five sections. Without embeddings it returns a single cluster —
// the writer's size-based batching then splits it. Each cluster gets a keyword label.
func cluster(ix *FindingIndex, threshold, mergeThreshold float64, maxClusters int) []Cluster {
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
	groups = mergeCloseGroups(groups, mergeThreshold)
	groups = foldTinyGroups(groups, minSectionFindings)
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

// minSectionFindings is the floor below which a cluster is too thin to earn
// its own section. Weak drafters given single-figure clusters produce one
// heading per asset ("## TFSA", "## Volvo XC60") and the memo shatters; a tiny
// cluster folds into its nearest neighbour instead. 1 disables the pass.
const minSectionFindings = 3

// foldTinyGroups merges groups smaller than min into their nearest-by-centroid
// larger neighbour, deterministically (smallest first, ties by original order).
// A tiny group with no centroid folds into the first kept group. If EVERY
// group is tiny (small matter), they are left alone — a short memo with a few
// short sections beats one undifferentiated blob.
func foldTinyGroups(groups []*group, min int) []*group {
	if min <= 1 || len(groups) < 2 {
		return groups
	}
	anyBig := false
	for _, g := range groups {
		if len(g.items) >= min {
			anyBig = true
			break
		}
	}
	if !anyBig {
		return groups
	}
	kept := make([]*group, 0, len(groups))
	var tiny []*group
	for _, g := range groups {
		if len(g.items) >= min {
			kept = append(kept, g)
		} else if len(g.items) > 0 {
			tiny = append(tiny, g)
		}
	}
	for _, t := range tiny {
		best := 0
		if len(t.centroid) > 0 {
			best = nearestGroupIdx(t.centroid, kept)
		}
		k := kept[best]
		k.items = append(k.items, t.items...)
		k.ids = append(k.ids, t.ids...)
		if len(t.centroid) > 0 && len(k.centroid) > 0 {
			// Count-weighted merge keeps the centroid honest for later folds.
			n := len(k.items)
			for i := range k.centroid {
				k.centroid[i] = (k.centroid[i]*float32(n-len(t.items)) + t.centroid[i]*float32(len(t.items))) / float32(n)
			}
		}
	}
	return kept
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

// mergeCloseGroups folds together clusters whose centroids' cosine ≥ minCos —
// greedily, in order, deterministic: each group merges into the FIRST already-kept
// group close enough, so the earlier (larger-seeded) cluster absorbs the later one
// and finding order within each is preserved. The merged centroid is the
// count-weighted mean. Groups without a centroid (no embeddings) are never merged.
// minCos ≤ 0 or ≥ 1 disables the pass.
func mergeCloseGroups(groups []*group, minCos float64) []*group {
	if minCos <= 0 || minCos >= 1 || len(groups) < 2 {
		return groups
	}
	out := make([]*group, 0, len(groups))
	for _, g := range groups {
		var home *group
		if len(g.centroid) > 0 {
			for _, o := range out {
				if len(o.centroid) == 0 {
					continue
				}
				if embeddings.CosineSimilarity(g.centroid, o.centroid) >= minCos {
					home = o
					break
				}
			}
		}
		if home == nil {
			out = append(out, g)
			continue
		}
		home.centroid = weightedMean(home.centroid, len(home.items), g.centroid, len(g.items))
		home.items = append(home.items, g.items...)
		home.ids = append(home.ids, g.ids...)
	}
	return out
}

// weightedMean is the count-weighted average of two centroids.
func weightedMean(a []float32, na int, b []float32, nb int) []float32 {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 || na+nb == 0 {
		return a
	}
	out := make([]float32, len(a))
	wa, wb := float32(na), float32(nb)
	for i := range a {
		var bv float32
		if i < len(b) {
			bv = b[i]
		}
		out[i] = (a[i]*wa + bv*wb) / (wa + wb)
	}
	return out
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
	df := map[string]int{}         // number of clusters containing the term
	surface := map[string]string{} // lowered term → first original-cased surface form
	for i, items := range clusters {
		tf := map[string]int{}
		for _, f := range items {
			for _, tok := range bm25.Tokenize(f.Content) {
				if utf8.RuneCountInString(tok) >= 4 {
					tf[tok]++
				}
			}
			// Original-cased surface forms, so a label renders "TFSA"/"Élodie", not
			// "Tfsa"/mojibake — the token index stays lowercased for matching.
			for _, w := range strings.FieldsFunc(f.Content, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsNumber(r)
			}) {
				if lw := strings.ToLower(w); surface[lw] == "" {
					surface[lw] = w
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
			if labelStop[w] { // function words make unreadable headings ("… Harwood Does")
				continue
			}
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
			w := terms[j].w
			if sf := surface[w]; sf != "" {
				w = sf
			}
			top = append(top, titleCase(w))
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

// labelStop extends the (deliberately tiny) BM25 stoplist for HEADING purposes only:
// verbs/adverbs that survive idf yet read as noise in a title.
var labelStop = map[string]bool{
	"does": true, "should": true, "would": true, "could": true, "will": true,
	"shall": true, "also": true, "been": true, "said": true, "were": true,
	"must": true, "which": true, "their": true, "there": true, "them": true,
	"they": true, "other": true, "than": true, "into": true, "upon": true,
	"about": true, "only": true, "more": true, "most": true, "such": true,
	"when": true, "while": true, "where": true, "these": true, "those": true,
}

// titleCase uppercases the first RUNE — byte-slicing here split multibyte letters
// ("élodie" → two invalid bytes → "��lodie" in section headings).
func titleCase(w string) string {
	r, size := utf8.DecodeRuneInString(w)
	if size == 0 {
		return w
	}
	return string(unicode.ToUpper(r)) + w[size:]
}
