// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"slices"
	"strings"
	"testing"
)

func party(name string, class Class) Entity { return Entity{name, class} }

func TestCoverageReportsMissingIssue(t *testing.T) {
	c := CheckCoverage([]Issue{{"I1", "Was there cherry-picking?", "Conduct"}, {"I2", "Disclosure?", "Requirement"}},
		map[string]string{"I1": "Yes.", "I2": "  "}, nil, nil)
	if !slices.Equal(c.MissingIssues, []string{"I2"}) {
		t.Fatalf("missing %v", c.MissingIssues)
	}
}

func TestCoverageReportsUncoveredParty(t *testing.T) {
	chao, bellini := party("Diana L. Chao", "Person"), party("Marcus T. Bellini", "Person")
	fs := []Finding{
		{FID: "F1", Verified: true, Entities: []Entity{chao, bellini}},
		{FID: "F2", Verified: true, Entities: []Entity{chao, party("Oceanic Fund I LP", "Fund")}},
		{FID: "F3", Verified: true, Entities: []Entity{party("Oceanic Fund I LP", "Fund")}},
		{FID: "F4", Verified: false, Entities: []Entity{bellini}}, // quarantined: does not count
	}
	c := CheckCoverage([]Issue{{"I1", "q", "Conduct"}}, map[string]string{"I1": "The scheme favoured the Oceanic fund."}, fs, nil)
	if len(c.UncoveredParties) != 1 || c.UncoveredParties[0].Name != "Diana L. Chao" || !slices.Equal(c.UncoveredParties[0].FIDs, []string{"F1", "F2"}) {
		t.Fatalf("uncovered %+v (want Chao only: Bellini is in one verified finding, Oceanic is mentioned)", c.UncoveredParties)
	}
}

// Carried from v1 writer/authorship_test.go: a firm name does not cover a person.
func TestNameCovered_FirmMentionDoesNotCoverPerson(t *testing.T) {
	if NameCovered("Whitmore Capital Advisors LLC faces exposure.", "Gerald R. Whitmore", "Person") {
		t.Error("the firm name wrongly counted as covering the person")
	}
	if !NameCovered("Whitmore holds a 62% membership interest.", "Gerald R. Whitmore", "Person") {
		t.Error("a genuine personal mention was not recognized")
	}
	if NameCovered("The Whitmoreville branch reported.", "Gerald R. Whitmore", "Person") {
		t.Error("substring inside a longer word wrongly matched")
	}
	if !NameCovered("Whitmore Capital's exposure", "Whitmore Capital Advisors LLC", "Firm") {
		t.Error("a firm named without its company words was not recognised")
	}
}

// Carried from v1 (roster enforcement): one entry per named respondent; the missing one gets an
// explicit gap note, never silence. v2 reports it from the check instead of writing a section.
func TestEnforceRoster_GapNoteForMissingRespondent(t *testing.T) {
	fs := []Finding{
		{FID: "F1", Verified: true, Entities: []Entity{party("Gerald R. Whitmore", "Person")}},
		{FID: "F2", Verified: true, Entities: []Entity{party("Diana L. Chao", "Person")}},
	}
	c := CheckCoverage([]Issue{{"I1", "q", "Conduct"}, {"I2", "Exposure", "RedFlag"}},
		map[string]string{"I1": "The scheme favoured Oceanic Fund.", "I2": "Whitmore Capital Advisors LLC faces entity-level exposure."},
		fs, []string{"Gerald R. Whitmore", "Diana L. Chao", "Marcus T. Bellini"})
	notes := map[string]string{}
	for _, g := range c.UncoveredParties {
		notes[g.Name] = g.Note
	}
	if len(notes) != 3 || !strings.Contains(notes["Gerald R. Whitmore"], "F1") || !strings.Contains(notes["Diana L. Chao"], "F2") ||
		notes["Marcus T. Bellini"] != "No verified findings mention Marcus T. Bellini: review the source directly." {
		t.Fatalf("roster gaps %+v", c.UncoveredParties)
	}
}

// A Conduct issue's actor must be covered even when it appears in one finding.
func TestCoverageConductNeedsActor(t *testing.T) {
	fs := []Finding{{FID: "F1", IssueID: "I1", Verified: true,
		Entities:  []Entity{party("Cherry-picking scheme", "Conduct"), party("Gerald Whitmore", "Person")},
		Relations: []Relation{{"Cherry-picking scheme", "committed by", "Gerald Whitmore"}}}}
	issues := []Issue{{"I1", "Was there cherry-picking?", "Conduct"}}
	c := CheckCoverage(issues, map[string]string{"I1": "Trades were cherry-picked."}, fs, nil)
	if len(c.UncoveredParties) != 1 || c.UncoveredParties[0].Name != "Gerald Whitmore" {
		t.Fatalf("actor not required: %+v", c.UncoveredParties)
	}
	if c := CheckCoverage(issues, map[string]string{"I1": "Whitmore cherry-picked trades."}, fs, nil); len(c.UncoveredParties) != 0 {
		t.Fatalf("a covered actor reported: %+v", c.UncoveredParties)
	}
	issues[0].Class = "Requirement" // not conduct: one finding does not make the party required
	if c := CheckCoverage(issues, map[string]string{"I1": "Trades were cherry-picked."}, fs, nil); len(c.UncoveredParties) != 0 {
		t.Fatalf("%+v", c.UncoveredParties)
	}
}

func TestCoverageSalientFigureMissing(t *testing.T) {
	fs := read([]Finding{{FID: "F1", Verified: true, Values: []Value{{Metric: "profit", Value: "$8,238,000"}, {Metric: "loss", Value: "$1,200,000"}, {Metric: "fee", Value: "$40"}}}})
	hs := Handles(fs, nil, 0)
	c := CheckCoverage([]Issue{{"I1", "q", "Conduct"}}, map[string]string{"I1": "Profits were $8,238,000; see also " + hs[1].Handle + "."}, fs, nil)
	if len(c.MissingFigures) != 0 {
		t.Fatalf("landed figures reported missing: %+v", c.MissingFigures)
	}
	c = CheckCoverage([]Issue{{"I1", "q", "Conduct"}}, map[string]string{"I1": "Profits were large."}, fs, nil)
	if len(c.MissingFigures) != 2 { // the $40 fee is below the salience floor
		t.Fatalf("missing figures %+v", c.MissingFigures)
	}
}
