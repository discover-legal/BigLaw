// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ─── CRMRepository (postgres) ────────────────────────────────────────────────
// Row visibility is enforced by the crm_* RLS policies (any authenticated firm
// principal); withTx projects the ctx identity into the transaction GUCs.

const pgCRMProfileCols = `id, client_id, external_ref, email, name, conflict_flag, created_at, updated_at`

func (r *pgRepo) PutCRMProfile(ctx context.Context, p CRMProfile) error {
	created, updated := p.CreatedAt, p.UpdatedAt
	if created.IsZero() {
		created = time.Now()
	}
	if updated.IsZero() {
		updated = time.Now()
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO crm_profiles (`+pgCRMProfileCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT(id) DO UPDATE SET
				client_id=excluded.client_id, external_ref=excluded.external_ref,
				email=excluded.email, name=excluded.name,
				conflict_flag=excluded.conflict_flag, updated_at=excluded.updated_at`,
			p.ID, p.ClientID, p.ExternalRef, p.Email, p.Name, p.ConflictFlag,
			created.UTC(), updated.UTC())
		if err != nil {
			return fmt.Errorf("store: postgres put crm profile %s: %w", p.ID, err)
		}
		return nil
	})
}

func (r *pgRepo) getCRMProfileWhere(ctx context.Context, where string, arg string) (*CRMProfile, bool, error) {
	var p CRMProfile
	found := false
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pgCRMProfileCols+` FROM crm_profiles WHERE `+where, arg)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			if err := scanCRMProfilePG(rows, &p); err != nil {
				return err
			}
			found = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, fmt.Errorf("store: postgres get crm profile (%s): %w", where, err)
	}
	if !found {
		return nil, false, nil
	}
	return &p, true, nil
}

func (r *pgRepo) GetCRMProfile(ctx context.Context, id string) (*CRMProfile, bool, error) {
	return r.getCRMProfileWhere(ctx, `id = $1`, id)
}

func (r *pgRepo) FindCRMProfileByExternalRef(ctx context.Context, ref string) (*CRMProfile, bool, error) {
	if ref == "" {
		return nil, false, nil
	}
	return r.getCRMProfileWhere(ctx, `external_ref = $1`, ref)
}

func (r *pgRepo) FindCRMProfileByClientID(ctx context.Context, clientID string) (*CRMProfile, bool, error) {
	if clientID == "" {
		return nil, false, nil
	}
	return r.getCRMProfileWhere(ctx, `client_id = $1`, clientID)
}

