// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
)

// execute runs (or resumes) a claimed task to its next stop (README § Long-running runs). Resume:
// every step that was in flight when a process died is charged at its reserved grant ("lost") and
// dropped, so it is issued again; the committed steps are replayed, never re-run.
func (e *Engine) execute(ctx context.Context, r *run) error {
	sp, err := newSpecs(r.t.Specs) // the task's snapshot: the same on every resume
	if err != nil {
		return e.fail(ctx, r, "", fmt.Errorf("engine: the task's spec snapshot does not load: %w", err))
	}
	r.specs, ctx = sp, withSpecs(ctx, sp)
	var spent agent.Usage
	var replay []agent.Step
	err = e.st.InTx(ctx, func(tx *sql.Tx) error {
		var lost []byte
		if err := tx.QueryRowContext(ctx, `SELECT lost FROM tasks WHERE id = $1 FOR UPDATE`, r.t.ID).Scan(&lost); err != nil {
			return err
		}
		var l agent.Usage
		_ = json.Unmarshal(lost, &l)
		q, err := tx.QueryContext(ctx, `SELECT step, kind, tool, status, coalesce(output, 'null'), grant_held, usage, model, final, note FROM task_steps WHERE task_id = $1 ORDER BY step`, r.t.ID)
		if err != nil {
			return err
		}
		defer q.Close()
		inflight := false
		for q.Next() {
			var s agent.Step
			var out, grant, usage []byte
			if err := q.Scan(&s.N, &s.Kind, &s.Tool, &s.Status, &out, &grant, &usage, &s.Model, &s.Final, &s.Note); err != nil {
				return err
			}
			s.Output = out
			_ = json.Unmarshal(grant, &s.Grant)
			_ = json.Unmarshal(usage, &s.Usage)
			if s.Status == "started" {
				l.Add(agent.Usage{Calls: s.Grant.Calls, USD: s.Grant.USD})
				inflight = true
				continue
			}
			spent.Add(s.Usage)
			replay = append(replay, s)
		}
		if err := q.Err(); err != nil || !inflight {
			spent.Add(l)
			return err
		}
		spent.Add(l)
		data, _ := json.Marshal(l)
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_steps WHERE task_id = $1 AND status = 'started'`, r.t.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET lost = $2 WHERE id = $1`, r.t.ID, data)
		return err
	})
	if err != nil {
		return err
	}
	r.b = agent.NewBudget(e.cfg.BudgetCalls, e.cfg.BudgetUSD, 0)
	r.b.Charge(spent)
	cs, err := e.classSpend(ctx, r.t.ID)
	if err != nil {
		return err
	}
	covered, err := e.covered(ctx, r.t.ID)
	if err != nil {
		return err
	}
	r.keep(Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD), cs, covered)
	rt, err := e.runtime(r)
	if err != nil {
		return e.fail(ctx, r, "internal", err)
	}
	brief, err := e.brief(ctx, r.t)
	if errors.Is(err, docs.ErrNotFound) { // deleted since submission: fail, never re-claim forever
		return e.fail(ctx, r, "", fmt.Errorf("%w: a document of this task no longer exists (deleted after the task was submitted)", ErrBadTask))
	} else if err != nil {
		return err
	}
	lead := agent.Agent{Name: "lead", Role: model.Lead, Model: r.t.LeadModel, Prompt: prompt(ctx, "lead"), Visible: leadVisible(r.sp()), MaxTurns: r.t.MaxSteps, Replay: replay}
	call := agent.Call{TaskID: r.t.ID, DocumentIDs: r.t.DocumentIDs, Input: brief}
	res, err := rt.Run(ctx, lead, call, r.b)
	switch {
	case err == nil && res.Final:
		return e.finished(ctx, r)
	case r.collapse != "":
		return e.fail(ctx, r, r.collapse, errCollapse)
	case ctx.Err() != nil || errors.Is(err, errLease):
		return err // stopped, cancelled or taken over: the run's state is as committed
	case errors.Is(err, errHeld): // a hook's approve opened a gate: the run waits for the lawyer (Decide resumes it)
		_, err = e.db.ExecContext(ctx, `UPDATE tasks SET status = 'awaiting_gate', stop_reason = 'awaiting_gate', lease_owner = NULL, lease_until = NULL, updated_at = now()
			WHERE id = $1 AND lease_owner = $2 AND status = 'running'`, r.t.ID, e.owner)
		return err
	case errors.Is(err, agent.ErrBudget), errors.Is(err, agent.ErrTurnCap), errors.Is(err, errMaxSteps):
		r.forced = map[bool]string{true: "max_steps", false: "budget_exhausted"}[errors.Is(err, errMaxSteps) || errors.Is(err, agent.ErrTurnCap)]
		var last int
		if err := e.db.QueryRowContext(ctx, `SELECT coalesce(max(step), 0) FROM task_steps WHERE task_id = $1`, r.t.ID).Scan(&last); err != nil {
			return err
		}
		if last, err = e.coverPass(ctx, rt, call, r, last); ctx.Err() != nil || errors.Is(err, errLease) {
			return err
		} else if err != nil {
			return e.fail(ctx, r, "", err)
		}
		if last, err = e.review(ctx, rt, lead, call, r, last); ctx.Err() != nil || errors.Is(err, errLease) {
			return err
		} else if err != nil {
			return e.fail(ctx, r, "", err)
		}
		res, err = rt.Invoke(ctx, lead, call, "finish", json.RawMessage(`{"op":"submit","limitations":null}`), r.b, false, last+1)
		if err == nil && res.Final {
			return e.finished(ctx, r)
		}
		if ctx.Err() != nil || errors.Is(err, errLease) {
			return err
		}
		return e.fail(ctx, r, "", cmpErr(err, errors.New("engine: the forced finish was not accepted")))
	}
	return e.fail(ctx, r, "", err)
}

