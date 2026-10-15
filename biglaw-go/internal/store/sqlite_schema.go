// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// sqliteSchema is applied idempotently on open. Document scalar fields are
// columns (so they can be queried/indexed); the open-ended Metadata and
// NosLegal facets are JSON. The embedding vector is never stored — it's
// recomputed into the in-memory index on boot.
const sqliteSchema = `
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
	ingested_at            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_documents_owner   ON documents(owner_id);
CREATE INDEX IF NOT EXISTS idx_documents_client  ON documents(detected_client_number);
CREATE INDEX IF NOT EXISTS idx_documents_ingested ON documents(ingested_at);

CREATE TABLE IF NOT EXISTS attachments (
	id         TEXT PRIMARY KEY,
	doc_id     TEXT NOT NULL,
	owner_id   TEXT NOT NULL DEFAULT '',
	filename   TEXT NOT NULL DEFAULT '',
	media_type TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT '',
	size       INTEGER NOT NULL DEFAULT 0,
	blob_key   TEXT NOT NULL DEFAULT '',
	page       INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_attachments_doc ON attachments(doc_id);

CREATE TABLE IF NOT EXISTS reviews (
	id         TEXT PRIMARY KEY,
	owner_id   TEXT NOT NULL DEFAULT '',
	matter_number TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL DEFAULT '',
	payload    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS document_versions (
	id             TEXT PRIMARY KEY,
	owner_id       TEXT NOT NULL DEFAULT '',
	matter_number  TEXT NOT NULL DEFAULT '',
	lineage_id     TEXT NOT NULL DEFAULT '',
	parent_id      TEXT NOT NULL DEFAULT '',
	round          INTEGER NOT NULL DEFAULT 0,
	source         TEXT NOT NULL DEFAULT '',
	author         TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL DEFAULT '',
	path           TEXT NOT NULL DEFAULT '',
	content_hash   TEXT NOT NULL DEFAULT '',
	text           TEXT NOT NULL DEFAULT '',
	decisions_json TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_document_versions_lineage ON document_versions(lineage_id, round);
CREATE INDEX IF NOT EXISTS idx_document_versions_hash    ON document_versions(content_hash);
CREATE INDEX IF NOT EXISTS idx_document_versions_path    ON document_versions(path);

CREATE TABLE IF NOT EXISTS crm_profiles (
	id            TEXT PRIMARY KEY,
	client_id     TEXT NOT NULL DEFAULT '',
	external_ref  TEXT NOT NULL DEFAULT '',
	email         TEXT NOT NULL DEFAULT '',
	name          TEXT NOT NULL DEFAULT '',
	conflict_flag INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_crm_profiles_external_ref
	ON crm_profiles(external_ref) WHERE external_ref <> '';
CREATE INDEX IF NOT EXISTS idx_crm_profiles_client ON crm_profiles(client_id);

CREATE TABLE IF NOT EXISTS crm_facts (
	id            TEXT PRIMARY KEY,
	profile_id    TEXT NOT NULL,
	category      TEXT NOT NULL DEFAULT 'note',
	predicate     TEXT NOT NULL DEFAULT '',
	value         TEXT NOT NULL DEFAULT '',
	note          TEXT NOT NULL DEFAULT '',
	source        TEXT NOT NULL DEFAULT '',
	proposed_by   TEXT NOT NULL DEFAULT '',
	approver_role TEXT NOT NULL DEFAULT 'lawyer',
	status        TEXT NOT NULL DEFAULT 'pending',
	decided_by    TEXT NOT NULL DEFAULT '',
	decision_note TEXT NOT NULL DEFAULT '',
	superseded_by TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL DEFAULT '',
	decided_at    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_crm_facts_profile_status ON crm_facts(profile_id, status);
CREATE INDEX IF NOT EXISTS idx_crm_facts_pending        ON crm_facts(status, approver_role);

CREATE TABLE IF NOT EXISTS intake_submissions (
	id            TEXT PRIMARY KEY,
	external_id   TEXT NOT NULL DEFAULT '',
	profile_id    TEXT NOT NULL DEFAULT '',
	client_id     TEXT NOT NULL DEFAULT '',
	client_number TEXT NOT NULL DEFAULT '',
	matter_number TEXT NOT NULL DEFAULT '',
	title         TEXT NOT NULL DEFAULT '',
	document_type TEXT NOT NULL DEFAULT '',
	matter_type   TEXT NOT NULL DEFAULT '',
	jurisdiction  TEXT NOT NULL DEFAULT '',
	summary       TEXT NOT NULL DEFAULT '',
	document_id   TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'received',
	note          TEXT NOT NULL DEFAULT '',
	assigned_to   TEXT NOT NULL DEFAULT '',
	task_id       TEXT NOT NULL DEFAULT '',
	conflict      INTEGER NOT NULL DEFAULT 0,
	conflict_json TEXT NOT NULL DEFAULT '',
	metadata_json TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_intake_profile_external
	ON intake_submissions(profile_id, external_id) WHERE external_id <> '';
CREATE INDEX IF NOT EXISTS idx_intake_profile ON intake_submissions(profile_id);
CREATE INDEX IF NOT EXISTS idx_intake_status  ON intake_submissions(status);
`

func migrateSQLite(db *sql.DB) error {
	if _, err := db.Exec(sqliteSchema); err != nil {
		return fmt.Errorf("base schema: %w", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE reviews ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE reviews ADD COLUMN matter_number TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE document_versions ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE document_versions ADD COLUMN matter_number TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("artifact ownership columns: %w", err)
		}
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_reviews_owner_matter ON reviews(owner_id, matter_number);
		CREATE INDEX IF NOT EXISTS idx_document_versions_owner_matter ON document_versions(owner_id, matter_number);
	`); err != nil {
		return fmt.Errorf("artifact ownership indexes: %w", err)
	}
	return nil
}