func (r *pgRepo) ListCRMProfiles(ctx context.Context) ([]CRMProfile, error) {
	var out []CRMProfile
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pgCRMProfileCols+` FROM crm_profiles ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p CRMProfile
			if err := scanCRMProfilePG(rows, &p); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: postgres list crm profiles: %w", err)
	}
	return out, nil
}

func scanCRMProfilePG(rows pgx.Rows, p *CRMProfile) error {
	return rows.Scan(&p.ID, &p.ClientID, &p.ExternalRef, &p.Email, &p.Name,
		&p.ConflictFlag, &p.CreatedAt, &p.UpdatedAt)
}

const pgCRMFactCols = `id, profile_id, category, predicate, value, note, source, proposed_by,
	approver_role, status, decided_by, decision_note, superseded_by, created_at, decided_at`

func (r *pgRepo) PutCRMFact(ctx context.Context, f CRMFact) error {
	created := f.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	var decided *time.Time
	if f.DecidedAt != nil {
		t := f.DecidedAt.UTC()
		decided = &t
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO crm_facts (`+pgCRMFactCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT(id) DO UPDATE SET
				profile_id=excluded.profile_id, category=excluded.category,
				predicate=excluded.predicate, value=excluded.value, note=excluded.note,
				source=excluded.source, proposed_by=excluded.proposed_by,
				approver_role=excluded.approver_role, status=excluded.status,
				decided_by=excluded.decided_by, decision_note=excluded.decision_note,
				superseded_by=excluded.superseded_by, decided_at=excluded.decided_at`,
			f.ID, f.ProfileID, f.Category, f.Predicate, f.Value, f.Note, f.Source,
			f.ProposedBy, f.ApproverRole, f.Status, f.DecidedBy, f.DecisionNote,
			f.SupersededBy, created.UTC(), decided)
		if err != nil {
			return fmt.Errorf("store: postgres put crm fact %s: %w", f.ID, err)
		}
		return nil
	})
}

func (r *pgRepo) GetCRMFact(ctx context.Context, id string) (*CRMFact, bool, error) {
	facts, err := r.queryCRMFacts(ctx, `SELECT `+pgCRMFactCols+` FROM crm_facts WHERE id = $1`, id)
	if err != nil {
		return nil, false, err
	}
	if len(facts) == 0 {
		return nil, false, nil
	}
	return &facts[0], true, nil
}

func (r *pgRepo) ListCRMFacts(ctx context.Context, profileID, status string) ([]CRMFact, error) {
	if status != "" {
		return r.queryCRMFacts(ctx, `SELECT `+pgCRMFactCols+` FROM crm_facts
			WHERE profile_id = $1 AND status = $2 ORDER BY created_at DESC`, profileID, status)
	}
	return r.queryCRMFacts(ctx, `SELECT `+pgCRMFactCols+` FROM crm_facts
		WHERE profile_id = $1 ORDER BY created_at DESC`, profileID)
}

func (r *pgRepo) ListPendingCRMFacts(ctx context.Context, approverRole string) ([]CRMFact, error) {
	return r.queryCRMFacts(ctx, `SELECT `+pgCRMFactCols+` FROM crm_facts
		WHERE status = 'pending' AND approver_role = $1 ORDER BY created_at DESC`, approverRole)
}

func (r *pgRepo) queryCRMFacts(ctx context.Context, q string, args ...interface{}) ([]CRMFact, error) {
	var out []CRMFact
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f CRMFact
			var decided *time.Time
			if err := rows.Scan(&f.ID, &f.ProfileID, &f.Category, &f.Predicate, &f.Value,
				&f.Note, &f.Source, &f.ProposedBy, &f.ApproverRole, &f.Status, &f.DecidedBy,
				&f.DecisionNote, &f.SupersededBy, &f.CreatedAt, &decided); err != nil {
				return err
			}
			f.DecidedAt = decided
			out = append(out, f)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: postgres query crm facts: %w", err)
	}
	return out, nil
}

// ─── IntakeRepository (postgres) ─────────────────────────────────────────────

const pgIntakeCols = `id, external_id, profile_id, client_id, client_number, matter_number,
	title, document_type, matter_type, jurisdiction, summary, document_id, status, note,
	assigned_to, task_id, conflict, conflict_json, metadata_json, created_at, updated_at`

func (r *pgRepo) PutIntakeSubmission(ctx context.Context, s IntakeSubmission) error {
	created, updated := s.CreatedAt, s.UpdatedAt
	if created.IsZero() {
		created = time.Now()
	}
	if updated.IsZero() {
		updated = time.Now()
	}
	conflictJSON := s.ConflictJSON
	if len(conflictJSON) == 0 {
		conflictJSON = []byte(`{}`)
	}
	metadataJSON := s.MetadataJSON
	if len(metadataJSON) == 0 {
		metadataJSON = []byte(`{}`)
	}
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO intake_submissions (`+pgIntakeCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
			ON CONFLICT(id) DO UPDATE SET
				external_id=excluded.external_id, profile_id=excluded.profile_id,
				client_id=excluded.client_id, client_number=excluded.client_number,
				matter_number=excluded.matter_number, title=excluded.title,
				document_type=excluded.document_type, matter_type=excluded.matter_type,
				jurisdiction=excluded.jurisdiction, summary=excluded.summary,
				document_id=excluded.document_id, status=excluded.status, note=excluded.note,
				assigned_to=excluded.assigned_to, task_id=excluded.task_id,
				conflict=excluded.conflict, conflict_json=excluded.conflict_json,
				metadata_json=excluded.metadata_json, updated_at=excluded.updated_at`,
			s.ID, s.ExternalID, s.ProfileID, s.ClientID, s.ClientNumber, s.MatterNumber,
			s.Title, s.DocumentType, s.MatterType, s.Jurisdiction, s.Summary, s.DocumentID,
			s.Status, s.Note, s.AssignedTo, s.TaskID, s.Conflict, conflictJSON, metadataJSON,
			created.UTC(), updated.UTC())
		if err != nil {
			return fmt.Errorf("store: postgres put intake submission %s: %w", s.ID, err)
		}
		return nil
	})
}

func (r *pgRepo) GetIntakeSubmission(ctx context.Context, id string) (*IntakeSubmission, bool, error) {
	subs, err := r.queryIntake(ctx, `SELECT `+pgIntakeCols+` FROM intake_submissions WHERE id = $1`, id)
	if err != nil {
		return nil, false, err
	}
	if len(subs) == 0 {
		return nil, false, nil
	}
	return &subs[0], true, nil
}

func (r *pgRepo) FindIntakeSubmissionByExternalID(ctx context.Context, profileID, externalID string) (*IntakeSubmission, bool, error) {
	if externalID == "" {
		return nil, false, nil
	}
	subs, err := r.queryIntake(ctx, `SELECT `+pgIntakeCols+` FROM intake_submissions
		WHERE profile_id = $1 AND external_id = $2`, profileID, externalID)
	if err != nil {
		return nil, false, err
	}
	if len(subs) == 0 {
		return nil, false, nil
	}
	return &subs[0], true, nil
}

func (r *pgRepo) ListIntakeSubmissionsByProfile(ctx context.Context, profileID string) ([]IntakeSubmission, error) {
	return r.queryIntake(ctx, `SELECT `+pgIntakeCols+` FROM intake_submissions
		WHERE profile_id = $1 ORDER BY created_at DESC`, profileID)
}

func (r *pgRepo) ListIntakeSubmissions(ctx context.Context) ([]IntakeSubmission, error) {
	return r.queryIntake(ctx, `SELECT `+pgIntakeCols+` FROM intake_submissions ORDER BY created_at DESC`)
}

func (r *pgRepo) queryIntake(ctx context.Context, q string, args ...interface{}) ([]IntakeSubmission, error) {
	var out []IntakeSubmission
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s IntakeSubmission
			if err := rows.Scan(&s.ID, &s.ExternalID, &s.ProfileID, &s.ClientID, &s.ClientNumber,
				&s.MatterNumber, &s.Title, &s.DocumentType, &s.MatterType, &s.Jurisdiction,
				&s.Summary, &s.DocumentID, &s.Status, &s.Note, &s.AssignedTo, &s.TaskID,
				&s.Conflict, &s.ConflictJSON, &s.MetadataJSON, &s.CreatedAt, &s.UpdatedAt); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: postgres query intake submissions: %w", err)
	}
	return out, nil
}
