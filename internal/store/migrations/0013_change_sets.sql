-- 0013_change_sets: Word track changes (README § Track changes). One row per POST
-- /tasks/{id}/changes: the document (and, for a compare, the newer version), the kind, the
-- tracked changes' author, who posted it, the edits as posted and what happened to each
-- (located at POST, applied or failed at each render). The rendered .docx is not stored: it is
-- rendered on demand at download, deterministically, from this row.
CREATE TABLE change_sets (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id      uuid        NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    document_id  uuid        NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    against_id   uuid        REFERENCES documents (id) ON DELETE CASCADE,
    kind         text        NOT NULL CHECK (kind IN ('edits', 'compare', 'accept', 'reject')),
    author       text        NOT NULL,
    created_by   jsonb       NOT NULL,
    edits        jsonb       NOT NULL,
    revision_ids integer[]   NOT NULL,
    outcomes     jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX change_sets_task ON change_sets (task_id, created_at DESC);
