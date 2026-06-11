// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package lpm

import "testing"

func TestGuardRecipientAllowlist(t *testing.T) {
	g := NewGuard(GuardConfig{AllowedDomains: []string{"firm.com", "client.com"}})

	ok := g.Check("M-001", []string{"a@firm.com", "b@client.com"}, "all clear")
	if !ok.Allowed {
		t.Errorf("allowed recipients should pass: %+v", ok)
	}

	bad := g.Check("M-001", []string{"a@firm.com", "leak@gmail.com"}, "hi")
	if bad.Allowed || len(bad.BlockedRecipients) != 1 || bad.BlockedRecipients[0] != "leak@gmail.com" {
		t.Errorf("external recipient should be blocked: %+v", bad)
	}
}

func TestGuardHandlesDisplayNameAddresses(t *testing.T) {
	g := NewGuard(GuardConfig{AllowedDomains: []string{"firm.com"}})
	d := g.Check("M-001", []string{"Jane Partner <jane@firm.com>"}, "ok")
	if !d.Allowed {
		t.Errorf("display-name address with allowed domain should pass: %+v", d)
	}
}

func TestGuardNoAllowlistFlagsButPermits(t *testing.T) {
	g := NewGuard(GuardConfig{})
	d := g.Check("M-001", []string{"anyone@anywhere.com"}, "ok")
	if !d.Allowed {
		t.Error("empty allowlist should permit (with a noted reason)")
	}
	if len(d.Reasons) == 0 {
		t.Error("empty allowlist should record a reason")
	}
}

func TestGuardCrossMatterLeakage(t *testing.T) {
	g := NewGuard(GuardConfig{KnownMatterNumbers: []string{"M-001", "M-002"}})
	d := g.Check("M-001", nil, "Re your matter, but also see M-002 for the other deal.")
	if d.Allowed {
		t.Error("cross-matter reference should block")
	}
	found := false
	for _, h := range d.LeakageHits {
		if h == "cross-matter reference: M-002" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected M-002 leakage hit, got %+v", d.LeakageHits)
	}

	// The current matter's own number must not flag.
	clean := g.Check("M-001", nil, "This concerns M-001 only.")
	if !clean.Allowed {
		t.Errorf("own matter number must not flag: %+v", clean)
	}
}

func TestGuardCrossMatterTokenBoundary(t *testing.T) {
	g := NewGuard(GuardConfig{KnownMatterNumbers: []string{"M-001", "M-002"}})
	// "M-0021" must not match the known "M-002".
	d := g.Check("M-001", nil, "ticket M-0021 unrelated")
	for _, h := range d.LeakageHits {
		if h == "cross-matter reference: M-002" {
			t.Error("substring match should not trip the boundary-aware check")
		}
	}
}

func TestGuardPIIScan(t *testing.T) {
	g := NewGuard(GuardConfig{AllowedDomains: []string{"firm.com"}})
	cases := map[string]string{
		"SSN":  "client SSN is 123-45-6789",
		"NINO": "NI number AB123456C on file",
		"IBAN": "pay to GB29NWBK60161331926819 today",
	}
	for name, content := range cases {
		d := g.Check("M-001", []string{"a@firm.com"}, content)
		if d.Allowed || len(d.LeakageHits) == 0 {
			t.Errorf("%s should be flagged: %+v", name, d)
		}
	}
}
