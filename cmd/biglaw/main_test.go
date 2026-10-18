// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   int
		status string
	}{{nil, 200, "ok"}, {errors.New("dial tcp 10.0.0.5:5432: secret-host"), 503, "database unavailable"}} {
		var logs bytes.Buffer
		rec := httptest.NewRecorder()
		routes(fakeDB{tc.err}, nil, slog.New(slog.NewTextHandler(&logs, nil))).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
		var body map[string]string
		json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tc.code || body["status"] != tc.status || body["search"] != "lexical" || strings.Contains(rec.Body.String(), "secret-host") {
			t.Errorf("health(%v) = %d %s", tc.err, rec.Code, rec.Body.String())
		}
		if tc.err != nil && !strings.Contains(logs.String(), "secret-host") {
			t.Errorf("health(%v): cause not logged for the operator: %q", tc.err, logs.String())
		}
	}
	for _, req := range []*http.Request{httptest.NewRequest("GET", "/", nil), httptest.NewRequest("POST", "/health", nil)} {
		rec := httptest.NewRecorder()
		routes(fakeDB{}, nil, discard()).ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Errorf("%s %s = 200, want an error", req.Method, req.URL.Path)
		}
	}
}

func TestRunCommands(t *testing.T) {
	cases := []struct {
		args []string
		env  func(string) string
		code int
		out  string
	}{
		{nil, env(), 2, "usage"},
		{[]string{"bogus"}, env(), 2, "unknown command"},
		{[]string{"version"}, env(), 0, "biglaw 2.0.0-dev"},
		{[]string{"version", "--config"}, env("BIGLAW_DATABASE_URL", dsn), 0, "BIGLAW_ADDR                127.0.0.1:3101  [default]"},
		{[]string{"version", "--config"}, env(), 2, "not valid"},
		{[]string{"version", "--bogus"}, env(), 2, "usage"},
		{[]string{"version", "--config", "x"}, env(), 2, "usage"},
		{[]string{"serve", "extra"}, env(), 2, "usage"},
		{[]string{"serve", "--allow-no-auth", "x"}, env(), 2, "usage"},
		{[]string{"serve"}, env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "https://api.anthropic.com/v1"), 2, "vendor breaker"},
		{[]string{"help"}, env(), 0, "--allow-no-auth"},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		code := run(context.Background(), tc.args, tc.env, nil, &out, &errb)
		if code != tc.code || !strings.Contains(out.String()+errb.String(), tc.out) {
			t.Errorf("biglaw %v: code %d, output %q", tc.args, code, out.String()+errb.String())
		}
	}
}

// TestServeEndToEnd opens (and migrates) a real store, serves /health, and
// stops cleanly on cancel.
// TestMain makes this test binary its own PDF reader (docs.MaybeRunChild), as main does.
func TestMain(m *testing.M) {
	docs.MaybeRunChild()
	os.Exit(m.Run())
}

// loopPDF has a page tree whose Pages node is its own kid: the reader walks it until its timeout.
func loopPDF() []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	var off []int
	for i, o := range []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [2 0 R] /Count 1 >>"} {
		off = append(off, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 3\n0000000000 65535 f \n%010d 00000 n \n%010d 00000 n \ntrailer\n<< /Size 3 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", off[0], off[1], x)
	return b.Bytes()
}

// TestServeStopCancelsIngest (r2 reproducer N6): a stop while an ingest is still reading a file
// cancels it after the grace period: serve returns nil, the client gets 503 "stopping" (not an
// empty reply), and the stored upload is removed, not left for the orphan sweep.
func TestServeStopCancelsIngest(t *testing.T) {
	st, err := store.Open(t.Context(), storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := shutdownGrace
	shutdownGrace = 300 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = old })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, st, docs.New(st), ln, discard()) }()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "loop.pdf")
	fw.Write(loopPDF())
	mw.Close()
	type reply struct {
		code int
		body string
	}
	got := make(chan reply, 1)
	go func() {
		resp, err := http.Post("http://"+ln.Addr().String()+"/documents/upload", mw.FormDataContentType(), &body)
		if err != nil {
			got <- reply{0, err.Error()}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got <- reply{resp.StatusCode, string(b)}
	}()
	files := func() (n int) { st.DB().QueryRow(`SELECT count(*) FROM files`).Scan(&n); return n }
	for start := time.Now(); files() == 0; time.Sleep(20 * time.Millisecond) {
		if time.Since(start) > 10*time.Second {
			t.Fatal("the upload was never stored")
		}
	}
	time.Sleep(200 * time.Millisecond) // the reader child is now walking the loop
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve returned %v after a stop with an ingest in flight", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
	if r := <-got; r.code != 503 || !strings.Contains(r.body, `"errorType":"stopping"`) || files() != 0 {
		t.Errorf("the in-flight upload: %d %s; %d files left", r.code, r.body, files())
	}
}

