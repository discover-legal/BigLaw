// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Confidentiality guard. Before any outbound content leaves the building — posted
// to a channel, written as a mailbox draft, or sent — it passes this gate, which
// enforces a recipient-domain allowlist and scans for the two leakage modes that
// matter most for a legal team: cross-matter contamination (another matter's
// number appearing in a message scoped to this one) and obvious PII (national
// insurance / SSN, payment-card, IBAN-ish strings). Every decision is recorded to
// the hash-chained audit log. The guard fails closed: an empty result is treated
// as "blocked" by callers, and a blocking finding stops the send.
package lpm

import (
	"regexp"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/audit"
)

// GuardConfig configures the confidentiality guard.
type GuardConfig struct {
	// AllowedDomains, when non-empty, restricts recipients to these domains
	// (case-insensitive, no leading "@"). Empty means no domain restriction.
	AllowedDomains []string
	// KnownMatterNumbers lets the guard detect cross-matter leakage by flagging
	// any *other* known matter number that appears in the content.
	KnownMatterNumbers []string
}

// GuardDecision is the outcome of a guard check.
type GuardDecision struct {
	Allowed           bool     `json:"allowed"`
	BlockedRecipients []string `json:"blockedRecipients,omitempty"`
	LeakageHits       []string `json:"leakageHits,omitempty"`
	Reasons           []string `json:"reasons,omitempty"`
}

// Guard enforces recipient and content confidentiality rules.
type Guard struct {
	allowed map[string]bool
	known   []string
}

// NewGuard builds a Guard from config.
func NewGuard(cfg GuardConfig) *Guard {
	allowed := make(map[string]bool, len(cfg.AllowedDomains))
	for _, d := range cfg.AllowedDomains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, "@")))
		if d != "" {
			allowed[d] = true
		}
	}
	return &Guard{allowed: allowed, known: cfg.KnownMatterNumbers}
}

var (
	piiSSN  = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)              // US SSN
	piiNINO = regexp.MustCompile(`\b[A-CEGHJ-PR-TW-Z]{2}\d{6}[A-D]\b`) // UK NI number
	piiCard = regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`)             // payment card
	piiIBAN = regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`)   // IBAN-ish
)

// Check evaluates recipients and content for a message scoped to matterNumber.
func (g *Guard) Check(matterNumber string, recipients []string, content string) GuardDecision {
	d := GuardDecision{Allowed: true}

	// ── Recipient-domain allowlist.
	if len(g.allowed) > 0 {
		for _, r := range recipients {
			dom := domainOf(r)
			if dom == "" || !g.allowed[dom] {
				d.BlockedRecipients = append(d.BlockedRecipients, r)
			}
		}
		if len(d.BlockedRecipients) > 0 {
			d.Allowed = false
			d.Reasons = append(d.Reasons, "recipient(s) outside the allowed-domain list")
		}
	} else {
		d.Reasons = append(d.Reasons, "no recipient allowlist configured (LPM_ALLOWED_DOMAINS)")
	}

	// ── Cross-matter contamination.
	for _, mn := range g.known {
		if mn == "" || mn == matterNumber {
			continue
		}
		if containsToken(content, mn) {
			d.LeakageHits = append(d.LeakageHits, "cross-matter reference: "+mn)
		}
	}

	// ── PII patterns.
	for label, re := range map[string]*regexp.Regexp{
		"possible SSN":          piiSSN,
		"possible NI number":    piiNINO,
		"possible payment card": piiCard,
		"possible IBAN":         piiIBAN,
	} {
		if re.MatchString(content) {
			d.LeakageHits = append(d.LeakageHits, label)
		}
	}

	if len(d.LeakageHits) > 0 {
		d.Allowed = false
		d.Reasons = append(d.Reasons, "content failed the confidentiality scan")
	}
	return d
}

// Audit records a guard decision to the append-only audit log.
func (g *Guard) Audit(actorID, matterNumber, action string, d GuardDecision) {
	audit.Default.Write(audit.WriteRequest{
		Event:   "lpm_confidentiality_guard",
		ActorID: actorID,
		Data: map[string]interface{}{
			"matterNumber":      matterNumber,
			"action":            action,
			"allowed":           d.Allowed,
			"blockedRecipients": len(d.BlockedRecipients),
			"leakageHits":       d.LeakageHits,
			"reasons":           d.Reasons,
		},
	})
}

func domainOf(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	// Tolerate "Name <user@host>" forms.
	if i := strings.LastIndexByte(addr, '<'); i >= 0 {
		if j := strings.IndexByte(addr[i:], '>'); j >= 0 {
			addr = addr[i+1 : i+j]
		}
	}
	at := strings.LastIndexByte(addr, '@')
	if at < 0 || at == len(addr)-1 {
		return ""
	}
	return addr[at+1:]
}

// containsToken reports whether token appears in s on a word-ish boundary, so
// "M-001" does not match inside "M-0012".
func containsToken(s, token string) bool {
	idx := 0
	for {
		i := strings.Index(s[idx:], token)
		if i < 0 {
			return false
		}
		i += idx
		before := i == 0 || !isWordByte(s[i-1])
		afterPos := i + len(token)
		after := afterPos == len(s) || !isWordByte(s[afterPos])
		if before && after {
			return true
		}
		idx = i + len(token)
		if idx >= len(s) {
			return false
		}
	}
}

func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z')
}
