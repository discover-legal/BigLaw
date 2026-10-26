-- 0011_sessions: sign-in (internal/auth, README § Sign-in). sessions are the server-side sessions
-- behind the opaque cookie: only the SHA-256 of the cookie's random id is stored, so a database
-- reader cannot present one; a session ends at expires_at (absolute) or after the idle timeout
-- since seen_at, and logout deletes its row. A sign-in in flight has no row: it is sealed in the
-- browser's login cookie (internal/auth session.go pending), so anonymous sign-ins cost nothing here.
CREATE TABLE sessions (
    id_hash        bytea       PRIMARY KEY,
    sub            text        NOT NULL,
    email          text        NOT NULL DEFAULT '',
    email_verified boolean     NOT NULL DEFAULT false,
    name           text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL,
    seen_at        timestamptz NOT NULL,
    expires_at     timestamptz NOT NULL
);
CREATE INDEX sessions_expiry ON sessions (expires_at);
CREATE INDEX sessions_sub ON sessions (sub);