// review runs the committee from the cycle the budget kept back when a stopped run's draft has
// changed since its last review (or was never reviewed), so the delivered draft is always the one
// the critics read; their open findings reach Limitations through finish. It returns the last step.
func (e *Engine) review(ctx context.Context, rt *agent.Runtime, lead agent.Agent, call agent.Call, r *run, last int) (int, error) {
	secs, err := load[Section](ctx, e, r.t.ID, kSection)
	cyc, cerr := load[Cycle](ctx, e, r.t.ID, kCycle)
	if err = cmpErr(err, cerr); err != nil || len(secs) == 0 || len(cyc) > 0 && cyc[len(cyc)-1].Version == version(secs) {
		return last, err
	}
	_, err = rt.Invoke(ctx, lead, call, "committee", json.RawMessage(`{}`), r.b, false, last+1)
	if ctx.Err() != nil || errors.Is(err, errLease) || modelOrJournal(err) {
		return last, cmpErr(ctx.Err(), err)
	}
	return last + 1, nil // a refused or failed committee is in the step log; finish discloses the unreviewed draft
}

// brief is the lead's first message: the request, the matter's documents and the packs.
func (e *Engine) brief(ctx context.Context, t Task) (json.RawMessage, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Request from the lawyer:\n%s\n\nJurisdiction: %s. Matter: %s. Budget: %d model calls or $%.2f, whichever runs out first; "+
		"evidence may spend half, and Go keeps back the drafting share, one committee review and finish.\n\nThe matter's documents (use these ids):\n",
		t.Description, cmpStr(t.Jurisdiction != "", t.Jurisdiction, "not stated"), t.MatterNumber, e.cfg.BudgetCalls, e.cfg.BudgetUSD)
	for _, id := range t.DocumentIDs {
		d, err := e.docs.Get(ctx, t.DocumentIDs, id)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "- %s: %s (%d sections)\n", d.ID, d.Name, d.Sections)
	}
	for _, p := range packsFor(ctx, t.Jurisdiction, "") {
		fmt.Fprintf(&b, "\nPack %s (%s):\n%s\nChecklist:\n- %s\n", p.Code, p.Status, p.Body, strings.Join(p.Checklist, "\n- "))
	}
	if idx := specsOf(ctx).skillIndex(); idx != "" { // names and descriptions only: use_skill reads one (progressive disclosure)
		fmt.Fprintf(&b, "\nSkills (process guidance, not evidence; read one with use_skill when it fits the task):\n%s", truncate(idx, maxSkillIndex))
	}
	return json.Marshal(b.String())
}

