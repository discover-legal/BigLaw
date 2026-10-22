// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package docs is a matter's documents: ingest (UTF-8 text, .docx, PDF text layer), the section
// tree and search chunks, Postgres full-text search, the page reader (outline and paged sections)
// and the quote-verification ladder. Every read takes the caller's scope, the task's documentIds
// (G1): a document outside it does not exist for that caller, and neither does its file.
package docs

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/docs/ooxml"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/ledongthuc/pdf"
	"github.com/lib/pq"
)

const (
	// MaxPDFPages caps a PDF read for its text layer (a page count is the file's own claim).
	MaxPDFPages = 2000
	// ingestSlots bounds concurrent extractions: each holds a whole file (≤ 64 MiB) plus, for a
	// .docx, up to 128 MiB of decompressed parts.
	ingestSlots = 2
)

var (
	ErrNotFound = store.ErrNotFound
	// ErrUnsupported (415): a format BigLaw does not read.
	ErrUnsupported = errors.New("docs: unsupported file: BigLaw reads UTF-8 text (.txt, .md, .csv, .json), .docx, PDFs, " +
		"and PNG, JPEG, GIF or WebP images (transcribed by the vision model)")
	// ErrUnreadable (422): a PDF or .docx that cannot be parsed (damaged, encrypted, or too large).
	ErrUnreadable = errors.New("docs: the file could not be read")
	// ErrBusy (503): every ingest slot stayed taken for slotWait. ErrStopping (503): the server
	// stopped during the request (its ingest was cancelled and its file removed).
	ErrBusy     = errors.New("docs: busy: other documents are being read; retry in a few minutes")
	ErrStopping = errors.New("docs: the server is stopping; retry when it is back")
	// slotWait bounds the wait for an ingest or reader slot, so a stored upload is ingested or removed
	// well within the hour after which SweepOrphanFiles takes it for a crash's leftover (uploadTotal +
	// 2 × slotWait + childTimeout + visionWait = 47 minutes).
	slotWait = 10 * time.Minute
)

// Document is one stored document. Text is what search, the reader and the ladder read.
// ContentType is the type BigLaw detected from the content, not what the uploader declared.
type Document struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	FileID      string `json:"fileId"` // the original upload; "" for a document posted as text
	Method      string `json:"method"` // "text" (read as text), "text_layer" (.docx, PDF text layer) or "vision" (transcribed)
	NoText      bool   `json:"noText"` // nothing readable: a blank PDF, a .docx with no text, a scan awaiting transcription. Never silently empty.
	Status      string `json:"status"` // ready, needs_transcription (no vision model yet) or failed (its transcription; notes say why)
	// Transcript is set when text was transcribed from images: by which model, where, how reliably.
	Transcript *Transcript `json:"transcript,omitempty"`
	Pages      int         `json:"pages"`
	Notes      []string    `json:"notes"`
	Sections   int         `json:"sections"`
	Chunks     int         `json:"chunks"`
	// EmbedModel embedded every chunk ("": lexical only); EmbedCostUSD nil: unpriced, unknown.
	EmbedModel   string    `json:"embedModel,omitempty"`
	EmbedTokens  int64     `json:"embedTokens"`
	EmbedCostUSD *float64  `json:"embedCostUsd"`
	CreatedAt    time.Time `json:"createdAt"`
	MatterNumber string    `json:"matterNumber,omitempty"` // the matter it was filed under (POST /documents)
	Text         string    `json:"-"`
}

// Service reads and writes documents in the store.
type Service struct {
	st                     *store.Store
	db                     *sql.DB
	slots                  chan struct{}
	embedder               model.Embedder // nil: lexical search only (UseEmbedder)
	embedModel             string
	vision                 model.Chatter // nil: images and scans wait (UseVision)
	visionModel, visionKey string
	log                    *slog.Logger
	readers                chan struct{} // documents reading scans with the vision model, outside the ingest slots
}

func New(st *store.Store) *Service {
	return &Service{st: st, db: st.DB(), slots: make(chan struct{}, ingestSlots), readers: make(chan struct{}, ingestSlots), log: slog.New(slog.DiscardHandler)}
}

// Extraction is what Extract read from a file. Blank lists the pages with no text layer, or a stamp
// beside page images (scanpdf.go); of those, Scans are images to transcribe (the first maxVisionPages+1
// of ScanPages), Unsupported hold images BigLaw cannot send. Imaged: other pages with images (not read).
type Extraction struct {
	Text, Method, ContentType  string
	NoText                     bool
	Pages, ScanPages           int
	Notes                      []string
	Blank, Unsupported, Imaged []int
	Scans                      []Scan
	Transcript                 *Transcript `json:"-"` // set by the vision read (vision.go)
}

const docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// textOf is the extraction of UTF-8 text (cleaned, as every extraction is), flagged when there is
// nothing in it.
func textOf(text string) Extraction {
	x := Extraction{Text: clean(text), Method: "text", ContentType: "text/plain; charset=utf-8"}
	if strings.TrimSpace(text) == "" {
		x.NoText, x.Notes = true, []string{"The document is empty or white space only."}
	}
	return x
}

// clean makes extracted text storable: invalid UTF-8 becomes U+FFFD and control characters other
// than tab, newline, carriage return and form feed become spaces (Postgres text holds no NUL).
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\f' || r == 0x7f {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "\uFFFD"))
}

// Extract reads the text of a file by its content (magic bytes), never trusting the declared type.
// PDFs and .docx files are parsed in a child process (MaybeRunChild), so a hostile file cannot
// crash the server, hang it, or take its memory.
func Extract(ctx context.Context, data []byte) (Extraction, error) {
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")), bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return extractInChild(ctx, data)
	case len(data) > maxText && utf8.Valid(data) && bytes.IndexByte(data, 0) < 0:
		return Extraction{}, textTooLarge()
	case utf8.Valid(data) && bytes.IndexByte(data, 0) < 0:
		return textOf(string(data)), nil
	case sniffImage(data) != "":
		return imageOf(data, sniffImage(data))
	}
	return Extraction{}, ErrUnsupported
}

// textTooLarge (422): more text than one document holds (maxText; the file itself may be up to
// store.MaxFileSize, e.g. a PDF of scans).
func textTooLarge() error {
	return fmt.Errorf("%w: its text is larger than %d bytes (split the document)", ErrUnreadable, maxText)
}

// parse is what the child runs.
func parse(data []byte) (Extraction, error) {
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return extractPDF(data)
	}
	d, err := ooxml.Open(data)
	if errors.Is(err, ooxml.ErrNotDocx) {
		return Extraction{}, fmt.Errorf("%w (a ZIP archive that is not a .docx: spreadsheets, slides and other archives are not read)", ErrUnsupported)
	} else if err != nil {
		return Extraction{}, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	text, err := d.Text()
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	if len(text) > maxText { // 128 MiB of parts can hold more text than any stored document
		return Extraction{}, textTooLarge()
	}
	x := Extraction{Text: clean(text), Method: "text_layer", ContentType: docxType}
	if strings.TrimSpace(x.Text) == "" {
		x.NoText, x.Notes = true, []string{"This Word document has no text (it may hold only images); nothing in it can be searched or quoted."}
	}
	return x, nil
}

