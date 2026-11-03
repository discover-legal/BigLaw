// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/lib/pq"
)

// Track changes (README § Track changes): a REST caller posts a change set against one of a task's
// documents (edits as find/replace with context, a compare against another version, or an accept
// or reject of the tracked changes already in the file); BigLaw locates every edit in the stored
// text at once (no file is opened), stores the set (migration 0013), and renders the .docx on each
// download in the bounded child, every edit a Word tracked change by the signed-in user. No model
// chooses a change: the caller does, and Go verifies the words are there.

// Change-set kinds and edit kinds.
const (
	KindEdits                 = "edits"
	KindCompare               = "compare"
	KindAccept                = "accept"
	KindReject                = "reject"
	KindInsertParagraph       = "insert_paragraph"
	KindInsertParagraphBefore = "insert_paragraph_before"
	KindDeleteParagraph       = "delete_paragraph"
)

// Bounds on one change set.
const (
	MaxEdits       = 500
	MaxEditBytes   = 8000 // find, replace, a paragraph
	maxEditContext = 200
	maxComment     = 2000
	changesPage    = 50
	revisionPage   = 200
)

// ChangeSet is one posted set of changes to one document, with what happened to each.
type ChangeSet struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"taskId"`
	DocumentID  string          `json:"documentId"`
	AgainstID   string          `json:"againstDocumentId,omitempty"` // compare: the newer version
	Kind        string          `json:"kind"`                        // edits, compare, accept, reject
	Author      string          `json:"author"`                      // the tracked changes' author: the signed-in user's name
	CreatedBy   json.RawMessage `json:"createdBy"`
	Edits       []Edit          `json:"edits"`
	RevisionIDs []int           `json:"revisionIds"` // accept, reject: the changes decided; empty: all
	Outcomes    []Outcome       `json:"outcomes"`    // one per edit; accept/reject: one per decided revision after a render
	CreatedAt   time.Time       `json:"createdAt"`
}

// Edit is one posted change, as the caller wrote it. Nothing in it is an offset. Kind "" is a
// substitution of Find (or an insertion, Find empty) by Replace (deletion: Replace empty), placed by
// Before and After (about 40 characters each) when Find recurs, inside Paragraph when given;
// insert_paragraph puts Replace as a new paragraph after Paragraph, insert_paragraph_before before it,
// delete_paragraph deletes Paragraph. Comment adds a Word comment on the change.
type Edit struct {
	Kind      string `json:"kind,omitempty"`
	Find      string `json:"find,omitempty"`
	Replace   string `json:"replace,omitempty"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
	Paragraph string `json:"paragraph,omitempty"`
	Comment   string `json:"comment,omitempty"`
}

// Outcome is what the tool did with an edit: located (or not_found, ambiguous, crosses_paragraph,
// overlaps) at POST, then applied or failed at render. Start and End are bytes into the document's
// text, for the caller's eyes; a render locates every edit again against the file.
type Outcome struct {
	Status string `json:"status"`
	Method string `json:"method,omitempty"` // exact, tolerant, context
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Reason string `json:"reason,omitempty"`
}

// ChangeIn is POST /tasks/{id}/changes's body.
type ChangeIn struct {
	DocumentID        string `json:"documentId"`
	Kind              string `json:"kind"`
	AgainstDocumentID string `json:"againstDocumentId"`
	Edits             []Edit `json:"edits"`
	RevisionIDs       []int  `json:"revisionIds"`
}

// ChangeSetPage is one page of a task's change sets, newest first.
type ChangeSetPage struct {
	ChangeSets []ChangeSet `json:"changeSets"`
	Next       *int        `json:"next"`
}

// ErrNotDocx: the document's file is not a .docx (text, a PDF, an image): nothing to track changes in.
var ErrNotDocx = errors.New("docs: the document is not a .docx, so no tracked change can be written into it")

