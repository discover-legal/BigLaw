// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

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

-- Tabular-review matrices (internal/tools tabular_review). Rows carry no
-- owner: any declared identity (system, partner, or a lawyer) may read, but
-- only the system principal writes — reviews are produced by the tool layer,
-- never directly by a user request. Anonymous callers still see nothing.
CREATE TABLE IF NOT EXISTS reviews (
	id         TEXT PRIMARY KEY,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	payload    JSONB NOT NULL DEFAULT '{}'::jsonb
);

ALTER TABLE reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE reviews FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS reviews_rls ON reviews;
CREATE POLICY reviews_rls ON reviews
	USING (
		current_setting('app.system', true) = 'on'
		OR current_setting('app.is_partner', true) = 'true'
		OR coalesce(current_setting('app.current_profile', true), '') <> ''
	)
	WITH CHECK (
		current_setting('app.system', true) = 'on'
	);
`

type pgRepo struct {
	pool *pgxpool.Pool
}

func openPostgres(cfg *config.Config) (DocRepository, error) {
	if cfg.Database.URL == "" {
		return nil, fmt.Errorf("store: postgres selected but DATABASE_URL is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("store: postgres connect: %w", err)
	}
	// Migrations run as the connecting role (DDL is not subject to RLS).
	if _, err := pool.Exec(ctx, pgSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: postgres migrate: %w", err)
	}
	return &pgRepo{pool: pool}, nil
}

func (r *pgRepo) Backend() string { return "postgres" }
func (r *pgRepo) Close() error    { r.pool.Close(); return nil }

// gucValues maps the context Identity to the three session settings. No identity
// → all empty → policies deny.
func gucValues(ctx context.Context) (profile, isPartner, system string) {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return "", "", "" // default-deny
	}
	if id.System {
		return "", "", "on"
	}
	if id.IsPartner {
		return id.ProfileID, "true", ""
	}
	return id.ProfileID, "false", ""
}

// withTx runs fn inside a transaction that has the request identity applied as
// transaction-local GUCs, so RLS policies see it.
func (r *pgRepo) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful Commit
	profile, isPartner, system := gucValues(ctx)
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.current_profile', $1, true),
		        set_config('app.is_partner',      $2, true),
		        set_config('app.system',          $3, true)`,
		profile, isPartner, system); err != nil {
		return fmt.Errorf("store: postgres set identity: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *pgRepo) Upsert(ctx context.Context, doc types.Document) error {
	noslegal, metadata := marshalFacets(doc)
	ingested := doc.IngestedAt
	if ingested.IsZero() {
		ingested = time.Now()
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO documents
				(id, title, content, source, jurisdiction, document_type, owner_id,
				 practice_area, detected_client_number, noslegal_json, metadata_json, ingested_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT(id) DO UPDATE SET
				title=excluded.title, content=excluded.content, source=excluded.source,
				jurisdiction=excluded.jurisdiction, document_type=excluded.document_type,
				owner_id=excluded.owner_id, practice_area=excluded.practice_area,
				detected_client_number=excluded.detected_client_number,
				noslegal_json=excluded.noslegal_json, metadata_json=excluded.metadata_json,
				ingested_at=excluded.ingested_at`,
			doc.ID, doc.Title, doc.Content, doc.Source, doc.Jurisdiction, doc.DocumentType,
			doc.OwnerID, doc.PracticeArea, doc.DetectedClientNumber,
			noslegal, metadata, ingested.UTC())
		if err != nil {
			return fmt.Errorf("store: postgres upsert %s: %w", doc.ID, err)
		}
		return nil
	})
}

func (r *pgRepo) GetByID(ctx context.Context, id string) (*types.Document, bool, error) {
	var doc *types.Document
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pgDocColumns+` FROM documents WHERE id = $1`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			d, serr := scanPGDoc(rows)
			if serr != nil {
				return serr
			}
			doc = d
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: postgres get %s: %w", id, err)
	}
	return doc, doc != nil, nil
}

func (r *pgRepo) List(ctx context.Context) ([]types.Document, error) {
	var out []types.Document
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pgDocColumns+` FROM documents ORDER BY ingested_at ASC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, serr := scanPGDoc(rows)
			if serr != nil {
				return serr
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: postgres list: %w", err)
	}
	return out, nil
}

func (r *pgRepo) Delete(ctx context.Context, id string) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id)
		return err
	})
}

