// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
)

// The cross-document join's verdicts (lab-r4 r7, owner decision: whether two figures are one
// quantity is the agent's, never a rule in Go). analysis.Candidates proposes the pairs; judge_figures
// (specs/agents/judge_figures.md, critic role, its tool figure_context) reads both figures' quotes,
// claims, typed entities and readings and the lead's aliases, and answers yes, no or unclear per pair
// with a reason quoting the passages. Go checks every verdict (analysis.CheckVerdicts: a verdict for
// a pair it did not propose is rejected, so a "yes" on two non-spans or two kinds asserts nothing;
// a "yes" without a reason is unclear) and keeps it in the task's ledger (kVerdict) under the pair's
// content and the run's aliases, so finish and the coverage pass read the same verdicts the join
// tool got. The judge is called like the reader: batched (analysis.JudgePairs pairs, maxJudgeBytes of
// text a call), on one model picked before the calls, metered and journaled through Invoke out of
// its caller's grant, and its yes and no answers are cached across runs of a matter (figure_reads,
// under the judge's own spec hash: an edit to its spec or prompt judges every pair again).

const (
	maxJudgeBytes = 32_000
	maxJudgeQuote = 4_000 // a quote over it is sent cut around its value
)

// judgeSide is one figure of a pair as the judge is sent it (no fid: the cache is across runs).
type judgeSide struct {
	Ref        string      `json:"ref"`
	DocumentID string      `json:"documentId"`
	Value      string      `json:"value"`
	ValueText  string      `json:"valueText"` // the figure's digits as its quote writes them, marked there ⟦…⟧ (bench7)
	Entity     *string     `json:"entity"`
	Metric     *string     `json:"metric"`
	Claim      string      `json:"claim"`
	Quotes     []string    `json:"quotes"`
	Reading    judgeReader `json:"reading"`
}

type judgeReader struct {
	Kind         string  `json:"kind"`
	Currency     string  `json:"currency"`
	Amount       string  `json:"amount"`
	Scale        string  `json:"scale"`
	Party        *string `json:"party"`
	Counterparty *string `json:"counterparty"`
	Measures     *string `json:"measures"`
	Period       *string `json:"period"`
	TypedEntity  *string `json:"typedEntity"`
}

type judgePair struct {
	ID   string    `json:"id"`
	Kind string    `json:"kind"`
	A    judgeSide `json:"a"`
	B    judgeSide `json:"b"`
}

func sideIn(s analysis.Side) judgeSide {
	qs := []string{}
	for i, q := range s.Quotes[:min(len(s.Quotes), 12)] {
		at := s.Value
		if m := markSpan(q, i, s.Fig.Stated); m != q { // the value's own span marked, so the judge reads that figure, not the passage's other one
			q, at = m, "⟦"
		}
		qs = append(qs, cutAround(q, at, maxJudgeQuote))
	}
	r := s.Fig
	return judgeSide{Ref: s.Ref, DocumentID: s.DocumentID, Value: s.Value, ValueText: r.Text, Entity: strp(s.Entity), Metric: strp(s.Metric), Claim: cutAround(s.Claim, s.Value, maxJudgeQuote), Quotes: qs,
		Reading: judgeReader{Kind: r.Kind, Currency: r.Currency, Amount: r.Amount, Scale: cmpStr(r.Scale == "", "1", r.Scale), Party: strp(r.Party), Counterparty: strp(r.Counterparty),
			Measures: strp(r.Measures), Period: strp(r.Period), TypedEntity: strp(r.TypedEntity)}}
}

// markSpan is the i-th quote of a side with the span admission matched its value to (stated, "q2.s1":
// the second verified quote's first span the reader is sent, quotePassage) marked ⟦…⟧; the quote as
// it is when the span is in another quote or unknown.
func markSpan(quote string, i int, stated string) string {
	q, sp, ok := strings.Cut(strings.TrimPrefix(stated, "q"), ".s")
	n, err1 := strconv.Atoi(q)
	k, err2 := strconv.Atoi(sp)
	if !ok || err1 != nil || err2 != nil || n != i+1 {
		return quote
	}
	spans := quotePassage("q", quote).Spans
	if k < 1 || k > len(spans) {
		return quote
	}
	x := spans[k-1]
	return quote[:x.Start] + "⟦" + quote[x.Start:x.End] + "⟧" + quote[x.End:]
}