// CreateChangeSet validates and locates in (every edit against the stored text; a compare's
// edits are the word-level difference of the document to the newer version), stores the set for
// task and returns it with an outcome per edit. The document (and the other version) must be in
// scope (ErrNotFound otherwise) and a .docx (ErrNotDocx). author is the tracked changes' author;
// by is who, as JSON.
func (s *Service) CreateChangeSet(ctx context.Context, scope []string, taskID, author string, by json.RawMessage, in ChangeIn) (ChangeSet, error) {
	if err := in.check(); err != nil {
		return ChangeSet{}, err
	}
	d, err := s.docx(ctx, scope, in.DocumentID)
	if err != nil {
		return ChangeSet{}, err
	}
	cs := ChangeSet{TaskID: taskID, DocumentID: d.ID, Kind: in.Kind, Author: author, CreatedBy: by, Edits: []Edit{}, RevisionIDs: []int{}, Outcomes: []Outcome{}}
	if cs.CreatedBy == nil {
		cs.CreatedBy = json.RawMessage("null")
	}
	switch in.Kind {
	case KindEdits:
		cs.Edits = in.Edits
	case KindCompare:
		against, err := s.docx(ctx, scope, in.AgainstDocumentID)
		if err != nil {
			return ChangeSet{}, err
		}
		cs.AgainstID, cs.Edits = against.ID, compareTexts(d.Text, against.Text)
		if cs.Edits == nil {
			cs.Edits = []Edit{}
		}
	default:
		cs.RevisionIDs = in.RevisionIDs
	}
	cs.Outcomes = locateAll(d.Text, cs.Edits)
	edits, _ := json.Marshal(cs.Edits)
	outcomes, _ := json.Marshal(cs.Outcomes)
	var against sql.NullString
	if cs.AgainstID != "" {
		against = sql.NullString{String: cs.AgainstID, Valid: true}
	}
	err = s.db.QueryRowContext(ctx, `INSERT INTO change_sets (task_id, document_id, against_id, kind, author, created_by, edits, revision_ids, outcomes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id, created_at`, taskID, cs.DocumentID, against, cs.Kind, cs.Author, []byte(cs.CreatedBy), edits, pq.Array(append([]int{}, cs.RevisionIDs...)), outcomes).
		Scan(&cs.ID, &cs.CreatedAt)
	if err != nil {
		return ChangeSet{}, fmt.Errorf("docs: change set: %w", err)
	}
	return cs, nil
}

// check validates the body's shape and bounds.
func (in ChangeIn) check() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrBadInput, fmt.Sprintf(format, a...))
	}
	switch in.Kind {
	case KindEdits:
		if len(in.Edits) == 0 {
			return bad("edits: at least one edit")
		}
		if len(in.Edits) > MaxEdits {
			return bad("edits: at most %d", MaxEdits)
		}
		if in.AgainstDocumentID != "" || len(in.RevisionIDs) > 0 {
			return bad("againstDocumentId and revisionIds belong to compare, accept and reject")
		}
	case KindCompare:
		if in.AgainstDocumentID == "" {
			return bad("againstDocumentId: the newer version to compare the document with")
		}
		if len(in.Edits) > 0 || len(in.RevisionIDs) > 0 {
			return bad("edits and revisionIds belong to edits, accept and reject")
		}
	case KindAccept, KindReject:
		if len(in.Edits) > 0 || in.AgainstDocumentID != "" {
			return bad("edits and againstDocumentId belong to edits and compare")
		}
		if len(in.RevisionIDs) > MaxEdits {
			return bad("revisionIds: at most %d", MaxEdits)
		}
		for _, id := range in.RevisionIDs {
			if id < 0 {
				return bad("revisionIds: a tracked change's id as /revisions lists it")
			}
		}
	default:
		return bad("kind: edits, compare, accept or reject")
	}
	for i, e := range in.Edits {
		switch e.Kind {
		case "":
			if e.Find == "" && e.Replace == "" {
				return bad("edits[%d]: find and replace are both empty", i)
			}
			if e.Find == "" && e.Before == "" && e.After == "" {
				return bad("edits[%d]: an insertion (no find) needs before or after context to place it", i)
			}
		case KindInsertParagraph, KindInsertParagraphBefore:
			if e.Replace == "" || e.Paragraph == "" {
				return bad("edits[%d]: %s needs paragraph (the anchor) and replace (the new paragraph)", i, e.Kind)
			}
		case KindDeleteParagraph:
			if e.Paragraph == "" {
				return bad("edits[%d]: delete_paragraph needs paragraph", i)
			}
		default:
			return bad("edits[%d]: kind: \"\" (a substitution), insert_paragraph, insert_paragraph_before or delete_paragraph", i)
		}
		for name, v := range map[string]struct {
			s   string
			max int
		}{"find": {e.Find, MaxEditBytes}, "replace": {e.Replace, MaxEditBytes}, "paragraph": {e.Paragraph, MaxEditBytes}, "before": {e.Before, maxEditContext}, "after": {e.After, maxEditContext}, "comment": {e.Comment, maxComment}} {
			if len(v.s) > v.max || strings.ContainsRune(v.s, 0) {
				return bad("edits[%d].%s: at most %d bytes, no NUL", i, name, v.max)
			}
		}
		if strings.Contains(e.Find, "\n") || strings.Contains(e.Replace, "\n") && e.Kind == "" {
			return bad("edits[%d]: find and replace hold no paragraph break: send one edit per paragraph, or a paragraph edit", i)
		}
	}
	return nil
}

