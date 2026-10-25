// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package api is BigLaw's REST surface over net/http: the document routes (docs), the matter
// list, tasks with their run, steps (and an SSE stream), gates, cancel and cost, Manage (manage.go:
// the agents, tools, packs, skills and hooks a run uses, admins' versioned overrides), and the embedded
// UI. JSON throughout; lists are never null; errors are {error, errorType} with a typed status.
package api

import (
	"cmp"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
)

//go:embed dist
var dist embed.FS

// poll is how often an event stream reads the run (a variable so tests can shorten it).
var poll = 500 * time.Millisecond

// taskIn is POST /tasks's body (the agent's schema subset validates it, way 6); unknown fields are ignored.
var taskIn = mustSchema(`{"type":"object","required":["description","workflowType","documentIds"],"properties":{
 "description":{"type":"string","minLength":1,"maxLength":20000},
 "workflowType":{"type":"string","enum":["memo","diligence","contract_review"]},
 "jurisdiction":{"type":["string","null"],"maxLength":16},
 "matterNumber":{"type":["string","null"],"maxLength":64},
 "documentIds":{"type":"array","minItems":1,"maxItems":500,"items":{"type":"string","maxLength":64}},
 "maxSteps":{"type":["integer","null"],"minimum":1,"maximum":10000},
 "toolBandit":{"type":["string","null"],"enum":["on","off",null]}}}`)

func mustSchema(s string) *agent.Schema {
	sc, err := agent.Compile(json.RawMessage(s))
	if err != nil {
		panic(err)
	}
	return sc
}

// Handler is the whole API, with ui (nil: the embedded dist) served at "/".
func Handler(d *docs.Service, e *engine.Engine, ui fs.FS, log *slog.Logger) *http.ServeMux {
	return handler(d, e, ui, log, local)
}

// handler is Handler with who resolving each request's principal (Manage's writes are an admin's).
func handler(d *docs.Service, e *engine.Engine, ui fs.FS, log *slog.Logger, who Who) *http.ServeMux {
	mux := http.NewServeMux()
	d.Routes(mux, log)
	reply := func(w http.ResponseWriter, code int, v any, err error) {
		if err != nil {
			code, kind, msg := engine.ErrorType(err)
			if code == 500 {
				log.Error("api: request failed", "err", err)
			}
			v = map[string]string{"error": msg, "errorType": kind}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /clients", func(w http.ResponseWriter, r *http.Request) {
		ds, err := d.List(r.Context(), "") // until PR 8's clients: one entry, a matter per matter number
		type matter struct {
			Number      string   `json:"number"`
			Title       string   `json:"title"`
			DocumentIDs []string `json:"documentIds"`
		}
		ms := []*matter{}
		for _, x := range ds {
			if x.MatterNumber == "" {
				continue
			}
			i := 0
			for i < len(ms) && ms[i].Number != x.MatterNumber {
				i++
			}
			if i == len(ms) {
				ms = append(ms, &matter{x.MatterNumber, "Matter " + x.MatterNumber, []string{}})
			}
			ms[i].DocumentIDs = append(ms[i].DocumentIDs, x.ID)
		}
		out := []any{}
		if len(ms) > 0 {
			out = append(out, map[string]any{"id": "matters", "name": "Matters (filed with documents; clients arrive in PR 8)", "matters": ms})
		}
		reply(w, 200, out, err)
	})
	mux.HandleFunc("POST /tasks", docs.Guard(log, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err == nil {
			if err = taskIn.Validate(body); err != nil {
				err = fmt.Errorf("%w: %v", engine.ErrBadTask, err)
			}
		}
		var in struct {
			engine.Task
			ToolBandit *string `json:"toolBandit"`
		}
		if err == nil {
			err = json.Unmarshal(body, &in)
			in.Task.ToolBandit = in.ToolBandit == nil || *in.ToolBandit == "on"
		}
		var t engine.Task
		if err == nil {
			t, err = e.Submit(r.Context(), in.Task)
		}
		var v engine.TaskView
		if err == nil {
			v, err = e.Get(r.Context(), t.ID)
		}
		reply(w, http.StatusCreated, v, err)
	}))
	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		ts, err := e.List(r.Context())
		reply(w, 200, ts, err)
	})
	mux.HandleFunc("GET /tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := e.Get(r.Context(), r.PathValue("id"))
		reply(w, 200, v, err)
	})
	mux.HandleFunc("GET /tasks/{id}/steps", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		ss, err := e.Steps(r.Context(), r.PathValue("id"), after)
		reply(w, 200, ss, err)
	})
	mux.HandleFunc("GET /tasks/{id}/cost", func(w http.ResponseWriter, r *http.Request) {
		c, err := e.Cost(r.Context(), r.PathValue("id"))
		reply(w, 200, c, err)
	})
	mux.HandleFunc("GET /tasks/{id}/deliverable.md", func(w http.ResponseWriter, r *http.Request) {
		v, err := e.Get(r.Context(), r.PathValue("id"))
		if err == nil && v.Output == nil {
			err = fmt.Errorf("%w: no deliverable yet", engine.ErrNotFound)
		}
		if err != nil {
			reply(w, 0, nil, err)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		io.WriteString(w, *v.Output)
	})
	mux.HandleFunc("POST /tasks/{id}/cancel", docs.Guard(log, func(w http.ResponseWriter, r *http.Request) {
		v, err := e.Cancel(r.Context(), r.PathValue("id"))
		reply(w, 200, v, err)
	}))
	for _, op := range []string{"approve", "reject"} {
		mux.HandleFunc("POST /tasks/{id}/gates/{gate}/"+op, docs.Guard(log, func(w http.ResponseWriter, r *http.Request) {
			var in struct{ Reason string }
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in) // approve has no body
			v, err := e.Decide(r.Context(), r.PathValue("id"), r.PathValue("gate"), op == "approve", in.Reason)
			reply(w, 200, v, err)
		}))
	}
	manageRoutes(mux, e, who, log, reply)
	mux.HandleFunc("GET /tasks/{id}/events", func(w http.ResponseWriter, r *http.Request) { events(w, r, e, reply) })
	mux.Handle("GET /", uiHandler(cmp.Or[fs.FS](ui, mustSub(dist, "dist"))))
	return mux
}

