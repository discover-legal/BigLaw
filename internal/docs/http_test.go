// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/store"
)

func serveDocs(t *testing.T, s *Service) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	s.Routes(mux, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func upload(t *testing.T, url, name string, data []byte) (*http.Response, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("note", "ignored")
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	resp, err := http.Post(url+"/documents/upload", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func count(t *testing.T, s *Service, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestUploadReadDeadline: 16 clients that send part of an upload and then stall while staying
// connected take every upload slot, and one more trickles a byte at a time. After the read
// deadline the slots are free, the pool's in-use connections are back to their baseline, a 17th
// upload succeeds, no file row persists, and the stalled clients were answered 408.
func TestUploadReadDeadline(t *testing.T) { // not parallel: it shortens the package's upload bounds
	shorten(t, 500*time.Millisecond, 64<<10, 30*time.Second)
	s := newService(t)
	srv := serveDocs(t, s)
	if resp, _ := upload(t, srv.URL, "warm.txt", []byte("warm up the pool")); resp.StatusCode != 201 {
		t.Fatalf("warm-up upload: %d", resp.StatusCode)
	}
	if err := s.Delete(t.Context(), mustOnlyDocument(t, s)); err != nil {
		t.Fatal(err)
	}
	baseline := s.db.Stats().InUse
	// The trickler takes a slot first, so the trickle (not only the stalls) must be ended.
	trickle := startUpload(t, srv)
	waitFor(t, 5*time.Second, func() bool { return s.st.Uploads() == 1 }, "the trickling client holds a slot")
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				if _, err := trickle.Write([]byte("x")); err != nil {
					return
				}
			}
		}
	}()
	var conns []net.Conn
	for range store.MaxConcurrentUploads { // 15 of these get the remaining slots, one waits
		conns = append(conns, startUpload(t, srv))
	}
	waitFor(t, 5*time.Second, func() bool { return s.st.Uploads() == store.MaxConcurrentUploads }, "every upload slot taken")
	if s.db.Stats().InUse <= baseline {
		t.Fatalf("stalled uploads hold no pool connections (%d in use, baseline %d): the trap is not live", s.db.Stats().InUse, baseline)
	}
	start := time.Now()
	waitFor(t, 10*time.Second, func() bool { return s.st.Uploads() == 0 }, "upload slots released after the read deadline")
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("slots freed after %s: the trickle was not ended by the per-window minimum", d)
	}
	waitFor(t, 5*time.Second, func() bool { return s.db.Stats().InUse <= baseline }, "pool connections back to the baseline")
	resp, out := upload(t, srv.URL, "after.txt", []byte("the 17th upload"))
	if resp.StatusCode != 201 || out["noText"] != false {
		t.Fatalf("an upload after the stalls: %d %v", resp.StatusCode, out)
	}
	if n := count(t, s, "files"); n != 1 {
		t.Errorf("%d file rows, want only the successful upload's", n)
	}
	for i, conn := range append(conns[:2], trickle) {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		r, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil || r.StatusCode != http.StatusRequestTimeout {
			t.Errorf("stalled client %d: %v %v, want 408", i, r, err)
		}
	}
}

// TestUploadTotalDeadline: a client that keeps up the minimum rate is still ended at the total
// bound, and its slot is freed.
func TestUploadTotalDeadline(t *testing.T) {
	shorten(t, 300*time.Millisecond, 1<<10, 1500*time.Millisecond)
	s := newService(t)
	srv := serveDocs(t, s)
	conn := startUpload(t, srv)
	start := time.Now()
	go func() {
		chunk := bytes.Repeat([]byte("y"), 2<<10)
		for time.Since(start) < 10*time.Second {
			if _, err := conn.Write(chunk); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	waitFor(t, 5*time.Second, func() bool { return s.st.Uploads() == 1 }, "the steady client holds a slot")
	waitFor(t, 6*time.Second, func() bool { return s.st.Uploads() == 0 }, "the slot freed at the total bound")
	if d := time.Since(start); d < 1400*time.Millisecond {
		t.Errorf("ended after %s, before the total bound", d)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if r, err := http.ReadResponse(bufio.NewReader(conn), nil); err != nil || r.StatusCode != http.StatusRequestTimeout {
		t.Errorf("steady client: %v %v, want 408", r, err)
	}
	if n := count(t, s, "files"); n != 0 {
		t.Errorf("%d file rows", n)
	}
}

func shorten(t *testing.T, window time.Duration, minBytes int, total time.Duration) {
	oldW, oldM, oldT := uploadWindow, uploadMinBytes, uploadTotal
	uploadWindow, uploadMinBytes, uploadTotal = window, minBytes, total
	t.Cleanup(func() { uploadWindow, uploadMinBytes, uploadTotal = oldW, oldM, oldT })
}

// startUpload opens a connection and sends an upload's headers and its first bytes, no more.
func startUpload(t *testing.T, srv *httptest.Server) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "POST /documents/upload HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary=B\r\nContent-Length: 10000000\r\n\r\n"+
		"--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"stall.txt\"\r\nContent-Type: text/plain\r\n\r\npartial upload ")
	return conn
}

func waitFor(t *testing.T, d time.Duration, ok func() bool, what string) {
	t.Helper()
	for end := time.Now().Add(d); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for: %s", what)
		}
	}
}

