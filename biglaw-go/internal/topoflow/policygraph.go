// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

import (
	"encoding/json"
	"math"
	"os"
	"sort"
)

// Edge is one (signature, action) cell [AF].
type Edge struct {
	Visits     float64 `json:"visits"`
	MeanReward float64 `json:"meanReward"`
	M2         float64 `json:"m2"` // online variance (Welford)
	TokensSum  int     `json:"tokensSum"`
	Failures   float64 `json:"failures"`
}

// Variance returns the online population variance.
func (e *Edge) Variance() float64 {
	if e.Visits > 1 {
		return e.M2 / e.Visits
	}
	return 0
}

// FailRate returns failures / visits.
func (e *Edge) FailRate() float64 {
	if e.Visits > 0 {
		return e.Failures / e.Visits
	}
	return 0
}

type edgeKey struct {
	Sig Signature
	Act Action
}

// PolicyGraph is a tabular contextual bandit over (signature, action) cells.
type PolicyGraph struct {
	edges     map[edgeKey]*Edge
	sigVisits map[Signature]int
	cfg       Config
}

// NewPolicyGraph creates an empty policy graph.
func NewPolicyGraph(cfg Config) *PolicyGraph {
	return &PolicyGraph{edges: map[edgeKey]*Edge{}, sigVisits: map[Signature]int{}, cfg: cfg}
}

func (g *PolicyGraph) cs(ns int) float64 {
	return math.Max(g.cfg.UCBFloor, g.cfg.UCBc0*math.Pow(2, -float64(ns)/float64(g.cfg.UCBHalfLife)))
}

// Score implements eq (4)+(5). Unvisited actions return +Inf to force exploration.
func (g *PolicyGraph) Score(sig Signature, a Action) float64 {
	e := g.edges[edgeKey{sig, a}]
	if e == nil || e.Visits == 0 {
		return math.Inf(1)
	}
	ns := g.sigVisits[sig]
	ucb := g.cs(ns) * math.Sqrt(math.Log(float64(ns)+1)/e.Visits)
	return e.MeanReward + ucb - g.cfg.Lam*e.FailRate()
}

// Select returns the highest-scoring legal action (first on ties).
func (g *PolicyGraph) Select(sig Signature, legal []Action) Action {
	best := legal[0]
	bestScore := g.Score(sig, best)
	for _, a := range legal[1:] {
		if s := g.Score(sig, a); s > bestScore {
			best, bestScore = a, s
		}
	}
	return best
}

// Backup performs a trajectory-level update [AF]. weight < 1 implements
// confidence-weighted backups (fractional visits); weight == 1 for verifiable Q.
func (g *PolicyGraph) Backup(sig Signature, a Action, reward float64, tokens int, failed bool, weight float64) {
	if weight <= 0 {
		return
	}
	k := edgeKey{sig, a}
	e := g.edges[k]
	if e == nil {
		e = &Edge{}
		g.edges[k] = e
	}
	delta := reward - e.MeanReward
	e.Visits += weight
	e.MeanReward += (weight / e.Visits) * delta
	e.M2 += weight * delta * (reward - e.MeanReward)
	e.TokensSum += tokens
	if failed {
		e.Failures += weight
	}
	g.sigVisits[sig]++
}

// ── persistence ─────────────────────────────────────────────────────────────

type edgeRecord struct {
	Sig  Signature `json:"sig"`
	Act  Action    `json:"act"`
	Edge Edge      `json:"edge"`
}

type sigVisitRecord struct {
	Sig    Signature `json:"sig"`
	Visits int       `json:"visits"`
}

type graphBlob struct {
	Edges     []edgeRecord     `json:"edges"`
	SigVisits []sigVisitRecord `json:"sigVisits"`
}

// Save writes the graph to a JSON file (the warm-start artifact).
func (g *PolicyGraph) Save(path string) error {
	var blob graphBlob
	for k, e := range g.edges {
		blob.Edges = append(blob.Edges, edgeRecord{Sig: k.Sig, Act: k.Act, Edge: *e})
	}
	for s, v := range g.sigVisits {
		blob.SigVisits = append(blob.SigVisits, sigVisitRecord{Sig: s, Visits: v})
	}
	data, err := json.MarshalIndent(blob, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadPolicyGraph reads a graph from a JSON file.
func LoadPolicyGraph(path string, cfg Config) (*PolicyGraph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var blob graphBlob
	if err := json.Unmarshal(data, &blob); err != nil {
		return nil, err
	}
	g := NewPolicyGraph(cfg)
	for _, r := range blob.Edges {
		e := r.Edge
		g.edges[edgeKey{r.Sig, r.Act}] = &e
	}
	for _, r := range blob.SigVisits {
		g.sigVisits[r.Sig] = r.Visits
	}
	return g, nil
}

// Clone returns a deep copy (used for warm-start arms).
func (g *PolicyGraph) Clone() *PolicyGraph {
	c := NewPolicyGraph(g.cfg)
	for k, e := range g.edges {
		ec := *e
		c.edges[k] = &ec
	}
	for s, v := range g.sigVisits {
		c.sigVisits[s] = v
	}
	return c
}

// Summary returns a human-inspectable per-signature view, best action first.
func (g *PolicyGraph) Summary() map[string][]map[string]any {
	out := map[string][]map[string]any{}
	for k, e := range g.edges {
		label := sigLabel(k.Sig)
		out[label] = append(out[label], map[string]any{
			"action":     k.Act,
			"visits":     e.Visits,
			"meanReward": e.MeanReward,
			"variance":   e.Variance(),
			"failRate":   e.FailRate(),
			"tokensSum":  e.TokensSum,
		})
	}
	for label := range out {
		rows := out[label]
		sort.Slice(rows, func(i, j int) bool {
			return rows[i]["meanReward"].(float64) > rows[j]["meanReward"].(float64)
		})
	}
	return out
}

func sigLabel(s Signature) string {
	digits := func(a [7]int) string {
		b := make([]byte, 7)
		for i, v := range a {
			b[i] = byte('0' + v)
		}
		return string(b)
	}
	return string(s.Regime) + "|m" + digits(s.Mask)
}