// docx is the document in scope, which must have a .docx file.
func (s *Service) docx(ctx context.Context, scope []string, id string) (Document, error) {
	d, err := s.Get(ctx, scope, id)
	if err != nil {
		return Document{}, err
	}
	if d.ContentType != docxType || d.FileID == "" {
		return Document{}, ErrNotDocx
	}
	return d, nil
}

// locateAll locates every edit in text, then marks a located edit whose span overlaps an earlier
// one's as overlaps (the first wins; two edits of one span would be applied twice).
func locateAll(text string, edits []Edit) []Outcome {
	nt := NewText(text)
	out := make([]Outcome, len(edits))
	for i, e := range edits {
		out[i] = locate(nt, e)
	}
	for i := range out {
		if out[i].Status != StatusLocated || edits[i].Kind != "" {
			continue
		}
		for j := range i {
			if out[j].Status != StatusLocated || edits[j].Kind != "" {
				continue
			}
			if a, b := out[i], out[j]; a.Start < b.End && b.Start < a.End || a.Start == b.Start && a.End == b.End {
				out[i] = Outcome{Status: StatusOverlaps, Method: a.Method, Start: a.Start, End: a.End, Reason: fmt.Sprintf("its text overlaps edit %d's", j)}
				break
			}
		}
	}
	return out
}

// ChangeSets pages a task's change sets, newest first.
func (s *Service) ChangeSets(ctx context.Context, taskID string, offset int) (ChangeSetPage, error) {
	page := ChangeSetPage{ChangeSets: []ChangeSet{}}
	if !store.ValidID(taskID) {
		return page, ErrNotFound
	}
	css, err := s.changeSets(ctx, `task_id = $1 ORDER BY created_at DESC, id LIMIT $2 OFFSET $3`, taskID, changesPage+1, max(offset, 0))
	if err != nil {
		return page, err
	}
	if len(css) > changesPage {
		next := offset + changesPage
		page.Next, css = &next, css[:changesPage]
	}
	page.ChangeSets = css
	return page, nil
}

// ChangeSet is one of a task's change sets.
func (s *Service) ChangeSet(ctx context.Context, taskID, id string) (ChangeSet, error) {
	if !store.ValidID(taskID) || !store.ValidID(id) {
		return ChangeSet{}, ErrNotFound
	}
	css, err := s.changeSets(ctx, `task_id = $1 AND id = $2`, taskID, id)
	if err != nil {
		return ChangeSet{}, err
	}
	if len(css) == 0 {
		return ChangeSet{}, ErrNotFound
	}
	return css[0], nil
}

