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
	"github.com/lib/pq"
)

const embedBatch = 64 // chunks per embeddings call

// embedWait bounds embedding one document (then it is lexical only, with EmbedNote) and
// queryWait one query's embedding, whatever the endpoint does (each call may retry for minutes).
var embedWait, queryWait = 10 * time.Minute, 30 * time.Second

// EmbedNote marks a document whose embedding failed or was cancelled: search over it is lexical
// only until Backfill embeds it.
const EmbedNote = "Search over this document is lexical only: embedding its chunks failed or was cancelled. " +
	"It is embedded again when the server next starts."

// UseEmbedder turns on hybrid search with e, the embed role's model modelID (call it before use):
// every chunk is embedded at ingest, and every query.
func (s *Service) UseEmbedder(e model.Embedder, modelID string) {
	s.embedder, s.embedModel = e, modelID
}

// Hybrid reports whether search fuses a vector ranking with the lexical one.
func (s *Service) Hybrid() bool { return s.embedder != nil }

// embedDoc embeds a new document; a failure leaves it lexical only with EmbedNote, never failing
// the ingest that stored it.
func (s *Service) embedDoc(ctx context.Context, d *Document) {
	if s.embedder == nil || d.Chunks == 0 {
		return
	}
	ectx, cancel := context.WithTimeout(ctx, embedWait)
	u, err := s.embed(ectx, d.ID)
	if cancel(); err == nil {
		d.EmbedModel, d.EmbedTokens, d.EmbedCostUSD = s.embedModel, int64(u.InputTokens), u.CostUSD
		return
	}
	d.Notes = append(d.Notes, EmbedNote)
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, _ = s.db.ExecContext(nctx, `UPDATE documents SET notes = array_append(notes, $2) WHERE id = $1 AND NOT $2 = ANY(notes)`, d.ID, EmbedNote)
}

// embed embeds document id's chunks (section path and search text, at most 4,096 characters),
// embedBatch per call, and only then marks it embedded by the embed model with the calls' usage,
// so a half-embedded document is never searched by vector. An unpriced call makes the cost
// unknown (nil), never $0.
func (s *Service) embed(ctx context.Context, id string) (model.Usage, error) {
	total := model.Usage{CostUSD: new(float64)}
	for after := int64(0); ; {
		var ids []int64
		var texts, vecs []string
		if err := s.db.QueryRowContext(ctx, `SELECT coalesce(array_agg(id ORDER BY id), '{}'), coalesce(array_agg(t ORDER BY id), '{}') FROM
			(SELECT id, left(locator || ' ' || coalesce(nullif(search_text, ''), body), $4) AS t FROM chunks
			WHERE document_id = $1 AND id > $2 ORDER BY id LIMIT $3) b`, id, after, embedBatch, model.MaxEmbedInput/4).Scan(pq.Array(&ids), pq.Array(&texts)); err != nil {
			return total, err
		}
		if len(ids) == 0 {
			_, err := s.db.ExecContext(ctx, `UPDATE documents SET embed_model = $2, embed_tokens = $3, embed_cost_usd = $4,
				notes = array_remove(notes, $5) WHERE id = $1`, id, s.embedModel, total.InputTokens, total.CostUSD, EmbedNote)
			return total, err
		}
		after = ids[len(ids)-1]
		e, err := s.embedder.Embed(ctx, model.EmbedRequest{Agent: "ingest", Inputs: texts})
		if err != nil {
			return total, err
		}
		if total.InputTokens += e.Usage.InputTokens; total.CostUSD != nil && e.Usage.CostUSD != nil {
			*total.CostUSD += *e.Usage.CostUSD
		} else {
			total.CostUSD = nil
		}
		for _, v := range e.Vectors {
			vecs = append(vecs, vectorText(v))
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE chunks c SET embedding = v.e::vector FROM unnest($1::bigint[], $2::text[]) v(id, e)
			WHERE c.id = v.id AND c.document_id = $3`, pq.Array(ids), pq.Array(vecs), id); err != nil {
			return total, err
		}
	}
}

// vectorText is v in pgvector's text form, [1,2.5,…] (fmt prints a float32's shortest form).
func vectorText(v []float32) string { return strings.ReplaceAll(fmt.Sprint(v), " ", ",") }

// Backfill embeds, one at a time, every document with chunks that the embed model has not
// embedded (stored before the role was set, under another model, or whose embedding failed). It
// skips documents stored after it starts (their ingest embeds them), stops when ctx ends or after
// 3 failures in a row (the endpoint is likely down), and returns how many it embedded.
func (s *Service) Backfill(ctx context.Context) (int, error) {
	start, done, failed := time.Now(), 0, 0
	for last := ""; s.embedder != nil && failed < 3; {
		err := s.db.QueryRowContext(ctx, `SELECT d.id FROM documents d WHERE d.embed_model IS DISTINCT FROM $1 AND d.created_at < $2
			AND d.id::text > $3 AND EXISTS (SELECT 1 FROM chunks c WHERE c.document_id = d.id) ORDER BY d.id::text LIMIT 1`,
			s.embedModel, start, last).Scan(&last)
		if errors.Is(err, sql.ErrNoRows) {
			return done, nil
		} else if err != nil {
			return done, fmt.Errorf("docs: backfill: %w", err)
		}
		if _, err := s.embed(ctx, last); ctx.Err() != nil {
			return done, ctx.Err()
		} else if err != nil {
			failed++
		} else {
			done, failed = done+1, 0
		}
	}
	if failed < 3 {
		return done, nil
	}
	return done, fmt.Errorf("docs: backfill stopped after 3 documents in a row failed to embed (%d embedded)", done)
}