func TestServeEndToEnd(t *testing.T) {
	st, err := store.Open(t.Context(), storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	orphan, err := st.PutFile(t.Context(), "crashed.pdf", "application/pdf", strings.NewReader("%PDF-")) // an ingest a crash interrupted
	if err != nil {
		t.Fatal(err)
	}
	st.DB().Exec(`UPDATE files SET created_at = now() - interval '2 hours'`)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, st, docs.New(st), ln, discard()) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/health") // ln is already accepting
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("/health = %d %s", resp.StatusCode, body)
	}
	if _, err := st.GetFile(t.Context(), orphan.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("serve did not sweep the orphan file at startup: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v after cancel", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop after cancel")
	}
}

// TestServeHybridEndToEnd: with an embed role, `biglaw serve` embeds at ingest through
// BIGLAW_MODEL_URL's /embeddings, backfills a document stored before the role was set, prices the
// calls, says "hybrid" on /health, and stops cleanly with its backfill.
func TestServeHybridEndToEnd(t *testing.T) {
	dsn := storetest.DSN(t)
	st, err := store.Open(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old, err := docs.New(st).IngestText(t.Context(), "old.txt", "Clause 1. The Supplier shall deliver.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs.New(st).IngestText(t.Context(), "poison.txt", "UNEMBEDDABLE clause."); err != nil { // the endpoint refuses it: stays lexical
		t.Fatal(err)
	}
	var calls atomic.Int32
	emb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		if r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&req) != nil || strings.Contains(strings.Join(req.Input, " "), "UNEMBEDDABLE") {
			w.WriteHeader(404)
			return
		}
		calls.Add(1)
		var data []map[string]any
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, float32(i + 1)}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]int{"prompt_tokens": 10}})
	}))
	defer emb.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		done <- run(ctx, []string{"serve"}, env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_ADDR", addr, "BIGLAW_MODEL_URL", emb.URL+"/v1",
			"BIGLAW_MODELS", "embed=text-embedding-3-small"), nil, io.Discard, &stderr)
	}()
	var body []byte
	for i := 0; i < 100; i++ {
		if resp, err := http.Get("http://" + addr + "/health"); err == nil {
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(string(body), `"search":"hybrid"`) {
		t.Fatalf("/health = %s", body)
	}
	var model sql.NullString
	var cost sql.NullFloat64
	for i := 0; i < 100 && !model.Valid; i++ {
		st.DB().QueryRow(`SELECT embed_model, embed_cost_usd FROM documents WHERE id = $1`, old.ID).Scan(&model, &cost)
		time.Sleep(50 * time.Millisecond)
	}
	if model.String != "text-embedding-3-small" || !cost.Valid || math.Abs(cost.Float64-10*0.02/1e6) > 1e-18 || calls.Load() == 0 {
		t.Fatalf("backfill: model %v, cost %v, %d calls\n%s", model, cost, calls.Load(), stderr.String())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exited %d:\n%s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
	if !strings.Contains(stderr.String(), "documents searched lexically only") || !strings.Contains(stderr.String(), "documents=1") {
		t.Errorf("no lexical-only count after the backfill (PR 3b r2 L2):\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "embedding backfill") {
		t.Errorf("no backfill log line:\n%s", stderr.String())
	}
}

// TestServeStopsCleanlyWhileWaitingForLock: a signal during startup (here,
// while another process holds the migration lock) gives a clear message,
// and the port is not open until migrations are done.
func TestServeStopsCleanlyWhileWaitingForLock(t *testing.T) {
	db := storetest.DSN(t)
	holder, err := store.Open(t.Context(), db, nil) // migrated; now hold the lock
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holdMigrationLock(t, db)
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	free.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- run(ctx, []string{"serve"}, env("BIGLAW_DATABASE_URL", db, "BIGLAW_ADDR", addr), nil, io.Discard, &stderr)
	}()
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(stderr.String(), "waiting for the migration lock"); {
		if time.Now().After(deadline) {
			t.Fatalf("serve never waited for the lock:\n%s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Errorf("%s accepts connections while migrations wait for the lock", addr)
	}
	cancel()
	got := <-code
	for _, want := range []string{"stopped by a signal before startup finished", "held by another BigLaw process"} {
		if got != 1 || !strings.Contains(stderr.String(), want) {
			t.Errorf("code %d, stderr lacks %q:\n%s", got, want, stderr.String())
		}
	}
}

// syncBuffer is a bytes.Buffer safe for one writer and one reader.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// holdMigrationLock takes the advisory lock store.MigrateFS uses for the
// DSN's schema on its own session until the test ends.
func holdMigrationLock(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `SELECT pg_advisory_lock(hashtext('biglaw.migrate.' || current_schema()))`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); db.Close() })
}

// TestServeLogsModelSetup: before touching the database, serve warns that the key would cross plain
// http and logs each role's price once, or that it is unpriced (r1 L2, R1-1; r2 owner ruling: the
// host plays no part, so qwen2.5:14b on localhost is priced).
func TestServeLogsModelSetup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stop at store.Open: only the startup log matters here
	var stderr bytes.Buffer
	run(ctx, []string{"serve"}, env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "http://localhost:4000/v1", "BIGLAW_MODEL_KEY", "sk-SECRET",
		"BIGLAW_MODELS", "lead=claude-sonnet-4-6,critic=qwen2.5:14b,specialist=my-finetune,embed=text-embedding-3-small"), nil, io.Discard, &stderr)
	out := stderr.String()
	for _, want := range []string{"BIGLAW_MODEL_KEY will be sent over plain http",
		`role=lead model=claude-sonnet-4-6 price="$3 in / $15 out per 1M tokens"`, `role=specialist model=my-finetune price="unpriced: cost unknown`,
		`role=critic model=qwen2.5:14b price="$1.6 in / $6.4 out per 1M tokens"`, `role=embed model=text-embedding-3-small price="$0.02 in / $0 out per 1M tokens"`} {
		if !strings.Contains(out, want) {
			t.Errorf("serve log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("serve logged the key:\n%s", out)
	}
}

// TestBinaryIsItsOwnPDFReader: the built binary answers the PDF reader child protocol, so the
// server's uploads parse PDFs out of process (docs.MaybeRunChild is wired into main).
func TestBinaryIsItsOwnPDFReader(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "biglaw")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "__biglaw-extract", strconv.Itoa(os.Getpid()), "60000", "1073741824")
	cmd.Stdin = strings.NewReader("%PDF-1.4 not really")
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), `"Err":"docs: the file could not be read: the PDF is damaged or encrypted"`) {
		t.Errorf("child answer: %s %v", out, err)
	}
}