func (r *pgRepo) AddAttachment(ctx context.Context, a types.Attachment) error {
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO attachments (id, doc_id, owner_id, filename, media_type, kind, size, blob_key, page, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT(id) DO UPDATE SET
				doc_id=excluded.doc_id, owner_id=excluded.owner_id, filename=excluded.filename,
				media_type=excluded.media_type, kind=excluded.kind, size=excluded.size,
				blob_key=excluded.blob_key, page=excluded.page, created_at=excluded.created_at`,
			a.ID, a.DocID, a.OwnerID, a.Filename, a.MediaType, string(a.Kind), a.Size, a.BlobKey, a.Page, created.UTC())
		if err != nil {
			return fmt.Errorf("store: postgres add attachment %s: %w", a.ID, err)
		}
		return nil
	})
}

func (r *pgRepo) ListAttachments(ctx context.Context, docID string) ([]types.Attachment, error) {
	var out []types.Attachment
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pgAttColumns+` FROM attachments WHERE doc_id = $1 ORDER BY created_at ASC`, docID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, serr := scanPGAttachment(rows)
			if serr != nil {
				return serr
			}
			out = append(out, *a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: postgres list attachments: %w", err)
	}
	return out, nil
}

func (r *pgRepo) GetAttachment(ctx context.Context, id string) (*types.Attachment, bool, error) {
	var att *types.Attachment
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pgAttColumns+` FROM attachments WHERE id = $1`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			a, serr := scanPGAttachment(rows)
			if serr != nil {
				return serr
			}
			att = a
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: postgres get attachment %s: %w", id, err)
	}
	return att, att != nil, nil
}

func (r *pgRepo) DeleteAttachment(ctx context.Context, id string) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM attachments WHERE id = $1`, id)
		return err
	})
}

// ─── ReviewRepository ────────────────────────────────────────────────────────────

func (r *pgRepo) PutReview(ctx context.Context, id string, createdAt time.Time, payload []byte) error {
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO reviews (id, created_at, payload) VALUES ($1,$2,$3)
			ON CONFLICT(id) DO UPDATE SET
				created_at=excluded.created_at, payload=excluded.payload`,
			id, createdAt.UTC(), payload)
		if err != nil {
			return fmt.Errorf("store: postgres put review %s: %w", id, err)
		}
		return nil
	})
}

func (r *pgRepo) GetReview(ctx context.Context, id string) ([]byte, bool, error) {
	var payload []byte
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT payload FROM reviews WHERE id = $1`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			if serr := rows.Scan(&payload); serr != nil {
				return serr
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: postgres get review %s: %w", id, err)
	}
	return payload, payload != nil, nil
}

const pgAttColumns = `id, doc_id, owner_id, filename, media_type, kind, size, blob_key, page, created_at`

func scanPGAttachment(rows pgx.Rows) (*types.Attachment, error) {
	var a types.Attachment
	var kind string
	var created time.Time
	if err := rows.Scan(&a.ID, &a.DocID, &a.OwnerID, &a.Filename, &a.MediaType,
		&kind, &a.Size, &a.BlobKey, &a.Page, &created); err != nil {
		return nil, err
	}
	a.Kind = types.AttachmentKind(kind)
	a.CreatedAt = created
	return &a, nil
}

const pgDocColumns = `id, title, content, source, jurisdiction, document_type, owner_id,
	practice_area, detected_client_number, noslegal_json, metadata_json, ingested_at`

func scanPGDoc(rows pgx.Rows) (*types.Document, error) {
	var d types.Document
	var noslegal, metadata string
	var ingested time.Time
	if err := rows.Scan(&d.ID, &d.Title, &d.Content, &d.Source, &d.Jurisdiction,
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
	d.IngestedAt = ingested
	return &d, nil
}
