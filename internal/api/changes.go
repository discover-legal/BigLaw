// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
)

// The track-changes routes (README § Track changes): a change set is posted against one of a
// task's documents, listed and read, and its .docx rendered on each download; a document's own
// tracked changes are listed. Every route is task-rooted, so the task's document set is the scope
// (G1): a document outside it is 404, as everywhere. The tracked changes' author is the signed-in
// user's name (without sign-in, the local user's). Rendering fires the change_set_rendered hook
// event (webhook, limitation) once the file is on its way.
func changeRoutes(mux *http.ServeMux, d *docs.Service, e *engine.Engine, who Who, log *slog.Logger, reply func(http.ResponseWriter, int, any, error)) {
	// task is the task's view, or nil after answering the error
	task := func(w http.ResponseWriter, r *http.Request) *engine.TaskView {
		v, err := e.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			reply(w, 0, nil, err)
			return nil
		}
		return &v
	}
	mux.HandleFunc("POST /tasks/{id}/changes", docs.Guard(log, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
		if err == nil {
			if err = changeIn.Validate(body); err != nil {
				err = fmt.Errorf("%w: %v", engine.ErrBadTask, err)
			}
		}
		if err != nil {
			reply(w, 0, nil, err)
			return
		}
		v := task(w, r)
		if v == nil {
			return
		}
		var in docs.ChangeIn
		if err := json.Unmarshal(body, &in); err != nil {
			reply(w, 0, nil, fmt.Errorf("%w: %v", engine.ErrBadTask, err))
			return
		}
		p := who(r)
		by, _ := json.Marshal(map[string]string{"id": p.ID, "name": p.Name}) // never an email: a member may read it
		cs, err := d.CreateChangeSet(r.Context(), v.DocumentIDs, v.ID, p.Name, by, in)
		if err != nil {
			docs.WriteError(w, log, err)
			return
		}
		docs.WriteJSON(w, http.StatusCreated, cs)
	}))
	mux.HandleFunc("GET /tasks/{id}/changes", func(w http.ResponseWriter, r *http.Request) {
		if v := task(w, r); v != nil {
			off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			page, err := d.ChangeSets(r.Context(), v.ID, off)
			answer(w, log, page, err)
		}
	})
	mux.HandleFunc("GET /tasks/{id}/changes/{cid}", func(w http.ResponseWriter, r *http.Request) {
		if v := task(w, r); v != nil {
			cs, err := d.ChangeSet(r.Context(), v.ID, r.PathValue("cid"))
			answer(w, log, cs, err)
		}
	})
	mux.HandleFunc("GET /tasks/{id}/changes/{cid}/file", func(w http.ResponseWriter, r *http.Request) {
		v := task(w, r)
		if v == nil {
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Minute))
		h := sha256.New()
		cs, err := d.RenderChangeSet(r.Context(), v.DocumentIDs, v.ID, r.PathValue("cid"), func(name string, size int64) error {
			w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.Header().Set("Content-Disposition", docs.ContentDisposition(name))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "sandbox")
			return nil
		}, io.MultiWriter(w, h))
		if err != nil {
			if w.Header().Get("Content-Length") != "" { // mid-body: cut the connection, never a short 200
				log.Warn("change set download cut", "err", err)
				panic(http.ErrAbortHandler)
			}
			docs.WriteError(w, log, err)
			return
		}
		applied, failed := 0, 0
		for _, o := range cs.Outcomes {
			if o.Status == docs.StatusApplied {
				applied++
			} else {
				failed++
			}
		}
		if err := e.ChangeSetRendered(r.Context(), v.ID, engine.ChangeSetEvent{ChangeSetID: cs.ID, DocumentID: cs.DocumentID, Kind: cs.Kind, Author: cs.Author,
			Applied: applied, Failed: failed, SHA256: hex.EncodeToString(h.Sum(nil))}); err != nil {
			log.Warn("change_set_rendered hooks", "task", v.ID, "changeSet", cs.ID, "err", err)
		}
	})
	mux.HandleFunc("GET /tasks/{id}/documents/{doc}/revisions", func(w http.ResponseWriter, r *http.Request) {
		if v := task(w, r); v != nil {
			off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			page, err := d.Revisions(r.Context(), v.DocumentIDs, r.PathValue("doc"), off)
			answer(w, log, page, err)
		}
	})
}

// answer writes a document-service result with its error mapping.
func answer(w http.ResponseWriter, log *slog.Logger, v any, err error) {
	if err != nil {
		docs.WriteError(w, log, err)
		return
	}
	docs.WriteJSON(w, 200, v)
}
