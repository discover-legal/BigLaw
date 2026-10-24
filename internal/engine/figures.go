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
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lib/pq"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/store"
)

// Figure reading (lab-r4 r4, owner decision): what a number means is decided by a small agent,
// read_figures (specs/agents/read_figures.md, critic role, its own tools figure_context and
// exact_calc), never by a word list in Go. Go finds the spans (analysis.Spans: digits exactly as
// written), asks the reader to label each by id, checks every label (analysis.ReadLabels), and then
// only compares: a typed value is admitted when one of its quotes' spans reads as the same kind,
// currency, scale and exact amount (valueStated); a claim's sum when one does (claimSums); a memo
// figure traces only when the reader read it clearly and a cited span or value reads as it does, and
// a figure it did not read clearly is flagged, never traced (G8, lint.go: fails closed, r5 H2); two
// figures conflict only when both read as one kind of one party's same measure for the same period
// (analysis.Join). An unclear reading is low confidence: its finding is gated for a lawyer, and it
// never verifies a figure by itself. The reader is called once per admission (the findings' quotes,
// values and claims batched, in parts of at most maxReadSpans spans) and once per drafted section
// (its worst case reserved before the drafter runs, r5 L4), metered and journaled like any call, and
// its complete answers are cached (figure_reads), so a run read again gets the same reading.

// readerSpec is part of every cached reading's key: the hash of the reader's spec, prompt and tools'
// specs, so an edit to any of them reads every figure again (lab-r4 r5 M3; derived, never bumped by
// hand). The key also holds the model the reader runs on, the matter (and, for a drafted section, the
// task) it read for, and exactly what it was sent.
var readerSpec = specHash(specFS, readerSpecFiles)

// readerSpecFiles are the files the reader's behaviour comes from.
var readerSpecFiles = []string{"specs/agents/read_figures.json", "specs/agents/read_figures.md", "specs/figure_context.json", "specs/exact_calc.json"}

