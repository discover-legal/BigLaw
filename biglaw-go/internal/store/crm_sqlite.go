// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ─── CRMRepository (sqlite) ──────────────────────────────────────────────────
// SQLite has no row-level security; CanAccessFirm is enforced in-process.

const sqliteCRMProfileCols = `id, client_id, external_ref, email, name, conflict_flag, created_at, updated_at`

func (r *sqliteRepo) PutCRMProfile(ctx context.Context, p CRMProfile) error {
	if !CanAccessFirm(ctx) {
		return fmt.Errorf("store: firm identity required")
	}
	created, updated := p.CreatedAt, p.UpdatedAt
	if created.IsZero() {
		created = time.Now()
	}
	if updated.IsZero() {
		updated = time.Now()
	}
	_, err := r.db.Exec(`
		INSERT INTO crm_profiles (`+sqliteCRMProfileCols+`) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			client_id=excluded.client_id, external_ref=excluded.external_ref,
			email=excluded.email, name=excluded.name,
			conflict_flag=excluded.conflict_flag, updated_at=excluded.updated_at`,
		p.ID, p.ClientID, p.ExternalRef, p.Email, p.Name, boolToInt(p.ConflictFlag),
		created.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: sqlite put crm profile %s: %w", p.ID, err)
	}
	return nil
}

func (r *sqliteRepo) getCRMProfileWhere(ctx context.Context, where string, arg string) (*CRMProfile, bool, error) {
	if !CanAccessFirm(ctx) {
		return nil, false, nil
	}
	row := r.db.QueryRow(`SELECT `+sqliteCRMProfileCols+` FROM crm_profiles WHERE `+where, arg)
	p, err := scanCRMProfileSQLite(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite get crm profile (%s): %w", where, err)
	}
	return p, true, nil
}

func (r *sqliteRepo) GetCRMProfile(ctx context.Context, id string) (*CRMProfile, bool, error) {
	return r.getCRMProfileWhere(ctx, `id = ?`, id)
}

func (r *sqliteRepo) FindCRMProfileByExternalRef(ctx context.Context, ref string) (*CRMProfile, bool, error) {
	if ref == "" {
		return nil, false, nil
	}
	return r.getCRMProfileWhere(ctx, `external_ref = ?`, ref)
}

func (r *sqliteRepo) FindCRMProfileByClientID(ctx context.Context, clientID string) (*CRMProfile, bool, error) {
	if clientID == "" {
		return nil, false, nil
	}
	return r.getCRMProfileWhere(ctx, `client_id = ?`, clientID)
}

