// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package writer

import "strings"

// The dedup/compression layer. A multi-agent bench re-surfaces the same issue many
// times (the same passage found in several rounds, by several agents, phrased with
// near-identical sentences); left alone, the deliverable re-litigates each such issue
// in N sections. Two deterministic passes fix that before any drafting happens:
//
//  1. mergeNearDuplicates — near-duplicate FINDINGS (token-shingle Jaccard ≥
//     DedupThreshold) collapse to one representative, keeping the strongest variant
//     (grounded beats ungrounded, then richer evidence) and UNIONING the citations.
//     Pure Go, order-stable, no model or embedding dependency — exactly the shape of
//     dedupeFindings, but catching duplicates whose leading characters differ.
//  2. mergeCloseGroups — topic CLUSTERS whose centroids' cosine ≥
//     ClusterMergeThreshold merge into one section, so one issue can't seed five
//     near-identical sections.
//
// Both are on by default and disabled together via WRITER_DEDUP=false
// (Options.DedupDisabled); the thresholds are WRITER_DEDUP_THRESHOLD and
// WRITER_CLUSTER_MERGE_THRESHOLD.
const (
	// defaultDedupThreshold is the Jaccard cutoff over 3-token shingles of the
	// CONCLUSION text — the standard near-duplicate cutoff. Near-identical sentences
	// (the observed failure: the same TFSA-transfer conclusion re-emitted with minor
	// rephrasing — an inserted article costs ~3 shingles) score ≥ 0.5; distinct
	// findings about the same topic share topic words but few identical 3-word runs,
	// so they land far below (typically < 0.1) and stay apart.
	defaultDedupThreshold = 0.50
	// defaultClusterMergeThreshold is the centroid-cosine cutoff for folding two topic
	// clusters into one section — deliberately far above the 0.55 join threshold, so
	// only clusters that are really the same topic (split by greedy insertion order)
	// collapse.
	defaultClusterMergeThreshold = 0.80
	dedupShingleSize             = 3
)

// dedupShingles returns the set of k-token shingles of a normalized (lowercase,
// alphanumeric-only tokens) text. Texts shorter than k tokens yield one shingle —
// the whole normalized text — so short findings still compare exactly.
func dedupShingles(s string) map[string]bool {
	toks := normTokens(s)
	out := map[string]bool{}
	if len(toks) == 0 {
		return out
	}
	if len(toks) < dedupShingleSize {
		out[strings.Join(toks, " ")] = true
		return out
	}
	for i := 0; i+dedupShingleSize <= len(toks); i++ {
		out[strings.Join(toks[i:i+dedupShingleSize], " ")] = true
	}
	return out
}

// normTokens lowercases and strips non-alphanumerics from each whitespace token.
func normTokens(s string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if t := b.String(); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// jaccard is |a∩b| / |a∪b| over shingle sets (0 when either is empty).
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, big := a, b
	if len(small) > len(big) {
		small, big = big, small
	}
	inter := 0
	for k := range small {
		if big[k] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// dedupText is what two findings are compared on: the analytical CONCLUSION —
// duplicates re-state the same conclusion, sometimes over different quotes, and
// including the evidence would dilute the overlap and let them slip through.
// Evidence is the fallback for a finding with no substantive conclusion.
func dedupText(f Finding) string {
	if strings.TrimSpace(f.Content) != "" {
		return f.Content
	}
	return f.Evidence
}

// mergeNearDuplicates collapses near-duplicate findings (shingle Jaccard on the
// conclusion text ≥ threshold) into one representative each, greedily in
// insertion order — deterministic and order-stable: every representative keeps the
// FIRST occurrence's ID and position. The representative's content is the strongest
// variant seen (grounded beats ungrounded; at equal groundedness, the longer
// evidence wins) and its Source is the union of all merged variants' sources
// ("; "-joined, first-seen order). threshold ≤ 0 or > 1 disables the pass.
func mergeNearDuplicates(fs []Finding, threshold float64) []Finding {
	if threshold <= 0 || threshold > 1 || len(fs) < 2 {
		return fs
	}
	type rep struct {
		f       Finding
		sh      map[string]bool
		sources []string
	}
	var reps []*rep
	for _, f := range fs {
		sh := dedupShingles(dedupText(f))
		var home *rep
		for _, r := range reps {
			if jaccard(sh, r.sh) >= threshold {
				home = r
				break
			}
		}
		if home == nil {
			r := &rep{f: f, sh: sh}
			if f.Source != "" {
				r.sources = append(r.sources, f.Source)
			}
			reps = append(reps, r)
			continue
		}
		if f.Source != "" && !containsStr(home.sources, f.Source) {
			home.sources = append(home.sources, f.Source)
		}
		stronger := (f.Grounded && !home.f.Grounded) ||
			(f.Grounded == home.f.Grounded && len(f.Evidence) > len(home.f.Evidence))
		if stronger {
			id := home.f.ID // identity + position stay with the first occurrence
			home.f = f
			home.f.ID = id
		}
	}
	out := make([]Finding, 0, len(reps))
	for _, r := range reps {
		r.f.Source = strings.Join(r.sources, "; ")
		out = append(out, r.f)
	}
	return out
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
