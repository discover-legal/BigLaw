// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/lib/pq"
)

const (
	MaxK     = 50   // search results per call
	MaxQuery = 1024 // bytes
	// MaxTerms caps a query's distinct words: each is matched against every chunk in scope, so a
	// search costs scope × terms (r2 N2: a pasted paragraph of 180 words took 12 s on 20 MiB).
	MaxTerms = 32
	// DefaultSectionTokens / MaxSectionTokens bound one page of a section.
	DefaultSectionTokens = 2000
	MaxSectionTokens     = 8000
)

// ErrBadInput (400): a search or section request outside its bounds.
var ErrBadInput = errors.New("docs: bad request")

// searchTimeout bounds one search statement (a variable for tests).
var searchTimeout = 10 * time.Second

// Hit is one search result: a verbatim chunk and where it is (Title is the section path).
type Hit struct {
	DocumentID string  `json:"documentId"`
	SectionID  string  `json:"sectionId"`
	Title      string  `json:"title"`
	Text       string  `json:"text"`
	Context    string  `json:"context,omitempty"`
	Rank       float64 `json:"rank"`
}

// Search ranks the scope's chunks for query. Each query word is a term (OR, not AND), weighted
// by how rare it is in the scope (idf = ln(1 + N/(1+df))) times ts_rank_cd of that term,
// saturated (normalisation 32). Both the English (stemmed) and the simple (exact) vectors are
// scored and summed, so an identifier such as FS-26-01234 counts even where stemming would mangle
// it. With an embedder (UseEmbedder) that ranking is fused with the scope's embedded chunks ranked
// exactly by cosine distance to the query's embedding (reciprocal-rank fusion, 1/(60+rank) from
// each; Rank is then the fused score); a failed query embedding is its model error, never a silent
// lexical answer. Ties go to the lower chunk id. With no searchable word and no embedder there are
// no hits; more than MaxTerms distinct words, or a statement past searchTimeout, is ErrBadInput.
func (s *Service) Search(ctx context.Context, scope []string, query string, k, offset int) ([]Hit, error) {
	if k < 1 || k > MaxK || offset < 0 || offset > 10000 || len(query) > MaxQuery || !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return nil, fmt.Errorf("%w: k is 1 to %d, offset 0 to 10000, the query at most %d bytes of UTF-8", ErrBadInput, MaxK, MaxQuery)
	}
	var qvec any // NULL: lexical only
	if s.embedder != nil && strings.TrimSpace(query) != "" && len(validIDs(scope)) > 0 {
		ectx, cancel := context.WithTimeout(ctx, queryWait)
		e, err := s.embedder.Embed(ectx, model.EmbedRequest{Agent: "search", Inputs: []string{query}})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("docs: search: embed the query: %w", err)
		}
		qvec = vectorText(e.Vectors[0])
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	var terms int
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(array_length(tsvector_to_array(to_tsvector('simple', $1)), 1), 0)`, query).Scan(&terms); err != nil {
		return nil, s.searchError(ctx, err)
	} else if terms > MaxTerms {
		return nil, fmt.Errorf("%w: the query has %d distinct words; search with at most %d", ErrBadInput, terms, MaxTerms)
	}
	rows, err := s.db.QueryContext(ctx, searchSQL, pq.Array(validIDs(scope)), query, k, offset, qvec, s.embedModel)
	if err != nil {
		return nil, s.searchError(ctx, err)
	}
	defer rows.Close()
	hits := []Hit{}
	for rows.Next() {
		var h Hit
		var id int64
		var ord int
		if err := rows.Scan(&id, &h.DocumentID, &ord, &h.Title, &h.Text, &h.Context, &h.Rank); err != nil {
			return nil, s.searchError(ctx, err)
		}
		h.SectionID = sectionID(ord)
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, s.searchError(ctx, err)
	}
	return hits, nil
}

func (s *Service) searchError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: the search took longer than %s; use fewer or rarer words, or fewer documents", ErrBadInput, searchTimeout)
	}
	return fmt.Errorf("docs: search: %w", err)
}

// searchSQL matches every term against the scope's chunks once (m), takes each term's rarity from
// those matches (w), ranks (lex), fuses that with the vector ranking when $5 (the query's vector)
// is set, and only then reads the k bodies. The vector leg reads the same scope ($1) and only
// chunks of documents its model ($6) embedded completely.
const searchSQL = `
WITH s AS (SELECT id, tsv, tsv_simple FROM chunks WHERE document_id = ANY($1::uuid[])),
terms AS (
  SELECT DISTINCT true AS en, plainto_tsquery('simple', l) AS q FROM unnest(tsvector_to_array(to_tsvector('english', $2))) AS l
  UNION
  SELECT DISTINCT false, plainto_tsquery('simple', l) FROM unnest(tsvector_to_array(to_tsvector('simple', $2))) AS l),
m AS (
  SELECT s.id, t.en, t.q, ts_rank_cd(CASE WHEN t.en THEN s.tsv ELSE s.tsv_simple END, t.q, 32) AS r
  FROM s JOIN terms t ON numnode(t.q) > 0 AND (CASE WHEN t.en THEN s.tsv ELSE s.tsv_simple END) @@ t.q),
