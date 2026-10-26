// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// What the REST API serves (DESIGN § UI contract): a task with its run, gates and findings, its
// step log and its cost ledger; and what a lawyer may do: cancel, approve or reject a gate.

var (
	ErrNotFound = errors.New("engine: not found")
	ErrConflict = errors.New("engine: conflict")
)

type Budget struct {
	Calls     int      `json:"calls"`
	USD       *float64 `json:"usd"` // null when any call is unpriced
	Remaining int      `json:"remaining"`
}

type Run struct {
	Status       string  `json:"status"`
	Step         int     `json:"step"`
	StopReason   *string `json:"stopReason"`
	Budget       Budget  `json:"budget"`
	OpenFindings int     `json:"openFindings"`
}

type FindingView struct {
	FID       string     `json:"fid"`
	IssueID   string     `json:"issueId"`
	Question  string     `json:"question"`
	Claim     string     `json:"claim"`
	Severity  string     `json:"severity"`
	Status    string     `json:"status"` // verified or quarantined
	Citations []Citation `json:"citations"`
	// ValueNotes are the typed values admission dropped from it, and why (lab-r4 r3 M7: shown, never only stored).
	ValueNotes []string `json:"valueNotes"`
}

type TaskView struct {
	Task
	Run            Run           `json:"run"`
	PendingGates   []Gate        `json:"pendingGates"`
	DecidedGates   []Gate        `json:"decidedGates"`
	Findings       []FindingView `json:"findings"`
	GroundingAlert *string       `json:"groundingAlert"`
	Error          *string       `json:"error"`
}

type StepView struct {
	Step    int             `json:"step"`
	Kind    string          `json:"kind"`
	Tool    string          `json:"tool"`
	Status  string          `json:"status"`
	Summary string          `json:"summary"`
	Model   string          `json:"model"`
	Input   json.RawMessage `json:"input"`
	Output  json.RawMessage `json:"output"`
	usage   agent.Usage
}

