// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo

	"github.com/discover-legal/biglaw-go/internal/types"
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
	created_at TEXT NOT NULL DEFAULT '',
	payload    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS document_versions (
	id             TEXT PRIMARY KEY,
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

type sqliteRepo struct {
	db   *sql.DB
	path string
}

func openSQLite(path string) (DocRepository, error) {
	if path == "" {
		path = filepath.Join("data", "biglaw.db")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create sqlite dir %s: %w", dir, err)
		}
	}
	// _pragma busy_timeout avoids "database is locked" under concurrent writes;
	// journal_mode=WAL gives reader/writer concurrency.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: sqlite migrate: %w", err)
	}
	return &sqliteRepo{db: db, path: path}, nil
}

func (r *sqliteRepo) Backend() string { return "sqlite" }
func (r *sqliteRepo) Close() error    { return r.db.Close() }

// SQLite is local single-tenant; it ignores Identity (the application layer
// enforces access). Signatures match the interface.
func (r *sqliteRepo) Upsert(_ context.Context, doc types.Document) error {
	noslegal, metadata := marshalFacets(doc)
	ingested := doc.IngestedAt
	if ingested.IsZero() {
		ingested = time.Now()
	}
	_, err := r.db.Exec(`
		INSERT INTO documents
			(id, title, content, source, jurisdiction, document_type, owner_id,
			 practice_area, detected_client_number, noslegal_json, metadata_json, ingested_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			title=excluded.title, content=excluded.content, source=excluded.source,
			jurisdiction=excluded.jurisdiction, document_type=excluded.document_type,
			owner_id=excluded.owner_id, practice_area=excluded.practice_area,
			detected_client_number=excluded.detected_client_number,
			noslegal_json=excluded.noslegal_json, metadata_json=excluded.metadata_json,
			ingested_at=excluded.ingested_at`,
		doc.ID, doc.Title, doc.Content, doc.Source, doc.Jurisdiction, doc.DocumentType,
		doc.OwnerID, doc.PracticeArea, doc.DetectedClientNumber,
		noslegal, metadata, ingested.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: sqlite upsert %s: %w", doc.ID, err)
	}
	return nil
}

func (r *sqliteRepo) GetByID(_ context.Context, id string) (*types.Document, bool, error) {
	row := r.db.QueryRow(`SELECT `+docColumns+` FROM documents WHERE id = ?`, id)
	doc, err := scanDoc(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite get %s: %w", id, err)
	}
	return doc, true, nil
}

func (r *sqliteRepo) List(_ context.Context) ([]types.Document, error) {
	rows, err := r.db.Query(`SELECT ` + docColumns + ` FROM documents ORDER BY ingested_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite list: %w", err)
	}
	defer rows.Close()
	var out []types.Document
	for rows.Next() {
		doc, err := scanDoc(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sqlite scan: %w", err)
		}
		out = append(out, *doc)
	}
	return out, rows.Err()
}

func (r *sqliteRepo) Delete(_ context.Context, id string) error {
	if _, err := r.db.Exec(`DELETE FROM documents WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: sqlite delete %s: %w", id, err)
	}
	return nil
}

func (r *sqliteRepo) AddAttachment(_ context.Context, a types.Attachment) error {
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := r.db.Exec(`
		INSERT INTO attachments (id, doc_id, owner_id, filename, media_type, kind, size, blob_key, page, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			doc_id=excluded.doc_id, owner_id=excluded.owner_id, filename=excluded.filename,
			media_type=excluded.media_type, kind=excluded.kind, size=excluded.size,
			blob_key=excluded.blob_key, page=excluded.page, created_at=excluded.created_at`,
		a.ID, a.DocID, a.OwnerID, a.Filename, a.MediaType, string(a.Kind), a.Size, a.BlobKey, a.Page,
		created.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: sqlite add attachment %s: %w", a.ID, err)
	}
	return nil
}

func (r *sqliteRepo) ListAttachments(_ context.Context, docID string) ([]types.Attachment, error) {
	rows, err := r.db.Query(`SELECT `+attColumns+` FROM attachments WHERE doc_id = ? ORDER BY created_at ASC`, docID)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite list attachments: %w", err)
	}
	defer rows.Close()
	var out []types.Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *sqliteRepo) GetAttachment(_ context.Context, id string) (*types.Attachment, bool, error) {
	row := r.db.QueryRow(`SELECT `+attColumns+` FROM attachments WHERE id = ?`, id)
	a, err := scanAttachment(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite get attachment %s: %w", id, err)
	}
	return a, true, nil
}

