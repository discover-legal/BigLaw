// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package docs is a matter's documents: ingest (UTF-8 text, .docx, PDF text layer), the section
// tree and search chunks, Postgres full-text search, the page reader (outline and paged sections)
// and the quote-verification ladder. Every read takes the caller's scope, the task's documentIds
// (G1): a document outside it does not exist for that caller, and neither does its file.
package docs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/docs/ooxml"
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
	ErrUnsupported = errors.New("docs: unsupported file: BigLaw reads UTF-8 text (.txt, .md, .csv, .json), .docx and PDFs with a text layer. " +
		"Images and scanned pages need a vision model, which this build does not have yet")
	// ErrUnreadable (422): a PDF or .docx that cannot be parsed (damaged, encrypted, or too large).
	ErrUnreadable = errors.New("docs: the file could not be read")
)

// Document is one stored document. Text is what search, the reader and the ladder read.
// ContentType is the type BigLaw detected from the content, not what the uploader declared.
type Document struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ContentType string    `json:"contentType"`
	FileID      string    `json:"fileId"` // the original upload; "" for a document posted as text
	Method      string    `json:"method"` // "text" (read as text) or "text_layer" (.docx, PDF text layer)
	NoText      bool      `json:"noText"` // nothing readable: a scanned PDF, or a .docx with no text. Never silently empty.
	Pages       int       `json:"pages"`
	Notes       []string  `json:"notes"`
	Sections    int       `json:"sections"`
	Chunks      int       `json:"chunks"`
	CreatedAt   time.Time `json:"createdAt"`
	Text        string    `json:"-"`
}

// Service reads and writes documents in the store.
type Service struct {
	st    *store.Store
	db    *sql.DB
	slots chan struct{}
}

func New(st *store.Store) *Service {
	return &Service{st: st, db: st.DB(), slots: make(chan struct{}, ingestSlots)}
}

// Extraction is what Extract read from a file.
type Extraction struct {
	Text, Method, ContentType string
	NoText                    bool
	Pages                     int
	Notes                     []string
}

const docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// textOf is the extraction of UTF-8 text, flagged when there is nothing in it.
func textOf(text string) Extraction {
	x := Extraction{Text: text, Method: "text", ContentType: "text/plain; charset=utf-8"}
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
// A PDF is parsed in a child process (pdfChild), so a hostile file cannot crash or hang the server.
func Extract(ctx context.Context, data []byte) (Extraction, error) {
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return extractPDFChild(ctx, data)
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		d, err := ooxml.Open(data)
		if errors.Is(err, ooxml.ErrNotDocx) {
			return Extraction{}, fmt.Errorf("%w (a ZIP archive that is not a .docx: spreadsheets, slides and other archives are not read)", ErrUnsupported)
		} else if err != nil {
			return Extraction{}, fmt.Errorf("%w: %v", ErrUnreadable, err)
		}
		x := Extraction{Text: clean(d.Text()), Method: "text_layer", ContentType: docxType}
		if strings.TrimSpace(x.Text) == "" {
			x.NoText, x.Notes = true, []string{"This Word document has no text (it may hold only images); nothing in it can be searched or quoted."}
		}
		return x, nil
	case utf8.Valid(data) && bytes.IndexByte(data, 0) < 0:
		return textOf(string(data)), nil
	}
	return Extraction{}, ErrUnsupported
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
			return Extraction{}, fmt.Errorf("%w: the PDF's text is larger than %d bytes", ErrUnreadable, maxText)
		}
	}
	x.Text = clean(strings.Join(pages, "\f")) // a form feed between pages
	var blank []string
	for i, p := range pages {
		if strings.TrimSpace(p) == "" {
			blank = append(blank, strconv.Itoa(i+1))
		}
	}
	switch {
	case len(blank) == x.Pages:
		x.NoText, x.Notes = true, []string{fmt.Sprintf("This PDF has no text layer (%d scanned page(s)?): nothing in it can be searched or quoted. "+
			"Re-export it with text, or transcribe it; this build has no vision model.", x.Pages)}
	case len(blank) > 0:
		if len(blank) > 10 {
			blank = append(blank[:10], "…")
		}
		x.Notes = []string{fmt.Sprintf("%d of %d pages have no text layer (scanned images?) and were not read: page %s.", len(blank), x.Pages, strings.Join(blank, ", "))}
	}
	return x, nil
}

