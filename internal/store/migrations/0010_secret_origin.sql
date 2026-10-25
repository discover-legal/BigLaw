-- 0010_secret_origin: each secret is bound to the origin it was set for (internal/engine remote.go
-- secretRef, README § Manage, Custom agents and tools): https://host:port, the only one a call sends
-- it to. A secret set before this migration has none and is sent nowhere until an admin sets it again.
ALTER TABLE secrets ADD COLUMN origin text NOT NULL DEFAULT '';
