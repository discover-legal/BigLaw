// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

// pgSchema creates the table and enforces row-level security. FORCE ROW LEVEL
// SECURITY makes the policies apply even to the table owner, so RLS protects
// regardless of which role the app connects as (no separate non-owner role to
// provision). The policy is DEFAULT-DENY: with no session GUCs set,
// current_setting(..., true) is NULL and every branch is false → zero rows.
//
//	app.system          = 'on'    → full access (boot load, monitors, MCP)
//	app.is_partner      = 'true'  → partner sees/manages all rows
//	app.current_profile = <id>    → a lawyer sees/writes only rows they own
//
// Identity is set per-transaction via set_config(name, value, is_local=true),
// so pooled connections never leak one request's identity into another.
const pgSchema = `
CREATE TABLE IF NOT EXISTS documents (
	id                     TEXT PRIMARY KEY,
	title                  TEXT NOT NULL DEFAULT '',
	content                TEXT NOT NULL DEFAULT '',
	source                 TEXT NOT NULL DEFAULT '',
	jurisdiction           TEXT NOT NULL DEFAULT '',
	document_type          TEXT NOT NULL DEFAULT '',
	owner_id               TEXT NOT NULL DEFAULT '',
	practice_area          TEXT NOT NULL DEFAULT '',
	detected_client_number TEXT NOT NULL DEFAULT '',
	noslegal_json          TEXT NOT NULL DEFAULT '',
	metadata_json          TEXT NOT NULL DEFAULT '',
	ingested_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_documents_owner    ON documents(owner_id);
CREATE INDEX IF NOT EXISTS idx_documents_client   ON documents(detected_client_number);
CREATE INDEX IF NOT EXISTS idx_documents_ingested ON documents(ingested_at);

ALTER TABLE documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE documents FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS documents_rls ON documents;
CREATE POLICY documents_rls ON documents
	USING (
		current_setting('app.system', true) = 'on'
		OR current_setting('app.is_partner', true) = 'true'
		OR owner_id = current_setting('app.current_profile', true)
	)
	WITH CHECK (
		current_setting('app.system', true) = 'on'
		OR current_setting('app.is_partner', true) = 'true'
		OR owner_id = current_setting('app.current_profile', true)
	);

CREATE TABLE IF NOT EXISTS attachments (
	id         TEXT PRIMARY KEY,
	doc_id     TEXT NOT NULL,
	owner_id   TEXT NOT NULL DEFAULT '',
	filename   TEXT NOT NULL DEFAULT '',
	media_type TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT '',
	size       BIGINT NOT NULL DEFAULT 0,
	blob_key   TEXT NOT NULL DEFAULT '',
	page       INTEGER NOT NULL DEFAULT 0,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_attachments_doc ON attachments(doc_id);

ALTER TABLE attachments ENABLE ROW LEVEL SECURITY;
ALTER TABLE attachments FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS attachments_rls ON attachments;
CREATE POLICY attachments_rls ON attachments
	USING (
		current_setting('app.system', true) = 'on'
		OR current_setting('app.is_partner', true) = 'true'
		OR owner_id = current_setting('app.current_profile', true)
	)
	WITH CHECK (
		current_setting('app.system', true) = 'on'
		OR current_setting('app.is_partner', true) = 'true'
		OR owner_id = current_setting('app.current_profile', true)
	);

-- Tabular-review matrices are owner scoped. Partners and the system retain
-- firm-wide access; lawyers see only their own artifacts.
CREATE TABLE IF NOT EXISTS reviews (
	id         TEXT PRIMARY KEY,
	owner_id   TEXT NOT NULL DEFAULT '',
	matter_number TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	payload    JSONB NOT NULL DEFAULT '{}'::jsonb
);
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE reviews ADD COLUMN IF NOT EXISTS matter_number TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_reviews_owner_matter ON reviews(owner_id, matter_number);

ALTER TABLE reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE reviews FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS reviews_rls ON reviews;
CREATE POLICY reviews_rls ON reviews
	USING (
		current_setting('app.system', true) = 'on'
		OR current_setting('app.is_partner', true) = 'true'
		OR owner_id = current_setting('app.current_profile', true)
	)
	WITH CHECK (
		current_setting('app.system', true) = 'on'
	);

-- Document-version lineages use the same owner scope as reviews.
CREATE TABLE IF NOT EXISTS document_versions (
	id             TEXT PRIMARY KEY,
	owner_id       TEXT NOT NULL DEFAULT '',
	matter_number  TEXT NOT NULL DEFAULT '',
	lineage_id     TEXT NOT NULL DEFAULT '',
	parent_id      TEXT NOT NULL DEFAULT '',
	round          INTEGER NOT NULL DEFAULT 0,
	source         TEXT NOT NULL DEFAULT '',
	author         TEXT NOT NULL DEFAULT '',
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	path           TEXT NOT NULL DEFAULT '',
	content_hash   TEXT NOT NULL DEFAULT '',
	text           TEXT NOT NULL DEFAULT '',
	decisions_json TEXT NOT NULL DEFAULT ''
);
ALTER TABLE document_versions ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE document_versions ADD COLUMN IF NOT EXISTS matter_number TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_document_versions_lineage ON document_versions(lineage_id, round);
CREATE INDEX IF NOT EXISTS idx_document_versions_owner_matter ON document_versions(owner_id, matter_number);
CREATE INDEX IF NOT EXISTS idx_document_versions_hash    ON document_versions(content_hash);
CREATE INDEX IF NOT EXISTS idx_document_versions_path    ON document_versions(path);

ALTER TABLE document_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_versions FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS document_versions_rls ON document_versions;
CREATE POLICY document_versions_rls ON document_versions
	USING (
		current_setting('app.system', true) = 'on'
		OR current_setting('app.is_partner', true) = 'true'
		OR owner_id = current_setting('app.current_profile', true)
	)
	WITH CHECK (
		current_setting('app.system', true) = 'on'
	);
`
