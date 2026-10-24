// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package figtest is the figure reader's stand-in for tests (test support, never imported by
// production code): Reader answers every read_figures request of a model.Fake (its Respond hook)
// as a careful reader would label plain fixture texts (a sign or code beside a number is its
// currency, "%" a percentage, the noun after it what it counts, a scale word right after it its
// scale, a value's class, unit, entity and metric what the analyst typed), so the tests that are not
// about figure reading run without scripting it. It is test data, not a rule of the engine: a test
// about figure reading scripts its own labels and, where it matters, overrides these (With).
package figtest

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// Passage is one passage of the reader's request (the engine's passageIn, as JSON has it).
type Passage struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	DocumentID *string `json:"documentId"`
	Text       string  `json:"text"`
	Class      *string `json:"class"`
	Unit       *string `json:"unit"`
	Entity     *string `json:"entity"`
	Metric     *string `json:"metric"`
	Spans      []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"spans"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// With is Reader with the party of every span written as a key of parties set to its value, what it
// measures to its value in measures, and every span keyed in reads ("quote:4,000") labelled as reads
// says: a test's right labels where the stand-in would read otherwise.
func With(parties map[string]string, reads map[string]analysis.Label, measures map[string]string) func(model.Request) (model.Reply, bool) {
	return func(req model.Request) (model.Reply, bool) {
		r, ok := Reader(req)
		if !ok || req.Agent != "read_figures" {
			return r, ok
		}
		var in Request
		RequestOf(req, &in)
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
			if m, ok := measures[text[l.Span]]; ok {
				out.Labels[i].Measures = &m
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

// Request is the figure reader's request as the oracle reads it.
type Request struct {
	Findings []struct {
		ID       string
		Parties  []string
		Passages []Passage
	}
}

// Reader answers every read_figures request (fake.Respond), and every judge_figures request as
// Judge does; other agents use their scripts.
func Reader(req model.Request) (model.Reply, bool) {
	if req.Agent == "judge_figures" {
		return Judge(nil)(req)
	}
	if req.Agent != "read_figures" {
		return model.Reply{}, false
	}
	var in Request
	if !RequestOf(req, &in) {
		return model.Reply{}, false
	}
	labels := []analysis.Label{}
	for _, f := range in.Findings {
		for _, p := range f.Passages {
			labels = append(labels, Labels(p, f.Passages, f.Parties)...)
		}
	}
	data, _ := json.Marshal(map[string]any{"labels": labels})
	return model.Reply{Text: string(data), Usage: model.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: &Cost}}, true
}

// Cost is what one figure reading costs in tests (the priced tests' per-call cost).
var Cost = 0.001

// RequestOf decodes a subagent request's JSON input (its first user message).
func RequestOf(req model.Request, v any) bool {
	for _, m := range req.Messages {
		if s, ok := m.Content.(string); ok && m.Role == "user" && strings.HasPrefix(s, "Request:\n") {
			return json.Unmarshal([]byte(strings.TrimPrefix(s, "Request:\n")), v) == nil
		}
	}
	return false
}

var (
	oracleCode  = regexp.MustCompile(`(?:^|[^A-Za-z])(USD|EUR|GBP|CHF|CAD|JPY|INR|AUD)\s?$`)
	oracleAfter = regexp.MustCompile(`^[\s,\t]?(USD|EUR|GBP|CHF|CAD|JPY|INR|AUD|dollars?|euros?)\b`)
	oracleScale = regexp.MustCompile(`(?i)^\s?(million|billion|thousand|mm|m|bn|k)\b`)
	oracleNoun  = regexp.MustCompile(`(?i)^\)?\s?(?:[a-z-]+\s){0,2}?(shares?|units?|trades?|employees?|people|persons?|transactions?|accounts?|votes?|options?|items?|copies|pages?|instalments?|installments?)\b`)
	oracleTime  = regexp.MustCompile(`(?i)^\)?[\s-]?(calendar\s|business\s)?(days?|weeks?|months?|years?|hours?)\b`)
	oracleMult  = regexp.MustCompile(`(?i)^\)?\s?(?:x\b|×|times\b)`) // a multiple: "1.5x", "2.0 times"
	oracleTimes = regexp.MustCompile(`(?:\s|^)[x×]\s?$`)             // a factor after a times sign: "shares x 1.5"
	oracleMonth = regexp.MustCompile(`(?i)(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s*\d{0,2},?\s*$`)
	oracleYear  = regexp.MustCompile(`^(19|20)\d\d$`)
	oracleRef   = regexp.MustCompile(`(?i)(section|rule|clause|paragraph|item|exhibit|schedule|article|§|no\.|#)\s*$`)
)

// Labels labels one Passage's spans (matched to the spans it was given by their text, in order).
func Labels(p Passage, all []Passage, parties []string) []analysis.Label {
	pass := analysis.PassageOf(p.ID, p.Text)
	if p.Kind == "value" && deref(p.Class) == "Date" {
		pass.Spans = nil
		for _, s := range analysis.Spans(p.Text) {
			if len(s.Readings()) > 0 {
				pass.Spans = append(pass.Spans, s)
			}
		}
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

func oracleLabel(p Passage, s analysis.Span, all []Passage, parties []string) analysis.Label {
	before, after := strings.TrimRight(p.Text[:s.Start], " "), p.Text[s.End:]
	none := "none" // a fixture's figures are for no period unless a test says so
	l := analysis.Label{Kind: analysis.KindOther, Currency: "none", Scale: "1", Decimal: oracleDecimal(s.Text), Sign: "positive", Period: &none}
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
	scaleAt := after
	if m := oracleAfter.FindStringSubmatchIndex(after); m != nil && oracleCode.MatchString(" "+after[m[2]:m[3]]) { // a code then its scale: "2810,EUR million"
		scaleAt = after[m[1]:]
	}
	if m := oracleScale.FindStringSubmatch(scaleAt); m != nil && (cur != "" || !strings.EqualFold(m[1], "m") && !strings.EqualFold(m[1], "k") && !strings.EqualFold(m[1], "mm")) {
		l.Scale = map[string]string{"million": "1000000", "mm": "1000000", "m": "1000000", "billion": "1000000000", "bn": "1000000000", "thousand": "1000", "k": "1000"}[strings.ToLower(m[1])]
		from := m[1]
		l.ScaleFrom = &from
	}
	switch {
	case l.Kind == analysis.KindPercent:
	case cur != "":
		l.Kind, l.Currency = analysis.KindMoney, cur
	case oracleTime.MatchString(after):
		m := oracleTime.FindStringSubmatch(after)
		u := map[string]string{"day": "days", "week": "weeks", "month": "months", "year": "years"}[strings.TrimSuffix(strings.ToLower(m[2]), "s")]
		if strings.EqualFold(strings.TrimSpace(m[1]), "business") && u == "days" {
			u = "business days"
		}
		l.Kind = analysis.KindOther // hours: no duration unit
		if u != "" {
			from := strings.TrimSpace(m[1] + m[2]) // the words beside it, as written
			l.Kind, l.Unit, l.UnitFrom = analysis.KindDuration, &u, &from
		}
	case oracleMult.MatchString(after):
		from := strings.TrimSpace(strings.TrimLeft(oracleMult.FindString(after), ")"))
		l.Kind, l.UnitFrom = analysis.KindNumber, &from
	case oracleTimes.MatchString(before):
		from := strings.TrimSpace(oracleTimes.FindString(before))
		l.Kind, l.UnitFrom = analysis.KindNumber, &from
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
	if p.Kind == "value" && deref(p.Metric) != "" { // what it measures: the analyst's metric for the value and its digits
		m := deref(p.Metric)
		l.Measures = &m
	} else {
		for _, v := range all {
			if v.Kind == "value" && deref(v.Metric) != "" && strings.Contains(v.Text, s.Text) {
				m := deref(v.Metric)
				l.Measures = &m
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

// JudgeSide is one figure of a judge_figures pair, as the stand-in reads it.
type JudgeSide struct {
	Ref, DocumentID, Value string
	Entity, Metric         *string
	Claim                  string
	Quotes                 []string
	Reading                struct {
		Kind, Currency, Amount, Scale                      string
		Party, Counterparty, Measures, Period, TypedEntity *string
	}
}

// JudgeRequest is the figure judge's request.
type JudgeRequest struct {
	Pairs []struct {
		ID, Kind string
		A, B     JudgeSide
	}
	Aliases []analysis.Alias
}

// Judge answers judge_figures requests (fake.Respond) as a careful judge would on plain fixtures:
// yes when both figures are one party's (the reader's party, else the analyst's entity, a name and
// its aliases one party) and measure
// the same thing (the reader's measures, else the metric) for the same period, no otherwise.
// decide, when it returns an answer, overrides that for a pair (a test about the join's verdicts).
func Judge(decide func(a, b JudgeSide) string) func(model.Request) (model.Reply, bool) {
	return func(req model.Request) (model.Reply, bool) {
		if req.Agent != "judge_figures" {
			return model.Reply{}, false
		}
		var in JudgeRequest
		if !RequestOf(req, &in) {
			return model.Reply{}, false
		}
		verdicts := []map[string]string{}
		for _, p := range in.Pairs {
			ans := ""
			if decide != nil {
				ans = decide(p.A, p.B)
			}
			if ans == "" {
				ans = "no"
				if party(in.Aliases, or(p.A.Reading.Party, p.A.Entity)) == party(in.Aliases, or(p.B.Reading.Party, p.B.Entity)) && fold(or(p.A.Reading.Measures, p.A.Metric)) == fold(or(p.B.Reading.Measures, p.B.Metric)) &&
					fold(deref(p.A.Reading.Period)) == fold(deref(p.B.Reading.Period)) {
					ans = "yes"
				}
			}
			verdicts = append(verdicts, map[string]string{"pair": p.ID, "sameQuantity": ans,
				"reason": "A says \"" + first(p.A.Quotes) + "\"; B says \"" + first(p.B.Quotes) + "\"."})
		}
		data, _ := json.Marshal(map[string]any{"verdicts": verdicts})
		return model.Reply{Text: string(data), Usage: model.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: &Cost}}, true
	}
}

// party is a name as the stand-in judges it: an alias as the name it is an alias of.
func party(aliases []analysis.Alias, name string) string {
	for _, a := range aliases {
		for _, o := range a.Aliases {
			if fold(o) == fold(name) {
				return fold(a.Name)
			}
		}
	}
	return fold(name)
}

func or(a, b *string) string {
	if a != nil && *a != "" {
		return *a
	}
	return deref(b)
}

func first(qs []string) string {
	if len(qs) == 0 {
		return ""
	}
	return qs[0]
}

func fold(s string) string {
	s = strings.Join(strings.Fields(strings.ToLower(s)), " ")
	for _, a := range []string{"the ", "a ", "an "} {
		s = strings.TrimPrefix(s, a)
	}
	return s
}
