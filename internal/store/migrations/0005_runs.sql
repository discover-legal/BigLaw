-- 0005_runs: tasks as durable runs (internal/engine), their step log, the matter ledger and the
-- tool bandit's statistics. Plus documents.matter_number (internal/docs): the matter a document
-- was filed under (POST /documents), which GET /documents?matter= and the client list read.
ALTER TABLE documents ADD COLUMN matter_number text;
CREATE INDEX documents_matter ON documents (matter_number);

-- A task is its run: status and stop reason, the lease of the process running it (heartbeat:
-- lease_until moves forward; a stale lease is taken over), and lost: the reserved grants of steps
-- that were in flight when a process died (charged to the budget, never refunded).
CREATE TABLE tasks (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    description     text        NOT NULL,
    workflow_type   text        NOT NULL,
    jurisdiction    text        NOT NULL,
    matter_number   text        NOT NULL,
    document_ids    uuid[]      NOT NULL,
    max_steps       integer     NOT NULL CHECK (max_steps > 0),
    tool_bandit     boolean     NOT NULL,
    lead_model      text        NOT NULL,
    status          text        NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'running', 'awaiting_gate', 'complete', 'failed', 'cancelled')),
    stop_reason     text        CHECK (stop_reason IN ('finished', 'budget_exhausted', 'max_steps', 'awaiting_gate', 'cancelled', 'failed')),
    output          text,
    grounding_alert text,
    error           text,
    lost            jsonb       NOT NULL DEFAULT '{}',
    lease_owner     text,
    lease_until     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tasks_runnable ON tasks (created_at) WHERE status IN ('queued', 'running');

-- The step log (README § Long-running runs): a lead turn or a tool call, written 'started' with its
-- reserved grant before it runs and committed (or refused/failed) with its output and usage, in
-- one transaction with the step's ledger writes and bandit rows. A note is a lawyer's gate
-- decision the lead reads when the run resumes.
CREATE TABLE task_steps (
    task_id    uuid        NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    step       integer     NOT NULL CHECK (step > 0),
    kind       text        NOT NULL CHECK (kind IN ('lead_turn', 'tool_call', 'note')),
    tool       text        NOT NULL,
    status     text        NOT NULL CHECK (status IN ('started', 'committed', 'refused', 'failed')),
    input      jsonb,
    output     jsonb,
    grant_held jsonb       NOT NULL DEFAULT '{}',
    usage      jsonb       NOT NULL DEFAULT '{}',
    model      text        NOT NULL DEFAULT '',
    final      boolean     NOT NULL DEFAULT false,
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, step)
);

-- The matter ledger: one JSON row per (kind, key): plan issues, findings, critic findings,
-- drafted sections, worksheets, gates, sweep visits, committee cycles, notes. A step's writes
-- carry its number; seq keeps insertion order (the plan's order is the deliverable's spine).
CREATE TABLE ledger (
    task_id uuid    NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    kind    text    NOT NULL,
    key     text    NOT NULL,
    data    jsonb   NOT NULL,
    step    integer NOT NULL DEFAULT 0,
    seq     bigint  GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (task_id, kind, key)
);

-- The tool bandit's record (agent.Stats): per task signature, tool and model binding.
CREATE TABLE tool_stats (
    sig       text   NOT NULL,
    tool      text   NOT NULL,
    model     text   NOT NULL,
    n         float8 NOT NULL DEFAULT 0,
    reward    float8 NOT NULL DEFAULT 0,
    reward_sq float8 NOT NULL DEFAULT 0,
    fails     float8 NOT NULL DEFAULT 0,
    calls     float8 NOT NULL DEFAULT 0,
    tokens    float8 NOT NULL DEFAULT 0,
    PRIMARY KEY (sig, tool, model)
);
