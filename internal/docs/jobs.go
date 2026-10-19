// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
)

// leaseFor outlasts one transcription job (2 × slotWait + childTimeout + visionWait = 32 minutes).
var leaseFor = 40 * time.Minute

// TranscribePending transcribes the documents stored as needs_transcription, oldest first, one at a
// time under a lease (a job whose server died is taken again when it ends), until none is left or ctx
// ends. One its reading refuses becomes failed, the reason in its notes. Any other failure ends the
// run; the document waits for the next with a note: a stop, a wrong model setting, a rate limit or
// busy readers spend none of its 3 attempts, an outage one (also a claim whose server died, so a
// fourth claim fails it without a call). Minimal on purpose: PR 4b moves it onto the run lease.
func (s *Service) TranscribePending(ctx context.Context) (done, failed int, err error) {
	for s.vision != nil {
		var id, fileID string
		var attempts int
		err := s.db.QueryRowContext(ctx, `UPDATE documents SET lease_until = now() + $1 * interval '1 millisecond', attempts = attempts + 1
			WHERE id = (SELECT id FROM documents WHERE status = 'needs_transcription' AND coalesce(lease_until, '-infinity') < now()
			ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id, file_id, attempts`, leaseFor.Milliseconds()).Scan(&id, &fileID, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			break
		} else if err != nil {
			return done, failed, fmt.Errorf("docs: transcription jobs: %w", err)
		}
		d, err := Document{}, errors.New("docs: its earlier attempts never ended (the server stopped during them)")
		if attempts <= 3 { // a document that takes its server down is not retried forever
			d, err = s.ingestFile(ctx, store.File{ID: fileID}, id)
		}
		if err == nil {
			done++
			s.embedDoc(ctx, &d)
			continue
		}
		refused, free, status, note := false, ctx.Err() != nil, "failed", strings.TrimPrefix(err.Error(), "docs: ")
		for i, kind := range []error{ErrUnreliable, ErrNoContent, ErrScanEncoding, ErrTooManyPages, ErrUnreadable, ErrUnsupported, model.ErrNotConfigured, model.ErrRateLimited, ErrBusy} {
			refused, free = refused || i < 6 && errors.Is(err, kind), free || i >= 6 && errors.Is(err, kind) // the last 3 say nothing about the document
		}
		if !refused && (free || attempts < 3) {
			status, note = "needs_transcription", "Waiting for transcription (its last attempt failed: "+note+"). The original is kept."
		} else if !refused {
			note = fmt.Sprintf("Transcription failed %d times (the vision model or the database was unavailable, or the server stopped).", min(attempts, 3))
		}
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_, uerr := s.db.ExecContext(uctx, `UPDATE documents SET status = $3, lease_until = NULL, notes = ARRAY[$2::text], attempts = attempts - $4::int
			WHERE id = $1 AND status = 'needs_transcription'`, id, note, status, map[bool]int{true: 1}[free])
		if cancel(); uerr != nil {
			return done, failed, fmt.Errorf("docs: transcription jobs: %w", uerr)
		} else if status != "failed" {
			return done, failed, fmt.Errorf("docs: a waiting document's transcription failed (it is retried at the next run): %w", err)
		}
		failed++
	}
	return done, failed, nil
}