func mustOnlyDocument(t *testing.T, s *Service) string {
	t.Helper()
	var id string
	if err := s.db.QueryRow(`SELECT id FROM documents`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestUploadErrorsTyped: refusals carry their status and errorType and leave no rows.
func TestUploadErrorsTyped(t *testing.T) {
	t.Parallel()
	s := newService(t)
	srv := serveDocs(t, s)
	for _, c := range []struct {
		name string
		data []byte
		code int
		kind string
	}{
		{"photo.png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, 415, "unsupported_type"},
		{"broken.pdf", []byte("%PDF-1.4 x"), 422, "unreadable"},
		{"bad\u2066name.txt", []byte("x"), 400, "bad_name"},
		{"", []byte("x"), 400, "bad_name"},
	} {
		resp, out := upload(t, srv.URL, c.name, c.data)
		if resp.StatusCode != c.code || out["errorType"] != c.kind || out["error"] == "" {
			t.Errorf("%q: %d %v, want %d %s", c.name, resp.StatusCode, out, c.code, c.kind)
		}
	}
	for _, h := range [][2]string{{"Origin", "https://evil.example"}, {"Origin", "null"}, {"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"},
		{"Origin", srv.URL}, {"Sec-Fetch-Site", "same-origin"}, {"Sec-Fetch-Site", "none"}} {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "csrf.txt")
		fw.Write([]byte("planted"))
		mw.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/documents/upload", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set(h[0], h[1])
		resp, err := http.DefaultClient.Do(req)
		want := 403
		if h[1] == srv.URL || h[1] == "same-origin" || h[1] == "none" {
			want = 201
		}
		if err != nil || resp.StatusCode != want {
			t.Errorf("%s: %s upload: %v %v, want %d", h[0], h[1], resp.StatusCode, err, want)
		}
	}
	resp, err := http.Post(srv.URL+"/documents/upload", "application/json", strings.NewReader(`{"text":"x"}`))
	if err != nil || resp.StatusCode != 400 {
		t.Errorf("a non-multipart upload: %v %v", resp.StatusCode, err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("other", "x")
	mw.Close()
	if resp, _ := http.Post(srv.URL+"/documents/upload", mw.FormDataContentType(), &body); resp.StatusCode != 400 {
		t.Errorf("multipart without a file part: %d", resp.StatusCode)
	}
	if n := count(t, s, "files") + count(t, s, "documents"); n != 6 {
		t.Errorf("%d rows after refused uploads, want the 3 same-origin uploads' 6", n)
	}
}

// TestFileNotReachableOutsideMatter: a file is read only through a document in scope.
func TestFileNotReachableOutsideMatter(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := t.Context()
	a, err := s.Ingest(ctx, "a.txt", "text/plain", strings.NewReader("matter A"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Ingest(ctx, "b.txt", "text/plain", strings.NewReader("matter B secret"))
	if err != nil {
		t.Fatal(err)
	}
	if f, err := s.File(ctx, []string{a.ID}, a.ID); err != nil || string(f.Data) != "matter A" {
		t.Fatalf("own file: %v", err)
	}
	for _, c := range []struct {
		scope []string
		id    string
	}{{[]string{a.ID}, b.ID}, {nil, b.ID}, {[]string{b.FileID}, b.FileID}, {[]string{a.ID}, b.FileID}} {
		if f, err := s.File(ctx, c.scope, c.id); !errors.Is(err, ErrNotFound) || f.Data != nil {
			t.Errorf("File(%v, %s) = %q, %v; want not found", c.scope, c.id, f.Data, err)
		}
	}
	text := ingestText(t, s, "posted.txt", "no original file")
	if _, err := s.File(ctx, []string{text.ID}, text.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a posted-text document has no file: %v", err)
	}
	srv := serveDocs(t, s)
	for id, want := range map[string]int{a.ID: 200, a.FileID: 404, "nope": 404, text.ID: 404} {
		if resp, err := http.Get(srv.URL + "/documents/" + id + "/file"); err != nil || resp.StatusCode != want {
			t.Errorf("GET /documents/%s/file: %v %v, want %d", id, resp.StatusCode, err, want)
		}
	}
}

// TestFileDeletedWithDocument: deleting a document deletes its file, sections and chunks; a file
// deleted by other means takes its document along.
func TestFileDeletedWithDocument(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := t.Context()
	d, err := s.Ingest(ctx, "a.txt", "text/plain", strings.NewReader("1. One\nalpha\n2. Two\nbeta"))
	if err != nil || d.Sections != 2 || d.Chunks != 2 {
		t.Fatalf("%+v %v", d, err)
	}
	keep := ingestText(t, s, "keep.txt", "kept")
	if err := s.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"files": 0, "file_chunks": 0, "documents": 1, "sections": 1, "chunks": 1} {
		if n := count(t, s, table); n != want {
			t.Errorf("%s: %d rows after delete, want %d", table, n, want)
		}
	}
	if err := s.Delete(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if err := s.Delete(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	if err := s.Delete(ctx, keep.ID); err != nil || count(t, s, "documents") != 0 {
		t.Errorf("deleting a posted-text document: %v", err)
	}
	e, _ := s.Ingest(ctx, "e.txt", "text/plain", strings.NewReader("e"))
	if _, err := s.db.Exec(`DELETE FROM files WHERE id = $1`, e.FileID); err != nil || count(t, s, "documents") != 0 || count(t, s, "chunks") != 0 {
		t.Errorf("a deleted file leaves its document: %v", err)
	}
}

// TestDownloadNameSafe: downloads carry RFC 6266 filename* with the stored name, and both forms
// are safe to save on Windows (reserved characters, device names, trailing dots).
func TestDownloadNameSafe(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"memo.docx": "memo.docx", "CON": "_CON", "aux.txt": "_aux.txt", "Com1.tar.gz": "_Com1.tar.gz", "lpt¹.log": "_lpt¹.log", "nul .txt": "_nul .txt",
		"c:evil.txt": "c_evil.txt", `a<b>"c"|d?.txt`: "a_b__c__d_.txt", "report.pdf.": "report.pdf", "Mémoire d'appel.docx": "Mémoire d'appel.docx",
		"x\x7fy": "x_y", "CONFIG.SYS": "CONFIG.SYS", "...": "_",
	} {
		h := ContentDisposition(name)
		typ, params, err := mime.ParseMediaType(h)
		if err != nil || typ != "attachment" || params["filename"] != want { // Go prefers filename* when both are present
			t.Errorf("ContentDisposition(%q) = %s: filename %q, want %q (%v)", name, h, params["filename"], want, err)
		}
		_, ascii, _ := strings.Cut(h, `filename="`)
		ascii, _, _ = strings.Cut(ascii, `"`)
		if !strings.Contains(h, `filename="`) && !strings.Contains(h, "filename=") || strings.ContainsFunc(ascii, func(r rune) bool { return r > 0x7e }) {
			t.Errorf("%q: the ASCII fallback is missing or not ASCII: %s", name, h)
		}
	}
	s := newService(t)
	d, err := s.Ingest(t.Context(), "aux.txt", "text/html", strings.NewReader("<script>alert(1)</script>"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(serveDocs(t, s).URL + "/documents/" + d.ID + "/file")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "<script>alert(1)</script>" || resp.Header.Get("Content-Disposition") != ContentDisposition("aux.txt") ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Security-Policy") != "sandbox" ||
		resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" { // detected; the declared text/html is never served
		t.Errorf("download: %d %q %v", resp.StatusCode, body, resp.Header)
	}
}

// TestWriteErrorKinds: every error kind maps to its status and errorType; an unclassified error
// is answered with fixed text (it may carry database detail) and logged.
func TestWriteErrorKinds(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		code int
		kind string
	}{
		{ErrNotFound, 404, "not_found"}, {fmt.Errorf("%w: k", ErrBadInput), 400, "bad_request"}, {store.ErrBadName, 400, "bad_name"},
		{store.ErrBadType, 400, "bad_content_type"}, {errCrossOrigin, 403, "cross_origin"}, {errTimeout, 408, "upload_timeout"},
		{store.ErrFileTooLarge, 413, "too_large"}, {ErrUnsupported, 415, "unsupported_type"}, {fmt.Errorf("%w: x", ErrUnreadable), 422, "unreadable"},
		{fmt.Errorf("store: put file: %w", context.Canceled), 499, "cancelled"}, {errors.New(`pq: duplicate key "secret-detail"`), 500, "internal"},
	} {
		var logs bytes.Buffer
		rec := httptest.NewRecorder()
		writeError(rec, slog.New(slog.NewTextHandler(&logs, nil)), c.err)
		var out map[string]string
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != c.code || out["errorType"] != c.kind || out["error"] == "" || strings.Contains(out["error"], "secret-detail") {
			t.Errorf("%v: %d %v, want %d %s", c.err, rec.Code, out, c.code, c.kind)
		}
		if (c.code == 500) != strings.Contains(logs.String(), "secret-detail") {
			t.Errorf("%v: logged %q", c.err, logs.String())
		}
	}
}
