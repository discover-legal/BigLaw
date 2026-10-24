// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
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
	return f
}

// oracleWith is the oracle with the party of every span written as a key of parties set to its value,
// and every span keyed in reads ("quote:4,000") labelled as reads says.
func oracleWith(parties map[string]string, reads map[string]analysis.Label) func(model.Request) (model.Reply, bool) {
	return func(req model.Request) (model.Reply, bool) {
		r, ok := readerOracle(req)
		if !ok {
			return r, ok
		}
		var in readRequest
		requestOf(req, &in)
		text, kind := map[string]string{}, map[string]string{}
		for _, f := range in.Findings {
			for _, p := range f.Passages {
				for _, s := range p.Spans {
					text[s.ID], kind[s.ID] = s.Text, p.Kind
				}
			}
		}
		var out struct {
			Labels []analysis.Label `json:"labels"`
		}
		_ = json.Unmarshal([]byte(r.Text), &out)
		for i, l := range out.Labels {
			if p, ok := parties[text[l.Span]]; ok {
				out.Labels[i].Party = &p
			}
			if r, ok := reads[kind[l.Span]+":"+text[l.Span]]; ok {
				r.Span = l.Span
				out.Labels[i] = r
			}
		}
		data, _ := json.Marshal(out)
		r.Text = string(data)
		return r, true
	}
}

// readRequest is the figure reader's request as the oracle reads it.
type readRequest struct {
	Findings []struct {
		ID       string
		Parties  []string
		Passages []passageIn
	}
}

// readerOracle answers every read_figures request (fake.Respond); other agents use their scripts.
func readerOracle(req model.Request) (model.Reply, bool) {
	if req.Agent != "read_figures" {
		return model.Reply{}, false
	}
	var in readRequest
	if !requestOf(req, &in) {
		return model.Reply{}, false
	}
	labels := []analysis.Label{}
	for _, f := range in.Findings {
		for _, p := range f.Passages {
			labels = append(labels, oracleLabels(p, f.Passages, f.Parties)...)
		}
	}
	data, _ := json.Marshal(map[string]any{"labels": labels})
	return model.Reply{Text: string(data), Usage: model.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: &oracleCost}}, true
}

// oracleCost is what one figure reading costs in tests (the priced tests' per-call cost).
var oracleCost = 0.001

// requestOf decodes a subagent request's JSON input (its first user message).
func requestOf(req model.Request, v any) bool {
	for _, m := range req.Messages {
		if s, ok := m.Content.(string); ok && m.Role == "user" && strings.HasPrefix(s, "Request:\n") {
			return json.Unmarshal([]byte(strings.TrimPrefix(s, "Request:\n")), v) == nil
		}
	}
	return false
}

var (
	oracleCode  = regexp.MustCompile(`(?:^|[^A-Za-z])(USD|EUR|GBP|CHF|CAD|JPY|INR|AUD)\s?$`)
	oracleAfter = regexp.MustCompile(`^\s?(USD|EUR|GBP|CHF|CAD|JPY|INR|AUD|dollars?|euros?)\b`)
	oracleScale = regexp.MustCompile(`(?i)^\s?(million|billion|thousand|mm|m|bn|k)\b`)
	oracleNoun  = regexp.MustCompile(`(?i)^\)?\s?(?:[a-z-]+\s){0,2}?(shares?|units?|trades?|employees?|people|persons?|transactions?|accounts?|votes?|options?|items?|copies|pages?|instalments?|installments?)\b`)
	oracleTime  = regexp.MustCompile(`(?i)^\)?\s?(?:calendar\s|business\s)?(days?|weeks?|months?|years?|hours?)\b`)
	oracleMonth = regexp.MustCompile(`(?i)(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s*\d{0,2},?\s*$`)
	oracleYear  = regexp.MustCompile(`^(19|20)\d\d$`)
	oracleRef   = regexp.MustCompile(`(?i)(section|rule|clause|paragraph|item|exhibit|schedule|article|§|no\.|#)\s*$`)
)

// oracleLabels labels one passage's spans.
func oracleLabels(p passageIn, all []passageIn, parties []string) []analysis.Label {
	pass := analysis.PassageOf(p.ID, p.Text)
	if p.Kind == "value" && deref(p.Class) == "Date" {
		pass.Spans = nil
		for _, s := range analysis.Spans(p.Text) {
			if len(s.Readings()) > 0 {
				pass.Spans = append(pass.Spans, s)
			}
		}
	}
	if p.Kind == "memo" { // a section's spans are its own figures, single digits included
		pass.Spans = analysis.Spans(p.Text)
	}
	var out []analysis.Label
	k := 0
	for _, given := range p.Spans { // each span given, at its next occurrence as written
		for k < len(pass.Spans) && pass.Spans[k].Text != given.Text {
			k++
		}
		if k == len(pass.Spans) {
			break
		}
		l := oracleLabel(p, pass.Spans[k], all, parties)
		l.Span = given.ID
		out = append(out, l)
		k++
	}
	return out
}

