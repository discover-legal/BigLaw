// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"testing"
)

// Carried from v1 ontology_test.go (5 vectors), against BELO-lite.

func TestSubclass(t *testing.T) {
	t.Parallel()
	if !Class("Person").IsA("Party") {
		t.Error("Person should be a Party")
	}
	if !Class("Conduct").IsA("Issue") {
		t.Error("Conduct should be an Issue")
	}
	if Class("Money").IsA("Party") {
		t.Error("Money is not a Party")
	}
	if !Class("Money").IsA("") || !Class("").IsA("Party") {
		t.Error("unknown should match anything (untyped nodes aren't rejected)")
	}
}

func TestLookupAliasAndSubstring(t *testing.T) {
	t.Parallel()
	if p, ok := Lookup("violates"); !ok || p.Name != "violates" {
		t.Errorf("canonical lookup failed: %v %v", p, ok)
	}
	if p, ok := Lookup("received undisclosed compensation from"); !ok || p.Name != "receivedFrom" {
		t.Errorf("substring alias should resolve to receivedFrom, got %v %v", p, ok)
	}
	if _, ok := Lookup("reviewed marketing materials"); ok {
		t.Error("a neutral relation should not resolve")
	}
}

func TestNormalizeDirectionFix(t *testing.T) {
	t.Parallel()
	s, _, p, o, _, ok := Normalize("Section 206 of the Advisers Act", "Authority", "violates", "Cherry-Picking Scheme", "Conduct")
	if !ok || p != "violates" || s != "Cherry-Picking Scheme" || o != "Section 206 of the Advisers Act" {
		t.Errorf("direction not fixed: s=%q o=%q ok=%v", s, o, ok)
	}
	s2, _, _, o2, _, ok2 := Normalize("Cherry-Picking Scheme", "Conduct", "violates", "Section 206", "Authority")
	if !ok2 || s2 != "Cherry-Picking Scheme" || o2 != "Section 206" {
		t.Errorf("correct orientation altered: s=%q o=%q ok=%v", s2, o2, ok2)
	}
	if _, _, _, _, _, ok3 := Normalize("$438,000", "Money", "violates", "78%", "Percentage"); ok3 {
		t.Error("incompatible domain/range should be dropped")
	}
}

// Carried changed: v1 checked its defense IssueKinds against belo.ttl; v2 has no derived defense
// issues (they moved to packs), so the vocabulary check is that every plan issue class is declared
// in belo.json and is an Issue, and that every predicate's domain and range are declared classes.
func TestIssueKindsDeclaredInSpec(t *testing.T) {
	t.Parallel()
	want := []Class{"Conduct", "Requirement", "Clause", "RedFlag"}
	got := IssueClasses()
	if len(got) != len(want) {
		t.Fatalf("issue classes %v, want %v", got, want)
	}
	for i, c := range got {
		if c != want[i] || ParseClass(string(c)) != c || !c.IsA("Issue") {
			t.Errorf("issue class %q is not a declared Issue", c)
		}
	}
	for _, p := range belo.Predicates {
		for _, c := range append(append([]Class{}, p.Domain...), p.Range...) {
			if ParseClass(string(c)) != c {
				t.Errorf("predicate %s uses undeclared class %q", p.Name, c)
			}
		}
	}
}

func TestClassifyLiteral(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]Class{
		"$438,000":                  "", // what a number measures is the figure reader's (Value.Fig), never guessed from its text
		"81.6%":                     "",
		"Section 206(1)":            "Authority",
		"Oceanic Fund I LP":         "Fund",
		"4,312":                     "",
		"Whitmore Capital Advisors": "", // a party name — don't guess
	} {
		if got := ClassifyLiteral(in); got != want {
			t.Errorf("ClassifyLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelationReorientedByDomainRange(t *testing.T) {
	t.Parallel()
	ents := []Entity{{"Cherry-picking scheme", "Conduct"}, {"Section 206", "Authority"}, {"Gerald Whitmore", "Person"}}
	kept, dropped := Relations(ents, []Relation{
		{"Section 206", "in violation of", "Cherry-picking scheme"},
		{"Gerald Whitmore", "perpetrated by", "cherry-picking  scheme"},
	})
	if len(dropped) != 0 || len(kept) != 2 ||
		kept[0] != (Relation{"Cherry-picking scheme", "violates", "Section 206"}) ||
		kept[1] != (Relation{"cherry-picking  scheme", "committedBy", "Gerald Whitmore"}) {
		t.Fatalf("kept %+v dropped %+v", kept, dropped)
	}
}

// Trap: "Section 206 committedBy Firm" fits neither way; it is dropped, never stored.
func TestIllTypedRelationDropped(t *testing.T) {
	t.Parallel()
	kept, dropped := Relations([]Entity{{"Whitmore Capital Advisors LLC", "Firm"}}, []Relation{
		{"Section 206", "committedBy", "Whitmore Capital Advisors LLC"},
		{"Whitmore Capital Advisors LLC", "reviewed", "the brochure"},
	})
	if len(kept) != 0 || len(dropped) != 2 {
		t.Fatalf("kept %+v", kept)
	}
}
