// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Issue is one plan issue: a section of the deliverable's spine, typed by a BELO-lite issue class.
type Issue struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Class    Class  `json:"class"`
}

// PartyGap is a party the draft never mentions, with the findings about it.
type PartyGap struct {
	Name string   `json:"name"`
	FIDs []string `json:"fids"`
	Note string   `json:"note"`
}

// Coverage is what finish{op:"check"} reports as gaps (v1's coverage spine, respondent roster and
// salient-figure guarantee, as a check instead of generated sections).
type Coverage struct {
	MissingIssues    []string   `json:"missingIssues"`
	UncoveredParties []PartyGap `json:"uncoveredParties"`
	MissingFigures   []Handle   `json:"missingFigures"`
}

// salientFloor: a figure at least this salient must land in the deliverable (big money, large
// counts, major rates).
const salientFloor = 55

// CheckCoverage reports plan issues with no drafted section; parties the draft never mentions
// that appear in two or more verified findings, that a Conduct issue's finding names as its actor
// (committedBy), or that the caller's roster names (with a gap note when no finding mentions
// them); and salient figures that never land (neither the value nor its handle is in the draft).
func CheckCoverage(issues []Issue, sections map[string]string, findings []Finding, roster []string) Coverage {
	c := Coverage{MissingIssues: []string{}, UncoveredParties: []PartyGap{}, MissingFigures: []Handle{}}
	var draft strings.Builder
	for _, is := range issues {
		if strings.TrimSpace(sections[is.ID]) == "" {
			c.MissingIssues = append(c.MissingIssues, is.ID)
		}
		draft.WriteString(sections[is.ID] + "\n")
	}
	text := draft.String()
	type party struct {
		name  string
		class Class
		fids  []string
		must  bool
	}
	var parties []*party
	find := func(name string, class Class) *party {
		for _, p := range parties {
			if norm(p.name) == norm(name) {
				return p
			}
		}
		p := &party{name: name, class: class}
		parties = append(parties, p)
		return p
	}
	conduct := map[string]bool{}
	for _, is := range issues {
		conduct[is.ID] = is.Class == "Conduct"
	}
	for _, f := range findings {
		if !f.Verified {
			continue
		}
		for _, e := range f.Entities {
			if cl := ParseClass(string(e.Class)); cl != "" && cl.IsA("Party") {
				if p := find(e.Name, cl); !slices.Contains(p.fids, f.FID) {
					p.fids = append(p.fids, f.FID)
				}
			}
		}
		kept, _ := Relations(f.Entities, f.Relations)
		for _, r := range kept {
			if r.Predicate == "committedBy" && conduct[f.IssueID] {
				p := find(r.Object, "Person")
				p.must = true
				if !slices.Contains(p.fids, f.FID) {
					p.fids = append(p.fids, f.FID)
				}
			}
		}
	}
	for _, r := range roster {
		find(r, map[bool]Class{true: "Firm", false: "Person"}[hasCorpWord(r)]).must = true
	}
	for _, p := range parties {
		if (p.must || len(p.fids) >= 2) && !NameCovered(text, p.name, p.class) {
			note := fmt.Sprintf("%s appears in %s but the draft never mentions them.", p.name, strings.Join(p.fids, ", "))
			if len(p.fids) == 0 {
				note = "No verified findings mention " + p.name + ": review the source directly."
			}
			c.UncoveredParties = append(c.UncoveredParties, PartyGap{p.name, append([]string{}, p.fids...), note})
		}
	}
	for _, h := range Handles(findings, nil, 0) {
		if Salience(h.Value) >= salientFloor && !strings.Contains(text, h.Value) && !strings.Contains(strings.ToLower(text), strings.ToLower(h.Handle)) {
			c.MissingFigures = append(c.MissingFigures, h)
		}
	}
	return c
}

var corpWords = map[string]bool{"llc": true, "lp": true, "l.p.": true, "inc": true, "ltd": true, "corp": true, "corporation": true, "company": true,
	"co": true, "advisors": true, "advisers": true, "capital": true, "partners": true, "group": true, "fund": true, "plc": true, "llp": true, "bank": true, "trust": true}

func hasCorpWord(name string) bool {
	return slices.ContainsFunc(strings.Fields(strings.ToLower(name)), func(w string) bool { return corpWords[strings.Trim(w, ".,")] })
}

var nameTok = regexp.MustCompile(`[\p{L}\p{N}'’.-]+`)

// NameCovered reports whether text mentions a party. A person is covered by their surname as a
// whole word not followed by a company word ("Whitmore Capital Advisors" does not cover Gerald
// Whitmore); a firm or fund by its name without its company words.
func NameCovered(text, name string, class Class) bool {
	lt := strings.ToLower(text)
	var words []string
	for _, w := range nameTok.FindAllString(strings.ToLower(name), -1) {
		if w = strings.Trim(w, ".,"); w != "" && !corpWords[w] && len(w) > 2 && w != "iii" && w != "jr." && w != "sr." {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return false
	}
	want := strings.Join(words, " ")
	if class == "Person" {
		want = words[len(words)-1]
	}
	re := regexp.MustCompile(`(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(want) + `([^\p{L}\p{N}]|$)`)
	for _, m := range re.FindAllStringIndex(lt, -1) {
		rest := strings.Fields(lt[m[1]:])
		if class != "Person" || len(rest) == 0 || !corpWords[strings.Trim(rest[0], ".,;:()")] {
			return true
		}
	}
	return false
}
