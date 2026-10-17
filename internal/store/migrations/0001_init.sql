-- 0001_init: binary files (uploads and generated .docx) live in Postgres.
-- Content is split into file_chunks rows of at most store.ChunkSize bytes
-- (1 MiB, enforced by the CHECK below), so writing a file needs only one
-- chunk of transient memory (lib/pq moves bytea as hex text); GetFile still
-- holds the whole file. schema_migrations is created by the runner itself
-- (migrate.go), not here.
CREATE TABLE files (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL,
    content_type text        NOT NULL,
    size         bigint      NOT NULL CHECK (size >= 0),
    sha256       text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE file_chunks (
    file_id uuid    NOT NULL REFERENCES files (id) ON DELETE CASCADE,
    seq     integer NOT NULL CHECK (seq >= 0),
    data    bytea   NOT NULL CHECK (octet_length(data) <= 1048576),
    PRIMARY KEY (file_id, seq)
);
