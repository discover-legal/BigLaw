// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"cmp"
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

// EmbedNote marks a document search reads lexically only until Backfill embeds it.
const EmbedNote = "Search over this document is lexical only: embedding its chunks failed or was cancelled. " +
	"It is embedded again when the server next starts."

// UseEmbedder turns on hybrid search with e, the embed role's model modelID (call it before use).
func (s *Service) UseEmbedder(e model.Embedder, modelID string) {
	s.embedder, s.embedModel = e, modelID
}

// Hybrid reports whether search fuses a vector ranking with the lexical one.
func (s *Service) Hybrid() bool { return s.embedder != nil }

// embedDoc embeds a new document; a failure leaves it lexical only, never failing its ingest.
func (s *Service) embedDoc(ctx context.Context, d *Document) {
	if s.embedder == nil || d.Chunks == 0 {
		return
	}
	if u, err := s.embed(ctx, d.ID); err != nil {
		d.Notes = append(d.Notes, EmbedNote)
	} else {
		d.EmbedModel, d.EmbedTokens, d.EmbedCostUSD = s.embedModel, int64(u.InputTokens), u.CostUSD
	}
}

// embed embeds document id's chunks (section path and search text, ≤ 4,096 characters), embedBatch
// per call, within embedWait. It unmarks the document first and marks it embedded by the embed model,
// with the usage (unpriced: cost nil), only after every batch landed at one length: a failed run
// leaves it lexical only with EmbedNote, never an earlier model's name over two models' vectors.
func (s *Service) embed(ctx context.Context, id string) (total model.Usage, err error) {
	ectx, cancel := context.WithTimeout(ctx, embedWait)
	defer cancel()
	defer func() {
		if err != nil {
			nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_, _ = s.db.ExecContext(nctx, `UPDATE documents SET notes = array_append(notes, $2) WHERE id = $1 AND NOT $2 = ANY(notes)`, id, EmbedNote)
		}
	}()
	total.CostUSD, ctx = new(float64), ectx
	if _, err := s.db.ExecContext(ctx, `UPDATE documents SET embed_model = NULL, embed_tokens = 0, embed_cost_usd = NULL WHERE id = $1`, id); err != nil {
		return total, err
	}
	for after, dims := int64(0), 0; ; {
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
			if dims = cmp.Or(dims, len(v)); len(v) != dims {
				return total, errors.New("docs: embed: the endpoint answered one document's batches at different vector lengths")
			}
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

// Backfill embeds (as ingest does) each document with chunks the embed model has not embedded,
// except those stored after it starts by the database's clock (their ingest does). It stops when
// ctx ends or after 3 failures in a row (the endpoint is likely down) and counts both outcomes.
func (s *Service) Backfill(ctx context.Context) (done, failed int, err error) {
	var start time.Time
	if err := s.db.QueryRowContext(ctx, `SELECT now()`).Scan(&start); s.embedder == nil || err != nil {
		return 0, 0, err
	}
	for last, inARow := "00000000-0000-0000-0000-000000000000", 0; inARow < 3; {
		err := s.db.QueryRowContext(ctx, `SELECT d.id FROM documents d WHERE d.embed_model IS DISTINCT FROM $1 AND d.created_at < $2
			AND d.id > $3 AND EXISTS (SELECT 1 FROM chunks c WHERE c.document_id = d.id) ORDER BY d.id LIMIT 1`, s.embedModel, start, last).Scan(&last)
		if errors.Is(err, sql.ErrNoRows) {
			return done, failed, nil
		} else if err != nil {
			return done, failed, fmt.Errorf("docs: backfill: %w", err)
		}
		if _, err := s.embed(ctx, last); ctx.Err() != nil {
			return done, failed, ctx.Err()
		} else if err != nil {
			failed, inARow = failed+1, inARow+1
		} else {
			done, inARow = done+1, 0
		}
	}
	return done, failed, fmt.Errorf("docs: backfill stopped after 3 documents in a row failed to embed (%d embedded)", done)
}

// LexicalOnly counts the documents with chunks the embed model has not embedded (0 without one).
func (s *Service) LexicalOnly(ctx context.Context) (n int, err error) {
	if s.embedder != nil {
		err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM documents d WHERE embed_model IS DISTINCT FROM $1
			AND EXISTS (SELECT 1 FROM chunks c WHERE c.document_id = d.id)`, s.embedModel).Scan(&n)
	}
	return n, err
}
