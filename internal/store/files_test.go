// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

func openStore(t *testing.T, dsn string) *Store {
	t.Helper()
	s, err := Open(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func noise(n int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, 2))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

// docx builds a valid ~5 MB .docx (a zip) whose bulk is an incompressible
// stored media part, so every byte value 0..255 crosses the wire.
func docx(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		"word/document.xml":   `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Memo</w:t></w:r></w:p></w:body></w:document>`,
	}
	for name, body := range parts {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "word/media/image1.bin", Method: zip.Store})
	w.Write(noise(5<<20, 1))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func count(t *testing.T, s *Store, table string) (n int) {
	t.Helper()
	if err := s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFilesRoundTripBytea(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	in := docx(t)
	if len(in) < 5<<20 {
		t.Fatalf("fixture is %d bytes, want ≥ 5 MB", len(in))
	}
	const ct = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	meta, err := s.PutFile(t.Context(), "memo.docx", ct, bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFile(t.Context(), meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, in) {
		t.Fatalf("round trip changed the bytes (in %d, out %d)", len(in), len(got.Data))
	}
	if got.Name != "memo.docx" || got.ContentType != ct || got.Size != int64(len(in)) || got.SHA256 != meta.SHA256 || got.CreatedAt.IsZero() {
		t.Fatalf("metadata mismatch: %+v vs %+v", got, meta)
	}
	if _, err := zip.NewReader(bytes.NewReader(got.Data), int64(len(got.Data))); err != nil {
		t.Fatalf("returned .docx does not open as a zip: %v", err)
	}
}

// TestFilesStoredInChunks pins the memory bound: content goes to the
// database in rows of at most ChunkSize, never as one value.
func TestFilesStoredInChunks(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	in := noise(3*ChunkSize+ChunkSize/2, 3)
	f, err := s.PutFile(t.Context(), "a.bin", "application/octet-stream", bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	var n, biggest int
	if err := s.db.QueryRow(`SELECT count(*), max(octet_length(data)) FROM file_chunks WHERE file_id = $1`, f.ID).Scan(&n, &biggest); err != nil {
		t.Fatal(err)
	}
	if n != 4 || biggest != ChunkSize {
		t.Fatalf("%d chunks, largest %d bytes; want 4 chunks of at most %d", n, biggest, ChunkSize)
	}
	if got, err := s.GetFile(t.Context(), f.ID); err != nil || !bytes.Equal(got.Data, in) {
		t.Fatalf("round trip: %v", err)
	}
}

type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errors.New("disk on fire")
	}
	k := min(len(p), r.n)
	r.n -= k
	return k, nil
}

func TestFilesEdgeCases(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	ctx := t.Context()
	put := func(name, ct string, r io.Reader) (File, error) { return s.PutFile(ctx, name, ct, r) }

	empty, err := put("empty.txt", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetFile(ctx, empty.ID); err != nil || got.Size != 0 || len(got.Data) != 0 || got.Data == nil {
		t.Fatalf("empty file: %+v, %v", got, err)
	}

	// Exactly at the cap round-trips; one byte over is refused and nothing persists.
	atCap := noise(MaxFileSize, 4)
	f, err := put("cap.bin", "application/octet-stream", bytes.NewReader(atCap))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetFile(ctx, f.ID); err != nil || !bytes.Equal(got.Data, atCap) {
		t.Fatalf("file at MaxFileSize: %v", err)
	}
	files, chunks := count(t, s, "files"), count(t, s, "file_chunks")
	if _, err := put("big.bin", "application/octet-stream", io.MultiReader(bytes.NewReader(atCap), strings.NewReader("x"))); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized: %v, want ErrFileTooLarge", err)
	}
	if _, err := put("broken.bin", "application/octet-stream", &failingReader{n: 2*ChunkSize + 7}); err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("reader error: %v", err)
	}
	if count(t, s, "files") != files || count(t, s, "file_chunks") != chunks {
		t.Fatal("a refused or failed upload left rows behind")
	}

	for _, name := range []string{"", ".", "..", "../../etc/passwd", `a\b`, "a/b", "a\x00b", "a\nb", "\xff\xfe", "invoice\u202Efdp.exe", "a\u2066b", strings.Repeat("n", MaxNameLen+1),
		// r2 reproducer: invisible format characters, separators, edge spaces.
		"a\u2028b.pdf", "a\u2029b.pdf", "invoice\u200b.pdf", "x\ufeff.exe", "pay\u00ad.pdf", "file.pdf\u180e.exe", "report.pdf ", " lead.pdf", "a.pdf\u3000",
		// PR 3 (r3 reproducer R3-4): invisible characters outside Cf, joiner abuse, white-space runs.
		"inv\u3164oice.pdf", "\u115fa.pdf", "a\u2800.pdf", "a\u034fb.pdf", "a\ufe0fb.pdf", "a\U000E0100b.pdf", "a\u200d\u200db.pdf", "\u200da.pdf", "a.pdf\u200d",
		"a\u200d.pdf", "1\u200c2.pdf", "report.pdf\u00a0\u00a0\u00a0\u00a0.exe", "a  b.pdf", "a\u00a0 b.pdf", "a\u3000\u3000b.pdf", "\uffa0x.txt", "a\u17b4b.pdf"} {
		if _, err := put(name, "text/plain", strings.NewReader("x")); !errors.Is(err, ErrBadName) {
			t.Errorf("name %q: %v, want ErrBadName", name, err)
		}
	}
	for _, name := range []string{"memo.docx", "Mémoire d'appel (v2).docx", "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645.pdf", "a..b.txt", strings.Repeat("n", MaxNameLen),
		"a b.pdf", "a\u3000b.pdf", "\U0001F468\u200d\U0001F4BB notes.txt", "a\u00a0b.pdf", "c:evil.txt", "CON", "\u0915\u094d\u200d\u0937.txt"} {
		if _, err := put(name, "text/plain", strings.NewReader("x")); err != nil {
			t.Errorf("name %q refused: %v", name, err)
		}
	}
	grows := "text/plain" // r3 auditor: <= 255 bytes as given, > 255 once canonical ("; " per parameter)
	for i := 0; len(grows) < 246; i++ {
		grows += fmt.Sprintf(";p%d=v", i)
	}
	if _, err := put("a.txt", strings.Repeat("a", 249)+"/plain", strings.NewReader("x")); err != nil {
		t.Errorf("a 255-byte content type: %v", err)
	}
	for _, ct := range []string{"", "text", "text/html\r\nSet-Cookie: a=b", "text/plain; charset", strings.Repeat("a", 250) + "/plain", grows, "*/*", "text/*",
		"text/html\r\n", "text/html\n", "a/b; c=\"\x00\"", "text/plain; charset=\"\u00e9\"", "text/plain\x7f"} { // r2: trailing CRLF, NUL, non-ASCII
		if _, err := put("a.txt", ct, strings.NewReader("x")); !errors.Is(err, ErrBadType) {
			t.Errorf("content type %q: %v, want ErrBadType", ct, err)
		}
	}
	// Stored in canonical form, not as given.
	if f, err := put("a.txt", "Text/HTML;  Charset=UTF-8", strings.NewReader("x")); err != nil || f.ContentType != "text/html; charset=UTF-8" {
		t.Errorf("canonical content type: %q, %v", f.ContentType, err)
	} else if got, _ := s.GetFile(ctx, f.ID); got.ContentType != f.ContentType {
		t.Errorf("stored content type %q, returned %q", got.ContentType, f.ContentType)
	}
	for _, id := range []string{"", "x", "'; DROP TABLE files; --", "00000000-0000-0000-0000-00000000000g", "00000000-0000-0000-0000-000000000000"} {
		if _, err := s.GetFile(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetFile(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

// TestFileChunkSizeChecked: the schema refuses a chunk over ChunkSize, so a
// row written out of band cannot make GetFile materialise more than one
// chunk beyond the recorded size (r2 reproducer).
func TestFileChunkSizeChecked(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	f, err := s.PutFile(t.Context(), "a.bin", "application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO file_chunks (file_id, seq, data) VALUES ($1, 1, $2)`, f.ID, make([]byte, ChunkSize+1))
	if err == nil || !strings.Contains(err.Error(), "check constraint") {
		t.Fatalf("a %d-byte chunk was accepted: %v", ChunkSize+1, err)
	}
}

// stallReader returns one byte, then blocks until release is closed.
type stallReader struct {
	sent    bool
	release chan struct{}
}

func (r *stallReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		p[0] = 'x'
		return 1, nil
	}
	<-r.release
	return 0, errors.New("client went away")
}

// TestSlowUploadsLeaveConnections: uploads whose readers stall hold at most
// MaxConcurrentUploads connections, so /health's Ping and reads still get
// one, and an upload beyond the cap waits for a slot instead of a connection
// (r2 reproducer: 20 stalled uploads took the whole pool).
func TestSlowUploadsLeaveConnections(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	ctx := t.Context()
	small, err := s.PutFile(ctx, "small.txt", "text/plain", strings.NewReader("ok"))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	done := make(chan error, MaxOpenConns)
	for range MaxOpenConns {
		go func() {
			_, err := s.PutFile(ctx, "slow.bin", "application/octet-stream", &stallReader{release: release})
			done <- err
		}()
	}
	// Wait until every slot is taken (the stalled uploads sit in their transactions).
	deadline := time.Now().Add(10 * time.Second)
	for len(s.uploads) < MaxConcurrentUploads && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(s.uploads) != MaxConcurrentUploads {
		t.Fatalf("%d upload slots taken, want %d", len(s.uploads), MaxConcurrentUploads)
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second) // /health's budget
	defer cancel()
	if err := s.Ping(pctx); err != nil {
		t.Errorf("Ping with %d stalled uploads: %v", MaxOpenConns, err)
	}
	if _, err := s.GetFile(pctx, small.ID); err != nil {
		t.Errorf("GetFile with %d stalled uploads: %v", MaxOpenConns, err)
	}
	wctx, wcancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer wcancel()
	if _, err := s.PutFile(wctx, "late.txt", "text/plain", strings.NewReader("x")); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "upload slot") {
		t.Errorf("an upload beyond the cap: %v, want a deadline while waiting for a slot", err)
	}
	close(release)
	for range MaxOpenConns {
		if err := <-done; err == nil || !strings.Contains(err.Error(), "client went away") {
			t.Errorf("stalled upload ended with %v", err)
		}
	}
	if n := count(t, s, "files"); n != 1 {
		t.Errorf("%d files after the stalled uploads failed, want 1", n)
	}
}

// TestConcurrentReadsBounded: at most MaxConcurrentReads GetFile calls hold a file at once; one
// over the cap waits for a slot (or its context) and then succeeds.
func TestConcurrentReadsBounded(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	f, err := s.PutFile(t.Context(), "a.txt", "text/plain", strings.NewReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	for range MaxConcurrentReads { // the slots a running read holds while it holds a file
		s.reads <- struct{}{}
	}
	wctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := s.GetFile(wctx, f.ID); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "read slot") {
		t.Fatalf("a read over the cap: %v, want a deadline while waiting for a slot", err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.GetFile(t.Context(), f.ID); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("a read ran with every slot taken: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	<-s.reads
	if err := <-done; err != nil {
		t.Fatalf("the waiting read after a slot freed: %v", err)
	}
	for range MaxConcurrentReads - 1 {
		<-s.reads
	}
	if len(s.reads) != 0 {
		t.Errorf("%d read slots still held after every read returned", len(s.reads))
	}
}

// TestFilesCorruptionDetected: storage corruption is ErrCorrupt, not a wrong
// document, and an implausible recorded size is refused before any content
// is read.
func TestFilesCorruptionDetected(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	ctx := t.Context()
	cases := map[string]string{
		"flipped bytes":    `UPDATE file_chunks SET data = 'tampered'::bytea WHERE file_id = $1`,
		"missing chunk":    `DELETE FROM file_chunks WHERE file_id = $1 AND seq = 1`,
		"extra chunk":      `INSERT INTO file_chunks (file_id, seq, data) VALUES ($1, 9, 'x'::bytea)`,
		"size understated": `UPDATE files SET size = size - 1 WHERE id = $1`,
		"size over cap":    `UPDATE files SET size = 1099511627776 WHERE id = $1`,
		"hash changed":     `UPDATE files SET sha256 = repeat('0', 64) WHERE id = $1`,
	}
	for name, corrupt := range cases {
		f, err := s.PutFile(ctx, "a.bin", "application/octet-stream", bytes.NewReader(noise(2*ChunkSize, 5)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(corrupt, f.ID); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, err := s.GetFile(ctx, f.ID); !errors.Is(err, ErrCorrupt) || got.Data != nil {
			t.Errorf("%s: err = %v, want ErrCorrupt and no data", name, err)
		}
	}
	// An upper-case hash written by another tool is the same hash.
	f, _ := s.PutFile(ctx, "b.txt", "text/plain", strings.NewReader("ok"))
	s.db.Exec(`UPDATE files SET sha256 = upper(sha256) WHERE id = $1`, f.ID)
	if _, err := s.GetFile(ctx, f.ID); err != nil {
		t.Errorf("upper-case hash: %v", err)
	}
}

func TestInTx(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	ctx := t.Context()
	insert := `INSERT INTO files (name, content_type, size, sha256) VALUES ('x', 't/t', 0, '')`
	boom := errors.New("boom")
	if err := s.InTx(ctx, func(tx *sql.Tx) error { tx.Exec(insert); return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	func() {
		defer func() { recover() }()
		s.InTx(ctx, func(tx *sql.Tx) error { tx.Exec(insert); panic("x") })
	}()
	if n := count(t, s, "files"); n != 0 {
		t.Fatalf("rolled-back transactions left %d rows", n)
	}
	if err := s.InTx(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(insert); return err }); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "files"); n != 1 {
		t.Fatalf("committed transaction left %d rows, want 1", n)
	}
}

func TestSchemaPerTestIsolation(t *testing.T) {
	t.Parallel()
	var schemaA string
	t.Run("pair", func(t *testing.T) {
		a := openStore(t, storetest.DSN(t))
		b := openStore(t, storetest.DSN(t))
		var sb string
		a.db.QueryRow(`SELECT current_schema()`).Scan(&schemaA)
		b.db.QueryRow(`SELECT current_schema()`).Scan(&sb)
		if schemaA == "" || schemaA == sb || schemaA == "public" {
			t.Fatalf("schemas not distinct: %q %q", schemaA, sb)
		}
		if _, err := a.PutFile(t.Context(), "only-in-a", "text/plain", strings.NewReader("a")); err != nil {
			t.Fatal(err)
		}
		if n := count(t, b, "files"); n != 0 {
			t.Fatalf("schema b sees %d of a's files", n)
		}
	})
	if schemaA == "" {
		return // the subtest skipped (no DSN) or failed; it reported why
	}
	// After the subtest's cleanup, its schemas are gone.
	admin := openDB(t, os.Getenv("BIGLAW_TEST_DSN"))
	var exists bool
	if err := admin.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schemaA).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatalf("schema %s still exists after its test ended", schemaA)
	}
}
