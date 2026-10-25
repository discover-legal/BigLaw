-- 0011_sessions: sign-in (internal/auth, README § Sign-in). sessions are the server-side sessions
-- behind the opaque cookie: only the SHA-256 of the cookie's random id is stored, so a database
-- reader cannot present one; a session ends at expires_at (absolute) or after the idle timeout
-- since seen_at, and logout deletes its row. auth_logins holds a sign-in in flight (state hash,
-- nonce, PKCE verifier, where to return) until the issuer sends the browser back, used once.
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

CREATE TABLE auth_logins (
    state_hash bytea       PRIMARY KEY,
    nonce      text        NOT NULL,
    verifier   text        NOT NULL,
    next       text        NOT NULL,
    created_at timestamptz NOT NULL
);
