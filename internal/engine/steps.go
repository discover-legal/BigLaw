// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// A step's writes (DESIGN § Commit protocol): ledger rows and bandit statistics are buffered while
// the step runs and committed in the transaction that marks it committed, so a crash leaves either
// all of a step or none of it, and a re-run step never writes twice.
type op struct {
	kind, key string
	data      json.RawMessage
	update    bool
	stat      *statOp
}

type statOp struct {
	sig string
	arm agent.Arm
	d   agent.Stat
}

var (
	errLease    = errors.New("engine: the run's lease was lost (cancelled, or another process took it)")
	errMaxSteps = errors.New("engine: the run reached its maxSteps")
	errCollapse = errors.New("engine: grounding collapse")
	errHeld     = errors.New("engine: the run waits at a configurable hook's gate for a lawyer")
)

// run is one task's run held by this process: its step buffer and what stopped it.
type run struct {
	e        *Engine
	t        Task
	mu       sync.Mutex
	buf      []op
	b        *agent.Budget // the task's budget (root)
	cycleUSD float64       // what the root keeps for one committee cycle (keep: Allocation.cycleKeep)
	cancel   context.CancelFunc
	forced   string       // finish from the reserve: budget_exhausted or max_steps
	collapse string       // the G4 alert that failed the run (strict)
	step     atomic.Int64 // the lead step that is running (the logs' step of a call inside it)
	specs    *Specs       // the spec set of its task's snapshot (nil: the built-ins)
	held     string       // a hook gate this run opened: its next step pauses the run there (hookrules.go approve)
}

// hold pauses the run at gate id once the step that opened it commits (Record refuses the next).
func (r *run) hold(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held = cmpStr(r.held == "", id, r.held)
}

// stepOf is the lead step running in a task's run (0: none here), for a log line.
func (e *Engine) stepOf(taskID string) int64 {
	if r := e.active(taskID); r != nil {
		return r.step.Load()
	}
	return 0
}

func (e *Engine) active(taskID string) *run {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs[taskID]
}