// events streams a run (SSE): each finished step once, in order (`step`), the run whenever it
// changes (`task`), and `done` with the run when it stops; then the stream ends.
func events(w http.ResponseWriter, r *http.Request, e *engine.Engine, reply func(http.ResponseWriter, int, any, error)) {
	id := r.PathValue("id")
	if _, err := e.Get(r.Context(), id); err != nil {
		reply(w, 0, nil, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	rc := http.NewResponseController(w)
	send := func(event string, v any) {
		data, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	}
	after, last := 0, engine.Run{}
	for {
		steps, err := e.Steps(r.Context(), id, after)
		v, gerr := e.Get(r.Context(), id)
		if err != nil || gerr != nil {
			return
		}
		for _, s := range steps {
			if s.Status == "started" {
				break
			}
			send("step", s)
			after = s.Step
		}
		if !reflect.DeepEqual(v.Run, last) {
			send("task", v.Run)
			last = v.Run
		}
		if v.Status != "running" && v.Status != "queued" {
			send("done", v.Run)
			rc.Flush()
			return
		}
		if rc.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(poll):
		}
	}
}

func mustSub(f fs.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return s
}

// noUI is "/" in a build without the UI embedded (a plain go build).
const noUI = `<!doctype html><meta charset="utf-8"><title>BigLaw</title><body style="font-family:sans-serif;max-width:40em;margin:3em auto">
<h1>BigLaw</h1><p>The API is running, but this binary was built without the web UI.</p>
<p>Build it with <code>make build</code> (Node 22: it builds <code>ui/</code> and embeds it), or use the REST API and MCP directly.</p>`

// uiHandler serves the built UI from ui, or the notice above when it holds no index.html.
func uiHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY") // no framing: a gate's Approve cannot be clickjacked
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		if _, err := fs.Stat(ui, "index.html"); errors.Is(err, fs.ErrNotExist) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, noUI)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})
}
