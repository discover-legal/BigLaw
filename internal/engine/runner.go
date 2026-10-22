// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package engine runs tasks: the memo workflow's lead agent over the registry, its Go tools
// (ledger, sweep, committee, finish), the subagent specs, the policy, the gates, and the durable
// run (a step log in Postgres, a lease with a heartbeat, resume from the last committed step).
package engine

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/lib/pq"
)

// Config is the engine's share of the configuration (BIGLAW_BUDGET_*, BIGLAW_COLLAPSE_*).
type Config struct {
	BudgetCalls       int
	BudgetUSD         float64
	CollapseThreshold float64
	CollapseAction    string // strict or warn
}

// Engine runs tasks. One per process; any number of processes may share a database (leases).
type Engine struct {
	st     *store.Store
	db     *sql.DB
	docs   *docs.Service
	chat   model.Chatter
	roles  model.Roles
	prices *model.Prices
	cfg    Config
	log    *slog.Logger
	owner  string

	mu   sync.Mutex
	runs map[string]*run
	wake chan struct{}

	afterCommit func(taskID string, step int) // tests: runs after each committed step
}

// Leases (variables so tests can shorten them): a run's lease lasts leaseFor and is renewed every
// beat; a process looks for runnable tasks every scan, and runs at most maxRuns at once.
var (
	leaseFor = 30 * time.Second
	beat     = 10 * time.Second
	scan     = 10 * time.Second
	maxRuns  = 4
)

func New(st *store.Store, d *docs.Service, chat model.Chatter, roles model.Roles, prices *model.Prices, cfg Config, log *slog.Logger) *Engine {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return &Engine{st: st, db: st.DB(), docs: d, chat: chat, roles: roles, prices: prices, cfg: cfg, log: cmp.Or(log, slog.New(slog.DiscardHandler)),
		owner: hex.EncodeToString(id), runs: map[string]*run{}, wake: make(chan struct{}, 1)}
}

// Task is a task as stored: its request and its run's state.
type Task struct {
	ID             string    `json:"id"`
	Description    string    `json:"description"`
	WorkflowType   string    `json:"workflowType"`
	Jurisdiction   string    `json:"jurisdiction"`
	MatterNumber   string    `json:"matterNumber"`
	DocumentIDs    []string  `json:"documentIds"`
	MaxSteps       int       `json:"maxSteps"`
	ToolBandit     bool      `json:"toolBandit"`
	LeadModel      string    `json:"-"`
	Status         string    `json:"status"`
	StopReason     *string   `json:"-"`
	Output         *string   `json:"output"`
	GroundingAlert string    `json:"-"`
	Error          string    `json:"-"`
	CreatedAt      time.Time `json:"createdAt"`
}

const taskCols = `id, description, workflow_type, jurisdiction, matter_number, document_ids, max_steps, tool_bandit, lead_model, status, stop_reason, output,
	coalesce(grounding_alert, ''), coalesce(error, ''), created_at`

func scanTask(s interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := s.Scan(&t.ID, &t.Description, &t.WorkflowType, &t.Jurisdiction, &t.MatterNumber, pq.Array(&t.DocumentIDs), &t.MaxSteps, &t.ToolBandit, &t.LeadModel,
		&t.Status, &t.StopReason, &t.Output, &t.GroundingAlert, &t.Error, &t.CreatedAt)
	return t, err
}

// ErrBadTask (400) is a task request BigLaw refuses; its text says why.
var ErrBadTask = errors.New("engine: bad task")

// Submit stores a task (status queued) and wakes the runner.
func (e *Engine) Submit(ctx context.Context, t Task) (Task, error) {
	lead, err := e.roles.Model(model.Lead)
	switch {
	case err != nil:
		return Task{}, err
	case t.WorkflowType != "memo":
		return Task{}, fmt.Errorf("%w: workflowType %q is not built yet: this build runs memo (diligence and contract_review come in later PRs)", ErrBadTask, t.WorkflowType)
	case len(t.DocumentIDs) == 0:
		return Task{}, fmt.Errorf("%w: documentIds is empty: a task reads only the documents it names", ErrBadTask)
	}
	for _, id := range t.DocumentIDs {
		if _, err := e.docs.Get(ctx, t.DocumentIDs, id); err != nil {
			return Task{}, fmt.Errorf("%w: document %s does not exist", ErrBadTask, id)
		}
	}
	t.MaxSteps = cmp.Or(t.MaxSteps, 400)
	row := e.db.QueryRowContext(ctx, `INSERT INTO tasks (description, workflow_type, jurisdiction, matter_number, document_ids, max_steps, tool_bandit, lead_model)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+taskCols, t.Description, t.WorkflowType, t.Jurisdiction, t.MatterNumber, pq.Array(t.DocumentIDs), t.MaxSteps, t.ToolBandit, lead)
	if t, err = scanTask(row); err != nil {
		return Task{}, fmt.Errorf("engine: submit: %w", err)
	}
	e.Wake()
	return t, nil
}

// Wake makes the runner look for work now.
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Start runs tasks until ctx ends: at once and every scan it claims runnable tasks (queued, or
// running with a stale lease: a crashed or stopped process's) and runs each to its next stop.
func (e *Engine) Start(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		for {
			e.mu.Lock()
			full := len(e.runs) >= maxRuns
			e.mu.Unlock()
			if full {
				break
			}
			r, err := e.claim(ctx)
			if err != nil || r == nil {
				if err != nil && ctx.Err() == nil {
					e.log.Warn("engine: claim", "err", err)
				}
				break
			}
			rctx, cancel := context.WithCancel(ctx)
			r.cancel = cancel
			wg.Add(1)
			go func() { defer wg.Done(); e.drive(rctx, r) }()
		}
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		case <-time.After(scan):
		}
	}
}

// claim takes one runnable task's lease.
func (e *Engine) claim(ctx context.Context) (*run, error) {
	row := e.db.QueryRowContext(ctx, `UPDATE tasks SET lease_owner = $1, lease_until = now() + $2 * interval '1 millisecond', status = 'running', updated_at = now()
		WHERE id = (SELECT id FROM tasks WHERE status IN ('queued', 'running') AND (lease_until IS NULL OR lease_until < now()) ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING `+taskCols, e.owner, leaseFor.Milliseconds())
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	r := &run{e: e, t: t}
	e.mu.Lock()
	e.runs[t.ID] = r
	e.mu.Unlock()
	return r, nil
}

// drive runs a claimed task, renewing its lease, and releases it.
func (e *Engine) drive(ctx context.Context, r *run) {
	defer func() {
		r.cancel()
		e.mu.Lock()
		delete(e.runs, r.t.ID)
		e.mu.Unlock()
		_, _ = e.db.Exec(`UPDATE tasks SET lease_owner = NULL, lease_until = NULL WHERE id = $1 AND lease_owner = $2 AND status <> 'running'`, r.t.ID, e.owner)
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(beat):
			}
			res, err := e.db.ExecContext(ctx, `UPDATE tasks SET lease_until = now() + $3 * interval '1 millisecond' WHERE id = $1 AND lease_owner = $2 AND status = 'running'`,
				r.t.ID, e.owner, leaseFor.Milliseconds())
			if err != nil {
				continue
			}
			if n, _ := res.RowsAffected(); n == 0 { // cancelled, or another process took it
				r.cancel()
			}
		}
	}()
	if err := e.execute(ctx, r); err != nil && ctx.Err() == nil {
		e.log.Warn("engine: run", "task", r.t.ID, "err", err)
	}
}
