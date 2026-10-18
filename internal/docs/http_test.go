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
	"net/textproto"
	"runtime"
	"runtime/metrics"
	"slices"
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
		{"..", []byte("x"), 400, "bad_name"},
		{"", []byte("x"), 400, "bad_request"}, // no file name: the client sent no file (r1 R12)
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
	for disposition, want := range map[string]int{`form-data; name="file"`: 400, `form-data; name="file"; filename="any.txt"`: 201} { // r1 R12
		body.Reset()
		mw = multipart.NewWriter(&body)
		pw, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Disposition": {disposition}, "Content-Type": {"*/*"}})
		pw.Write([]byte("text"))
		mw.Close()
		resp, _ := http.Post(srv.URL+"/documents/upload", mw.FormDataContentType(), &body)
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != want || want == 400 && !strings.Contains(out["error"], "no file name") {
			t.Errorf("%s with a wildcard type: %d %v, want %d", disposition, resp.StatusCode, out, want)
		}
	}
	if n := count(t, s, "files") + count(t, s, "documents"); n != 8 {
		t.Errorf("%d rows after refused uploads, want the 4 accepted uploads' 8", n)
	}
}

// fileData is a document's original file through WriteFile (nil when nothing was written).
func fileData(s *Service, scope []string, id string) ([]byte, error) {
	var b bytes.Buffer
	err := s.WriteFile(context.Background(), scope, id, func(string, store.File) error { return nil }, &b)
	return b.Bytes(), err
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
	if data, err := fileData(s, []string{a.ID}, a.ID); err != nil || string(data) != "matter A" {
		t.Fatalf("own file: %v", err)
	}
	for _, c := range []struct {
		scope []string
		id    string
	}{{[]string{a.ID}, b.ID}, {nil, b.ID}, {[]string{b.FileID}, b.FileID}, {[]string{a.ID}, b.FileID}} {
		if data, err := fileData(s, c.scope, c.id); !errors.Is(err, ErrNotFound) || data != nil {
			t.Errorf("WriteFile(%v, %s) = %q, %v; want not found", c.scope, c.id, data, err)
		}
	}
	text := ingestText(t, s, "posted.txt", "no original file")
	if _, err := fileData(s, []string{text.ID}, text.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a posted-text document has no file: %v", err)
	}
	srv := serveDocs(t, s)
	c, _ := s.Ingest(ctx, "c.txt", "text/plain", strings.NewReader("stored bytes that rot"))
	s.db.Exec(`UPDATE file_chunks SET data = 'rotten bytes that rot'::bytea WHERE file_id = $1`, c.FileID)
	if resp, err := http.Get(srv.URL + "/documents/" + c.ID + "/file"); err != nil || resp.StatusCode != 500 {
		t.Errorf("a corrupt file is downloaded: %v %v, want 500 before any byte (verified first)", resp.StatusCode, err)
	}
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
	f, _ := s.Ingest(ctx, "f.txt", "text/plain", strings.NewReader("f")) // r1 auditor A6: a delete other than Delete
	if _, err := s.db.Exec(`DELETE FROM documents WHERE id = $1`, f.ID); err != nil || count(t, s, "files") != 0 || count(t, s, "file_chunks") != 0 {
		t.Errorf("a document deleted by SQL leaves its file: %v", err)
	}
}

// TestOrphanFilesSwept (r1 reproducer R7): a file that no document references, left by a server
// that died mid-ingest, is deleted by the sweep once it is an hour old; an owned file and a
// fresh unowned one (an ingest in flight) are kept.
func TestOrphanFilesSwept(t *testing.T) {
	t.Parallel()
	s := newService(t)
	ctx := t.Context()
	owned, _ := s.Ingest(ctx, "owned.txt", "text/plain", strings.NewReader("kept"))
	var ids []string
	for _, name := range []string{"old.pdf", "fresh.pdf"} {
		f, err := s.st.PutFile(ctx, name, "application/pdf", strings.NewReader("%PDF-1.4 interrupted"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	s.db.Exec(`UPDATE files SET created_at = now() - interval '2 hours' WHERE id = $1 OR id = $2`, ids[0], owned.FileID)
	if n, err := s.SweepOrphanFiles(ctx); err != nil || n != 1 || count(t, s, "files") != 2 {
		t.Errorf("sweep: %d %v, %d files left", n, err, count(t, s, "files"))
	}
	if _, err := s.st.GetFile(ctx, ids[0]); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the old orphan: %v", err)
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
	for name, want := range map[string]string{ // r1 prosecutor L-3: both encodings exactly
		"100%.txt": `attachment; filename=100_.txt; filename*=UTF-8''100%25.txt`,
		`a;b".txt`: `attachment; filename="a;b_.txt"; filename*=UTF-8''a%3Bb_.txt`,
		"née.pdf":  `attachment; filename=n_e.pdf; filename*=UTF-8''n%C3%A9e.pdf`,
	} {
		if got := ContentDisposition(name); got != want {
			t.Errorf("ContentDisposition(%q) = %s, want %s", name, got, want)
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

// TestDownloadBounded (r1 reproducer R6, prosecutor M-2, auditor A1/A8): 12 clients that send the
// GET for an 8 MiB file and never read hold at most the read slots' chunks (v1 of this route held
// one whole file per stalled client: +96 MiB here), each is cut within a few transfer windows and
// its slot freed, and a client that reads gets the whole file.
func TestDownloadBounded(t *testing.T) { // not parallel: it shortens the package's transfer window
	s := newService(t)
	data := bytes.Repeat([]byte("The Supplier shall deliver widgets. "), 8<<20/36)
	d, err := s.Ingest(t.Context(), "big.txt", "text/plain", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	old := uploadWindow
	uploadWindow = 1500 * time.Millisecond
	t.Cleanup(func() { uploadWindow = old })
	srv := serveDocs(t, s)
	base := liveHeap()
	for range 12 {
		c, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		c.(*net.TCPConn).SetReadBuffer(4096)
		fmt.Fprintf(c, "GET /documents/%s/file HTTP/1.1\r\nHost: %s\r\n\r\n", d.ID, srv.Listener.Addr())
	}
	// Measure the state being bounded (r1 L-10): every read slot's holder blocked writing to its
	// stalled client (not still fetching chunks), the live heap after a GC (not garbage made since).
	var grew int64
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 10*time.Second {
			t.Fatalf("the %d read slot holders never all blocked writing (%d did)", store.MaxConcurrentReads, stalledWriters())
		}
		if stalledWriters() == store.MaxConcurrentReads {
			if grew = liveHeap() - base; stalledWriters() == store.MaxConcurrentReads {
				break
			}
		}
	}
	t.Logf("12 stalled downloads of an 8 MiB file: live heap +%d MiB, read slots in use %d", grew>>20, s.st.Reads())
	// Each holder keeps the chunk it holds back and the one it is writing; 8 MiB for the rest.
	if grew > store.MaxConcurrentReads*2*store.ChunkSize+8<<20 || s.st.Reads() > store.MaxConcurrentReads {
		t.Errorf("stalled downloads hold %d MiB and %d read slots", grew>>20, s.st.Reads())
	}
	start := time.Now()
	for s.st.Reads() != 0 || time.Since(start) < uploadWindow {
		if time.Since(start) > 20*time.Second {
			t.Fatalf("%d read slots still held by stalled downloads after 20 s", s.st.Reads())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("every stalled download cut after %s", time.Since(start).Round(time.Millisecond))
	resp, err := http.Get(srv.URL + "/documents/" + d.ID + "/file")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || !bytes.Equal(got, data) || resp.Header.Get("Content-Length") != fmt.Sprint(len(data)) {
		t.Errorf("a reading client: %d of %d bytes, %v, Content-Length %q", len(got), len(data), err, resp.Header.Get("Content-Length"))
	}
}

// liveHeap is the heap live after a full GC (runtime/metrics: no garbage allocated since counts).
func liveHeap() int64 {
	runtime.GC()
	m := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(m)
	return int64(m[0].Value.Uint64())
}

// stalledWriters counts goroutines blocked writing a download to a client that stopped reading.
func stalledWriters() int {
	buf := make([]byte, 8<<20)
	n := 0
	for _, g := range strings.Split(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
		if strings.Contains(g, "docs.floorWriter.Write(") && strings.Contains(g, "waitWrite") {
			n++
		}
	}
	return n
}

// plainWriter hides the connection from http.ResponseController, as a middleware wrapper without
// Unwrap would.
type plainWriter struct{ http.ResponseWriter }

// TestTransferDeadlinesFailClosed (red team r1): where the read or write deadline cannot be set,
// an upload is refused and a download is cut before its first byte, never run without a bound.
func TestTransferDeadlinesFailClosed(t *testing.T) {
	t.Parallel()
	s := newService(t)
	d, err := s.Ingest(t.Context(), "a.txt", "text/plain", strings.NewReader("bounded"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Routes(mux, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mux.ServeHTTP(plainWriter{w}, r) }))
	t.Cleanup(srv.Close)
	if resp, _ := upload(t, srv.URL, "b.txt", []byte("x")); resp.StatusCode != 500 || count(t, s, "documents") != 1 {
		t.Errorf("an upload without a read deadline: %d", resp.StatusCode)
	}
	if resp, err := http.Get(srv.URL + "/documents/" + d.ID + "/file"); err == nil {
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil && string(body) == "bounded" {
			t.Errorf("a download without a write deadline was sent: %d %q", resp.StatusCode, body)
		}
	}
}

// deadlineRecorder is a ResponseWriter that records each write's size and whether a fresh write
// deadline preceded it (http.ResponseController finds SetWriteDeadline by method).
type deadlineRecorder struct {
	httptest.ResponseRecorder
	fresh  bool
	writes []int
	bad    int
}

func (d *deadlineRecorder) SetWriteDeadline(time.Time) error { d.fresh = true; return nil }
func (d *deadlineRecorder) Write(p []byte) (int, error) {
	if !d.fresh {
		d.bad++
	}
	d.fresh, d.writes = false, append(d.writes, len(p))
	return len(p), nil
}

// TestDownloadDeadlinePerWindow (r2 prosecutor L-2): a download writes at most uploadMinBytes
// under each write deadline, so a client reading just above the floor completes a large file.
func TestDownloadDeadlinePerWindow(t *testing.T) {
	t.Parallel()
	rec := &deadlineRecorder{}
	fw := floorWriter{rec, http.NewResponseController(rec), time.Now().Add(time.Hour)}
	if n, err := fw.Write(make([]byte, 2<<20+5)); err != nil || n != 2<<20+5 {
		t.Fatal(n, err)
	}
	if len(rec.writes) != 33 || slices.Max(rec.writes) > uploadMinBytes || rec.bad != 0 {
		t.Errorf("%d writes, largest %d bytes, %d without a fresh deadline; want 33 of at most %d, each with one", len(rec.writes), slices.Max(rec.writes), rec.bad, uploadMinBytes)
	}
}

// TestIngestSlotWaitBounded (r2 reproducer N5): an upload waits at most slotWait for an ingest
// slot, then is 503 busy and its stored file removed, so no upload outlives the orphan sweep's hour.
func TestIngestSlotWaitBounded(t *testing.T) { // not parallel: it shortens slotWait
	s := newService(t)
	old := slotWait
	slotWait = 200 * time.Millisecond
	t.Cleanup(func() { slotWait = old })
	for range ingestSlots {
		s.slots <- struct{}{}
	}
	start := time.Now()
	resp, body := upload(t, serveDocs(t, s).URL, "a.txt", []byte("waiting"))
	if resp.StatusCode != 503 || body["errorType"] != "busy" || count(t, s, "files") != 0 || time.Since(start) > 5*time.Second {
		t.Errorf("an upload with every slot taken: %d %v after %s, %d files", resp.StatusCode, body, time.Since(start), count(t, s, "files"))
	}
}