// cutAround is s, or at most n bytes of it around value's first occurrence (else its start), cut at
// rune boundaries.
func cutAround(s, value string, n int) string {
	if len(s) <= n {
		return s
	}
	at := max(0, strings.Index(s, value))
	lo := max(0, at-n/2)
	hi := min(len(s), lo+n)
	for lo > 0 && !utf8Start(s[lo]) {
		lo--
	}
	for hi < len(s) && !utf8Start(s[hi]) {
		hi--
	}
	return s[lo:hi]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func pairIn(c analysis.Candidate) judgePair {
	return judgePair{ID: c.ID, Kind: c.Kind, A: sideIn(c.A), B: sideIn(c.B)}
}

// judgeKey is a pair's key in the cross-run cache: the judge's spec, the model, the matter and the task's documents (a verdict's reason may
// quote a document only that set holds: lab-r4 r8 L2), the aliases that name either side (r8 L4: an
// alias of another party judges nothing again) and the pair's content (both sides without their
// refs, in a fixed order), so a pair restated by other findings is the same question.
func judgeKey(specHash, model, scope string, docs []string, aliases []analysis.Alias, x analysis.Candidate) string {
	p := pairIn(x)
	a, b := p.A, p.B
	a.Ref, b.Ref = "", ""
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(jb) < string(ja) {
		ja, jb = jb, ja
	}
	docs = slices.Sorted(slices.Values(docs))
	data, _ := json.Marshal(map[string]any{"spec": specHash, "model": model, "scope": scope, "docs": docs, "aliases": pairAliases(aliases, x), "kind": p.Kind,
		"a": json.RawMessage(ja), "b": json.RawMessage(jb)})
	sum := sha256.Sum256(data)
	return "judge:" + hex.EncodeToString(sum[:])
}

// pairAliases are the aliases that name one of a pair's sides: its typed entity, or the reader's party
// or counterparty for it (case and spacing aside).
func pairAliases(aliases []analysis.Alias, cands ...analysis.Candidate) []analysis.Alias {
	fold := func(x string) string { return strings.ToLower(strings.Join(strings.Fields(x), " ")) }
	names := map[string]bool{}
	for _, c := range cands {
		for _, s := range []analysis.Side{c.A, c.B} {
			for _, n := range []string{s.Entity, s.Fig.Party, s.Fig.Counterparty} {
				if n = fold(n); n != "" {
					names[n] = true
				}
			}
		}
	}
	out := []analysis.Alias{}
	for _, a := range aliases {
		if names[fold(a.Name)] || slices.ContainsFunc(a.Aliases, func(o string) bool { return names[fold(o)] }) {
			out = append(out, a)
		}
	}
	return out
}

// ledgerKey is a pair's verdict's key in the task's ledger: the two values (canons) in their two
// documents, whichever findings state them (so a verdict holds as later findings restate a pair:
// lab-r4 r8 M1), and the aliases naming either side.
func ledgerKey(aliases []analysis.Alias, x analysis.Candidate) string {
	sides := []string{x.A.Canon + "@" + x.A.DocumentID, x.B.Canon + "@" + x.B.DocumentID}
	slices.Sort(sides)
	data, _ := json.Marshal(map[string]any{"sides": sides, "aliases": pairAliases(aliases, x)})
	sum := sha256.Sum256(data)
	return "pair:" + hex.EncodeToString(sum[:])
}

// verdictSet is the task's verdicts (its ledger) by ledgerKey, and the run's aliases.
func (e *Engine) verdictSet(ctx context.Context, taskID string) (map[string]analysis.Verdict, []analysis.Alias, error) {
	aliases, err := load[analysis.Alias](ctx, e, taskID, kAlias)
	rows, rerr := e.rows(ctx, taskID, kVerdict)
	if err = cmp.Or(err, rerr); err != nil {
		return nil, nil, err
	}
	have := map[string]analysis.Verdict{}
	for _, r := range rows {
		var v analysis.Verdict
		if json.Unmarshal(r.Data, &v) == nil {
			have[r.Key] = v
		}
	}
	return have, aliases, nil
}

// verdicts are the task's verdicts on cands (its ledger), by candidate id.
func (e *Engine) verdicts(ctx context.Context, taskID string, cands []analysis.Candidate) (map[string]analysis.Verdict, error) {
	have, aliases, err := e.verdictSet(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := map[string]analysis.Verdict{}
	for _, c := range cands {
		if v, ok := have[ledgerKey(aliases, c)]; ok {
			v.Pair = c.ID
			out[c.ID] = v
		}
	}
	return out, nil
}

// judgedSet reports which pairs the task has a verdict for (analysis.Candidates keeps them first).
func (e *Engine) judgedSet(ctx context.Context, taskID string) (func(analysis.Candidate) bool, error) {
	have, aliases, err := e.verdictSet(ctx, taskID)
	return func(c analysis.Candidate) bool { _, ok := have[ledgerKey(aliases, c)]; return ok }, err
}

// judge has judge_figures decide every candidate the task has no verdict for: from the cross-run
// cache, else in calls out of b, on one model. Every verdict is kept in the task's ledger; a pair
// it could not judge (no model, the grant could not fund it, a call failed or answered off its
// schema) has none. Only a stop or a journal or database failure is an error.
func (e *Engine) judge(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller string, cands []analysis.Candidate) (map[string]analysis.Verdict, error) {
	out, err := e.verdicts(ctx, c.TaskID, cands)
	aliases, aerr := load[analysis.Alias](ctx, e, c.TaskID, kAlias)
	scope, serr := e.readScope(ctx, c.TaskID)
	if err = cmp.Or(err, aerr, serr); err != nil {
		return nil, err
	}
	model, models := labelModels(ctx, rt, "judge_figures")
	jh := specsOf(ctx).agentHash("judge_figures")
	keep := func(x analysis.Candidate, v analysis.Verdict) error {
		v.Pair = x.ID
		out[x.ID] = v
		return e.write(ctx, c.TaskID, kVerdict, ledgerKey(aliases, x), v, true)
	}
	var todo []analysis.Candidate
	for _, x := range cands {
		if _, ok := out[x.ID]; ok {
			continue
		}
		key := func(m string) string { return judgeKey(jh, m, scope, c.DocumentIDs, aliases, x) }
		v, ok, err := cachedAnswer(ctx, e, models, scope != "", key, func(v analysis.Verdict) bool { return v.SameQuantity == "yes" || v.SameQuantity == "no" })
		if err != nil {
			return nil, err
		}
		if !ok {
			todo = append(todo, x)
		} else if err := keep(x, v); err != nil {
			return nil, err
		}
	}
	if model == "" {
		return out, nil
	}
	for len(todo) > 0 {
		var chunk []analysis.Candidate
		bytes := 0
		for len(todo) > 0 && len(chunk) < analysis.JudgePairs {
			p, _ := json.Marshal(pairIn(todo[0]))
			if len(chunk) > 0 && bytes+len(p) > maxJudgeBytes {
				break
			}
			chunk, bytes, todo = append(chunk, todo[0]), bytes+len(p), todo[1:]
		}
		got, stop, err := e.judgeChunk(ctx, rt, c, b, caller, model, aliases, chunk)
		if err != nil {
			return nil, err
		}
		for _, x := range chunk {
			v, ok := got[x.ID]
			if !ok {
				continue
			}
			if err := keep(x, v); err != nil {
				return nil, err
			}
			if v.Pair = ""; v.SameQuantity != "unclear" { // a yes or no, without the run's pair id
				if err := e.storeRead(ctx, scope != "", judgeKey(jh, model, scope, c.DocumentIDs, aliases, x), v, model, c.TaskID, scope, []string{x.A.DocumentID, x.B.DocumentID}); err != nil {
					return nil, err
				}
			}
		}
		if stop { // the grant cannot fund another call: the rest stay unjudged
			break
		}
	}
	return out, nil
}

// judgeChunk is one judge call over chunk: its checked verdicts (analysis.CheckVerdicts: a yes or no
// whose reason does not quote both passages is unclear), and whether the grant is spent.
func (e *Engine) judgeChunk(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller, model string, aliases []analysis.Alias, chunk []analysis.Candidate) (map[string]analysis.Verdict, bool, error) {
	ps := make([]judgePair, len(chunk))
	for i, x := range chunk {
		ps[i] = pairIn(x)
	}
	input, _ := json.Marshal(map[string]any{"pairs": ps, "aliases": pairAliases(aliases, chunk...)})
	res, err := labelCall(ctx, rt, c, b, caller, "judge_figures", model, input)
	switch why, fatal := labelFailed(ctx, err); {
	case fatal != nil:
		return nil, true, fatal
	case why == whyUnfunded:
		return nil, true, nil
	case why != "":
		e.log.Warn("figure judge failed", "task", c.TaskID, "err", err)
		return nil, false, nil
	}
	var out struct{ Verdicts []analysis.Verdict }
	if json.Unmarshal(res.Output, &out) != nil {
		return nil, false, nil
	}
	vs, notes := analysis.CheckVerdicts(chunk, out.Verdicts)
	for _, n := range notes {
		e.log.Warn("figure judge", "task", c.TaskID, "note", n)
	}
	return vs, false, nil
}

// joined is the cross-document join as the task's verdicts decide it: the conflicts ("yes"), the
// pairs judged unclear, how many of the pairs a finish judges first (judgeLimit) have no verdict, and
// how many more pairs of differing figures were never put to the judge.
type joined struct {
	all, open []analysis.Conflict
	unclear   []analysis.Candidate
	unjudged  int
	beyond    int // pairs never put to the judge
	uncovered int // figures none of whose counterparts were
}

// joinCands are the join's candidates over usable findings: those judged already, then up to limit
// in analysis.Candidates' order.
func joinCands(use []Finding, limit int, judged func(analysis.Candidate) bool) ([]analysis.Candidate, int, int) {
	an := make([]analysis.Finding, 0, len(use))
	for _, f := range use {
		an = append(an, f.Finding)
	}
	return analysis.CandidatesCover(an, limit, judged)
}

// judgeCalls are the calls a grant of calls gives the figure judge (at least one when it has two).
func judgeCalls(calls int) int { return min(max(calls-1, 0), max(1, int(judgeShare*float64(calls)))) }

// judgeLimit is how many pairs a finish of this engine's budget judges: the pairs a run's join
// discloses as unjudged are counted among those.
func (e *Engine) judgeLimit() int {
	a := Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD)
	return analysis.CandidateLimit(judgeCalls(1 + a.Cover + a.Cycle))
}

// judgeJoin judges the task's candidates out of b (finish's and the coverage pass's own join), as
// many as its calls fund; its usage is b's.
func (e *Engine) judgeJoin(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller string) error {
	use, err := e.usable(ctx, c.TaskID)
	judged, jerr := e.judgedSet(ctx, c.TaskID)
	if err = cmp.Or(err, jerr); err != nil {
		return err
	}
	calls, _ := b.Remaining()
	if cands, _, _ := joinCands(use, analysis.CandidateLimit(calls), judged); len(cands) > 0 {
		_, err = e.judge(ctx, rt, c, b, caller, cands)
	}
	return err
}

// pendingPairs is how many of the task's first limit candidates have no verdict yet: what a judge
// call would be granted for.
func (e *Engine) pendingPairs(ctx context.Context, taskID string, limit int) (int, error) {
	use, err := e.usable(ctx, taskID)
	judged, jerr := e.judgedSet(ctx, taskID)
	if err = cmp.Or(err, jerr); err != nil {
		return 0, err
	}
	cands, _, _ := joinCands(use, limit, judged)
	n := 0
	for _, c := range cands {
		if !judged(c) {
			n++
		}
	}
	return n, nil
}

// lines are the join's disclosures: possible conflicts the judge could not decide (low confidence,
// for a lawyer), and pairs no verdict covers.
func (j joined) lines() []string {
	var out []string
	if len(j.unclear) > 0 {
		var ps []string
		for _, x := range j.unclear[:min(len(j.unclear), 20)] {
			ps = append(ps, fmt.Sprintf("[%s] %s vs [%s] %s", x.A.FID, x.A.Value, x.B.FID, x.B.Value))
		}
		out = append(out, fmt.Sprintf("Figures (low confidence): the figure judge could not tell whether %d %s two documents state differently %s one quantity, so %s not reported as %s: %s%s; a lawyer should compare them.",
			len(j.unclear), plural(len(j.unclear), "pair of figures", "pairs of figures"), plural(len(j.unclear), "is", "are"), plural(len(j.unclear), "it is", "they are"), plural(len(j.unclear), "a conflict", "conflicts"),
			strings.Join(ps, "; "), cmpStr(len(j.unclear) > 20, fmt.Sprintf(" and %d more", len(j.unclear)-20), "")))
	}
	if j.unjudged > 0 {
		out = append(out, fmt.Sprintf("Figures: %d %s of figures that two documents state differently, among those the join puts to the figure judge first (each figure's most alike and closest counterparts), %s not judged (the budget could not fund the judge, or its answer was unusable), so a conflict among them may be missing.",
			j.unjudged, plural(j.unjudged, "pair", "pairs"), plural(j.unjudged, "was", "were")))
	}
	if j.uncovered > 0 { // lab-r4 r9 residual a: the figures left out, not the pairs
		out = append(out, fmt.Sprintf("Figures: %d %s with a differing figure of the same kind in another document had none of %s counterparts put to the figure judge (%d %s never asked), so a conflict involving %s may be missing.",
			j.uncovered, plural(j.uncovered, "figure", "figures"), plural(j.uncovered, "its", "their"), j.beyond, plural(j.beyond, "pair was", "pairs were"), plural(j.uncovered, "it", "them")))
	}
	return out
}

// judging is the engine as the analysis tools' source: its findings, and join_discrepancies' pairs
// judged out of the tool's grant.
type judging struct {
	*Engine
	rt *agent.Runtime
}

func (j judging) Judge(ctx context.Context, c agent.Call, cands []analysis.Candidate) (map[string]analysis.Verdict, agent.Usage, error) {
	b := agent.NewBudget(c.Budget.Calls, c.Budget.USD, 0)
	vs, err := j.judge(ctx, j.rt, c, b, "join_discrepancies", cands)
	return vs, b.Spent(), err
}

// Judged reports the pairs the task has verdicts for: join_discrepancies keeps them ahead of its cap.
func (j judging) Judged(ctx context.Context, c agent.Call) (func(analysis.Candidate) bool, error) {
	return j.judgedSet(ctx, c.TaskID)
}
