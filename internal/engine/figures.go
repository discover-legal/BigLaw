// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
)

// Figure reading (lab-r4 r4, owner decision): what a number means is decided by a small agent,
// read_figures (specs/agents/read_figures.md, critic role, its own tools figure_context and
// exact_calc), never by a word list in Go. Go finds the spans (analysis.Spans: digits exactly as
// written), asks the reader to label each by id, checks every label (analysis.ReadLabels), and then
// only compares: a typed value is admitted when one of its quotes' spans reads as the same kind,
// currency and exact amount (valueStated); a claim's sum when one does (claimSums); a memo figure
// traces when a cited span or value reads as it does (G8, lint.go); two figures conflict only when
// both read as one kind of one party (analysis.Join). An unclear reading is low confidence: its
// finding is gated for a lawyer, and it never verifies a figure by itself. The reader is called once
// per admission (a finding's quotes, values and claim batched; one call holds up to maxReadSpans
// spans) and once per drafted section, out of the calling tool's grant (metered and journaled like any
// call), and its answers are cached by the hash of what it read (figure_reads), so a run read again
// gets the same reading.

// readerVersion is part of every cached reading's key: a change to the reader's prompt or request
// reads every figure again.
const readerVersion = "read_figures/1"

// maxReadSpans and maxReadBytes bound one reader call (its answer is a label per span).
const (
	maxReadSpans = 80
	maxReadBytes = 32_000
	maxReadItems = 40
)

type spanIn struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// passageIn is one passage as the reader's request has it; the analysis.Passage beside it holds
// the same text and spans for checking its labels.
type passageIn struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"` // quote, value, claim or memo
	DocumentID *string  `json:"documentId"`
	Text       string   `json:"text"`
	Class      *string  `json:"class"`
	Unit       *string  `json:"unit"`
	Entity     *string  `json:"entity"`
	Metric     *string  `json:"metric"`
	Spans      []spanIn `json:"spans"`
}