func (s *Service) changeSets(ctx context.Context, where string, args ...any) ([]ChangeSet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, document_id, against_id, kind, author, created_by, edits, revision_ids, outcomes, created_at FROM change_sets WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("docs: change sets: %w", err)
	}
	defer rows.Close()
	var out []ChangeSet
	for rows.Next() {
		var cs ChangeSet
		var against sql.NullString
		var by, edits, outcomes []byte
		if err := rows.Scan(&cs.ID, &cs.TaskID, &cs.DocumentID, &against, &cs.Kind, &cs.Author, &by, &edits, pq.Array(&cs.RevisionIDs), &outcomes, &cs.CreatedAt); err != nil {
			return nil, fmt.Errorf("docs: change sets: %w", err)
		}
		cs.AgainstID, cs.CreatedBy = against.String, json.RawMessage(by)
		if err := errors.Join(json.Unmarshal(edits, &cs.Edits), json.Unmarshal(outcomes, &cs.Outcomes)); err != nil {
			return nil, fmt.Errorf("docs: change sets: %w", err)
		}
		if cs.Edits == nil {
			cs.Edits = []Edit{}
		}
		if cs.Outcomes == nil {
			cs.Outcomes = []Outcome{}
		}
		if cs.RevisionIDs == nil {
			cs.RevisionIDs = []int{}
		}
		out = append(out, cs)
	}
	return out, rows.Err()
}

// RenderChangeSet writes the change set's .docx to w: the document's file read and verified from
// the store (a read slot), the changes applied in the child (an ingest slot, as Extract), head
// called with the download name and size before the first byte. The outcomes on the row are
// updated to what the render did and returned. A file whose text no longer equals the stored text
// renders nothing and marks every edit failed (text_drift).
func (s *Service) RenderChangeSet(ctx context.Context, scope []string, taskID, id string, head func(name string, size int64) error, w io.Writer) (ChangeSet, error) {
	cs, err := s.ChangeSet(ctx, taskID, id)
	if err != nil {
		return cs, err
	}
	d, err := s.docx(ctx, scope, cs.DocumentID)
	if err != nil {
		return cs, err
	}
	var file bytes.Buffer
	var f store.File
	if _, err := s.st.ReadFile(ctx, d.FileID, func(x store.File) error { f = x; return nil }, &file); err != nil {
		return cs, err
	}
	req := docxRequest{Verb: cs.Kind, Author: cs.Author, Date: cs.CreatedAt.UTC().Format(time.RFC3339), Text: d.Text, Edits: cs.Edits, RevisionIDs: cs.RevisionIDs}
	if err := take(ctx, s.slots); err != nil {
		return cs, err
	}
	res, out, err := docxInChild(ctx, req, file.Bytes())
	<-s.slots
	if err != nil {
		return cs, err
	}
	cs.Outcomes = res.Outcomes
	if cs.Outcomes == nil {
		cs.Outcomes = []Outcome{}
	}
	outcomes, _ := json.Marshal(cs.Outcomes)
	if _, err := s.db.ExecContext(ctx, `UPDATE change_sets SET outcomes = $2 WHERE id = $1`, cs.ID, outcomes); err != nil {
		return cs, fmt.Errorf("docs: change set: %w", err)
	}
	name := strings.TrimSuffix(f.Name, ".docx") + ".changes.docx"
	if err := head(name, int64(len(out))); err != nil {
		return cs, err
	}
	_, err = w.Write(out)
	return cs, err
}

// RevisionsPage is one page of a document's tracked changes.
type RevisionsPage struct {
	Revisions []docxRevision `json:"revisions"`
	Next      *int           `json:"next"`
}

// Revisions pages the tracked changes already in a document's file (read in the child), in
// document order.
func (s *Service) Revisions(ctx context.Context, scope []string, docID string, offset int) (RevisionsPage, error) {
	page := RevisionsPage{Revisions: []docxRevision{}}
	d, err := s.docx(ctx, scope, docID)
	if err != nil {
		return page, err
	}
	var file bytes.Buffer
	if _, err := s.st.ReadFile(ctx, d.FileID, nil, &file); err != nil {
		return page, err
	}
	if err := take(ctx, s.slots); err != nil {
		return page, err
	}
	res, _, err := docxInChild(ctx, docxRequest{Verb: "revisions"}, file.Bytes())
	<-s.slots
	if err != nil {
		return page, err
	}
	revs := res.Revisions
	offset = min(max(offset, 0), len(revs))
	if len(revs) > offset+revisionPage {
		next := offset + revisionPage
		page.Next, revs = &next, revs[:next]
	}
	page.Revisions = append(page.Revisions, revs[offset:]...)
	return page, nil
}
