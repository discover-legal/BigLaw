// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package modeltier is BigLaw's own model tier list. The quality guidance
// used to speak in lab-branded class names; this replaces them with a
// classic S/A/B/C/D tier list, and Rate() places any model ID — cloud name
// or local "family:14b" string — on it. The tier drives the quality-booster
// recommendations: the compensator boosters (staged extraction, writer
// multipass, spine) exist for C and D tier; from B tier upward the model
// transcribes verbatim natively and only the structural coverage passes are
// worth their wall-clock.
//
// The list, top to bottom:
//
//	S — god tier: buy assurance, not crutches
//	A — frontier workhorse: structure over scaffolding
//	B — solid pick: native verbatim copy-out, keep the coverage passes
//	C — mid: the daily drivers the compensators were built for
//	D — bottom of the list: carried entirely by the scaffolding
package modeltier

import (
	"regexp"
	"strings"
)

// Tier is one row of the tier list. The numeric ordering ascends with
// capability (TierD < TierC < … < TierS) so comparisons read naturally.
type Tier int

const (
	TierD Tier = iota
	TierC
	TierB
	TierA
	TierS
)

var tierNames = [...]string{"D", "C", "B", "A", "S"}

func (t Tier) String() string {
	if t < TierD || t > TierS {
		return "C"
	}
	return tierNames[t]
}

// MarshalJSON renders the tier as its letter.
func (t Tier) MarshalJSON() ([]byte, error) { return []byte(`"` + t.String() + `"`), nil }

// NeedsCompensators reports whether the tier depends on the weakness-
// compensator boosters (staged extraction, writer multipass, spine routing)
// for verbatim citation fidelity.
func (t Tier) NeedsCompensators() bool { return t <= TierC }

// Info describes one row for docs and the API.
type Info struct {
	Name     string   `json:"name"`
	Blurb    string   `json:"blurb"`
	Guidance string   `json:"guidance"`
	Examples []string `json:"examples"`
}

// Ladder returns the tier list top-to-bottom (S first), the way a tier list
// is read.
func Ladder() []Info {
	return []Info{
		{
			Name:     "S",
			Blurb:    "god tier — buy assurance, not crutches",
			Guidance: "BIGLAW_QUALITY=fast; add debate/verification by stakes. Compensators are pure wall-clock waste here.",
			Examples: []string{"any Opus", "o3 / o4-class reasoners", "gpt-5-pro"},
		},
		{
			Name:     "A",
			Blurb:    "frontier workhorse — structure over scaffolding",
			Guidance: "BIGLAW_QUALITY=fast plus evidence graph + crossdoc on multi-doc matters; debate for anything filed.",
			Examples: []string{"any Sonnet", "gpt-5.x", "gemini *-pro", "qwen-max", "deepseek-v3", "deepseek-r1", "llama3.1:405b", "grok"},
		},
		{
			Name:     "B",
			Blurb:    "solid pick — native verbatim copy-out; keep the coverage passes",
			Guidance: "BIGLAW_QUALITY=fast plus the coverage passes (evidence graph, figures, crossdoc, deviations) and 3-5 verification votes.",
			Examples: []string{"any Haiku", "gpt-*-mini", "gpt-*-nano", "gemini *-flash", "qwen-plus", "qwen-turbo", "llama3.1:70b"},
		},
		{
			Name:     "C",
			Blurb:    "mid — the daily drivers the compensators were built for",
			Guidance: "BIGLAW_QUALITY=balanced. Staged extraction is what takes this tier's verbatim citations from ~0% to ~94%.",
			Examples: []string{"qwen2.5:7b", "qwen2.5:14b", "llama3.1:8b", "mistral:7b", "gemma2:9b", "mixtral-8x7b"},
		},
		{
			Name:     "D",
			Blurb:    "bottom of the list — carried entirely by the scaffolding",
			Guidance: "Every compensator on (BIGLAW_QUALITY=balanced). Expect the boosters to do the lifting.",
			Examples: []string{"qwen2.5:0.5b", "qwen2.5:1.5b", "qwen2.5:3b", "llama3.2:1b", "llama3.2:3b", "gemma:2b", "phi-3-mini"},
		},
	}
}

// paramSize matches an embedded parameter count like "7b", "1.5b", "405b"
// (must not be followed by another letter/digit, so "14b-instruct" matches
// and "qwen2.5" does not).
var paramSize = regexp.MustCompile(`(\d+(?:\.\d+)?)b(?:$|[^a-z0-9])`)

// Rate places a model ID on the tier list. Handles routing prefixes
// ("local:", "ollama:"), vendor path prefixes ("anthropic/…"), quantization
// suffixes, and bare local family:size strings. Unknown models rate C —
// the conservative default, since under-scaffolding a weak model costs far
// more quality than over-scaffolding a strong one costs time.
func Rate(modelID string) Tier {
	id := strings.ToLower(strings.TrimSpace(modelID))
	for _, p := range []string{"local:", "ollama:"} {
		id = strings.TrimPrefix(id, p)
	}
	if i := strings.LastIndex(id, "/"); i >= 0 { // vendor path ("openrouter/x/y")
		id = id[i+1:]
	}
	if id == "" {
		return TierC
	}

	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(id, s) {
				return true
			}
		}
		return false
	}

	// Small-variant markers first: "gpt-5-mini" must not ride the gpt-5 rule.
	switch {
	case has("haiku", "-mini", "-nano", "flash", "qwen-turbo", "qwen-plus"):
		return TierB
	case has("opus", "gpt-5-pro"), strings.HasPrefix(id, "o3"), strings.HasPrefix(id, "o4"):
		return TierS
	case has("sonnet", "gpt-5", "gpt-4.1", "gpt-4o", "deepseek", "grok", "mistral-large", "command-a"),
		has("qwen") && has("max"),
		has("gemini") && has("pro", "ultra"):
		return TierA
	}

	// Local models: rate by parameter count.
	if m := paramSize.FindStringSubmatch(id); m != nil {
		size := parseFloat(m[1])
		switch {
		case size < 4:
			return TierD
		case size < 27:
			return TierC
		case size < 200:
			return TierB
		default:
			return TierA
		}
	}

	return TierC
}

func parseFloat(s string) float64 {
	var v float64
	var frac float64 = 0
	div := 1.0
	inFrac := false
	for _, r := range s {
		switch {
		case r == '.':
			inFrac = true
		case r >= '0' && r <= '9':
			d := float64(r - '0')
			if inFrac {
				div *= 10
				frac += d / div
			} else {
				v = v*10 + d
			}
		}
	}
	return v + frac
}