func oracleLabel(p passageIn, s analysis.Span, all []passageIn, parties []string) analysis.Label {
	before, after := strings.TrimRight(p.Text[:s.Start], " "), p.Text[s.End:]
	l := analysis.Label{Kind: analysis.KindOther, Currency: "none", Scale: "1", Decimal: oracleDecimal(s.Text), Sign: "positive"}
	if s.Neg() || strings.HasSuffix(before, "(") && strings.HasPrefix(after, ")") && strings.ContainsAny(p.Text[:s.Start], ":\t") ||
		p.Kind == "value" && (strings.HasPrefix(strings.TrimSpace(p.Text), "-") || strings.HasPrefix(strings.TrimSpace(p.Text), "−")) {
		l.Sign = "negative"
	}
	if strings.HasPrefix(strings.TrimLeft(after, ")"), "%") || regexp.MustCompile(`(?i)^\)?\s?per\s?cent`).MatchString(after) {
		l.Kind = analysis.KindPercent
	}
	cur := ""
	switch {
	case strings.HasSuffix(before, "US$") || strings.HasSuffix(before, "$") && !strings.HasSuffix(before, "C$") && !strings.HasSuffix(before, "A$"):
		cur = "USD"
	case strings.HasSuffix(before, "C$"):
		cur = "CAD"
	case strings.HasSuffix(before, "€"):
		cur = "EUR"
	case strings.HasSuffix(before, "£"):
		cur = "GBP"
	case oracleCode.MatchString(before):
		cur = oracleCode.FindStringSubmatch(before)[1]
	case oracleAfter.MatchString(after):
		w := strings.ToLower(oracleAfter.FindStringSubmatch(after)[1])
		cur = map[string]string{"dollar": "USD", "dollars": "USD", "euro": "EUR", "euros": "EUR"}[w]
		if cur == "" {
			cur = strings.ToUpper(w)
		}
	}
	if m := oracleScale.FindStringSubmatch(after); m != nil && (cur != "" || !strings.EqualFold(m[1], "m") && !strings.EqualFold(m[1], "k") && !strings.EqualFold(m[1], "mm")) {
		l.Scale = map[string]string{"million": "1000000", "mm": "1000000", "m": "1000000", "billion": "1000000000", "bn": "1000000000", "thousand": "1000", "k": "1000"}[strings.ToLower(m[1])]
		from := m[1]
		l.ScaleFrom = &from
	}
	switch {
	case l.Kind == analysis.KindPercent:
	case cur != "":
		l.Kind, l.Currency = analysis.KindMoney, cur
	case oracleTime.MatchString(after):
		l.Kind = analysis.KindOther
	case oracleNoun.MatchString(after):
		l.Kind = analysis.KindCount
	case oracleMonth.MatchString(before) || oracleYear.MatchString(s.Text) && !strings.HasSuffix(before, "$"):
		l.Kind = analysis.KindDate
	case oracleRef.MatchString(before):
		l.Kind = analysis.KindOther
	case p.Kind == "value":
		switch deref(p.Class) {
		case "Money":
			l.Kind, l.Currency = analysis.KindMoney, "USD"
			if u := strings.ToUpper(strings.TrimSpace(deref(p.Unit))); len(u) == 3 {
				l.Currency = u
			}
		case "Count":
			l.Kind = analysis.KindCount
		case "Percentage":
			l.Kind = analysis.KindPercent
		case "Date":
			l.Kind = analysis.KindDate
		}
	default: // a bare number: what a typed value of the finding with the same digits says it is, else a count
		l.Kind = analysis.KindCount
		for _, v := range all {
			if v.Kind == "value" && strings.Contains(v.Text, s.Text) {
				vs := analysis.PassageOf("x", v.Text)
				for _, w := range vs.Spans {
					if w.Text == s.Text {
						lv := oracleLabel(v, w, nil, parties)
						l.Kind, l.Currency = lv.Kind, lv.Currency
						if l.Scale == "1" {
							l.Scale, l.ScaleFrom = lv.Scale, lv.ScaleFrom
						}
					}
				}
			}
		}
	}
	if p.Kind == "value" && deref(p.Entity) != "" {
		e := deref(p.Entity)
		l.Party = &e
	} else if len(parties) == 1 {
		l.Party = &parties[0]
	} else {
		for _, v := range all {
			if v.Kind == "value" && deref(v.Entity) != "" && strings.Contains(v.Text, s.Text) {
				e := deref(v.Entity)
				l.Party = &e
			}
		}
	}
	return l
}

// oracleDecimal is the decimal mark a US-style fixture writes: a "." unless it groups.
func oracleDecimal(text string) string {
	switch {
	case strings.Count(text, ".") == 1:
		return "."
	case strings.Count(text, ",") == 1 && len(text)-strings.LastIndex(text, ",")-1 != 3 && !strings.Contains(text, "."):
		return ","
	}
	return "none"
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
	s := Section{IssueID: "I1", Markdown: md}
	body, regs := memoBody(s)
	spans := memoSpans(body, regs)
	it := newReading(nil, []analysis.Passage{{ID: "m", Text: body, Spans: spans}}, []passageIn{{ID: "m", Kind: "memo", Text: body}})
	it.labels, it.read = oracleLabels(it.in[0], it.in, nil), true
	rs, _ := it.readings()
	s.Read = &SectionRead{Hash: textHash(body), Figs: ofPassage(rs, "m")}
	return []Section{s}
}

// read is a clear reading (a typed value's Fig, as admission sets it from the figure reader's labels).
func read(kind, cur, amount, party string) *analysis.Reading {
	return &analysis.Reading{Kind: kind, Currency: cur, Amount: amount, Party: party}
}

// agentCall is a Go tool's call with input in.
func agentCall(in []byte) agent.Call { return agent.Call{Input: in} }