// Record is the run's Journal (agent.Journal): a started step is inserted with its grant while
// the lease holds (a second started record settles the grant to what a stopped step spent); any
// other status commits the step with the buffered writes in one transaction.
func (r *run) Record(ctx context.Context, s agent.Step) error {
	r.mu.Lock()
	held := r.held
	r.mu.Unlock()
	if s.Status == "started" && held != "" {
		return errHeld
	}
	if s.Status == "started" && s.N > r.t.MaxSteps && r.forced == "" {
		return errMaxSteps
	}
	if s.Status == "started" {
		r.step.Store(int64(s.N))
	}
	grant, _ := json.Marshal(s.Grant)
	usage, _ := json.Marshal(s.Usage)
	r.mu.Lock()
	buf := r.buf
	if s.Status != "started" {
		r.buf = nil
	}
	r.mu.Unlock()
	findings := false
	err := r.e.st.InTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET updated_at = now() WHERE id = $1 AND lease_owner = $2 AND status = 'running'`, r.t.ID, r.e.owner)
		if err != nil {
			return err
		} else if n, _ := res.RowsAffected(); n == 0 {
			return errLease
		}
		res, err = tx.ExecContext(ctx, `INSERT INTO task_steps (task_id, step, kind, tool, status, input, output, grant_held, usage, model, final, note)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT (task_id, step) DO UPDATE SET status = EXCLUDED.status,
			output = EXCLUDED.output, usage = EXCLUDED.usage, model = EXCLUDED.model, final = EXCLUDED.final, note = EXCLUDED.note,
			grant_held = CASE WHEN EXCLUDED.status = 'started' THEN EXCLUDED.grant_held ELSE task_steps.grant_held END
			WHERE task_steps.status = 'started'`,
			r.t.ID, s.N, s.Kind, s.Tool, s.Status, jsonb(s.Input), jsonb(s.Output), grant, usage, s.Model, s.Final, s.Note)
		if err != nil {
			return err
		} else if n, _ := res.RowsAffected(); n == 0 || s.Status == "started" {
			return nil // already recorded (a duplicate write is a no-op), or only started: no writes yet
		}
		for _, o := range buf {
			if o.stat != nil {
				err = addStat(ctx, tx, o.stat)
			} else {
				findings = findings || o.kind == kFinding
				_, err = tx.ExecContext(ctx, upsertSQL(o.update), r.t.ID, o.kind, o.key, []byte(o.data), s.N)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if s.Status != "started" { // not committed: the writes go back for the step's retry
			r.mu.Lock()
			r.buf = append(buf, r.buf...)
			r.mu.Unlock()
		}
		return err
	}
	if h := r.e.afterCommit; h != nil && s.Status != "started" {
		h(r.t.ID, s.N)
	}
	if findings {
		return r.checkCollapse(ctx)
	}
	return nil
}

func jsonb(v json.RawMessage) any {
	if len(v) == 0 {
		return nil
	}
	return []byte(v)
}

// checkCollapse is G4 over every finding of the task: at least 8 findings with an unverified share
// at or above the threshold fails the run (strict) or records the alert and goes on (warn).
func (r *run) checkCollapse(ctx context.Context) error {
	fs, err := load[Finding](ctx, r.e, r.t.ID, kFinding)
	if err != nil {
		return err
	}
	bad, total := 0, 0
	for _, f := range fs {
		if f.HookBlocked != "" { // a policy quarantine, not a quote that failed verification
			continue
		}
		total++
		bad += btoi(!f.Verified)
	}
	alert, fail := Collapse(total-bad, total, r.e.cfg.CollapseThreshold, r.e.cfg.CollapseAction)
	if alert == "" {
		return nil
	}
	if fail {
		r.collapse = alert
		return errCollapse
	}
	_, err = r.e.db.ExecContext(ctx, `UPDATE tasks SET grounding_alert = $2 WHERE id = $1 AND grounding_alert IS NULL`, r.t.ID, alert)
	if err == nil && r.t.GroundingAlert == "" {
		r.t.GroundingAlert = alert // this run's deliverable discloses it (Evidence Note)
	}
	return err
}

// minCollapseSample is how many findings G4 needs before it judges.
const minCollapseSample = 8

// Collapse is G4: the alert when verified of total findings is a collapse, and whether the task fails.
func Collapse(verified, total int, threshold float64, action string) (alert string, fail bool) {
	if total < minCollapseSample || float64(total-verified) < threshold*float64(total) {
		return "", false
	}
	rate := 100 * float64(total-verified) / float64(total)
	alert = fmt.Sprintf("grounding collapse: %d of %d findings (%.0f%%) failed quote verification", total-verified, total, rate)
	if action == "warn" {
		return alert + "; warn mode: the run went on, and every unverified finding stays quarantined", false
	}
	return alert + "; the task fails rather than deliver (BIGLAW_COLLAPSE_ACTION=strict)", true
}

// stats is the bandit's record in Postgres (tool_stats), shared by every task: a run's additions
// are buffered in its running step and committed with it.
type stats struct{ r *run }

func (s stats) Load(ctx context.Context, sig string) (map[agent.Arm]agent.Stat, error) {
	q, err := s.r.e.db.QueryContext(ctx, `SELECT tool, model, n, reward, reward_sq, fails, calls, tokens FROM tool_stats WHERE sig = $1`, sig)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	out := map[agent.Arm]agent.Stat{}
	for q.Next() {
		var a agent.Arm
		var st agent.Stat
		if err := q.Scan(&a.Tool, &a.Model, &st.N, &st.Reward, &st.RewardSq, &st.Fails, &st.Calls, &st.Tokens); err != nil {
			return nil, err
		}
		out[a] = st
	}
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	for _, o := range s.r.buf {
		if o.stat != nil && o.stat.sig == sig {
			x := out[o.stat.arm]
			out[o.stat.arm] = agent.Stat{N: x.N + o.stat.d.N, Reward: x.Reward + o.stat.d.Reward, RewardSq: x.RewardSq + o.stat.d.RewardSq,
				Fails: x.Fails + o.stat.d.Fails, Calls: x.Calls + o.stat.d.Calls, Tokens: x.Tokens + o.stat.d.Tokens}
		}
	}
	return maps.Clone(out), q.Err()
}

func (s stats) Add(_ context.Context, sig string, a agent.Arm, d agent.Stat) error {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.r.buf = append(s.r.buf, op{stat: &statOp{sig, a, d}})
	return nil
}

func addStat(ctx context.Context, tx *sql.Tx, s *statOp) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tool_stats (sig, tool, model, n, reward, reward_sq, fails, calls, tokens) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (sig, tool, model) DO UPDATE SET n = tool_stats.n + EXCLUDED.n, reward = tool_stats.reward + EXCLUDED.reward,
		reward_sq = tool_stats.reward_sq + EXCLUDED.reward_sq, fails = tool_stats.fails + EXCLUDED.fails, calls = tool_stats.calls + EXCLUDED.calls,
		tokens = tool_stats.tokens + EXCLUDED.tokens`, s.sig, s.arm.Tool, s.arm.Model, s.d.N, s.d.Reward, s.d.RewardSq, s.d.Fails, s.d.Calls, s.d.Tokens)
	return err
}