// specHash is the hash of files (an agent's spec, prompt and tools' specs): part of its cache keys.
func specHash(fsys fs.FS, files []string) string {
	h := sha256.New()
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			panic("engine: " + err.Error())
		}
		fmt.Fprintf(h, "%s %d\n", f, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The bounds of one reader call (its answer is a label per span), below its input schema's: a call
// holds at most maxReadItems parts of maxReadSpans spans and maxReadBytes of text in all, a part at
// most maxReadPassages passages. A finding or section over them is read in several parts (a passage's
// spans split between parts); a span over maxSpanText characters or a passage over maxReadBytes is
// not sent at all (it stays unread, unclear), so one oversized item never sinks the others (r5 L1).
const (
	maxReadSpans    = 80
	maxReadBytes    = 32_000
	maxReadItems    = 40
	maxReadPassages = 60
	maxSpanText     = 64
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
	in      []passageIn // what is sent of each passage: its spans that fit
	parties []string
	memo    bool // a drafted section: its cached reading is the task's own (r5 L3)
	labels  []analysis.Label
	read    bool              // the reader answered for all of it (or the cache held its answers)
	why     string            // why some of it was not read
	unread  map[string]string // span id → why it was not read (not sent, or its part was not read)
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// newReading is a reading item over ps (each with its spans) and what the reader is told of each:
// every span that fits (maxSpanText) of every passage that fits (maxReadBytes).
func newReading(parties []string, ps []analysis.Passage, in []passageIn) *reading {
	r := &reading{ps: ps, in: in, parties: parties, unread: map[string]string{}}
	for i := range in {
		in[i].Spans = []spanIn{}
		for k, s := range ps[i].Spans {
			id := analysis.SpanID(ps[i].ID, k)
			switch {
			case len(in[i].Text) > maxReadBytes:
				r.unread[id] = "its passage is too long for the figure reader"
			case len(s.Text) > maxSpanText:
				r.unread[id] = "it is too long for the figure reader (an identifier)"
			default:
				in[i].Spans = append(in[i].Spans, spanIn{id, s.Text})
			}
		}
	}
	return r
}

// part is one call's share of an item: some of its passages, each with some of its spans.
type part struct {
	it    *reading
	in    []passageIn
	ids   []string
	bytes int
	key   string
}

// parts splits an item into parts within the call bounds, in passage and span order.
func (r *reading) parts() []*part {
	var out []*part
	cur := &part{it: r}
	flush := func() {
		if len(cur.ids) > 0 {
			out = append(out, cur)
		}
		cur = &part{it: r}
	}
	for _, p := range r.in {
		for len(p.Spans) > 0 {
			if len(cur.in) == maxReadPassages || len(cur.ids) == maxReadSpans || cur.bytes+len(p.Text) > maxReadBytes {
				flush()
			}
			n := min(len(p.Spans), maxReadSpans-len(cur.ids))
			q := p
			q.Spans = slices.Clone(p.Spans[:n])
			p.Spans = p.Spans[n:]
			cur.in, cur.bytes = append(cur.in, q), cur.bytes+len(q.Text)
			for _, s := range q.Spans {
				cur.ids = append(cur.ids, s.ID)
			}
		}
	}
	flush()
	return out
}

// passages are the checked passages a part was sent (for checking its answer is complete).
func (pt *part) passages() []analysis.Passage {
	var out []analysis.Passage
	for _, p := range pt.in {
		i := slices.IndexFunc(pt.it.ps, func(x analysis.Passage) bool { return x.ID == p.ID })
		x := pt.it.ps[i]
		var ss []analysis.Span
		for k, s := range x.Spans {
			if slices.ContainsFunc(p.Spans, func(y spanIn) bool { return y.ID == analysis.SpanID(x.ID, k) }) {
				ss = append(ss, s)
			} else {
				ss = append(ss, analysis.Span{Start: -1, End: -1}) // keeps the ids of the spans it was sent
			}
		}
		x.Spans = ss
		out = append(out, x)
	}
	return out
}

// complete reports whether labels are an answer for exactly the part's spans: one accepted label for
// each span id it was sent, none for any other, and not unclear throughout. Only a complete answer is
// cached (r5 M3, r6 L3).
func (pt *part) complete(labels []analysis.Label) bool {
	if len(labels) != len(pt.ids) {
		return false
	}
	seen := map[string]bool{}
	for _, l := range labels {
		if !slices.Contains(pt.ids, l.Span) || seen[l.Span] {
			return false
		}
		seen[l.Span] = true
	}
	rs, notes := analysis.ReadLabels(pt.passages(), labels)
	someRead := false // an answer that reads nothing clearly (unclear throughout) is this run's only: a later run may read it (r6 L3)
	for _, id := range pt.ids {
		someRead = someRead || !rs[id].Unclear
	}
	return len(notes) == 0 && someRead
}

// readings is the item's checked readings by span id, and the reader's refused labels.
func (r *reading) readings() (map[string]analysis.Reading, []string) {
	rs, notes := analysis.ReadLabels(r.ps, r.labels)
	for id, x := range rs {
		if why, ok := r.unread[id]; ok {
			x.Unclear, x.Why = true, "not read: "+why
		} else if !r.read && x.Why == "not read" {
			x.Why = "not read: " + r.why
		}
		rs[id] = x
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
// maxReadItems parts out of b, on one model (picked before the calls, so it is part of the cache key).
// An item it could not read in full (no model, the grant could not fund it, a call failed or answered
// off its schema) keeps why, for the spans it was not read for; only a stop or a journal or database
// failure is an error.
func (e *Engine) readFigures(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller string, items []*reading) error {
	model, models := "", []string(nil)
	if rt != nil && rt.Chat != nil && rt.Reg != nil {
		models = rt.Candidates(spec("read_figures").Role)
		model = rt.Bandit.PickModel(ctx, "read_figures", models)
	}
	models = append([]string{model}, slices.DeleteFunc(models, func(m string) bool { return m == model })...) // a reading any candidate model made is reused before another call
	scope, err := e.readScope(ctx, c.TaskID)
	if err != nil {
		return err
	}
	var todo []*part
	for _, it := range items {
		it.read = true
	parts:
		for _, pt := range it.parts() {
			for _, m := range models {
				labels, ok, err := e.cachedReading(ctx, readKey(m, scope, cmpStr(it.memo, c.TaskID, ""), it.parties, pt.in), m, scope != "")
				if err != nil {
					return err
				}
				if ok {
					it.labels = append(it.labels, labels...)
					continue parts
				}
			}
			pt.key = readKey(model, scope, cmpStr(it.memo, c.TaskID, ""), it.parties, pt.in)
			todo = append(todo, pt)
		}
	}
	for len(todo) > 0 {
		k, bytes := 0, 0
		for k < len(todo) && k < maxReadItems && (k == 0 || bytes+todo[k].bytes <= maxReadBytes) {
			k, bytes = k+1, bytes+todo[k].bytes
		}
		chunk := todo[:k]
		todo = todo[k:]
		why, err := e.readChunk(ctx, rt, c, b, caller, model, scope, chunk)
		if err != nil {
			return err
		}
		if why == "" {
			continue
		}
		for _, pt := range chunk {
			pt.it.read, pt.it.why = false, why
			for _, id := range pt.ids {
				pt.it.unread[id] = why
			}
		}
	}
	return nil
}

// readerReserve is the worst case of the figure reader's answer over a drafted section (one turn, at
// most maxReadBytes of text and maxReadSpans spans), on the model it would run on: held back from the
// drafter's grant (which holds the call beside the drafter's own turns, goTurns) when the grant
// holds more than that.
func readerReserve(ctx context.Context, rt *agent.Runtime) agent.Grant {
	if rt == nil || rt.Chat == nil || rt.Reg == nil {
		return agent.Grant{}
	}
	s := spec("read_figures")
	s.MaxTurns = 1
	return rt.WorstCase(s, prompt("agents/read_figures"), rt.Bandit.PickModel(ctx, "read_figures", rt.Candidates(s.Role)), maxReadBytes+maxReadSpans*(maxSpanText+32))
}

// readScope is the matter a task reads for ("" outside a task: nothing is cached).
func (e *Engine) readScope(ctx context.Context, taskID string) (string, error) {
	if taskID == "" || e.db == nil {
		return "", nil
	}
	var matter string
	err := e.db.QueryRowContext(ctx, `SELECT matter_number FROM tasks WHERE id = $1`, taskID).Scan(&matter)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return "matter:" + matter, err
}

// readKey is a part's cache key: the reader's spec, the model, the matter (and a section's task), the
// parties it was told and exactly what it was sent.
func readKey(model, scope, task string, parties []string, in []passageIn) string {
	data, _ := json.Marshal(map[string]any{"spec": readerSpec, "model": model, "scope": scope, "task": task, "parties": parties, "passages": in})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// readChunk is one reader call over chunk: span ids prefixed by the part's number in the call. A
// part's answer is used as given (an unlabelled span stays unclear) and cached only when complete.
func (e *Engine) readChunk(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller, model, scope string, chunk []*part) (string, error) {
	if model == "" {
		return "no model was available for it", nil
	}
	var findings []map[string]any
	for i, pt := range chunk {
		pre := strconv.Itoa(i+1) + "."
		ps := make([]passageIn, len(pt.in))
		for j, p := range pt.in {
			p.ID = pre + p.ID
			p.Spans = slices.Clone(p.Spans)
			for k := range p.Spans {
				p.Spans[k].ID = pre + p.Spans[k].ID
			}
			ps[j] = p
		}
		findings = append(findings, map[string]any{"id": strconv.Itoa(i + 1), "parties": append([]string{}, pt.it.parties...), "passages": ps})
	}
	input, _ := json.Marshal(map[string]any{"findings": findings})
	res, err := rt.Invoke(ctx, agent.Agent{Name: caller, Visible: []string{"read_figures"}}, agent.Call{TaskID: c.TaskID, DocumentIDs: c.DocumentIDs, Model: model}, "read_figures", input, b, false, 0)
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
	for i, pt := range chunk {
		pt.it.labels = append(pt.it.labels, by[i]...)
		if !pt.complete(by[i]) { // an empty, partial or refused answer is this run's only: never cached
			for _, id := range pt.ids {
				if !slices.ContainsFunc(by[i], func(l analysis.Label) bool { return l.Span == id }) {
					pt.it.unread[id] = "the figure reader gave it no label"
				}
			}
			continue
		}
		if scope != "" {
			if err := e.cacheReading(ctx, pt, c.TaskID, scope, model, by[i]); err != nil {
				return "", err
			}
		}
	}
	return "", nil
}

// cachedReading is the reader's stored answer for key (figure_reads), when caching is on.
func (e *Engine) cachedReading(ctx context.Context, key, model string, on bool) ([]analysis.Label, bool, error) {
	if !on || model == "" {
		return nil, false, nil
	}
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

// cacheReading stores a part's complete answer, under the task that read it (deleted with it) and
// the documents its quotes are from (deleted with any of them).
func (e *Engine) cacheReading(ctx context.Context, pt *part, taskID, scope, model string, labels []analysis.Label) error {
	docs := []string{}
	for _, p := range pt.in {
		if p.DocumentID != nil && store.ValidID(*p.DocumentID) && !slices.Contains(docs, *p.DocumentID) {
			docs = append(docs, *p.DocumentID)
		}
	}
	data, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	return e.storeRead(ctx, pt.key, data, model, taskID, scope, docs)
}

// storeRead stores one cached answer (figure_reads) under the task that asked and the documents it
// read (deleted with the task or any of them).
func (e *Engine) storeRead(ctx context.Context, key string, data []byte, model, taskID, scope string, docs []string) error {
	docs = slices.DeleteFunc(slices.Clone(docs), func(d string) bool { return !store.ValidID(d) })
	_, err := e.db.ExecContext(ctx, `INSERT INTO figure_reads (key, labels, model, task_id, matter_number, document_ids) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (key) DO NOTHING`, key, data, model, taskID, strings.TrimPrefix(scope, "matter:"), pq.Array(docs))
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
					return &analysis.Reading{Kind: analysis.KindDate, Text: v.Value, Party: r.Party, Measures: r.Measures, Period: r.Period}, true, false, ""
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
		why := cmpStr(vr.Unclear, vr.Why, soft.Why)
		if why == "" { // both clear, at two scales (lab-r4 r5 H1): the same figure only if a lawyer says so
			why = "its quote states " + cmpStr(soft.ScaleFrom != "", soft.Text+" "+soft.ScaleFrom, soft.Text) + ", at another scale than the value"
		}
		vr.Unclear, vr.Why = true, why
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
