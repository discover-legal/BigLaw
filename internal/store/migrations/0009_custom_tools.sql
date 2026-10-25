-- 0009_custom_tools: custom tools' secrets (internal/engine custom.go, README § Manage, Custom
-- agents and tools) and the change log's entries that are not versions. A secret's value is read
-- only to send it as a header on a remote call: no route, log, error, snapshot or export holds it.
CREATE TABLE secrets (
    name       text        PRIMARY KEY CHECK (name ~ '^[a-z][a-z0-9_-]{1,39}$'),
    value      text        NOT NULL,
    updated_by text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- A secret set or deleted, a Test call: who, what and when (never a secret's value).
CREATE TABLE manage_events (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item       text        NOT NULL,
    action     text        NOT NULL,
    author     text        NOT NULL,
    note       text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX manage_events_time ON manage_events (created_at DESC);
