// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"encoding/json"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/engine/figtest"
	"github.com/discover-legal/BigLaw/internal/model"
)

// The figure reader is a model; in tests it is scripted. readerOracle is the fixture's stand-in: it
// labels the plain fixture texts as a careful reader would (a sign or code beside a number is its
// currency, "%" a percentage, the noun after it what it counts, a scale word its scale, a value's
// class and unit what the analyst typed), so the many engine tests that are not about figure
// reading run as before. It is test data, not a rule of the engine: a test about figure reading
// scripts its own labels (scriptedLabels) and, where it matters, overrides the oracle.

// newFake is the tests' scripted model with the oracle standing in for the figure reader, unless
// the scripts answer read_figures themselves.
func newFake(scripts map[string][]model.Reply) *model.Fake {
	f := model.NewFake(scripts)
	if _, scripted := scripts["read_figures"]; !scripted {
		f.Respond = readerOracle
	}
	if _, scripted := scripts["judge_figures"]; scripted && f.Respond != nil { // a test's own verdicts
		f.Respond = func(req model.Request) (model.Reply, bool) {
			if req.Agent == "judge_figures" {
				return model.Reply{}, false
			}
			return readerOracle(req)
		}
	}
	return f
}

// oracleWith is the stand-in with a test's right labels (figtest.With).
func oracleWith(parties map[string]string, reads map[string]analysis.Label, measures map[string]string) func(model.Request) (model.Reply, bool) {
	return figtest.With(parties, reads, measures)
}

// readerOracle answers every read_figures request (fake.Respond); other agents use their scripts.
func readerOracle(req model.Request) (model.Reply, bool) { return figtest.Reader(req) }

// readRequest is the figure reader's request as a test reads it.
type readRequest struct {
	Findings []struct {
		ID       string
		Parties  []string
		Passages []passageIn
	}
}

// requestOf decodes a subagent request's JSON input (its first user message).
func requestOf(req model.Request, v any) bool { return figtest.RequestOf(req, v) }

// oracleLabels are the stand-in's labels of one passage of a request.
func oracleLabels(p passageIn, all []passageIn, parties []string) []analysis.Label {
	conv := func(x any, to any) {
		data, _ := json.Marshal(x)
		_ = json.Unmarshal(data, to)
	}
	var fp figtest.Passage
	var fall []figtest.Passage
	conv(p, &fp)
	conv(all, &fall)
	return figtest.Labels(fp, fall, parties)
}

// scriptedLabels is a read_figures reply holding exactly these labels (a test about figure reading).
func scriptedLabels(ls ...analysis.Label) model.Reply {
	data, _ := json.Marshal(map[string]any{"labels": ls})
	return model.Reply{Text: string(data)}
}

// lab is one label: kind, currency ("" for none), scale and the words it comes from ("" for 1),
// party ("" unknown), sign ("" positive).
func lab(span, kind, cur, scale, from, party, sign string) analysis.Label {
	l := analysis.Label{Span: span, Kind: kind, Currency: cmpStr(cur == "", "none", cur), Scale: cmpStr(scale == "", "1", scale), Decimal: "none", Sign: cmpStr(sign == "", "positive", sign)}
	if from != "" {
		l.ScaleFrom = &from
	}
	if party != "" {
		l.Party = &party
	}
	return l
}

// oracleReadings is what the oracle reads a finding's quotes, values and claim as (no model call).
func oracleReadings(claim string, quotes []string, vals []analysis.Value) map[string]analysis.Reading {
	var qs []analysis.Quote
	for _, q := range quotes {
		qs = append(qs, analysis.Quote{DocumentID: "d", Quote: q})
	}
	it := findingReading(Finding{Finding: analysis.Finding{Claim: claim}}, qs, vals)
	var req readRequest
	req.Findings = append(req.Findings, struct {
		ID       string
		Parties  []string
		Passages []passageIn
	}{"1", it.parties, it.in})
	for _, p := range it.in {
		it.labels = append(it.labels, oracleLabels(p, it.in, it.parties)...)
	}
	it.read = true
	rs, _ := it.readings()
	return rs
}

// valueInQuote reports whether the oracle's reading admits typed value v from one quote text.
func valueInQuote(v analysis.Value, quote string) bool {
	_, ok, _, _ := valueStated(v, 0, oracleReadings("", []string{quote}, []analysis.Value{v}), quote)
	return ok
}

// claimSumsIn is claimSums over the oracle's reading of a claim and one quote text.
func claimSumsIn(claim, quote string, have []analysis.Value) []analysis.Value {
	return claimSums(claim, oracleReadings(claim, []string{quote}, nil), have)
}

// oracleSections is one drafted section of md, its figures read by the oracle.
func oracleSections(md string) []Section {
	return []Section{readSection(Section{IssueID: "I1", Markdown: md})}
}

// readSection is s with its drafter's figures read by the oracle (as saveSection reads them).
func readSection(s Section) Section {
	body, regs := memoBody(s)
	spans := memoSpans(body, regs)
	it := newReading(nil, []analysis.Passage{{ID: "m", Text: body, Spans: spans}}, []passageIn{{ID: "m", Kind: "memo", Text: body}})
	it.labels, it.read = oracleLabels(it.in[0], it.in, nil), true
	rs, _ := it.readings()
	s.Read = &SectionRead{Hash: textHash(body), Figs: ofPassage(rs, "m")}
	return s
}

// read is a clear reading (a typed value's Fig, as admission sets it from the figure reader's labels).
func read(kind, cur, amount, party string) *analysis.Reading {
	return &analysis.Reading{Kind: kind, Currency: cur, Amount: amount, Party: party, Period: "none"}
}

// agentCall is a Go tool's call with input in.
func agentCall(in []byte) agent.Call { return agent.Call{Input: in} }
