// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/lib/pq"
)

const (
	MaxK     = 50   // search results per call
	MaxQuery = 1024 // bytes: each word is a term scored against every chunk in scope
	// DefaultSectionTokens / MaxSectionTokens bound one page of a section.
	DefaultSectionTokens = 2000
	MaxSectionTokens     = 8000
)

// ErrBadInput (400): a search or section request outside its bounds.
var ErrBadInput = errors.New("docs: bad request")

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
// it. Ties are broken by chunk id. A query with no searchable word returns no hits.
func (s *Service) Search(ctx context.Context, scope []string, query string, k, offset int) ([]Hit, error) {
	if k < 1 || k > MaxK || offset < 0 || offset > 10000 || len(query) > MaxQuery || !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return nil, fmt.Errorf("%w: k is 1 to %d, offset 0 to 10000, the query at most %d bytes of UTF-8", ErrBadInput, MaxK, MaxQuery)
	}
	rows, err := s.db.QueryContext(ctx, `
WITH s AS (SELECT id, tsv, tsv_simple FROM chunks WHERE document_id = ANY($1::uuid[])),
n AS (SELECT count(*)::float8 AS n FROM s),
terms AS (
  SELECT DISTINCT true AS en, plainto_tsquery('simple', l) AS q FROM unnest(tsvector_to_array(to_tsvector('english', $2))) AS l
  UNION
  SELECT DISTINCT false, plainto_tsquery('simple', l) FROM unnest(tsvector_to_array(to_tsvector('simple', $2))) AS l),
w AS (
  SELECT t.en, t.q, ln(1 + n.n / (1 + count(s.id))) AS idf
  FROM terms t CROSS JOIN n LEFT JOIN s ON (CASE WHEN t.en THEN s.tsv ELSE s.tsv_simple END) @@ t.q
  WHERE numnode(t.q) > 0 GROUP BY t.en, t.q, n.n)
SELECT c.id, c.document_id, c.section_ord, c.locator, c.body, c.context,
  sum(ts_rank_cd(CASE WHEN w.en THEN c.tsv ELSE c.tsv_simple END, w.q, 32) * w.idf) AS rank
FROM chunks c JOIN w ON (CASE WHEN w.en THEN c.tsv ELSE c.tsv_simple END) @@ w.q
WHERE c.document_id = ANY($1::uuid[])
GROUP BY c.id ORDER BY rank DESC, c.id LIMIT $3 OFFSET $4`, pq.Array(validIDs(scope)), query, k, offset)
	if err != nil {
		return nil, fmt.Errorf("docs: search: %w", err)
	}
	defer rows.Close()
	hits := []Hit{}
	for rows.Next() {
		var h Hit
		var id int64
		var ord int
		if err := rows.Scan(&id, &h.DocumentID, &ord, &h.Title, &h.Text, &h.Context, &h.Rank); err != nil {
			return nil, fmt.Errorf("docs: search: %w", err)
		}
		h.SectionID = sectionID(ord)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

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
	if !inScope(scope, docID) || !store.ValidID(docID) || err != nil || !strings.HasPrefix(sectionID, "s") || ord < 0 || sectionID != "s"+strconv.Itoa(ord) {
		return SectionPage{}, ErrNotFound
	}
	if limit == 0 {
		limit = DefaultSectionTokens
	}
	if offset < 0 || limit < 1 || limit > MaxSectionTokens {
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
