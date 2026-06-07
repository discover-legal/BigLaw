// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Email → matter router. A specialised small model (the low-power tier) assigns
// each inbound message to a matter, with two safeguards that keep it honest on a
// cheap model: a deterministic fast path (a matter ref in the subject wins
// outright, no model call), and a recursive self-check (a second pass must agree)
// before a model-assigned routing is trusted. Any matter number the model returns
// that is not in the known roster is rejected as a hallucination.
package lpm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/email"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

// MatterOption is one candidate matter the router may assign a message to.
type MatterOption struct {
	MatterNumber string
	ClientNumber string
	Description  string
}

// RouteResult is the router's decision for a single message.
type RouteResult struct {
	MatterNumber string
	Confidence   float64
	Method       RoutingMethod
}

// Router assigns inbound emails to matters.
type Router struct {
	provider      providers.Provider
	model         string
	minConfidence float64
}

// NewRouter builds a Router. minConfidence is the floor below which a model
// assignment is downgraded to "unrouted" (defaults to 0.6 when <= 0).
func NewRouter(provider providers.Provider, model string, minConfidence float64) *Router {
	if minConfidence <= 0 {
		minConfidence = 0.6
	}
	return &Router{provider: provider, model: model, minConfidence: minConfidence}
}

// Route assigns one message to a matter, or returns an unrouted result.
func (r *Router) Route(msg email.Message, matters []MatterOption) RouteResult {
	known := make(map[string]bool, len(matters))
	for _, m := range matters {
		known[m.MatterNumber] = true
	}

	// ── Fast path: a recognised matter ref in the subject wins with no model call.
	if msg.MatterRef != "" && known[msg.MatterRef] {
		return RouteResult{MatterNumber: msg.MatterRef, Confidence: 1.0, Method: RouteRegex}
	}

	if r.provider == nil || len(matters) == 0 {
		return RouteResult{Method: RouteUnrouted}
	}

	pick, conf := r.classify(msg, matters)
	// Reject hallucinated matters and low-confidence picks.
	if pick == "" || !known[pick] || conf < r.minConfidence {
		return RouteResult{Method: RouteUnrouted, Confidence: conf}
	}

	// ── Recursive self-check: a second pass must confirm the same matter.
	if !r.confirm(msg, pick) {
		return RouteResult{Method: RouteUnrouted, Confidence: conf * 0.5}
	}
	return RouteResult{MatterNumber: pick, Confidence: conf, Method: RouteModel}
}

func (r *Router) classify(msg email.Message, matters []MatterOption) (string, float64) {
	var roster strings.Builder
	for _, m := range matters {
		fmt.Fprintf(&roster, "- %s: %s\n", m.MatterNumber, sanitizeEmailField(m.Description))
	}
	system := strings.Join([]string{
		"You route an inbound email to the correct legal matter from a fixed roster.",
		"Choose ONLY from the matter numbers listed. If none clearly fits, return an empty matterNumber.",
		"Never invent a matter number. Base the decision on subject and sender only.",
		`Respond with ONE JSON object: {"matterNumber":string,"confidence":number}.`,
	}, "\n")
	user := fmt.Sprintf("MATTER ROSTER:\n%s\nEMAIL:\nFrom: %s\nSubject: %s\nPreview: %s",
		roster.String(), sanitizeEmailField(msg.From), sanitizeEmailField(msg.Subject), sanitizeEmailField(msg.Snippet))

	resp, err := r.provider.Chat(providers.ChatParams{
		Model: r.model, MaxTokens: 120, System: system, CacheSystem: true,
		Messages: []providers.Message{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", 0
	}
	r.record(resp)
	var out struct {
		MatterNumber string  `json:"matterNumber"`
		Confidence   float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(extractJSON(firstText(resp))), &out); err != nil {
		return "", 0
	}
	return strings.TrimSpace(out.MatterNumber), clamp01(out.Confidence)
}

func (r *Router) confirm(msg email.Message, pick string) bool {
	system := "You verify an email-to-matter routing. Answer ONLY with JSON {\"agree\":boolean}: does this email plausibly belong to the stated matter?"
	user := fmt.Sprintf("MATTER: %s\nFrom: %s\nSubject: %s\nPreview: %s",
		pick, sanitizeEmailField(msg.From), sanitizeEmailField(msg.Subject), sanitizeEmailField(msg.Snippet))
	resp, err := r.provider.Chat(providers.ChatParams{
		Model: r.model, MaxTokens: 40, System: system,
		Messages: []providers.Message{{Role: "user", Content: user}},
	})
	if err != nil {
		return false
	}
	r.record(resp)
	var out struct {
		Agree bool `json:"agree"`
	}
	if err := json.Unmarshal([]byte(extractJSON(firstText(resp))), &out); err != nil {
		return false
	}
	return out.Agree
}

func (r *Router) record(resp *providers.ChatResponse) {
	v := costOf(r.model, resp)
	cost.Default.Record(cost.RecordRequest{
		Model: r.model, Provider: "anthropic",
		InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
		CostUSD: &v, DurationMs: resp.DurationMs, Context: "lpm_email_routing",
	})
}

// sanitizeEmailField strips control characters and injection markers from
// untrusted email content before it enters a prompt.
func sanitizeEmailField(s string) string {
	for _, marker := range []string{"FINDING:", "END_FINDING", "NO_FINDINGS", "NO_CHALLENGE"} {
		s = strings.ReplaceAll(s, marker, "["+marker+"]")
	}
	var b strings.Builder
	for _, ru := range s {
		if ru < 0x08 || (ru >= 0x0b && ru <= 0x1f && ru != '\n' && ru != '\t') || ru == 0x7f {
			continue
		}
		b.WriteRune(ru)
	}
	out := b.String()
	if len(out) > 1000 {
		out = out[:1000]
	}
	return out
}