// pdfChild is the argument that makes the BigLaw binary (or a test binary, through its TestMain) a
// PDF reader: MaybeRunChild. pdfTimeout bounds one PDF's extraction (a variable for tests).
const pdfChild = "__biglaw-pdf-text"

var (
	pdfTimeout = 2 * time.Minute
	maxText    = store.MaxFileSize // a PDF's extracted text (a variable for tests)
)

// MaybeRunChild turns this process into a PDF text reader and exits, when it was started as one:
// it reads a PDF on stdin and writes its Extraction as JSON on stdout. The PDF parser
// (ledongthuc/pdf) recurses on nested objects, and a deep enough nesting is a fatal stack overflow
// that recover cannot catch, so it never runs in the server's own process. main, and the TestMain
// of every package whose tests ingest a PDF, call this first.
func MaybeRunChild() {
	if len(os.Args) != 2 || os.Args[1] != pdfChild {
		return
	}
	debug.SetMaxStack(64 << 20) // fail fast on hostile nesting
	debug.SetMemoryLimit(1 << 30)
	data, err := io.ReadAll(io.LimitReader(os.Stdin, store.MaxFileSize+1))
	var out struct {
		X          Extraction
		Unreadable string
	}
	if err == nil {
		if out.X, err = extractPDF(data); err != nil {
			out.Unreadable = strings.TrimPrefix(err.Error(), ErrUnreadable.Error()+": ")
		}
	}
	if err != nil && out.Unreadable == "" {
		os.Exit(2)
	}
	if json.NewEncoder(os.Stdout).Encode(out) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

// extractPDFChild runs extractPDF in a child process with no environment, killed at pdfTimeout or
// when ctx ends. A crash, a timeout or garbage output is ErrUnreadable, never text.
func extractPDFChild(ctx context.Context, data []byte) (Extraction, error) {
	exe, err := os.Executable()
	if err != nil {
		return Extraction{}, fmt.Errorf("docs: pdf reader: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, exe, pdfChild)
	var stdout bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Env = bytes.NewReader(data), &stdout, []string{}
	err = cmd.Run()
	var out struct {
		X          Extraction
		Unreadable string
	}
	switch {
	case ctx.Err() != nil:
		return Extraction{}, ctx.Err()
	case cctx.Err() != nil:
		return Extraction{}, fmt.Errorf("%w: reading the PDF took longer than %s", ErrUnreadable, pdfTimeout)
	case err != nil || json.Unmarshal(stdout.Bytes(), &out) != nil:
		return Extraction{}, fmt.Errorf("%w: the PDF reader failed on this file (it may be damaged or hostile)", ErrUnreadable)
	case out.Unreadable != "":
		return Extraction{}, fmt.Errorf("%w: %s", ErrUnreadable, out.Unreadable)
	}
	return out.X, nil
}

// Ingest stores r as a file (streamed: see store.PutFile), extracts its text and stores the
// document, its sections and chunks. A file whose text cannot be read is removed again.
func (s *Service) Ingest(ctx context.Context, name, contentType string, r io.Reader) (Document, error) {
	f, err := s.st.PutFile(ctx, name, contentType, r)
	if err != nil {
		return Document{}, err
	}
	d, err := s.ingestFile(ctx, f)
	if err != nil {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.db.ExecContext(dctx, `DELETE FROM files WHERE id = $1`, f.ID)
	}
	return d, err
}

func (s *Service) ingestFile(ctx context.Context, f store.File) (Document, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return Document{}, ctx.Err()
	}
	f, err := s.st.GetFile(ctx, f.ID)
	if err != nil {
		return Document{}, err
	}
	x, err := Extract(ctx, f.Data)
	if err != nil {
		return Document{}, err
	}
	return s.insert(ctx, Document{Name: f.Name, FileID: f.ID}, x)
}

// IngestText stores a document given as text (no original file).
func (s *Service) IngestText(ctx context.Context, name, text string) (Document, error) {
	if len(text) > store.MaxFileSize {
		return Document{}, store.ErrFileTooLarge
	}
	if !store.ValidName(name) {
		return Document{}, store.ErrBadName
	}
	if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return Document{}, fmt.Errorf("%w (the text is not valid UTF-8, or holds a NUL)", ErrUnsupported)
	}
	return s.insert(ctx, Document{Name: name}, textOf(text))
}

func (s *Service) insert(ctx context.Context, d Document, x Extraction) (Document, error) {
	d.Text, d.Method, d.ContentType, d.NoText, d.Pages, d.Notes = x.Text, x.Method, x.ContentType, x.NoText, x.Pages, append([]string{}, x.Notes...)
	secs := Parse(d.Text)
	chunks := Chunkify(d.Text, secs)
	d.Sections, d.Chunks = len(secs), len(chunks)
	err := s.st.InTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `INSERT INTO documents (name, content_type, file_id, text, method, no_text, pages, notes)
			VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6, $7, $8) RETURNING id, created_at`,
			d.Name, d.ContentType, d.FileID, d.Text, d.Method, d.NoText, d.Pages, pq.Array(d.Notes)).Scan(&d.ID, &d.CreatedAt); err != nil {
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
		var sec, bs, be []int64
		var loc, body, search, about []string
		for _, c := range chunks {
			sec, bs, be = append(sec, int64(c.Section)), append(bs, int64(c.Start)), append(be, int64(c.End))
			loc, body, search, about = append(loc, c.Locator), append(body, c.Text), append(search, c.Search), append(about, c.Context)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO chunks (document_id, section_ord, locator, body, search_text, context, byte_start, byte_end)
			SELECT $1, * FROM unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::text[], $7::int[], $8::int[])`,
			d.ID, pq.Array(sec), pq.Array(loc), pq.Array(body), pq.Array(search), pq.Array(about), pq.Array(bs), pq.Array(be))
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
	var d Document
	var file sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, name, content_type, file_id, text, method, no_text, coalesce(pages, 0), notes, created_at,
		(SELECT count(*) FROM sections WHERE document_id = d.id), (SELECT count(*) FROM chunks WHERE document_id = d.id)
		FROM documents d WHERE id = $1`, id).Scan(&d.ID, &d.Name, &d.ContentType, &file, &d.Text, &d.Method, &d.NoText, &d.Pages,
		pq.Array(&d.Notes), &d.CreatedAt, &d.Sections, &d.Chunks)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, ErrNotFound
	} else if err != nil {
		return Document{}, fmt.Errorf("docs: get document: %w", err)
	}
	d.FileID = file.String
	return d, nil
}

// File returns a document's original file, only through a document in scope.
func (s *Service) File(ctx context.Context, scope []string, id string) (store.File, error) {
	d, err := s.Get(ctx, scope, id)
	if err != nil {
		return store.File{}, err
	}
	if d.FileID == "" {
		return store.File{}, ErrNotFound
	}
	return s.st.GetFile(ctx, d.FileID)
}

// Delete removes a document with its sections, chunks and original file, in one transaction.
func (s *Service) Delete(ctx context.Context, id string) error {
	if !store.ValidID(id) {
		return ErrNotFound
	}
	return s.st.InTx(ctx, func(tx *sql.Tx) error {
		var file sql.NullString
		err := tx.QueryRowContext(ctx, `DELETE FROM documents WHERE id = $1 RETURNING file_id`, id).Scan(&file)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err == nil && file.Valid {
			_, err = tx.ExecContext(ctx, `DELETE FROM files WHERE id = $1`, file.String)
		}
		return err
	})
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
