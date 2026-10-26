-- 0008_overrides: Manage (internal/engine manage.go, README § Manage). An admin's edit of an agent,
-- tool, pack or skill is a new version of that item: who, when, an optional note and the item's full
-- files (repo path → content). The latest version of an item is what new tasks run on; files NULL is
-- the built-in again (a revert to it), '{}' a deleted skill (a tombstone, itself revertable). Rows are
-- never updated or deleted: they are the history and the change log.
CREATE TABLE spec_versions (
    item         text        NOT NULL,
    version      integer     NOT NULL CHECK (version > 0),
    action       text        NOT NULL CHECK (action IN ('save', 'revert', 'create', 'delete')),
    files        jsonb,
    from_version integer     NOT NULL CHECK (from_version >= 0),
    restored     integer     CHECK (restored >= 0),
    author       text        NOT NULL,
    note         text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (item, version)
);
CREATE INDEX spec_versions_time ON spec_versions (created_at DESC);

-- A task's snapshot: the overrides in effect when it was submitted (item → version and files), so a
-- save reaches the next task only and a resumed run reads what it started with. '{}': built-ins.
ALTER TABLE tasks ADD COLUMN specs jsonb NOT NULL DEFAULT '{}';