w AS (SELECT en, q, ln(1 + (SELECT count(*) FROM s)::float8 / (1 + count(*))) AS idf FROM m GROUP BY en, q),
lex AS (SELECT m.id, sum(m.r * w.idf) AS rank FROM m JOIN w ON w.en = m.en AND w.q = m.q GROUP BY m.id),
vec AS (SELECT c.id, row_number() OVER (ORDER BY c.embedding <=> $5::vector, c.id) AS r
  FROM chunks c JOIN documents d ON d.id = c.document_id
  WHERE $5::vector IS NOT NULL AND c.document_id = ANY($1::uuid[]) AND d.embed_model = $6
    AND c.embedding IS NOT NULL AND vector_dims(c.embedding) = vector_dims($5::vector)),
fused AS (SELECT coalesce(l.id, v.id) AS id,
    CASE WHEN $5::vector IS NULL THEN l.rank ELSE coalesce(1.0 / (60 + l.r), 0) + coalesce(1.0 / (60 + v.r), 0) END AS rank
  FROM (SELECT id, rank, row_number() OVER (ORDER BY rank DESC, id) AS r FROM lex) l FULL JOIN vec v ON v.id = l.id),
top AS (SELECT id, rank FROM fused ORDER BY rank DESC, id LIMIT $3 OFFSET $4)
SELECT c.id, c.document_id, c.section_ord, c.locator, c.body, c.context, top.rank
FROM top JOIN chunks c ON c.id = top.id ORDER BY top.rank DESC, c.id`

func sectionID(ord int) string { return "s" + strconv.Itoa(ord) }

// OutlineEntry is one section as the page reader lists it. Tokens estimates what Section returns
// for it (the section with its subsections).
type OutlineEntry struct {
	SectionID string `json:"sectionId"`
	Number    string `json:"number"`
	Title     string `json:"title"`
	Level     int    `json:"level"`
	Tokens    int    `json:"tokens"`
}

// Outline lists a document's sections in order.
func (s *Service) Outline(ctx context.Context, scope []string, docID string) ([]OutlineEntry, error) {
	if !inScope(scope, docID) || !store.ValidID(docID) {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ord, number, title, level, tokens FROM sections WHERE document_id = $1 ORDER BY ord`, docID)
	if err != nil {
		return nil, fmt.Errorf("docs: outline: %w", err)
	}
	defer rows.Close()
	out := []OutlineEntry{}
	for rows.Next() {
		var e OutlineEntry
		var ord int
		if err := rows.Scan(&ord, &e.Number, &e.Title, &e.Level, &e.Tokens); err != nil {
			return nil, fmt.Errorf("docs: outline: %w", err)
		}
		e.SectionID = sectionID(ord)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound // every stored document has at least one section
	}
	return out, nil
}

// SectionPage is one page of a section's text. Offset and Next count characters from the start of
// the section; Truncated means the section goes on and Next is where the next page starts.
type SectionPage struct {
	SectionID string `json:"sectionId"`
	Number    string `json:"number"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	Offset    int    `json:"offset"`
	Total     int    `json:"total"`
	Truncated bool   `json:"truncated"`
	Next      int    `json:"next,omitempty"`
}

// Section reads a section with its subsections, verbatim, from offset (characters), at most limit
// tokens (0 means DefaultSectionTokens), cut at white space where possible. A long section is
// paged, never silently cut: Truncated and Next say there is more.
func (s *Service) Section(ctx context.Context, scope []string, docID, sectionID string, offset, limit int) (SectionPage, error) {
	ord, err := strconv.Atoi(strings.TrimPrefix(sectionID, "s"))
	if !inScope(scope, docID) || !store.ValidID(docID) || err != nil || !strings.HasPrefix(sectionID, "s") || ord < 0 || ord > math.MaxInt32 || sectionID != "s"+strconv.Itoa(ord) {
		return SectionPage{}, ErrNotFound
	}
	if limit == 0 {
		limit = DefaultSectionTokens
	}
	if offset < 0 || offset > math.MaxInt32 || limit < 1 || limit > MaxSectionTokens { // Postgres integers
		return SectionPage{}, fmt.Errorf("%w: offset ≥ 0 and limit 1 to %d tokens", ErrBadInput, MaxSectionTokens)
	}
	p := SectionPage{SectionID: sectionID, Offset: offset}
	err = s.db.QueryRowContext(ctx, `SELECT x.number, x.title, x.char_end - x.char_start,
		substr(d.text, x.char_start + 1 + $3, greatest(0, least($4, x.char_end - x.char_start - $3)))
		FROM sections x JOIN documents d ON d.id = x.document_id WHERE x.document_id = $1 AND x.ord = $2`,
		docID, ord, offset, limit*7/2+1).Scan(&p.Number, &p.Title, &p.Total, &p.Text)
	if errors.Is(err, sql.ErrNoRows) {
		return SectionPage{}, ErrNotFound
	} else if err != nil {
		return SectionPage{}, fmt.Errorf("docs: section: %w", err)
	}
	if offset > p.Total {
		return SectionPage{}, fmt.Errorf("%w: offset %d is past the section's end (%d characters)", ErrBadInput, offset, p.Total)
	}
	if utf8.RuneCountInString(p.Text) > limit*7/2 { // one character more than a page was read
		cut := p.Text[:len(string([]rune(p.Text)[:limit*7/2]))]
		if j := strings.LastIndexAny(cut, " \n\t\f"); j > len(cut)/2 {
			cut = cut[:j+1]
		}
		p.Text, p.Truncated = cut, true
	}
	p.Next = offset + utf8.RuneCountInString(p.Text)
	if !p.Truncated {
		p.Next = 0
	}
	return p, nil
}