// TestServeNeverSendsImagesToTheLead (trap, PR 3c): with only a lead role, `biglaw serve` stores an
// uploaded image as needs_transcription (503 vision_not_configured) and sends nothing to the model
// endpoint. Restarted with a vision role, it transcribes the waiting image at startup through the
// endpoint's chat route, every call naming the vision model and none the lead.
func TestServeNeverSendsImagesToTheLead(t *testing.T) {
	dsn := storetest.DSN(t)
	var mu sync.Mutex
	var models []string
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		models = append(models, req.Model)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "Notice of hearing on 3 May 2027."}, "finish_reason": "stop"}},
			"usage": map[string]int{"prompt_tokens": 800, "completion_tokens": 12}})
	}))
	defer chat.Close()
	serveWith := func(roles string, while func(addr string)) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan int, 1)
		var stderr syncBuffer
		go func() {
			done <- run(ctx, []string{"serve"}, env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_ADDR", addr, "BIGLAW_MODEL_URL", chat.URL+"/v1", "BIGLAW_MODELS", roles), nil, io.Discard, &stderr)
		}()
		for i := 0; i < 100; i++ {
			if resp, err := http.Get("http://" + addr + "/health"); err == nil {
				resp.Body.Close()
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		while(addr)
		cancel()
		if code := <-done; code != 0 {
			t.Fatalf("serve exited %d:\n%s", code, stderr.String())
		}
		return stderr.String()
	}
	var id string
	serveWith("lead=text-model", func(addr string) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "letter.png")
		fw.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x00\x00\x00\x00\x3a\x7e\x9b\x55\x00\x00\x00\x0aIDATx\x9cc\x60\x00\x00\x00\x02\x00\x01H\xaf\xa4q\x00\x00\x00\x00IEND\xaeB`\x82"))
		mw.Close()
		resp, err := http.Post("http://"+addr+"/documents/upload", mw.FormDataContentType(), &body)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			ErrorType string
			Document  struct{ ID, Status string }
		}
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != 503 || out.ErrorType != "vision_not_configured" || out.Document.Status != "needs_transcription" {
			t.Fatalf("upload with only a lead: %d %+v", resp.StatusCode, out)
		}
		id = out.Document.ID
	})
	mu.Lock()
	if len(models) != 0 {
		t.Fatalf("an image was sent to the model endpoint with no vision role: %v", models)
	}
	mu.Unlock()
	st, err := store.Open(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var status, text string
	log := serveWith("lead=text-model,vision=qwen2.5vl:7b", func(string) {
		for i := 0; i < 100 && status != "ready"; i++ {
			st.DB().QueryRow(`SELECT status, text FROM documents WHERE id = $1`, id).Scan(&status, &text)
			time.Sleep(50 * time.Millisecond)
		}
	})
	mu.Lock()
	defer mu.Unlock()
	if status != "ready" || text != "Notice of hearing on 3 May 2027." || len(models) != 2 || models[0] != "qwen2.5vl:7b" || models[1] != "qwen2.5vl:7b" ||
		!strings.Contains(log, "transcription jobs") {
		t.Errorf("waiting image after a vision role: %s %q, calls %v\n%s", status, text, models, log)
	}
}