func extractPDF(data []byte) (x Extraction, err error) {
	defer func() { // the PDF parser panics on some malformed files
		if recover() != nil {
			x, err = Extraction{}, fmt.Errorf("%w: the PDF is damaged", ErrUnreadable)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: the PDF is damaged or encrypted", ErrUnreadable)
	}
	x = Extraction{Method: "text_layer", ContentType: "application/pdf", Pages: r.NumPage()}
	if x.Pages < 1 || x.Pages > MaxPDFPages {
		return Extraction{}, fmt.Errorf("%w: the PDF claims %d pages (BigLaw reads 1 to %d)", ErrUnreadable, x.Pages, MaxPDFPages)
	}
	pages, size := make([]string, x.Pages), 0
	for i := range pages {
		if pages[i], err = r.Page(i + 1).GetPlainText(nil); err != nil {
			return Extraction{}, fmt.Errorf("%w: page %d of the PDF is damaged", ErrUnreadable, i+1)
		}
		if size += len(pages[i]); size > maxText { // compressed text can expand far beyond the file
			return Extraction{}, textTooLarge()
		}
	}
	x.Text = clean(strings.Join(pages, "\f")) // a form feed between pages
	for i, p := range pages {
		sc, bad := pageScan(r, r.Page(i+1), i+1, data)
		n := utf8.RuneCountInString(strings.TrimSpace(p))
		if n >= stampMax && (bad || len(sc.Images) > 0) { // text over an image: noted unless a mixed page's image is read
			x.Imaged = append(x.Imaged, i+1)
		}
		if n >= mixedMax || n > 0 && !bad && len(sc.Images) == 0 { // a page of text (text over a damaged image: neither Blank nor a scan)
			continue
		} else if n < stampMax {
			if x.Blank = append(x.Blank, i+1); bad {
				x.Unsupported = append(x.Unsupported, i+1)
			}
		}
		if !bad && len(sc.Images) > 0 { // a scan, or a mixed page (never in Blank: it never holds or refuses the document)
			if x.ScanPages++; x.ScanPages <= maxVisionPages+1 {
				x.Scans = append(x.Scans, sc)
			}
		}
	}
	x.NoText, x.Notes = strings.TrimSpace(x.Text) == "", pageNotes(&x)
	return x, nil
}

// Ingest stores r as a file (streamed: see store.PutFile), extracts its text and stores the
// document, its sections and chunks. A file whose text cannot be read is removed again.
func (s *Service) Ingest(ctx context.Context, name, contentType string, r io.Reader) (Document, error) {
	f, err := s.st.PutFile(ctx, name, contentType, r)
	if err != nil {
		return Document{}, err
	}
	d, err := s.ingestFile(ctx, f, "")
	if err != nil && !errors.Is(err, ErrVisionNotConfigured) { // that one keeps the original, to transcribe later
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.db.ExecContext(dctx, `DELETE FROM files WHERE id = $1`, f.ID)
		return d, err
	}
	s.embedDoc(ctx, &d) // outside the ingest slot (nothing to embed while it waits)
	return d, err
}

// ingestFile reads file f into a new document, or into document id when it awaits transcription.
// The ingest slot is held for the extraction only: vision calls wait for a reader slot (holding the
// text, not the file), so slow model calls never hold up other uploads.
func (s *Service) ingestFile(ctx context.Context, f store.File, id string) (Document, error) {
	x, err := Extraction{}, take(ctx, s.slots)
	if err != nil {
		return Document{}, err
	}
	if f, err = s.st.GetFile(ctx, f.ID); err == nil {
		x, err = Extract(ctx, f.Data)
	}
	if <-s.slots; err == nil && len(x.Scans) > 0 && s.vision != nil {
		if f.Data, err = nil, take(ctx, s.readers); err == nil {
			defer func() { <-s.readers }()
			f, err = s.st.GetFile(ctx, f.ID)
		}
	}
	if err == nil {
		err = s.readScans(ctx, f.Data, &x)
	}
	d := Document{ID: id, Name: f.Name, FileID: f.ID, Status: "ready"}
	if errors.Is(err, ErrVisionNotConfigured) && id == "" {
		d.Status = "needs_transcription"
		d, ierr := s.insert(ctx, d, x)
		return d, cmp.Or(ierr, err)
	} else if err != nil {
		return Document{}, err
	}
	return s.insert(ctx, d, x)
}

// take waits for one of slots, at most slotWait (ErrBusy).
func take(ctx context.Context, slots chan struct{}) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(slotWait):
		return ErrBusy
	}
}

// IngestText stores a document given as text (no original file).
func (s *Service) IngestText(ctx context.Context, name, text string) (Document, error) {
	if len(text) > maxText {
		return Document{}, textTooLarge()
	}
	if !store.ValidName(name) {
		return Document{}, store.ErrBadName
	}
	if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return Document{}, fmt.Errorf("%w (the text is not valid UTF-8, or holds a NUL)", ErrUnsupported)
	}
	d, err := s.insert(ctx, Document{Name: name, Status: "ready"}, textOf(text))
	if err == nil {
		s.embedDoc(ctx, &d)
	}
	return d, err
}