func (r *sqliteRepo) DeleteAttachment(_ context.Context, id string) error {
	if _, err := r.db.Exec(`DELETE FROM attachments WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: sqlite delete attachment %s: %w", id, err)
	}
	return nil
}

// ─── ReviewRepository ────────────────────────────────────────────────────────────

func (r *sqliteRepo) PutReview(_ context.Context, id string, createdAt time.Time, payload []byte) error {
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := r.db.Exec(`
		INSERT INTO reviews (id, created_at, payload) VALUES (?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			created_at=excluded.created_at, payload=excluded.payload`,
		id, createdAt.UTC().Format(time.RFC3339Nano), string(payload))
	if err != nil {
		return fmt.Errorf("store: sqlite put review %s: %w", id, err)
	}
	return nil
}

func (r *sqliteRepo) GetReview(_ context.Context, id string) ([]byte, bool, error) {
	var payload string
	err := r.db.QueryRow(`SELECT payload FROM reviews WHERE id = ?`, id).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite get review %s: %w", id, err)
	}
	return []byte(payload), true, nil
}

// ─── VersionRepository ───────────────────────────────────────────────────────────

func (r *sqliteRepo) PutVersion(_ context.Context, v DocumentVersion) error {
	created := v.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := r.db.Exec(`
		INSERT INTO document_versions
			(id, lineage_id, parent_id, round, source, author, created_at, path, content_hash, text, decisions_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			lineage_id=excluded.lineage_id, parent_id=excluded.parent_id, round=excluded.round,
			source=excluded.source, author=excluded.author, created_at=excluded.created_at,
			path=excluded.path, content_hash=excluded.content_hash, text=excluded.text,
			decisions_json=excluded.decisions_json`,
		v.ID, v.LineageID, v.ParentID, v.Round, v.Source, v.Author,
		created.UTC().Format(time.RFC3339Nano), v.Path, v.ContentHash, v.Text, string(v.Decisions))
	if err != nil {
		return fmt.Errorf("store: sqlite put version %s: %w", v.ID, err)
	}
	return nil
}

func (r *sqliteRepo) GetVersion(_ context.Context, id string) (*DocumentVersion, bool, error) {
	row := r.db.QueryRow(`SELECT `+verColumns+` FROM document_versions WHERE id = ?`, id)
	v, err := scanVersion(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite get version %s: %w", id, err)
	}
	return v, true, nil
}

func (r *sqliteRepo) ListLineage(_ context.Context, lineageID string) ([]DocumentVersion, error) {
	rows, err := r.db.Query(`SELECT `+verColumns+
		` FROM document_versions WHERE lineage_id = ? ORDER BY round ASC, created_at ASC`, lineageID)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite list lineage: %w", err)
	}
	defer rows.Close()
	var out []DocumentVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sqlite scan version: %w", err)
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func (r *sqliteRepo) FindVersionByHash(_ context.Context, contentHash string) (*DocumentVersion, bool, error) {
	return r.findVersion(`content_hash`, contentHash)
}

func (r *sqliteRepo) FindVersionByPath(_ context.Context, path string) (*DocumentVersion, bool, error) {
	return r.findVersion(`path`, path)
}

func (r *sqliteRepo) findVersion(column, value string) (*DocumentVersion, bool, error) {
	row := r.db.QueryRow(`SELECT `+verColumns+` FROM document_versions WHERE `+column+
		` = ? ORDER BY created_at DESC, round DESC LIMIT 1`, value)
	v, err := scanVersion(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite find version by %s: %w", column, err)
	}
	return v, true, nil
}

const verColumns = `id, lineage_id, parent_id, round, source, author, created_at, path, content_hash, text, decisions_json`

func scanVersion(s rowScanner) (*DocumentVersion, error) {
	var v DocumentVersion
	var created, decisions string
	if err := s.Scan(&v.ID, &v.LineageID, &v.ParentID, &v.Round, &v.Source, &v.Author,
		&created, &v.Path, &v.ContentHash, &v.Text, &decisions); err != nil {
		return nil, err
	}
	if created != "" {
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			v.CreatedAt = t
		}
	}
	if decisions != "" {
		v.Decisions = []byte(decisions)
	}
	return &v, nil
}

// ─── shared row helpers (also used by the Postgres impl) ────────────────────────

const attColumns = `id, doc_id, owner_id, filename, media_type, kind, size, blob_key, page, created_at`

func scanAttachment(s rowScanner) (*types.Attachment, error) {
	var a types.Attachment
	var kind, created string
	if err := s.Scan(&a.ID, &a.DocID, &a.OwnerID, &a.Filename, &a.MediaType,
		&kind, &a.Size, &a.BlobKey, &a.Page, &created); err != nil {
		return nil, err
	}
	a.Kind = types.AttachmentKind(kind)
	if created != "" {
		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			a.CreatedAt = t
		}
	}
	return &a, nil
}

const docColumns = `id, title, content, source, jurisdiction, document_type, owner_id,
	practice_area, detected_client_number, noslegal_json, metadata_json, ingested_at`

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanDoc(s rowScanner) (*types.Document, error) {
	var d types.Document
	var noslegal, metadata, ingested string
	if err := s.Scan(&d.ID, &d.Title, &d.Content, &d.Source, &d.Jurisdiction,
		&d.DocumentType, &d.OwnerID, &d.PracticeArea, &d.DetectedClientNumber,
		&noslegal, &metadata, &ingested); err != nil {
		return nil, err
	}
	if noslegal != "" {
		var n types.NosLegalTags
		if json.Unmarshal([]byte(noslegal), &n) == nil {
			d.NosLegal = &n
		}
	}
	if metadata != "" {
		_ = json.Unmarshal([]byte(metadata), &d.Metadata)
	}
	if ingested != "" {
		if t, err := time.Parse(time.RFC3339Nano, ingested); err == nil {
			d.IngestedAt = t
		}
	}
	return &d, nil
}

func marshalFacets(doc types.Document) (noslegal, metadata string) {
	if doc.NosLegal != nil {
		if b, err := json.Marshal(doc.NosLegal); err == nil {
			noslegal = string(b)
		}
	}
	if len(doc.Metadata) > 0 {
		if b, err := json.Marshal(doc.Metadata); err == nil {
			metadata = string(b)
		}
	}
	return noslegal, metadata
}
