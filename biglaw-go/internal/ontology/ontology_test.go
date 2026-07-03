// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ontology

import (
	"strings"
	"testing"
)

func TestSubclass(t *testing.T) {
	if !Person.IsA(Party) {
		t.Error("Person should be a Party")
	}
	if !Rule.IsA(Authority) {
		t.Error("Rule should be an Authority")
	}
	if Money.IsA(Party) {
		t.Error("Money is not a Party")
	}
	if !Money.IsA(Unknown) || !Unknown.IsA(Party) {
		t.Error("Unknown should match anything (untyped nodes aren't rejected)")
	}
}

func TestLookupAliasAndSubstring(t *testing.T) {
	if p, ok := Lookup("violates"); !ok || p.Name != "violates" {
		t.Errorf("canonical lookup failed: %v %v", p, ok)
	}
	// substring fallback: the extractor rarely emits the exact alias
	if p, ok := Lookup("received undisclosed compensation from"); !ok || p.Name != "receivedFrom" {
		t.Errorf("substring alias should resolve to receivedFrom, got %v %v", p, ok)
	}
	if _, ok := Lookup("reviewed marketing materials"); ok {
		t.Error("a neutral/non-controlled relation should not resolve")
	}
}

func TestNormalizeDirectionFix(t *testing.T) {
	// Stated in reverse: "Section 206 (Authority) violates the conduct" — must re-orient to
	// "<conduct> violates Section 206" so domain/range holds.
	s, _, p, o, _, ok := Normalize("Section 206 of the Advisers Act", Authority, "violates", "Cherry-Picking Scheme", Conduct)
	if !ok || p != "violates" || s != "Cherry-Picking Scheme" || o != "Section 206 of the Advisers Act" {
		t.Errorf("direction not fixed: s=%q o=%q ok=%v", s, o, ok)
	}
	// Correct orientation passes through unchanged.
	s2, _, _, o2, _, ok2 := Normalize("Cherry-Picking Scheme", Conduct, "violates", "Section 206", Authority)
	if !ok2 || s2 != "Cherry-Picking Scheme" || o2 != "Section 206" {
		t.Errorf("correct orientation altered: s=%q o=%q ok=%v", s2, o2, ok2)
	}
	// Both classes known and incompatible with the predicate either way → dropped.
	if _, _, _, _, _, ok3 := Normalize("$438,000", Money, "violates", "78%", Percentage); ok3 {
		t.Error("incompatible domain/range should be dropped")
	}
}

// The Layer-3 analytic vocabulary in Go must stay in lockstep with the canonical Turtle
// spec: every IssueKind is a belo:DefenseIssue subclass declared in belo.ttl.
func TestIssueKindsDeclaredInSpec(t *testing.T) {
	kinds := []IssueKind{DiscrepancyKind, ElementGapKind, LimitationsKind, CriminalExposureKind, MentalStateKind, InnocentReadingKind}
	seen := map[IssueKind]bool{}
	for _, k := range kinds {
		if seen[k] {
			t.Errorf("duplicate IssueKind %q", k)
		}
		seen[k] = true
		if !strings.Contains(Spec, "belo:"+string(k)) {
			t.Errorf("IssueKind %q has no belo:%s class in belo.ttl", k, k)
		}
	}
	for _, pred := range []string{"carriesParallelExposure", "requiresMentalState", "timeBarredBy", "admitsInnocentReading"} {
		if !strings.Contains(Spec, "belo:"+pred) {
			t.Errorf("analytic predicate belo:%s missing from belo.ttl", pred)
		}
	}
}

func TestClassifyLiteral(t *testing.T) {
	cases := map[string]Class{
		"$438,000":                  Money,
		"81.6%":                     Percentage,
		"Section 206(1)":            Authority,
		"Oceanic Fund I LP":         Fund,
		"4,312":                     Count,
		"Whitmore Capital Advisors": Unknown, // a party name — don't guess
	}
	for in, want := range cases {
		if got := ClassifyLiteral(in); got != want {
			t.Errorf("ClassifyLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}
