// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package evidencegraph

import (
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/providers"
)

const lakeshorePassage = "Item 12 did not disclose any arrangement with Lakeshore Trading LLC, any relationship between WCA employee Bellini and Kevin Ostrowski (a 40% owner of Lakeshore Trading), or any potential conflict of interest arising from directed brokerage."

// The grounding gate is the whole point: a fabricated edge must be rejected, a verbatim
// one kept — a wrong edge in the substrate bakes in mis-attribution.
func TestGraph_GroundingGate(t *testing.T) {
	g := New()
	if !g.Add(Fact{Subject: "Kevin Ostrowski", Relation: "is 40% owner of", Object: "Lakeshore Trading", Value: "40%", Quote: "Kevin Ostrowski (a 40% owner of Lakeshore Trading)"}, lakeshorePassage) {
		t.Fatal("verbatim-grounded fact was rejected")
	}
	if g.Add(Fact{Subject: "Kevin Ostrowski", Relation: "is 51% owner of", Object: "Lakeshore Trading", Value: "51%", Quote: "Ostrowski owns 51% of Lakeshore"}, lakeshorePassage) {
		t.Error("ungrounded (fabricated) fact was kept — grounding gate failed")
	}
	if g.Add(Fact{Subject: "X", Quote: ""}, lakeshorePassage) {
		t.Error("empty-quote fact was kept")
	}
	if got := g.FactsAbout("ostrowski"); len(got) != 1 || got[0].Value != "40%" {
		t.Errorf("FactsAbout(ostrowski) = %v, want the 40%% fact", got)
	}
}

type mockChat struct{ entityJSON, relationJSON string }

func (m mockChat) Chat(p providers.ChatParams) (*providers.ChatResponse, error) {
	body := m.entityJSON
	switch {
	case strings.Contains(p.System, "extract relationships"):
		body = m.relationJSON
	case strings.Contains(p.System, "RDF triples"), strings.Contains(p.System, "distinct allegations"):
		body = "[]" // this test exercises passes 1-2 (entities/relations) only
	}
	return &providers.ChatResponse{Content: []providers.ContentBlock{{Type: providers.BlockText, Text: body}}}, nil
}

// The two-pass extractor must capture the parenthetical 40% (Pass 1) AND drop a
// hallucinated relation whose quote isn't in the chunk (grounding).
func TestExtractInto_ParentheticalAndGrounding(t *testing.T) {
	g := New()
	chat := mockChat{
		entityJSON:   `[{"entity":"Kevin Ostrowski","attribute":"40% owner of Lakeshore Trading","value":"40%","quote":"Kevin Ostrowski (a 40% owner of Lakeshore Trading)"},{"entity":"Bellini","attribute":"WCA employee","value":"","quote":"WCA employee Bellini"}]`,
		relationJSON: `[{"subject":"Bellini","relation":"is employee of","object":"WCA","value":"","quote":"WCA employee Bellini"},{"subject":"Ostrowski","relation":"bribed","object":"the SEC","value":"$9,999","quote":"Ostrowski bribed the SEC"}]`,
	}
	kept, rej := ExtractInto(g, chat, "m", nil, lakeshorePassage, "referral")
	if kept != 3 {
		t.Errorf("kept=%d, want 3 (Ostrowski 40%%, Bellini attr, Bellini-employee-of-WCA)", kept)
	}
	if rej != 1 {
		t.Errorf("rej=%d, want 1 (the fabricated 'bribed the SEC')", rej)
	}
	if len(g.FactsAbout("ostrowski")) != 1 {
		t.Error("lost the parenthetical Ostrowski 40% fact")
	}
}

// Allegations must be grounded (quote verbatim in source) and deduped by label.
func TestGraph_Allegations(t *testing.T) {
	g := New()
	src := "The Division alleges cherry-picking trade allocations and a separate directed-brokerage kickback scheme."
	if !g.AddAllegation("Cherry-Picking Trade Allocations", "cherry-picking trade allocations", src) {
		t.Error("grounded allegation rejected")
	}
	if g.AddAllegation("Cherry-Picking Trade Allocations", "cherry-picking trade allocations", src) {
		t.Error("duplicate allegation kept")
	}
	if g.AddAllegation("Insider Trading Ring", "insider trading ring", src) {
		t.Error("ungrounded allegation (not in source) kept")
	}
	if !g.AddAllegation("Directed-Brokerage Kickback Scheme", "directed-brokerage kickback scheme", src) {
		t.Error("second grounded allegation rejected")
	}
	if a := g.Allegations(); len(a) != 2 {
		t.Errorf("Allegations()=%v, want 2 distinct grounded", a)
	}
}
