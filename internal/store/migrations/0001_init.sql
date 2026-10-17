-- 0001_init: binary files (uploads and generated .docx) live in Postgres as bytea.
-- schema_migrations is created by the runner itself (migrate.go), not here.
CREATE TABLE files (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL,
    content_type text        NOT NULL,
    size         bigint      NOT NULL CHECK (size >= 0),
    sha256       text        NOT NULL,
    data         bytea       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (octet_length(data) = size)
);