func (s *Service) insert(ctx context.Context, d Document, x Extraction) (Document, error) {
	d.Text, d.Method, d.ContentType, d.NoText, d.Pages, d.Notes = x.Text, x.Method, x.ContentType, x.NoText, x.Pages, append([]string{}, x.Notes...)
	d.Transcript = x.Transcript
	transcript, err := json.Marshal(d.Transcript) // SQL NULL when nothing was sent to the vision model
	if err != nil {
		return Document{}, err
	}
	secs, capped := parseTree(d.Text)
	if capped { // r2 auditor L3: the outline must not look complete
		d.Notes = append(d.Notes, fmt.Sprintf("The outline lists the first %d headings only: later headings are text of section %s (search and quotes still cover all of it).",
			MaxSections, sectionID(len(secs)-1)))
	}
	chunks := Chunkify(d.Text, secs)
	d.Sections, d.Chunks = len(secs), len(chunks)
	err = s.st.InTx(ctx, func(tx *sql.Tx) error { // a transcription job (d.ID set) completes only a document still waiting for it
		if err := tx.QueryRowContext(ctx, `INSERT INTO documents (id, name, content_type, file_id, text, method, no_text, pages, notes, status, transcript)
			VALUES (coalesce(NULLIF($1, '')::uuid, gen_random_uuid()), $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, NULLIF($11::jsonb, 'null'))
			ON CONFLICT (id) DO UPDATE SET text = $5, method = $6, no_text = $7, pages = $8, notes = $9, status = $10, transcript = EXCLUDED.transcript, lease_until = NULL
			WHERE documents.status = 'needs_transcription' RETURNING id, created_at`, d.ID, d.Name, d.ContentType, d.FileID, d.Text, d.Method, d.NoText,
			d.Pages, pq.Array(d.Notes), d.Status, transcript).Scan(&d.ID, &d.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sections WHERE document_id = $1`, d.ID); err != nil {
			return err
		}
		var at []int
		for _, sec := range secs {
			at = append(at, sec.Start, sec.End)
		}
		runes := runeOffsets(d.Text, at)
		var ord, level, cs, ce, tok []int64
		var num, title []string
		for i, sec := range secs {
			ord, level, cs, ce = append(ord, int64(i)), append(level, int64(sec.Level)), append(cs, int64(runes[sec.Start])), append(ce, int64(runes[sec.End]))
			num, title, tok = append(num, sec.Number), append(title, sec.Title), append(tok, int64(tokens(d.Text[sec.Start:sec.End])))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sections (document_id, ord, number, title, level, char_start, char_end, tokens)
			SELECT $1, * FROM unnest($2::int[], $3::text[], $4::text[], $5::int[], $6::int[], $7::int[], $8::int[])`,
			d.ID, pq.Array(ord), pq.Array(num), pq.Array(title), pq.Array(level), pq.Array(cs), pq.Array(ce), pq.Array(tok)); err != nil {
			return err
		}
		// COPY streams the chunks a row at a time (r2 N1: an array literal of every body was the
		// largest of the server's copies of the text).
		cp, err := tx.PrepareContext(ctx, `COPY chunks (document_id, section_ord, locator, body, search_text, context, byte_start, byte_end, transcribed) FROM STDIN`)
		if err != nil {
			return err
		}
		defer cp.Close()
		for _, c := range chunks {
			if c.Search == c.Text {
				c.Search = "" // prose is indexed from body (the schema's coalesce): stored once
			}
			if _, err := cp.ExecContext(ctx, d.ID, c.Section, c.Locator, c.Text, c.Search, c.Context, c.Start, c.End, d.Transcript.covers(c.Start, c.End) != nil); err != nil {
				return err
			}
		}
		_, err = cp.ExecContext(ctx)
		return err
	})
	if err != nil {
		return Document{}, fmt.Errorf("docs: store document: %w", err)
	}
	return d, nil
}

// runeOffsets maps each byte offset in at to its rune offset in text, in one pass.
func runeOffsets(text string, at []int) map[int]int {
	sorted := slices.Sorted(slices.Values(at))
	out, b, r := make(map[int]int, len(at)), 0, 0
	for _, a := range sorted {
		r += utf8.RuneCountInString(text[b:a])
		b, out[a] = a, r
	}
	return out
}

// inScope reports whether id is one of scope's document ids (uuids compare case-insensitively,
// as Postgres compares them).
func inScope(scope []string, id string) bool {
	for _, s := range scope {
		if strings.EqualFold(s, id) {
			return true
		}
	}
	return false
}

// Get returns a document with its text, if it is in scope.
func (s *Service) Get(ctx context.Context, scope []string, id string) (Document, error) {
	if !inScope(scope, id) || !store.ValidID(id) {
		return Document{}, ErrNotFound
	}
	ds, err := s.list(ctx, `d.id = $1`, id, "text")
	if err == nil && len(ds) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Document{}, err
	}
	return ds[0], nil
}

// List returns the documents filed under matter ("" for all), oldest first, without their text.
func (s *Service) List(ctx context.Context, matter string) ([]Document, error) {
	return s.list(ctx, `$1 = '' OR d.matter_number = $1`, matter, "''")
}