func (e *Engine) task(ctx context.Context, id string) (Task, error) {
	t, err := scanTask(e.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id::text = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return t, err
}

// Get is a task as the API shows it.
func (e *Engine) Get(ctx context.Context, id string) (TaskView, error) {
	t, err := e.task(ctx, id)
	if err != nil {
		return TaskView{}, err
	}
	return e.view(ctx, t)
}

// List is every task, newest first.
func (e *Engine) List(ctx context.Context) ([]TaskView, error) {
	q, err := e.db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	var ts []Task
	for q.Next() {
		t, err := scanTask(q)
		if err != nil {
			q.Close()
			return nil, err
		}
		ts = append(ts, t)
	}
	q.Close()
	out := []TaskView{}
	for _, t := range ts {
		v, err := e.view(ctx, t)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, q.Err()
}

func (e *Engine) view(ctx context.Context, t Task) (TaskView, error) {
	v := TaskView{Task: t, PendingGates: []Gate{}, DecidedGates: []Gate{}, Findings: []FindingView{}}
	v.Run.Status, v.Run.StopReason = t.Status, t.StopReason
	if t.GroundingAlert != "" {
		v.GroundingAlert = &t.GroundingAlert
	}
	if t.Error != "" {
		v.Error = &t.Error
	}
	steps, err := e.steps(ctx, t.ID, 0, 0)
	issues, ierr := load[Issue](ctx, e, t.ID, kIssue)
	fs, ferr := load[Finding](ctx, e, t.ID, kFinding)
	gs, gerr := load[Gate](ctx, e, t.ID, kGate)
	crit, cerr := load[Critic](ctx, e, t.ID, kCritic)
	if err = cmpErr(err, ierr, ferr, gerr, cerr); err != nil {
		return v, err
	}
	var u agent.Usage
	for _, s := range steps {
		u.Add(s.usage)
		v.Run.Step = s.Step
	}
	var lost agent.Usage
	_ = e.db.QueryRowContext(ctx, `SELECT lost FROM tasks WHERE id = $1`, t.ID).Scan(jsonScan{&lost})
	u.Add(lost)
	v.Run.Budget = Budget{Calls: u.Calls, Remaining: max(0, e.cfg.BudgetCalls-u.Calls)}
	if u.Unpriced == 0 {
		v.Run.Budget.USD = &u.USD
	}
	for _, c := range crit {
		v.Run.OpenFindings += btoi(c.Status == "open")
	}
	for _, g := range gs {
		if g.Decision == "" {
			v.PendingGates = append(v.PendingGates, g)
		} else {
			v.DecidedGates = append(v.DecidedGates, g)
		}
	}
	for _, f := range fs {
		q := ""
		if i := slices.IndexFunc(issues, func(is Issue) bool { return is.ID == f.IssueID }); i >= 0 {
			q = issues[i].Question
		}
		v.Findings = append(v.Findings, FindingView{f.FID, f.IssueID, q, f.Claim, cmpStr(f.Severity != "", f.Severity, "medium"),
			cmpStr(f.HookBlocked != "", "quarantined by hook "+f.HookBlocked, map[bool]string{true: "verified", false: "quarantined"}[f.Verified]), append([]Citation{}, f.Citations...), append([]string{}, f.ValueNotes...)})
	}
	return v, nil
}

type jsonScan struct{ v any }

func (j jsonScan) Scan(src any) error {
	if b, ok := src.([]byte); ok {
		return json.Unmarshal(b, j.v)
	}
	return nil
}

// Steps pages a task's step log after step after.
func (e *Engine) Steps(ctx context.Context, id string, after int) ([]StepView, error) {
	return e.steps(ctx, id, after, 1000)
}

// steps reads up to limit steps after after; limit 0 reads every step, without inputs and outputs
// (the run's budget and the cost ledger count them all).
func (e *Engine) steps(ctx context.Context, id string, after, limit int) ([]StepView, error) {
	q, err := e.db.QueryContext(ctx, `SELECT step, kind, tool, status, CASE WHEN $3 = 0 THEN 'null' ELSE coalesce(input, 'null') END,
		CASE WHEN $3 = 0 THEN 'null' ELSE coalesce(output, 'null') END, usage, model, note FROM task_steps
		WHERE task_id::text = $1 AND step > $2 ORDER BY step LIMIT nullif($3, 0)`, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	out := []StepView{}
	for q.Next() {
		var s StepView
		var in, o, usage []byte
		var note string
		if err := q.Scan(&s.Step, &s.Kind, &s.Tool, &s.Status, &in, &o, &usage, &s.Model, &note); err != nil {
			return nil, err
		}
		s.Input, s.Output = in, o
		_ = json.Unmarshal(usage, &s.usage)
		s.Summary = summary(s, note)
		out = append(out, s)
	}
	return out, q.Err()
}

// summary is a step's one line for the loop log.
func summary(s StepView, note string) string {
	var in map[string]any
	_ = json.Unmarshal(s.Input, &in)
	var out map[string]any
	_ = json.Unmarshal(s.Output, &out)
	switch {
	case s.Kind == "note":
		return fmt.Sprint(out["text"])
	case s.Status == "started":
		return "running…"
	case note != "":
		return truncate(note, 200)
	case s.Kind == "lead_turn":
		var names []string
		calls, _ := out["toolCalls"].([]any)
		for _, c := range calls {
			if f, ok := c.(map[string]any)["function"].(map[string]any); ok {
				names = append(names, fmt.Sprint(f["name"]))
			}
		}
		if len(names) > 0 {
			return "chose " + strings.Join(names, ", ")
		}
		return truncate(fmt.Sprint(out["text"]), 200)
	}
	parts := []string{}
	for _, k := range []string{"op", "query", "issueId", "sectionId"} {
		if x, ok := in[k].(string); ok && x != "" {
			parts = append(parts, map[bool]string{true: x, false: fmt.Sprintf("%s %q", k, x)}[k == "op"])
		}
	}
	for _, k := range []string{"hits", "findings", "outline", "conflicts", "handles"} {
		if l, ok := out[k].([]any); ok {
			parts = append(parts, fmt.Sprintf("→ %d %s", len(l), k))
		}
	}
	if a, ok := out["accepted"].(bool); ok {
		parts = append(parts, map[bool]string{true: "→ accepted", false: "→ not accepted"}[a])
	}
	return strings.Join(append([]string{}, parts...), " ")
}

type CostEntry struct {
	Step         int      `json:"step"`
	Tool         string   `json:"tool"`
	Model        string   `json:"model"`
	InputTokens  int      `json:"inputTokens"`
	OutputTokens int      `json:"outputTokens"`
	USD          *float64 `json:"usd"`
}

type Cost struct {
	TaskID  string `json:"taskId"`
	Summary struct {
		Calls         int      `json:"calls"`
		InputTokens   int      `json:"inputTokens"`
		OutputTokens  int      `json:"outputTokens"`
		USD           *float64 `json:"usd"`
		UnpricedCalls int      `json:"unpricedCalls"`
	} `json:"summary"`
	Entries []CostEntry `json:"entries"`
	// Reader is the figure reader's record (ReaderStats): its calls, how many failed (and why) or
	// went unfunded, and the failure rate, so a run's unread figures are never a mystery (bench7).
	Reader *ReaderCost `json:"reader,omitempty"`
}

// ReaderCost is the figure reader's line of a task's cost.
type ReaderCost struct {
	ReaderStats
	FailureRate float64 `json:"failureRate"` // failed and unfunded calls over all its calls
}

// Cost is a task's cost ledger: every step that spent, and the grants lost to crashes.
func (e *Engine) Cost(ctx context.Context, id string) (Cost, error) {
	t, err := e.task(ctx, id)
	if err != nil {
		return Cost{}, err
	}
	steps, err := e.steps(ctx, id, 0, 0)
	if err != nil {
		return Cost{}, err
	}
	c := Cost{TaskID: t.ID, Entries: []CostEntry{}}
	var lost, all agent.Usage
	_ = e.db.QueryRowContext(ctx, `SELECT lost FROM tasks WHERE id = $1`, t.ID).Scan(jsonScan{&lost})
	if lost.Calls > 0 {
		steps = append(steps, StepView{Tool: "lost (in flight at a restart)", usage: lost})
	}
	for _, s := range steps {
		if s.usage.Calls == 0 && s.usage.USD == 0 {
			continue
		}
		all.Add(s.usage)
		en := CostEntry{s.Step, s.Tool, s.Model, s.usage.InputTokens, s.usage.OutputTokens, nil}
		if s.usage.Unpriced == 0 {
			usd := s.usage.USD
			en.USD = &usd
		}
		c.Entries = append(c.Entries, en)
	}
	c.Summary.Calls, c.Summary.InputTokens, c.Summary.OutputTokens, c.Summary.UnpricedCalls = all.Calls, all.InputTokens, all.OutputTokens, all.Unpriced
	if st, err := e.readerStats(ctx, t.ID); err != nil {
		return Cost{}, err
	} else if st.Calls > 0 {
		c.Reader = &ReaderCost{ReaderStats: st, FailureRate: float64(st.Failed+st.Unfunded) / float64(st.Calls)}
	}
	if all.Unpriced == 0 {
		c.Summary.USD = &all.USD
	}
	return c, nil
}

// Cancel stops a task that has not ended; a running process sees it at its next step or beat.
func (e *Engine) Cancel(ctx context.Context, id string) (TaskView, error) {
	t, err := e.task(ctx, id)
	if err != nil {
		return TaskView{}, err
	}
	res, err := e.db.ExecContext(ctx, `UPDATE tasks SET status = 'cancelled', stop_reason = 'cancelled', updated_at = now() WHERE id = $1 AND status IN ('queued', 'running', 'awaiting_gate')`, t.ID)
	if err != nil {
		return TaskView{}, err
	} else if n, _ := res.RowsAffected(); n == 0 {
		return TaskView{}, fmt.Errorf("%w: the task has already ended (%s)", ErrConflict, t.Status)
	}
	if r := e.active(t.ID); r != nil && r.cancel != nil {
		r.cancel()
	}
	return e.Get(ctx, t.ID)
}

// Decide approves or rejects an open gate (G7). Approving the deliverable completes the task;
// rejecting it (a reason is required) sends the lead back to work with the reason; once every
// finding gate is decided, the run resumes with the decisions. by is who decided (recorded on the gate).
func (e *Engine) Decide(ctx context.Context, id, gateID string, approve bool, reason string, by Person) (TaskView, error) {
	t, err := e.task(ctx, id)
	if err != nil {
		return TaskView{}, err
	}
	if !approve && strings.TrimSpace(reason) == "" {
		return TaskView{}, fmt.Errorf("%w: a rejection needs a reason: it is what the lead is asked to fix", ErrBadTask)
	}
	err = e.st.InTx(ctx, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = $1 FOR UPDATE`, t.ID).Scan(&status); err != nil {
			return err
		}
		var g Gate
		var data []byte
		err := tx.QueryRowContext(ctx, `SELECT data FROM ledger WHERE task_id = $1 AND kind = $2 AND key = $3`, t.ID, kGate, gateID).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		_ = json.Unmarshal(data, &g)
		if g.Decision != "" || status != "awaiting_gate" {
			return fmt.Errorf("%w: gate %s is not open", ErrConflict, gateID)
		}
		g.Decision, g.Reason, g.DecidedAt = map[bool]string{true: "approved", false: "rejected"}[approve], strings.TrimSpace(reason), time.Now().UTC().Format(time.RFC3339)
		g.DecidedBy = &by
		if _, err := tx.ExecContext(ctx, upsertSQL(true), t.ID, kGate, gateID, mustJSON(g), 0); err != nil {
			return err
		}
		note := ""
		switch {
		case g.Kind == "deliverable" && approve:
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET status = 'complete', updated_at = now() WHERE id = $1`, t.ID)
			return err
		case g.Kind == "deliverable":
			note = "The lawyer rejected the deliverable. Their reason: " + g.Reason + "\nRevise the memo to address it (more evidence, a redraft, a committee) and call finish again."
		default:
			var open int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ledger WHERE task_id = $1 AND kind = $2 AND data->>'decision' IS NULL`, t.ID, kGate).Scan(&open); err != nil || open > 0 {
				return err
			}
			rows, err := tx.QueryContext(ctx, `SELECT data FROM ledger WHERE task_id = $1 AND kind = $2 AND data->>'kind' IN ('finding', 'hook') AND data->>'noted' IS NULL ORDER BY seq`, t.ID, kGate)
			if err != nil {
				return err
			}
			note = "The lawyer decided the gates:"
			hooked := false
			for rows.Next() {
				var x Gate
				var d []byte
				_ = rows.Scan(&d)
				_ = json.Unmarshal(d, &x)
				what := x.FindingID
				if x.Kind == "hook" {
					what, hooked = "the call of "+x.Subject+" held by hook "+x.Hook+" (gate "+x.GateID+")", true
				}
				note += fmt.Sprintf("\n- %s %s%s", what, x.Decision, map[bool]string{true: ": " + x.Reason, false: ""}[x.Reason != ""])
			}
			rows.Close()
			// each decision goes to the lead once: a later note lists only the gates decided since
			if _, err := tx.ExecContext(ctx, `UPDATE ledger SET data = jsonb_set(data, '{noted}', 'true') WHERE task_id = $1 AND kind = $2 AND data->>'kind' IN ('finding', 'hook') AND data->>'noted' IS NULL`, t.ID, kGate); err != nil {
				return err
			}
			if strings.Contains(note, "\n- F") {
				note += "\nRejected findings are out of the memo: redraft what cited them, then call finish again."
			}
			if hooked {
				note += " An approved call goes through once, when you make it again with exactly the same input; a rejected one must not be made: do the work without it, or disclose what it would have found."
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_steps (task_id, step, kind, tool, status, output)
			SELECT $1, coalesce(max(step), 0) + 1, 'note', 'gate', 'committed', $2 FROM task_steps WHERE task_id = $1`, t.ID, mustJSON(map[string]string{"text": note}))
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET status = 'running', stop_reason = NULL, lease_owner = NULL, lease_until = NULL, updated_at = now() WHERE id = $1`, t.ID)
		}
		return err
	})
	if err != nil {
		return TaskView{}, err
	}
	e.Wake()
	return e.Get(ctx, t.ID)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
