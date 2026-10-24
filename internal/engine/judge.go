// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
// tool got. The judge is called like the reader: batched (maxJudgePairs pairs, maxJudgeBytes of
// text a call), on one model picked before the calls, metered and journaled through Invoke out of
// its caller's grant, and its yes and no answers are cached across runs of a matter (figure_reads,
// under the judge's own spec hash: an edit to its spec or prompt judges every pair again).

const (
	maxJudgePairs = 15
	maxJudgeBytes = 32_000
	maxJudgeQuote = 4_000 // a quote over it is sent cut around its value
)

var judgeSpec = specHash(specFS, []string{"specs/agents/judge_figures.json", "specs/agents/judge_figures.md", "specs/figure_context.json"})

// judgeSide is one figure of a pair as the judge is sent it (no fid: the cache is across runs).
type judgeSide struct {
	Ref        string      `json:"ref"`
	DocumentID string      `json:"documentId"`
	Value      string      `json:"value"`
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
	for _, q := range s.Quotes[:min(len(s.Quotes), 12)] {
		qs = append(qs, cutAround(q, s.Value, maxJudgeQuote))
	}
	r := s.Fig
	return judgeSide{Ref: s.Ref, DocumentID: s.DocumentID, Value: s.Value, Entity: strp(s.Entity), Metric: strp(s.Metric), Claim: cutAround(s.Claim, s.Value, maxJudgeQuote), Quotes: qs,
		Reading: judgeReader{Kind: r.Kind, Currency: r.Currency, Amount: r.Amount, Scale: cmpStr(r.Scale == "", "1", r.Scale), Party: strp(r.Party), Counterparty: strp(r.Counterparty),
			Measures: strp(r.Measures), Period: strp(r.Period), TypedEntity: strp(r.TypedEntity)}}
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

// judgeKey is a pair's key: the judge's spec, the model ("" for the task's ledger, which holds the
// run's verdict whichever model gave it), the matter, the aliases and the pair's content (both sides
// without their refs, in a fixed order), so a pair restated by other findings is the same question.
func judgeKey(model, scope string, aliases []analysis.Alias, p judgePair) string {
	a, b := p.A, p.B
	a.Ref, b.Ref = "", ""
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(jb) < string(ja) {
		ja, jb = jb, ja
	}
	data, _ := json.Marshal(map[string]any{"spec": judgeSpec, "model": model, "scope": scope, "aliases": aliases, "kind": p.Kind, "a": json.RawMessage(ja), "b": json.RawMessage(jb)})
	sum := sha256.Sum256(data)
	return "judge:" + hex.EncodeToString(sum[:])
}

// verdicts are the task's verdicts on cands (its ledger), by candidate id.
func (e *Engine) verdicts(ctx context.Context, taskID string, cands []analysis.Candidate) (map[string]analysis.Verdict, error) {
	aliases, err := load[analysis.Alias](ctx, e, taskID, kAlias)
	rows, rerr := e.rows(ctx, taskID, kVerdict)
	if err = cmpErr(err, rerr); err != nil {
		return nil, err
	}
	have := map[string]analysis.Verdict{}
	for _, r := range rows {
		var v analysis.Verdict
		if json.Unmarshal(r.Data, &v) == nil {
			have[r.Key] = v
		}
	}
	out := map[string]analysis.Verdict{}
	for _, c := range cands {
		if v, ok := have[judgeKey("", "", aliases, pairIn(c))]; ok {
			v.Pair = c.ID
			out[c.ID] = v
		}
	}
	return out, nil
}

// judge has judge_figures decide every candidate the task has no verdict for: from the cross-run
// cache, else in calls out of b, on one model. Every verdict is kept in the task's ledger; a pair
// it could not judge (no model, the grant could not fund it, a call failed or answered off its
// schema) has none. Only a stop or a journal or database failure is an error.
func (e *Engine) judge(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller string, cands []analysis.Candidate) (map[string]analysis.Verdict, error) {
	out, err := e.verdicts(ctx, c.TaskID, cands)
	aliases, aerr := load[analysis.Alias](ctx, e, c.TaskID, kAlias)
	scope, serr := e.readScope(ctx, c.TaskID)
	if err = cmpErr(err, aerr, serr); err != nil {
		return nil, err
	}
	model, models := "", []string(nil)
	if rt != nil && rt.Chat != nil && rt.Reg != nil {
		models = rt.Candidates(spec("judge_figures").Role)
		model = rt.Bandit.PickModel(ctx, "judge_figures", models)
	}
	models = append([]string{model}, slices.DeleteFunc(models, func(m string) bool { return m == model })...)
	keep := func(x analysis.Candidate, v analysis.Verdict) error {
		v.Pair = x.ID
		out[x.ID] = v
		return e.write(ctx, c.TaskID, kVerdict, judgeKey("", "", aliases, pairIn(x)), v, true)
	}
	var todo []analysis.Candidate
next:
	for _, x := range cands {
		if _, ok := out[x.ID]; ok {
			continue
		}
		for _, m := range models {
			v, ok, err := e.cachedVerdict(ctx, judgeKey(m, scope, aliases, pairIn(x)), m, scope != "")
			if err != nil {
				return nil, err
			}
			if ok {
				if err := keep(x, v); err != nil {
					return nil, err
				}
				continue next
			}
		}
		todo = append(todo, x)
	}
	if model == "" {
		return out, nil
	}
	for len(todo) > 0 {
		var chunk []analysis.Candidate
		bytes := 0
		for len(todo) > 0 && len(chunk) < maxJudgePairs {
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
			if v.SameQuantity != "unclear" && scope != "" {
				if err := e.cacheVerdict(ctx, judgeKey(model, scope, aliases, pairIn(x)), c.TaskID, scope, model, x, v); err != nil {
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

// judgeChunk is one judge call over chunk: its checked verdicts (a "yes" with no reason is unclear),
// and whether the grant is spent.
func (e *Engine) judgeChunk(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller, model string, aliases []analysis.Alias, chunk []analysis.Candidate) (map[string]analysis.Verdict, bool, error) {
	ps := make([]judgePair, len(chunk))
	for i, x := range chunk {
		ps[i] = pairIn(x)
	}
	input, _ := json.Marshal(map[string]any{"pairs": ps, "aliases": append([]analysis.Alias{}, aliases...)})
	res, err := rt.Invoke(ctx, agent.Agent{Name: caller, Visible: []string{"judge_figures"}}, agent.Call{TaskID: c.TaskID, DocumentIDs: c.DocumentIDs, Model: model}, "judge_figures", input, b, false, 0)
	switch {
	case ctx.Err() != nil:
		return nil, true, ctx.Err()
	case errors.Is(err, agent.ErrJournal):
		return nil, true, err
	case errors.Is(err, agent.ErrBudget):
		return nil, true, nil
	case err != nil:
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
	for id, v := range vs {
		if v.SameQuantity == "yes" && strings.TrimSpace(v.Reason) == "" {
			v.SameQuantity, v.Reason = "unclear", "the judge gave no reason"
			vs[id] = v
		}
	}
	return vs, false, nil
}

// cachedVerdict is the judge's stored verdict for key (figure_reads), when caching is on.
func (e *Engine) cachedVerdict(ctx context.Context, key, model string, on bool) (analysis.Verdict, bool, error) {
	var v analysis.Verdict
	if !on || model == "" {
		return v, false, nil
	}
	var data []byte
	err := e.db.QueryRowContext(ctx, `SELECT labels FROM figure_reads WHERE key = $1`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	} else if err != nil {
		return v, false, err
	}
	if json.Unmarshal(data, &v) != nil || v.SameQuantity != "yes" && v.SameQuantity != "no" {
		return v, false, nil
	}
	return v, true, nil
}

// cacheVerdict stores a yes or no verdict (figure_reads, the reader's table: a verdict's row is keyed
// "judge:…" and holds the verdict where a reading holds labels), under the task that asked and both
// sides' documents.
func (e *Engine) cacheVerdict(ctx context.Context, key, taskID, scope, model string, x analysis.Candidate, v analysis.Verdict) error {
	v.Pair = ""
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return e.storeRead(ctx, key, data, model, taskID, scope, []string{x.A.DocumentID, x.B.DocumentID})
}

// joined is the cross-document join as the task's verdicts decide it: the conflicts ("yes"), the
// pairs judged unclear, and how many the cap or the judge left without a verdict.
type joined struct {
	all, open []analysis.Conflict
	unclear   []analysis.Candidate
	unjudged  int
}

// joinCands are the join's candidates over usable findings.
func joinCands(use []Finding) ([]analysis.Candidate, int) {
	an := make([]analysis.Finding, 0, len(use))
	for _, f := range use {
		an = append(an, f.Finding)
	}
	return analysis.Candidates(an)
}

// judgeJoin judges the task's candidates out of b (finish's and the coverage pass's own join); its
// usage is b's.
func (e *Engine) judgeJoin(ctx context.Context, rt *agent.Runtime, c agent.Call, b *agent.Budget, caller string) error {
	use, err := e.usable(ctx, c.TaskID)
	if cands, _ := joinCands(use); err == nil && len(cands) > 0 {
		_, err = e.judge(ctx, rt, c, b, caller, cands)
	}
	return err
}

// unclearLine and unjudgedLine are the join's disclosures: possible conflicts the judge could not
// decide (low confidence, for a lawyer), and pairs no verdict covers.
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
		out = append(out, fmt.Sprintf("Figures: %d %s of figures that two documents state differently %s not judged (past the join's cap of %d pairs, or the budget could not fund the figure judge), so a conflict among them may be missing.",
			j.unjudged, plural(j.unjudged, "pair", "pairs"), plural(j.unjudged, "was", "were"), analysis.MaxCandidates))
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