func (s *Service) list(ctx context.Context, where string, arg any, text string) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, content_type, file_id, `+text+`, method, no_text, coalesce(pages, 0), notes, created_at,
		(SELECT count(*) FROM sections WHERE document_id = d.id), (SELECT count(*) FROM chunks WHERE document_id = d.id),
		embed_model, embed_tokens, embed_cost_usd, status, transcript, coalesce(matter_number, '') FROM documents d WHERE `+where+` ORDER BY created_at, id`, arg)
	if err != nil {
		return nil, fmt.Errorf("docs: get documents: %w", err)
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		var d Document
		var file, embedModel sql.NullString
		var transcript []byte
		err := rows.Scan(&d.ID, &d.Name, &d.ContentType, &file, &d.Text, &d.Method, &d.NoText, &d.Pages, pq.Array(&d.Notes), &d.CreatedAt, &d.Sections, &d.Chunks,
			&embedModel, &d.EmbedTokens, &d.EmbedCostUSD, &d.Status, &transcript, &d.MatterNumber)
		if err == nil && transcript != nil {
			err = json.Unmarshal(transcript, &d.Transcript)
		}
		if err != nil {
			return nil, fmt.Errorf("docs: get documents: %w", err)
		}
		d.FileID, d.EmbedModel = file.String, embedModel.String
		out = append(out, d)
	}
	return out, rows.Err()
}

// File files a document under a matter number (the matter it belongs to).
func (s *Service) File(ctx context.Context, id, matter string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE documents SET matter_number = nullif($2, '') WHERE id = $1`, id, matter)
	return err
}

// WriteFile writes a document's original file to w, only through a document in scope
// (store.ReadFile: one chunk in memory at a time). head gets the detected content type and the
// file's metadata before the first byte.
func (s *Service) WriteFile(ctx context.Context, scope []string, id string, head func(contentType string, f store.File) error, w io.Writer) error {
	if !inScope(scope, id) || !store.ValidID(id) {
		return ErrNotFound
	}
	var ct string
	var file sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT content_type, file_id FROM documents WHERE id = $1`, id).Scan(&ct, &file)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !file.Valid {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("docs: file: %w", err)
	}
	_, err = s.st.ReadFile(ctx, file.String, func(f store.File) error { return head(ct, f) }, w)
	return err
}

// Delete removes a document with its sections, chunks and original file (the schema's cascade and
// trigger), in one statement.
func (s *Service) Delete(ctx context.Context, id string) error {
	if !store.ValidID(id) {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM documents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("docs: delete: %w", err)
	} else if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SweepOrphanFiles deletes files no document references that are older than an hour: an upload
// whose server died between storing the file and storing its document (Ingest removes the file
// on every error it returns). serve runs it at startup. Every file is a document's until a later
// PR stores other files, which must then teach this query about them.
func (s *Service) SweepOrphanFiles(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM files f WHERE created_at < now() - interval '1 hour'
		AND NOT EXISTS (SELECT 1 FROM documents d WHERE d.file_id = f.id)`)
	if err != nil {
		return 0, fmt.Errorf("docs: sweep orphan files: %w", err)
	}
	return res.RowsAffected()
}

// Resolve finds the document in scope that ref names: its id, or its name as a model cites it
// (case, directory and extension ignored: "Notice.docx", "docs/notice"). Ambiguity is not found.
func (s *Service) Resolve(ctx context.Context, scope []string, ref string) (string, error) {
	if inScope(scope, ref) {
		return ref, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM documents WHERE id = ANY($1::uuid[])`, pq.Array(validIDs(scope)))
	if err != nil {
		return "", fmt.Errorf("docs: resolve: %w", err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return "", err
		}
		if k := NormalizeSourceKey(ref); k != "" && NormalizeSourceKey(name) == k {
			found = append(found, id)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", ErrNotFound
	}
	return found[0], nil
}

// NormalizeSourceKey reduces a document reference to a lookup key: lower case, base name, no
// extension (ported from v1 protocols).
func NormalizeSourceKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndexAny(s, "/\\"); i >= 0 {
		s = s[i+1:]
	}
	for _, ext := range []string{".docx", ".doc", ".pdf", ".xlsx", ".xls", ".pptx", ".txt", ".md", ".csv", ".rtf", ".html", ".json"} {
		if strings.HasSuffix(s, ext) {
			s = strings.TrimSuffix(s, ext)
			break
		}
	}
	return strings.TrimSpace(s)
}

// validIDs is the scope's well-formed ids, for a uuid[] parameter (a malformed id matches nothing).
func validIDs(scope []string) []string {
	out := []string{}
	for _, id := range scope {
		if store.ValidID(id) {
			out = append(out, id)
		}
	}
	return out
}