// finished records an accepted finish: the deliverable, and the gate the run now waits at.
func (e *Engine) finished(ctx context.Context, r *run) error {
	var out string
	if rs, err := e.rows(ctx, r.t.ID, kNote); err == nil {
		for _, x := range rs {
			if x.Key == "deliverable" {
				_ = json.Unmarshal(x.Data, &out)
			}
		}
	}
	gs, err := load[Gate](ctx, e, r.t.ID, kGate)
	if err != nil {
		return err
	}
	stop := cmpStr(r.forced != "", r.forced, "finished")
	for _, g := range gs {
		if (g.Kind == "finding" || g.Kind == "hook") && g.Decision == "" {
			stop = "awaiting_gate"
		}
	}
	_, err = e.db.ExecContext(ctx, `UPDATE tasks SET status = 'awaiting_gate', stop_reason = $3, output = $4, lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND lease_owner = $2 AND status = 'running'`, r.t.ID, e.owner, stop, out)
	return err
}

// fail ends a run as failed (G4 or G6) with a fixed message: never "no finding", never a model's
// or database's own words.
func (e *Engine) fail(ctx context.Context, r *run, alert string, cause error) error {
	_, kind, msg := ErrorType(cause)
	text := kind + ": " + msg
	if alert != "" {
		text = alert
	}
	_, err := e.db.ExecContext(context.WithoutCancel(ctx), `UPDATE tasks SET status = 'failed', stop_reason = 'failed', error = $3,
		grounding_alert = coalesce(nullif($4, ''), grounding_alert), lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND lease_owner = $2 AND status = 'running'`, r.t.ID, e.owner, text, alert)
	if kind == "internal" {
		e.log.Error("engine: task failed", "task", r.t.ID, "err", cause)
	}
	return err
}

// ErrorType maps a failure to its HTTP status, errorType and fixed user message (G6): model
// failures by kind, BigLaw's own refusals by kind; anything else is an internal error.
func ErrorType(err error) (int, string, string) {
	for _, c := range []struct {
		err       error
		code      int
		kind, msg string
	}{
		{model.ErrNotConfigured, 503, "model_not_configured", "no model is configured for this role (set BIGLAW_MODEL_URL and BIGLAW_MODELS)"},
		{model.ErrRateLimited, 503, "model_rate_limited", "the model endpoint is rate limiting BigLaw; try again later"},
		{model.ErrTimeout, 504, "model_timeout", "the model endpoint did not answer in time"},
		{model.ErrNoContent, 422, "no_content", "the model returned no usable answer"},
		{model.ErrUnavailable, 502, "model_unavailable", "the model endpoint failed"},
		{errCollapse, 422, "grounding_collapse", "too many findings failed quote verification, so the task failed rather than deliver (G4)"},
		{ErrBadTask, 400, "bad_request", ""},
		{ErrNotFound, 404, "not_found", "not found"},
		{ErrConflict, 409, "conflict", ""},
		{ErrForbidden, 403, "forbidden", ""},
		{ErrInvalid, 422, "invalid", ""},
		{context.Canceled, 499, "cancelled", "the request was cancelled"},
	} {
		if errors.Is(err, c.err) {
			if c.msg == "" {
				return c.code, c.kind, strings.TrimPrefix(err.Error(), "engine: ")
			}
			if me := (*model.Error)(nil); errors.As(err, &me) && strings.Contains(me.Detail, "(") { // the provider's error type and message, redacted (model.providerError: bench7's "credit balance is too low" was only "HTTP 400")
				return c.code, c.kind, c.msg + ": " + me.Detail
			}
			return c.code, c.kind, c.msg
		}
	}
	return 500, "internal", "internal error (logged)"
}
