// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
	"github.com/discover-legal/BigLaw/internal/engine/figtest"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

func TestMain(m *testing.M) {
	docs.MaybeRunChild()
	poll = 20 * time.Millisecond
	os.Exit(m.Run())
}

type server struct {
	*httptest.Server
	t  *testing.T
	e  *engine.Engine
	c  *http.Client // the browser requests go from (nil: http.DefaultClient)
	st *store.Store
}

// as is s with requests from browser c (a signed-in user's cookie jar).
func (s *server) as(c *http.Client) *server { x := *s; x.c = c; return &x }

func (s *server) client() *http.Client {
	if s.c != nil {
		return s.c
	}
	return http.DefaultClient
}

// newServer is the API over a fresh schema, an engine on scripts, and its runner.
func newServer(t *testing.T, scripts func(docIDs []string) map[string][]model.Reply, roles model.Roles, texts ...string) (*server, []string) {
	return newServerWith(t, nil, scripts, roles, texts...)
}

// newServerWith is newServer with the handler made by wrap (nil: Handler) once the server's URL is known.
func newServerWith(t *testing.T, wrap func(url string, st *store.Store, d *docs.Service, e *engine.Engine) http.Handler, scripts func(docIDs []string) map[string][]model.Reply, roles model.Roles, texts ...string) (*server, []string) {
	t.Helper()
	st, err := store.OpenPool(context.Background(), storetest.DSN(t), nil, storetest.Pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := docs.New(st)
	var ids []string
	for i, x := range texts {
		doc, err := d.IngestText(context.Background(), fmt.Sprintf("doc%d.md", i+1), x)
		if err != nil {
			t.Fatal(err)
		}
		d.File(context.Background(), doc.ID, "M-1")
		ids = append(ids, doc.ID)
	}
	var sc map[string][]model.Reply
	if scripts != nil {
		sc = scripts(ids)
	}
	e := engine.New(st, d, readerFake(sc), roles, nil, engine.Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Start(ctx) }()
	srv := httptest.NewServer(nil)
	if wrap == nil {
		srv.Config.Handler = Handler(d, e, nil, slog.New(slog.DiscardHandler))
	} else {
		srv.Config.Handler = wrap(srv.URL, st, d, e)
	}
	t.Cleanup(func() { srv.Close(); cancel(); <-done })
	return &server{srv, t, e, nil, st}, ids
}

func (s *server) do(method, path string, body any) (int, string) {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.URL+path, r)
	resp, err := s.client().Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var roles = model.Roles{Lead: "lead-m"}

const doc1 = "1. Damages\nGreystone claims damages of $644,910 for unpaid invoices.\n"

func call(name string, args any) model.ToolCall {
	b, _ := json.Marshal(args)
	return model.ToolCall{Function: model.Function{Name: name, Arguments: string(b)}}
}

func reply(v any) model.Reply {
	b, _ := json.Marshal(v)
	return model.Reply{Text: string(b)}
}

type m = map[string]any

// memo is a complete scripted memo run over one document.
func memo(ids []string) map[string][]model.Reply {
	ledger := m{"op": "plan", "issues": []m{{"id": "I1", "question": "What is claimed?", "class": "Conduct", "practice": "litigation"}}, "issueId": nil, "fid": nil,
		"detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": nil}
	return map[string][]model.Reply{
		"lead": {{ToolCalls: []model.ToolCall{call("ledger", ledger)}},
			{ToolCalls: []model.ToolCall{call("extract_facts", m{"question": "q", "documentId": ids[0], "sectionId": nil, "through": nil, "offset": nil, "query": "damages", "issueId": "I1"})}},
			{ToolCalls: []model.ToolCall{call("draft_section", m{"issueId": "I1", "fids": []string{"F1"}, "revise": nil})}},
			{ToolCalls: []model.ToolCall{call("committee", m{})}},
			{ToolCalls: []model.ToolCall{call("finish", m{"op": "submit", "limitations": nil})}},
			{ToolCalls: []model.ToolCall{call("finish", m{"op": "submit", "limitations": nil})}}},
		"extract_facts": {reply(m{"findings": []m{{"issueId": nil, "claim": "Greystone claims $644,910.", "quotes": []m{{"documentId": ids[0], "quote": "Greystone claims damages of $644,910 for unpaid invoices", "locator": nil}},
			"values": []m{}, "entities": []m{}, "relations": []m{}, "confidence": 0.9}}, "gaps": []string{}})},
		"draft_section": {reply(m{"markdown": "Greystone claims damages of $644,910 [F1].", "summary": "s"})},
		"prosecute":     {reply(m{"verdicts": []m{}})}, "reproduce": {reply(m{"answers": []m{}})}, "audit_gaps": {reply(m{"gaps": []m{}})},
		"meta": {{Text: "Checked."}, {Text: "Checked again."}},
	}
}

func (s *server) waitFor(id string, ok func(engine.TaskView) bool) engine.TaskView {
	s.t.Helper()
	for range 200 {
		_, body := s.do("GET", "/tasks/"+id, nil)
		var v engine.TaskView
		json.Unmarshal([]byte(body), &v)
		if ok(v) {
			return v
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.t.Fatalf("task %s never got there", id)
	return engine.TaskView{}
}

// TestRunProgressAPI: POST /tasks, then GET /tasks/{id} carries run {status, step, stopReason,
// budget, openFindings}, gates and findings; /steps?after=N pages the log; gates decide; cost. The
// run's 12 calls include the figure reader's one (its section's, with the finding it cites: a finding
// stating no typed value is read when a section first cites it, bench7; figtest.Reader).
func TestRunProgressAPI(t *testing.T) {
	t.Parallel()
	s, ids := newServer(t, memo, roles, doc1)
	if _, body := s.do("GET", "/me", nil); !strings.Contains(body, `"model":true`) { // a lead model is set up: no first-run card
		t.Fatalf("/me: %s", body)
	}
	code, body := s.do("POST", "/tasks", m{"description": "What is claimed?", "workflowType": "memo", "jurisdiction": "US", "matterNumber": "M-1", "documentIds": ids, "unknownField": 1})
	var v engine.TaskView
	if json.Unmarshal([]byte(body), &v); code != 201 || v.ID == "" || v.Run.Budget.Remaining != 150 {
		t.Fatalf("%d %s", code, body)
	}
	v = s.waitFor(v.ID, func(v engine.TaskView) bool { return v.Status == "awaiting_gate" })
	if v.Run.StopReason == nil || *v.Run.StopReason != "finished" || v.Run.Step != 10 || v.Run.Budget.Calls != 12 || v.Run.Budget.USD != nil || len(v.Findings) != 1 || v.Output == nil {
		t.Fatalf("%+v", v)
	}
	_, body = s.do("GET", "/tasks/"+v.ID+"/steps?after=8", nil)
	var steps []engine.StepView
	if json.Unmarshal([]byte(body), &steps); len(steps) != 2 || steps[0].Step != 9 || steps[1].Tool != "finish" || !strings.Contains(steps[1].Summary, "accepted") {
		t.Fatalf("steps after 8: %s", body)
	}
	if code, _ := s.do("POST", "/tasks/"+v.ID+"/gates/"+v.PendingGates[0].GateID+"/reject", m{}); code != 400 {
		t.Fatalf("a rejection without a reason: %d", code)
	}
	if code, _ := s.do("POST", "/tasks/"+v.ID+"/gates/nope/approve", nil); code != 404 {
		t.Fatalf("unknown gate: %d", code)
	}
	code, body = s.do("POST", "/tasks/"+v.ID+"/gates/"+v.PendingGates[0].GateID+"/reject", m{"reason": "Cite the invoice dates."})
	if code != 200 || !strings.Contains(body, `"decision":"rejected"`) || !strings.Contains(body, `"decidedBy":{"id":"local","name":"Local user"}`) { // no sign-in: the local user decided
		t.Fatalf("%d %s", code, body)
	}
	v = s.waitFor(v.ID, func(v engine.TaskView) bool { return v.Status == "awaiting_gate" && len(v.DecidedGates) == 1 })
	code, body = s.do("POST", "/tasks/"+v.ID+"/gates/"+v.PendingGates[0].GateID+"/approve", nil)
	if code != 200 || !strings.Contains(body, `"status":"complete"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := s.do("POST", "/tasks/"+v.ID+"/gates/"+v.PendingGates[0].GateID+"/approve", nil); code != 409 {
		t.Fatalf("a decided gate: %d", code)
	}
	_, body = s.do("GET", "/tasks/"+v.ID+"/cost", nil)
	var c engine.Cost
	if json.Unmarshal([]byte(body), &c); c.Summary.Calls != 14 || c.Summary.UnpricedCalls != 13 || c.Summary.USD != nil || len(c.Entries) == 0 || c.Reader == nil || c.Reader.Calls != 1 || c.Reader.FailureRate != 0 { // the figure reader's line (bench7: its failures were invisible)
		t.Fatalf("cost %s", body)
	}
	if code, body := s.do("GET", "/tasks/"+v.ID+"/deliverable.md", nil); code != 200 || !strings.Contains(body, "$644,910 [F1]") {
		t.Fatalf("%d %s", code, body)
	}
}

// TestEventsStream: the SSE stream sends each finished step once, in order, the run when it
// changes, and done when the run stops.
func TestEventsStream(t *testing.T) {
	t.Parallel()
	s, ids := newServer(t, memo, roles, doc1)
	_, body := s.do("POST", "/tasks", m{"description": "What is claimed?", "workflowType": "memo", "jurisdiction": "US", "matterNumber": "M-1", "documentIds": ids})
	var v engine.TaskView
	json.Unmarshal([]byte(body), &v)
	resp, err := http.Get(s.URL + "/tasks/" + v.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var events []string
	last := 0
	sc := bufio.NewScanner(resp.Body)
	for ev := ""; sc.Scan(); {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			ev = strings.TrimPrefix(line, "event: ")
			events = append(events, ev)
		}
		if strings.HasPrefix(line, "data: ") && ev == "step" {
			var st engine.StepView
			json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &st)
			if st.Step != last+1 || st.Status == "started" {
				t.Fatalf("step %d after %d (%s)", st.Step, last, st.Status)
			}
			last = st.Step
		}
	}
	if resp.Header.Get("Content-Type") != "text/event-stream" || last != 10 || events[len(events)-1] != "done" || !strings.Contains(strings.Join(events, ","), "task") {
		t.Fatalf("events %v last %d", events, last)
	}
}

// TestEventsStreamSendsTheLastStep (CI #61, PG14, shuffle 1792973692437222411: "last 9"): the
// stream's run finishing between its two reads of a poll still sends every step before done. The gap
// waits there until the run has stopped, deterministically; with the reads in the other order (the
// steps, then the run) the stream sends done without the steps committed in the gap.
func TestEventsStreamSendsTheLastStep(t *testing.T) {
	s, ids := newServer(t, memo, roles, doc1)
	_, body := s.do("POST", "/tasks", m{"description": "What is claimed?", "workflowType": "memo", "documentIds": ids})
	var v engine.TaskView
	json.Unmarshal([]byte(body), &v)
	var once sync.Once
	streamGap.Store(func(id string) {
		if id != v.ID {
			return
		}
		once.Do(func() { s.waitFor(v.ID, func(v engine.TaskView) bool { return v.Status == "awaiting_gate" }) })
	})
	t.Cleanup(func() { streamGap.Store(func(string) {}) })
	resp, err := http.Get(s.URL + "/tasks/" + v.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	steps := strings.Count(string(b), "event: step\n")
	if steps != 10 || !strings.HasSuffix(strings.TrimSpace(string(b)), `"openFindings":0}`) || !strings.Contains(string(b), "event: done") {
		t.Fatalf("%d steps before done, want 10:\n%s", steps, b)
	}
}

// TestEventsStreamReopensAfterAGate: a stream ends with done when the run pauses at a gate; once the
// gate is decided the run resumes, and a stream opened again (as the Task screen does) sends every
// step from the first once, in order, the ones after the pause too, then done when the run stops again.
func TestEventsStreamReopensAfterAGate(t *testing.T) {
	t.Parallel()
	s, ids := newServer(t, memo, roles, doc1)
	_, body := s.do("POST", "/tasks", m{"description": "What is claimed?", "workflowType": "memo", "documentIds": ids})
	var v engine.TaskView
	json.Unmarshal([]byte(body), &v)
	read := func() (steps []int, done *engine.Run) {
		resp, err := http.Get(s.URL + "/tasks/" + v.ID + "/events")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for ev := ""; sc.Scan(); {
			line, data := sc.Text(), []byte(strings.TrimPrefix(sc.Text(), "data: "))
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case ev == "step" && strings.HasPrefix(line, "data: "):
				var st engine.StepView
				json.Unmarshal(data, &st)
				steps = append(steps, st.Step)
			case ev == "done" && strings.HasPrefix(line, "data: "):
				done = &engine.Run{}
				json.Unmarshal(data, done)
			}
		}
		return steps, done
	}
	first, done := read()
	if done == nil || done.Status != "awaiting_gate" || len(first) != 10 {
		t.Fatalf("first stream: steps %v, done %+v", first, done)
	}
	v = s.waitFor(v.ID, func(v engine.TaskView) bool { return len(v.PendingGates) == 1 })
	if code, body := s.do("POST", "/tasks/"+v.ID+"/gates/"+v.PendingGates[0].GateID+"/reject", m{"reason": "Cite the invoice dates."}); code != 200 || !strings.Contains(body, `"status":"running"`) {
		t.Fatalf("%d %s", code, body)
	}
	again, done := read()
	for i, n := range again {
		if n != i+1 {
			t.Fatalf("reopened stream: steps %v", again)
		}
	}
	if done == nil || done.Status != "awaiting_gate" || len(again) <= len(first) || done.Step != len(again) {
		t.Fatalf("reopened stream: steps %v, done %+v", again, done)
	}
}

// TestModelErrorsAsJSON: each model failure kind maps to its status, errorType and a fixed message
// (nothing of the endpoint's own text); a cancelled request is 499.
func TestModelErrorsAsJSON(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
		kind string
	}{
		{model.ErrNotConfigured, 503, "model_not_configured"}, {model.ErrRateLimited, 503, "model_rate_limited"}, {model.ErrTimeout, 504, "model_timeout"},
		{model.ErrNoContent, 422, "no_content"}, {model.ErrUnavailable, 502, "model_unavailable"}, {context.Canceled, 499, "cancelled"}, {errors.New("pq: secret"), 500, "internal"},
	} {
		err := fmt.Errorf("wrapped: %w", &model.Error{Kind: c.err, Detail: "upstream said https://key@host/x"})
		if c.err == context.Canceled || c.kind == "internal" {
			err = fmt.Errorf("wrapped: %w", c.err)
		}
		code, kind, msg := engine.ErrorType(err)
		if code != c.code || kind != c.kind || strings.Contains(msg, "upstream") || strings.Contains(msg, "secret") {
			t.Errorf("%v: %d %s %q", c.err, code, kind, msg)
		}
	}
	// Over HTTP: no lead model configured → 503 model_not_configured as JSON.
	s, ids := newServer(t, nil, model.Roles{}, doc1)
	code, body := s.do("POST", "/tasks", m{"description": "x", "workflowType": "memo", "documentIds": ids})
	if code != 503 || !strings.Contains(body, `"errorType":"model_not_configured"`) {
		t.Fatalf("%d %s", code, body)
	}
	for _, bad := range []m{{"description": "", "workflowType": "memo", "documentIds": ids}, {"description": "x", "workflowType": "essay", "documentIds": ids},
		{"description": "x", "workflowType": "memo", "documentIds": []string{}}} {
		if code, body := s.do("POST", "/tasks", bad); code != 400 || !strings.Contains(body, "bad_request") {
			t.Errorf("%v: %d %s", bad, code, body)
		}
	}
}

// TestAPIListsNeverNull: every list endpoint answers [] on an empty database.
func TestAPIListsNeverNull(t *testing.T) {
	t.Parallel()
	s, _ := newServer(t, nil, roles)
	for _, p := range []string{"/tasks", "/documents", "/documents?matter=M-9", "/clients", "/documents/search?q=anything&ids="} {
		if code, body := s.do("GET", p, nil); code != 200 || strings.TrimSpace(body) != "[]" {
			t.Errorf("%s: %d %q", p, code, body)
		}
	}
}

// TestCancelAPI: cancel stops a task; a second cancel is a conflict.
func TestCancelAPI(t *testing.T) {
	t.Parallel()
	s, ids := newServer(t, func([]string) map[string][]model.Reply { return nil }, roles, doc1)
	_, body := s.do("POST", "/tasks", m{"description": "x", "workflowType": "memo", "documentIds": ids})
	var v engine.TaskView
	json.Unmarshal([]byte(body), &v)
	s.waitFor(v.ID, func(v engine.TaskView) bool { return v.Status == "failed" || v.Status == "running" })
	if code, _ := s.do("POST", "/tasks/"+v.ID+"/cancel", nil); code != 200 && code != 409 {
		t.Fatalf("cancel: %d", code)
	}
	if code, _ := s.do("POST", "/tasks/"+v.ID+"/cancel", nil); code != 409 {
		t.Fatalf("second cancel: %d", code)
	}
}

// TestEmbeddedUIServesIndex and TestBuildWithoutUIServesNotice: "/" serves the built UI when one is
// embedded, and otherwise explains how to build it while the API keeps working.
func TestEmbeddedUIServesIndex(t *testing.T) {
	h := uiHandler(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>BigLaw UI</title>")}, "assets/app.js": {Data: []byte("x")}})
	for p, want := range map[string]string{"/": "BigLaw UI", "/assets/app.js": "x"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

func TestBuildWithoutUIServesNotice(t *testing.T) {
	rec := httptest.NewRecorder()
	uiHandler(mustSub(dist, "dist")).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if _, err := dist.Open("dist/index.html"); err == nil {
		t.Skip("this build has the UI embedded (make build)")
	}
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "make build") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	uiHandler(mustSub(dist, "dist")).ServeHTTP(rec, httptest.NewRequest("GET", "/assets/x.js", nil))
	if rec.Code != 404 {
		t.Fatalf("missing asset: %d", rec.Code)
	}
}

// TestWritesRefuseCrossOrigin (PR 4b r1 H1): a page the lawyer visits cannot spend the budget
// (POST /tasks), decide a human gate (G7), cancel a task or file a document through a loopback
// BigLaw: a write a browser sends from another site is 403 cross_origin and changes nothing; the
// UI (same origin) and non-browser clients (no Origin, no Sec-Fetch-Site) pass.
func TestWritesRefuseCrossOrigin(t *testing.T) {
	t.Parallel()
	s, ids := newServer(t, memo, roles, doc1)
	send := func(method, path string, body any, h ...string) (int, string) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, s.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "text/plain") // a "simple" request: no CORS preflight
		for i := 0; i < len(h); i += 2 {
			req.Header.Set(h[i], h[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}
	hostile := [][]string{{"Origin", "https://evil.example"}, {"Origin", "null"}, {"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"}}
	task := m{"description": "x", "workflowType": "memo", "documentIds": ids}
	for _, h := range hostile {
		for _, w := range []struct {
			path string
			body any
		}{{"/tasks", task}, {"/documents", m{"name": "planted.md", "text": "Planted.", "matterNumber": "M-1"}}} {
			if code, body := send("POST", w.path, w.body, h...); code != 403 || !strings.Contains(body, `"errorType":"cross_origin"`) {
				t.Errorf("%v POST %s: %d %s", h, w.path, code, body)
			}
		}
	}
	if _, body := s.do("GET", "/tasks", nil); strings.TrimSpace(body) != "[]" {
		t.Fatalf("a cross-site POST /tasks created a task: %s", body)
	}
	if _, body := s.do("GET", "/documents", nil); strings.Contains(body, "planted") {
		t.Fatalf("a cross-site POST /documents filed a document: %s", body)
	}
	code, body := send("POST", "/tasks", task, "Origin", s.URL, "Sec-Fetch-Site", "same-origin")
	var v engine.TaskView
	if json.Unmarshal([]byte(body), &v); code != 201 {
		t.Fatalf("same-origin submit: %d %s", code, body)
	}
	v = s.waitFor(v.ID, func(v engine.TaskView) bool { return v.Status == "awaiting_gate" })
	gate := "/tasks/" + v.ID + "/gates/" + v.PendingGates[0].GateID
	for _, h := range hostile {
		for _, p := range []string{gate + "/approve", gate + "/reject", "/tasks/" + v.ID + "/cancel"} {
			if code, body := send("POST", p, m{"reason": "forged"}, h...); code != 403 || !strings.Contains(body, `"errorType":"cross_origin"`) {
				t.Errorf("%v POST %s: %d %s", h, p, code, body)
			}
		}
	}
	if v = s.waitFor(v.ID, func(engine.TaskView) bool { return true }); v.Status != "awaiting_gate" || len(v.DecidedGates) != 0 {
		t.Fatalf("a cross-site request changed the task: %s %+v", v.Status, v.DecidedGates)
	}
	if code, body := send("POST", gate+"/approve", nil); code != 200 || !strings.Contains(body, `"status":"complete"`) {
		t.Fatalf("a non-browser approve: %d %s", code, body)
	}
}

// TestUIRefusesFraming (PR 4b r1 M7): another site cannot frame the UI and trick the lawyer into
// clicking Approve on a gate (clickjacking): every UI response forbids framing.
func TestUIRefusesFraming(t *testing.T) {
	for _, ui := range []fs.FS{fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>BigLaw UI</title>")}}, fstest.MapFS{}} {
		rec := httptest.NewRecorder()
		uiHandler(ui).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" {
			t.Errorf("headers %v", rec.Header())
		}
	}
}

// readerFake is a scripted model whose figure reader is the stand-in (figtest.Reader), so a run's
// figures are read as the engine's tests read them.
func readerFake(sc map[string][]model.Reply) *model.Fake {
	f := model.NewFake(sc)
	f.Respond = figtest.Reader
	return f
}
