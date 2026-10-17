// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"errors"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

func openStore(t *testing.T, dsn string) *Store {
	t.Helper()
	s, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
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
	rng := rand.New(rand.NewPCG(1, 2))
	blob := make([]byte, 5<<20)
	for i := range blob {
		blob[i] = byte(rng.Uint32())
	}
	w.Write(blob)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFilesRoundTripBytea(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	in := docx(t)
	if len(in) < 5<<20 {
		t.Fatalf("fixture is %d bytes, want ≥ 5 MB", len(in))
	}
	const ct = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	meta, err := s.PutFile(t.Context(), "memo.docx", ct, in)
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
	if got.Name != "memo.docx" || got.ContentType != ct || got.Size != int64(len(in)) || got.SHA256 != meta.SHA256 {
		t.Fatalf("metadata mismatch: %+v vs %+v", got, meta)
	}
	if _, err := zip.NewReader(bytes.NewReader(got.Data), int64(len(got.Data))); err != nil {
		t.Fatalf("returned .docx does not open as a zip: %v", err)
	}
	info, err := s.FileInfo(t.Context(), meta.ID)
	if err != nil || info.Data != nil || info.Size != meta.Size {
		t.Fatalf("FileInfo = %+v, %v", info, err)
	}
	if err := s.DeleteFile(t.Context(), meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFile(t.Context(), meta.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v, want ErrNotFound", err)
	}
	if err := s.DeleteFile(t.Context(), meta.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v, want ErrNotFound", err)
	}
}

func TestFilesEdgeCases(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	ctx := t.Context()

	empty, err := s.PutFile(ctx, "empty.txt", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetFile(ctx, empty.ID); err != nil || got.Size != 0 || len(got.Data) != 0 {
		t.Fatalf("empty file: %+v, %v", got, err)
	}
	if _, err := s.PutFile(ctx, "big.bin", "application/octet-stream", make([]byte, MaxFileSize+1)); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized: %v, want ErrFileTooLarge", err)
	}
	for _, id := range []string{"", "x", "'; DROP TABLE files; --", "00000000-0000-0000-0000-00000000000g", "00000000-0000-0000-0000-000000000000"} {
		if _, err := s.GetFile(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetFile(%q) = %v, want ErrNotFound", id, err)
		}
	}
	// Silent corruption of the stored bytes is detected, not served.
	f, _ := s.PutFile(ctx, "a.txt", "text/plain", []byte("original"))
	if _, err := s.DB().Exec(`UPDATE files SET data = $1 WHERE id = $2`, []byte("tampered"), f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFile(ctx, f.ID); err == nil {
		t.Fatal("tampered file served without a checksum error")
	}
}

func TestInTx(t *testing.T) {
	t.Parallel()
	s := openStore(t, storetest.DSN(t))
	ctx := t.Context()
	count := func() (n int) {
		s.DB().QueryRow(`SELECT count(*) FROM files`).Scan(&n)
		return n
	}
	insert := `INSERT INTO files (name, content_type, size, sha256, data) VALUES ('x','t',0,'',''::bytea)`
	boom := errors.New("boom")
	if err := s.InTx(ctx, func(tx *sql.Tx) error { tx.Exec(insert); return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	func() {
		defer func() { recover() }()
		s.InTx(ctx, func(tx *sql.Tx) error { tx.Exec(insert); panic("x") })
	}()
	if n := count(); n != 0 {
		t.Fatalf("rolled-back transactions left %d rows", n)
	}
	if err := s.InTx(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(insert); return err }); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
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
		a.DB().QueryRow(`SELECT current_schema()`).Scan(&schemaA)
		b.DB().QueryRow(`SELECT current_schema()`).Scan(&sb)
		if schemaA == "" || schemaA == sb || schemaA == "public" {
			t.Fatalf("schemas not distinct: %q %q", schemaA, sb)
		}
		if _, err := a.PutFile(t.Context(), "only-in-a", "text/plain", []byte("a")); err != nil {
			t.Fatal(err)
		}
		var n int
		b.DB().QueryRow(`SELECT count(*) FROM files`).Scan(&n)
		if n != 0 {
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
