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
