// SPDX-License-Identifier: Apache-2.0
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

func (r *pgRepo) PutReview(ctx context.Context, id, ownerID, matterNumber string, createdAt time.Time, payload []byte) error {
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO reviews (id, owner_id, matter_number, created_at, payload) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT(id) DO UPDATE SET
				owner_id=excluded.owner_id, matter_number=excluded.matter_number,
				created_at=excluded.created_at, payload=excluded.payload`,
			id, ownerID, matterNumber, createdAt.UTC(), payload)
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

// ─── VersionRepository ───────────────────────────────────────────────────────────

func (r *pgRepo) PutVersion(ctx context.Context, v DocumentVersion) error {
	created := v.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO document_versions
				(id, owner_id, matter_number, lineage_id, parent_id, round, source, author, created_at, path, content_hash, text, decisions_json)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT(id) DO UPDATE SET
				owner_id=excluded.owner_id, matter_number=excluded.matter_number,
				lineage_id=excluded.lineage_id, parent_id=excluded.parent_id, round=excluded.round,
				source=excluded.source, author=excluded.author, created_at=excluded.created_at,
				path=excluded.path, content_hash=excluded.content_hash, text=excluded.text,
				decisions_json=excluded.decisions_json`,
			v.ID, v.OwnerID, v.MatterNumber, v.LineageID, v.ParentID, v.Round, v.Source, v.Author,
			created.UTC(), v.Path, v.ContentHash, v.Text, string(v.Decisions))
		if err != nil {
			return fmt.Errorf("store: postgres put version %s: %w", v.ID, err)
		}
		return nil
	})
}

func (r *pgRepo) GetVersion(ctx context.Context, id string) (*DocumentVersion, bool, error) {
	return r.findVersionWhere(ctx, `id = $1`, id)
}

func (r *pgRepo) ListLineage(ctx context.Context, lineageID string) ([]DocumentVersion, error) {
	var out []DocumentVersion
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pgVerColumns+
			` FROM document_versions WHERE lineage_id = $1 ORDER BY round ASC, created_at ASC`, lineageID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, serr := scanPGVersion(rows)
			if serr != nil {
				return serr
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: postgres list lineage: %w", err)
	}
	return out, nil
}

func (r *pgRepo) FindVersionByHash(ctx context.Context, contentHash string) (*DocumentVersion, bool, error) {
	return r.findVersionWhere(ctx, `content_hash = $1 ORDER BY created_at DESC, round DESC`, contentHash)
}

func (r *pgRepo) FindVersionByPath(ctx context.Context, path string) (*DocumentVersion, bool, error) {
	return r.findVersionWhere(ctx, `path = $1 ORDER BY created_at DESC, round DESC`, path)
}

func (r *pgRepo) findVersionWhere(ctx context.Context, where, value string) (*DocumentVersion, bool, error) {
	var ver *DocumentVersion
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pgVerColumns+` FROM document_versions WHERE `+where+` LIMIT 1`, value)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			v, serr := scanPGVersion(rows)
			if serr != nil {
				return serr
			}
			ver = v
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: postgres find version: %w", err)
	}
	return ver, ver != nil, nil
}

const pgVerColumns = `id, owner_id, matter_number, lineage_id, parent_id, round, source, author, created_at, path, content_hash, text, decisions_json`

func scanPGVersion(rows pgx.Rows) (*DocumentVersion, error) {
	var v DocumentVersion
	var created time.Time
	var decisions string
	if err := rows.Scan(&v.ID, &v.OwnerID, &v.MatterNumber, &v.LineageID, &v.ParentID, &v.Round, &v.Source, &v.Author,
		&created, &v.Path, &v.ContentHash, &v.Text, &decisions); err != nil {
		return nil, err
	}
	v.CreatedAt = created
	if decisions != "" {
		v.Decisions = []byte(decisions)
	}
	return &v, nil
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