// reading is one item of the reader's work: a finding's passages, or a section's.
type reading struct {
	ps      []analysis.Passage
	in      []passageIn
	parties []string
	key     string
	labels  []analysis.Label
	read    bool   // the reader answered for it, or the cache held its answer
	why     string // why it was not read
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// newReading is a reading item over ps (each with its spans) and what the reader is told of each.
func newReading(parties []string, ps []analysis.Passage, in []passageIn) *reading {
	r := &reading{ps: ps, in: in, parties: parties}
	for i := range in {
		in[i].Spans = []spanIn{}
		for k, s := range ps[i].Spans {
			in[i].Spans = append(in[i].Spans, spanIn{analysis.SpanID(ps[i].ID, k), s.Text})
		}
	}
	data, _ := json.Marshal(map[string]any{"v": readerVersion, "parties": parties, "passages": in})
	sum := sha256.Sum256(data)
	r.key = hex.EncodeToString(sum[:])
	return r
}

func (r *reading) spans() (n, bytes int) {
	for i, p := range r.ps {
		n, bytes = n+len(p.Spans), bytes+len(r.in[i].Text)
	}
	return n, bytes
}

// readings is the item's checked readings by span id, and the reader's refused labels.
func (r *reading) readings() (map[string]analysis.Reading, []string) {
	rs, notes := analysis.ReadLabels(r.ps, r.labels)
	if !r.read {
		for id, x := range rs {
			x.Why = "not read: " + r.why
			rs[id] = x
		}
	}
	return rs, notes
}

// findingReading is the reader's item for one finding: its verified quotes, its typed values (a
// date's every number, so its party can be read) and its claim, with the parties it names.
func findingReading(f Finding, quotes []analysis.Quote, vals []analysis.Value) *reading {
	var ps []analysis.Passage
	var in []passageIn
	var parties []string
	add := func(p analysis.Passage, x passageIn) {
		if len(p.Spans) > 0 {
			ps, in = append(ps, p), append(in, x)
		}
	}
	for i, q := range quotes {
		id := "q" + strconv.Itoa(i+1)
		add(analysis.PassageOf(id, q.Quote), passageIn{ID: id, Kind: "quote", DocumentID: strp(q.DocumentID), Text: q.Quote})
	}
	for j, v := range vals {
		id := "v" + strconv.Itoa(j+1)
		p := analysis.PassageOf(id, v.Value)
		if analysis.ParseClass(v.Class) == "Date" { // its numbers are the date's: read for its party
			p.Spans = slices.DeleteFunc(analysis.Spans(v.Value), func(s analysis.Span) bool { return len(s.Readings()) == 0 })
		}
		add(p, passageIn{ID: id, Kind: "value", Text: v.Value, Class: strp(v.Class), Unit: strp(v.Unit), Entity: strp(v.Entity), Metric: strp(v.Metric)})
		if e := norm(v.Entity); e != "" && !slices.Contains(parties, e) {
			parties = append(parties, e)
		}
	}
	add(analysis.PassageOf("c", f.Claim), passageIn{ID: "c", Kind: "claim", Text: f.Claim})
	for _, e := range f.Entities {
		if n := norm(e.Name); n != "" && !slices.Contains(parties, n) {
			parties = append(parties, n)
		}
	}
	return newReading(parties[:min(len(parties), 40)], ps, in)
}

// readFigures has the reader label every item's spans: from the cache, else in calls of up to
// maxReadSpans spans out of b. An item it could not read (no model, the grant could not fund it, the
// call failed or answered off its schema) keeps why; only a stop or a journal failure is an error.
func (e *Engine) readFigures(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller string, items []*reading) error {
	var todo []*reading
	for _, it := range items {
		if n, _ := it.spans(); n == 0 {
			it.read = true
			continue
		}
		labels, ok, err := e.cachedReading(ctx, it.key)
		if err != nil {
			return err
		}
		if ok {
			it.labels, it.read = labels, true
			continue
		}
		todo = append(todo, it)
	}
	for len(todo) > 0 {
		k, spans, bytes := 0, 0, 0
		for k < len(todo) && k < maxReadItems {
			n, by := todo[k].spans()
			if k > 0 && (spans+n > maxReadSpans || bytes+by > maxReadBytes) {
				break
			}
			k, spans, bytes = k+1, spans+n, bytes+by
		}
		chunk := todo[:k]
		todo = todo[k:]
		why, err := e.readChunk(ctx, rt, c, b, caller, chunk)
		if err != nil {
			return err
		}
		for _, it := range chunk {
			if !it.read {
				it.why = why
			}
		}
	}
	return nil
}

// readChunk is one reader call over chunk: span ids prefixed by the item's number in the call.
func (e *Engine) readChunk(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller string, chunk []*reading) (string, error) {
	if rt == nil || rt.Chat == nil || rt.Reg == nil {
		return "no model was available for it", nil
	}
	var findings []map[string]any
	for i, it := range chunk {
		pre := strconv.Itoa(i+1) + "."
		ps := make([]passageIn, len(it.in))
		for j, p := range it.in {
			p.ID = pre + p.ID
			p.Spans = slices.Clone(p.Spans)
			for k := range p.Spans {
				p.Spans[k].ID = pre + p.Spans[k].ID
			}
			ps[j] = p
		}
		findings = append(findings, map[string]any{"id": strconv.Itoa(i + 1), "parties": append([]string{}, it.parties...), "passages": ps})
	}
	input, _ := json.Marshal(map[string]any{"findings": findings})
	res, err := rt.Invoke(ctx, agent.Agent{Name: caller, Visible: []string{"read_figures"}}, agent.Call{TaskID: c.TaskID, DocumentIDs: c.DocumentIDs}, "read_figures", input, b, false, 0)
	switch {
	case ctx.Err() != nil:
		return "", ctx.Err()
	case errors.Is(err, agent.ErrJournal):
		return "", err
	case errors.Is(err, agent.ErrBudget):
		return "the budget could not fund the figure reader", nil
	case err != nil:
		return "the figure reader's call failed", nil
	}
	var out struct{ Labels []analysis.Label }
	if json.Unmarshal(res.Output, &out) != nil {
		return "the figure reader's answer was malformed", nil
	}
	by := make([][]analysis.Label, len(chunk))
	for _, l := range out.Labels {
		n, rest, ok := strings.Cut(l.Span, ".")
		i, err := strconv.Atoi(n)
		if !ok || err != nil || i < 1 || i > len(chunk) {
			continue // a span it was not given: ReadLabels would reject it too
		}
		l.Span = rest
		for _, p := range []**string{&l.ScaleFrom, &l.RangeWith} {
			if *p != nil && strings.HasPrefix(**p, n+".") {
				s := strings.TrimPrefix(**p, n+".")
				*p = &s
			}
		}
		by[i-1] = append(by[i-1], l)
	}
	for i, it := range chunk {
		it.labels, it.read = by[i], true
		if err := e.cacheReading(ctx, it.key, res.Model, by[i]); err != nil {
			return "", err
		}
	}
	return "", nil
}

// cachedReading is the reader's stored answer for key (figure_reads).
func (e *Engine) cachedReading(ctx context.Context, key string) ([]analysis.Label, bool, error) {
	var data []byte
	err := e.db.QueryRowContext(ctx, `SELECT labels FROM figure_reads WHERE key = $1`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	var labels []analysis.Label
	if json.Unmarshal(data, &labels) != nil {
		return nil, false, nil
	}
	return labels, true, nil
}

// cacheReading stores the reader's answer for key; the first answer stored is the one every later
// reading of the same passages gets.
func (e *Engine) cacheReading(ctx context.Context, key, model string, labels []analysis.Label) error {
	data, err := json.Marshal(append([]analysis.Label{}, labels...))
	if err == nil {
		_, err = e.db.ExecContext(ctx, `INSERT INTO figure_reads (key, labels, model) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`, key, data, model)
	}
	return err
}

// ofPassage are the readings of one passage (by its id), in span order.
func ofPassage(rs map[string]analysis.Reading, id string) []analysis.Reading {
	var out []analysis.Reading
	for i := 0; ; i++ {
		r, ok := rs[analysis.SpanID(id, i)]
		if !ok {
			return out
		}
		out = append(out, r)
	}
}

// quoteReadings are the readings of a finding's quotes (passages q1, q2, …), in order.
func quoteReadings(rs map[string]analysis.Reading) []analysis.Reading {
	var out []analysis.Reading
	for id, r := range rs {
		if strings.HasPrefix(id, "q") {
			out = append(out, r)
		}
	}
	order := func(id string) (int, int) {
		p, sp, _ := strings.Cut(strings.TrimPrefix(id, "q"), ".s")
		a, _ := strconv.Atoi(p)
		b, _ := strconv.Atoi(sp)
		return a, b
	}
	slices.SortFunc(out, func(x, y analysis.Reading) int {
		a, b := order(x.Span)
		c, d := order(y.Span)
		return cmp.Or(cmp.Compare(a, c), cmp.Compare(b, d))
	})
	return out
}

// valueStated checks typed value j of a finding against its quotes' readings (the figure reader's)
// and returns the value's own reading. A quantity (money, a count, a percentage) needs one span of
// the value that reads as its class, and a quote span that reads as the same kind, currency and exact
// amount; when either reading is unclear but they agree on what can be read, low says the value is
// admitted at low confidence (its finding gated for a lawyer). A date or any other value needs its
// text in its quotes, or every number and date of it there as written (covered); a date's reading
// carries the party the reader gave it. why says why a value is not admitted.
func valueStated(v analysis.Value, j int, rs map[string]analysis.Reading, quotes string) (fig *analysis.Reading, ok, low bool, why string) {
	if strings.TrimSpace(v.Value) == "" {
		return nil, false, false, "it is empty"
	}
	id := "v" + strconv.Itoa(j+1)
	own := ofPassage(rs, id)
	text := strings.Contains(strings.ToLower(norm(quotes)), strings.ToLower(norm(v.Value)))
	want := analysis.KindOf(v)
	if want == "" { // a date, a name, an identifier
		if !text && !covered(v.Value, quotes) {
			return nil, false, false, "its quotes do not state it"
		}
		if analysis.ParseClass(v.Class) == "Date" {
			for _, r := range own {
				if r.Party != "" && !r.Unclear {
					return &analysis.Reading{Kind: analysis.KindDate, Text: v.Value, Party: r.Party}, true, false, ""
				}
			}
		}
		return nil, true, false, ""
	}
	if len(own) == 0 {
		if text && !strings.ContainsFunc(v.Value, unicode.IsDigit) { // a figure written in words, stated as written: never compared
			return nil, true, false, ""
		}
		return nil, false, false, "it states no number"
	}
	vr := own[0]
	if len(own) > 1 {
		k := slices.IndexFunc(own, func(r analysis.Reading) bool { return r.Kind == want })
		if k < 0 || slices.ContainsFunc(own[k+1:], func(r analysis.Reading) bool { return r.Kind == want }) {
			return nil, false, false, "it is not one figure"
		}
		vr = own[k]
	}
	if vr.Kind != want && vr.Kind != analysis.KindUnclear {
		return nil, false, false, "the figure reader read it as " + article(vr.Kind)
	}
	if vr.Amount == "" {
		return nil, false, false, "the figure reader could not read it (" + vr.Why + ")"
	}
	why = "no span of its quotes reads as " + describe(vr)
	var soft *analysis.Reading
	for _, q := range quoteReadings(rs) {
		same, unclear := analysis.SameFigure(vr, q)
		switch {
		case same:
			return &vr, true, false, ""
		case unclear && soft == nil:
			soft = &q
		case q.Amount == vr.Amount && q.Kind != vr.Kind && !q.Unclear:
			why = "its quote states it as " + article(q.Kind) + cmpStr(q.Party != "", " ("+q.Party+")", "")
		case q.Amount == vr.Amount && q.Currency != vr.Currency && q.Kind == vr.Kind:
			why = "its quote states it in " + q.Currency
		}
	}
	if soft != nil {
		vr.Unclear, vr.Why = true, cmpStr(vr.Unclear, vr.Why, soft.Why)
		return &vr, true, true, ""
	}
	if vr.Unclear && why != "" {
		why += " (its own reading is unclear: " + vr.Why + ")"
	}
	return nil, false, false, why
}

func article(kind string) string {
	if kind == analysis.KindOther || kind == analysis.KindUnclear {
		return "an " + kind + " figure"
	}
	return "a " + kind + " figure"
}

// describe is a reading as a note says it ("money 2000000 USD").
func describe(r analysis.Reading) string {
	return strings.TrimSpace(r.Kind + " " + r.Amount + " " + r.Currency)
}

// claimSums are the sums of money a claim states and its verified quotes state too, which no typed
// value carries (bench5 SEC F388: "payments totaling $103,800.00", the figure in its own quote, typed
// nowhere, so the $92,600 vs $103,800 conflict had nothing to pair). Each is kept untyped: no entity,
// no metric (so the label join never compares it; the near join does, by its quotes' words), with
// the claim's reading of it (its party included). A sum is kept only when the figure reader read
// the claim's span and a quote span as the same money, clearly: a share count never stands for
// dollars, a bare "3" ("Section 3") never for "$3 million" (lab-r4 verifier H1, r4 H1).
func claimSums(claim string, rs map[string]analysis.Reading, have []analysis.Value) []analysis.Value {
	var out []analysis.Value
	canon := func(v analysis.Value) string { _, c, _, _ := analysis.Canon(v); return c }
	seen := map[string]bool{}
	for _, v := range have {
		seen[canon(v)] = true
	}
	qs := quoteReadings(rs)
	for _, cr := range ofPassage(rs, "c") {
		if !cr.Clear() || cr.Kind != analysis.KindMoney || cr.Start < 0 || cr.End > len(claim) {
			continue
		}
		fig := cr
		v := analysis.Value{Value: written(claim, cr), Class: "Money", Unit: cmpStr(cr.Currency != "USD", cr.Currency, ""), Fig: &fig}
		c := canon(v)
		if c == "" || seen[c] || !slices.ContainsFunc(qs, func(q analysis.Reading) bool { same, _ := analysis.SameFigure(cr, q); return same }) {
			continue
		}
		seen[c] = true
		out = append(out, v)
	}
	return out
}

// written is a reading's figure as text writes it: its span, the marks glued before it ("$", "US$",
// "€", a minus) and the words its scale comes from when they follow it ("$4.2 million").
func written(text string, r analysis.Reading) string {
	s, e := r.Start, r.End
	for s > 0 {
		c, n := utf8.DecodeLastRuneInString(text[:s])
		if unicode.IsSpace(c) || unicode.IsDigit(c) || strings.ContainsRune("([{\"'“‘", c) {
			break
		}
		s -= n
	}
	if from := strings.TrimSpace(r.ScaleFrom); from != "" {
		rest := strings.TrimLeft(text[e:], " ")
		if len(rest) >= len(from) && strings.EqualFold(rest[:len(from)], from) {
			e = len(text) - len(rest) + len(from)
		}
	}
	return strings.TrimSpace(text[s:e])
}

// lowNote is the note on a value admitted at low confidence.
func lowNote(v analysis.Value, fig *analysis.Reading) string {
	why := "unclear"
	if fig != nil && fig.Why != "" {
		why = fig.Why
	}
	return fmt.Sprintf("value %q (%s) admitted at low confidence, gated for a lawyer: the figure reader's reading is unclear (%s)", v.Value, cmpStr(v.Metric != "", v.Metric, "no metric"), why)
}