func (r *sqliteRepo) ListCRMProfiles(ctx context.Context) ([]CRMProfile, error) {
	if !CanAccessFirm(ctx) {
		return nil, nil
	}
	rows, err := r.db.Query(`SELECT ` + sqliteCRMProfileCols + ` FROM crm_profiles ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite list crm profiles: %w", err)
	}
	defer rows.Close()
	var out []CRMProfile
	for rows.Next() {
		p, err := scanCRMProfileSQLite(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sqlite scan crm profile: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanCRMProfileSQLite(row rowScanner) (*CRMProfile, error) {
	var p CRMProfile
	var conflict int
	var created, updated string
	if err := row.Scan(&p.ID, &p.ClientID, &p.ExternalRef, &p.Email, &p.Name, &conflict, &created, &updated); err != nil {
		return nil, err
	}
	p.ConflictFlag = conflict != 0
	p.CreatedAt = parseSQLiteTime(created)
	p.UpdatedAt = parseSQLiteTime(updated)
	return &p, nil
}

const sqliteCRMFactCols = `id, profile_id, category, predicate, value, note, source, proposed_by,
	approver_role, status, decided_by, decision_note, superseded_by, created_at, decided_at`

func (r *sqliteRepo) PutCRMFact(ctx context.Context, f CRMFact) error {
	if !CanAccessFirm(ctx) {
		return fmt.Errorf("store: firm identity required")
	}
	created := f.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	decided := ""
	if f.DecidedAt != nil {
		decided = f.DecidedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := r.db.Exec(`
		INSERT INTO crm_facts (`+sqliteCRMFactCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			profile_id=excluded.profile_id, category=excluded.category,
			predicate=excluded.predicate, value=excluded.value, note=excluded.note,
			source=excluded.source, proposed_by=excluded.proposed_by,
			approver_role=excluded.approver_role, status=excluded.status,
			decided_by=excluded.decided_by, decision_note=excluded.decision_note,
			superseded_by=excluded.superseded_by, decided_at=excluded.decided_at`,
		f.ID, f.ProfileID, f.Category, f.Predicate, f.Value, f.Note, f.Source, f.ProposedBy,
		f.ApproverRole, f.Status, f.DecidedBy, f.DecisionNote, f.SupersededBy,
		created.UTC().Format(time.RFC3339Nano), decided)
	if err != nil {
		return fmt.Errorf("store: sqlite put crm fact %s: %w", f.ID, err)
	}
	return nil
}

func (r *sqliteRepo) GetCRMFact(ctx context.Context, id string) (*CRMFact, bool, error) {
	if !CanAccessFirm(ctx) {
		return nil, false, nil
	}
	row := r.db.QueryRow(`SELECT `+sqliteCRMFactCols+` FROM crm_facts WHERE id = ?`, id)
	f, err := scanCRMFactSQLite(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite get crm fact %s: %w", id, err)
	}
	return f, true, nil
}

func (r *sqliteRepo) ListCRMFacts(ctx context.Context, profileID, status string) ([]CRMFact, error) {
	if !CanAccessFirm(ctx) {
		return nil, nil
	}
	q := `SELECT ` + sqliteCRMFactCols + ` FROM crm_facts WHERE profile_id = ?`
	args := []interface{}{profileID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC`
	return r.queryCRMFacts(q, args...)
}

func (r *sqliteRepo) ListPendingCRMFacts(ctx context.Context, approverRole string) ([]CRMFact, error) {
	if !CanAccessFirm(ctx) {
		return nil, nil
	}
	return r.queryCRMFacts(`SELECT `+sqliteCRMFactCols+` FROM crm_facts
		WHERE status = 'pending' AND approver_role = ? ORDER BY created_at DESC`, approverRole)
}

func (r *sqliteRepo) queryCRMFacts(q string, args ...interface{}) ([]CRMFact, error) {
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite query crm facts: %w", err)
	}
	defer rows.Close()
	var out []CRMFact
	for rows.Next() {
		f, err := scanCRMFactSQLite(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sqlite scan crm fact: %w", err)
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func scanCRMFactSQLite(row rowScanner) (*CRMFact, error) {
	var f CRMFact
	var created, decided string
	if err := row.Scan(&f.ID, &f.ProfileID, &f.Category, &f.Predicate, &f.Value, &f.Note,
		&f.Source, &f.ProposedBy, &f.ApproverRole, &f.Status, &f.DecidedBy, &f.DecisionNote,
		&f.SupersededBy, &created, &decided); err != nil {
		return nil, err
	}
	f.CreatedAt = parseSQLiteTime(created)
	if decided != "" {
		t := parseSQLiteTime(decided)
		f.DecidedAt = &t
	}
	return &f, nil
}

// ─── IntakeRepository (sqlite) ───────────────────────────────────────────────

const sqliteIntakeCols = `id, external_id, profile_id, client_id, client_number, matter_number,
	title, document_type, matter_type, jurisdiction, summary, document_id, status, note,
	assigned_to, task_id, conflict, conflict_json, metadata_json, created_at, updated_at`

func (r *sqliteRepo) PutIntakeSubmission(ctx context.Context, s IntakeSubmission) error {
	if !CanAccessFirm(ctx) {
		return fmt.Errorf("store: firm identity required")
	}
	created, updated := s.CreatedAt, s.UpdatedAt
	if created.IsZero() {
		created = time.Now()
	}
	if updated.IsZero() {
		updated = time.Now()
	}
	_, err := r.db.Exec(`
		INSERT INTO intake_submissions (`+sqliteIntakeCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
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
		s.Status, s.Note, s.AssignedTo, s.TaskID, boolToInt(s.Conflict),
		string(s.ConflictJSON), string(s.MetadataJSON),
		created.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: sqlite put intake submission %s: %w", s.ID, err)
	}
	return nil
}

func (r *sqliteRepo) GetIntakeSubmission(ctx context.Context, id string) (*IntakeSubmission, bool, error) {
	if !CanAccessFirm(ctx) {
		return nil, false, nil
	}
	row := r.db.QueryRow(`SELECT `+sqliteIntakeCols+` FROM intake_submissions WHERE id = ?`, id)
	s, err := scanIntakeSQLite(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite get intake submission %s: %w", id, err)
	}
	return s, true, nil
}

func (r *sqliteRepo) FindIntakeSubmissionByExternalID(ctx context.Context, profileID, externalID string) (*IntakeSubmission, bool, error) {
	if !CanAccessFirm(ctx) || externalID == "" {
		return nil, false, nil
	}
	row := r.db.QueryRow(`SELECT `+sqliteIntakeCols+` FROM intake_submissions
		WHERE profile_id = ? AND external_id = ?`, profileID, externalID)
	s, err := scanIntakeSQLite(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: sqlite find intake submission: %w", err)
	}
	return s, true, nil
}

func (r *sqliteRepo) ListIntakeSubmissionsByProfile(ctx context.Context, profileID string) ([]IntakeSubmission, error) {
	if !CanAccessFirm(ctx) {
		return nil, nil
	}
	return r.queryIntake(`SELECT `+sqliteIntakeCols+` FROM intake_submissions
		WHERE profile_id = ? ORDER BY created_at DESC`, profileID)
}

func (r *sqliteRepo) ListIntakeSubmissions(ctx context.Context) ([]IntakeSubmission, error) {
	if !CanAccessFirm(ctx) {
		return nil, nil
	}
	return r.queryIntake(`SELECT ` + sqliteIntakeCols + ` FROM intake_submissions ORDER BY created_at DESC`)
}

func (r *sqliteRepo) queryIntake(q string, args ...interface{}) ([]IntakeSubmission, error) {
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: sqlite query intake submissions: %w", err)
	}
	defer rows.Close()
	var out []IntakeSubmission
	for rows.Next() {
		s, err := scanIntakeSQLite(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sqlite scan intake submission: %w", err)
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func scanIntakeSQLite(row rowScanner) (*IntakeSubmission, error) {
	var s IntakeSubmission
	var conflict int
	var conflictJSON, metadataJSON, created, updated string
	if err := row.Scan(&s.ID, &s.ExternalID, &s.ProfileID, &s.ClientID, &s.ClientNumber,
		&s.MatterNumber, &s.Title, &s.DocumentType, &s.MatterType, &s.Jurisdiction,
		&s.Summary, &s.DocumentID, &s.Status, &s.Note, &s.AssignedTo, &s.TaskID,
		&conflict, &conflictJSON, &metadataJSON, &created, &updated); err != nil {
		return nil, err
	}
	s.Conflict = conflict != 0
	if conflictJSON != "" {
		s.ConflictJSON = []byte(conflictJSON)
	}
	if metadataJSON != "" {
		s.MetadataJSON = []byte(metadataJSON)
	}
	s.CreatedAt = parseSQLiteTime(created)
	s.UpdatedAt = parseSQLiteTime(updated)
	return &s, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func parseSQLiteTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}
