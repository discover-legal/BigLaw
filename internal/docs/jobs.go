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

	"github.com/discover-legal/BigLaw/internal/store"
)

// leaseFor outlasts one transcription job (slotWait + childTimeout + visionWait = 22 minutes).
var leaseFor = 30 * time.Minute

// TranscribePending transcribes the documents stored as needs_transcription, oldest first, one at a
// time under a lease (two servers never take one; a job whose server died is taken again when its
// lease ends), until none is left or ctx ends. One its reading refuses (unreliable, no content,
// unsupported, too many pages, damaged) becomes failed, the reason in its notes, the original kept.
// Any other failure (the model endpoint, the database) ends the run with its error; the document is
// retried when its lease ends, and is failed after its third attempt (also when the server died
// during each of them: a fourth claim fails it without a call). Minimal on purpose: PR 4b
// moves it onto the engine's run lease (DESIGN revision 5, way 5).
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
		refused, note := false, strings.TrimPrefix(fmt.Sprint(err), "docs: ")
		for _, kind := range []error{ErrUnreliable, ErrNoContent, ErrScanEncoding, ErrTooManyPages, ErrUnreadable, ErrUnsupported} {
			refused = refused || errors.Is(err, kind)
		}
		switch {
		case err == nil:
			done++
			s.embedDoc(ctx, &d)
			continue
		case ctx.Err() != nil || !refused && attempts < 3:
			return done, failed, fmt.Errorf("docs: a waiting document's transcription failed (it is retried): %w", err)
		case !refused:
			note = fmt.Sprintf("Transcription failed %d times (the vision model or the database was unavailable, or the server stopped).", min(attempts, 3))
		}
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_, err = s.db.ExecContext(uctx, `UPDATE documents SET status = 'failed', lease_until = NULL, notes = ARRAY[$2::text] WHERE id = $1 AND status = 'needs_transcription'`, id, note)
		if cancel(); err != nil {
			return done, failed, fmt.Errorf("docs: transcription jobs: %w", err)
		}
		failed++
	}
	return done, failed, nil
}
