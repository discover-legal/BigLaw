// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"archive/zip"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
)

// Principal is who makes a request (README § Manage, Who). With sign-in (BIGLAW_OIDC_ISSUER,
// RequireSignIn) it is the session's user: SignedIn, an admin when BIGLAW_ADMINS names them, a
// member otherwise. Without it every request is the local user (LocalUser), an admin only on a
// server bound to loopback for local work, or one its operator started with --admin-without-auth
// (Open: anyone who can reach it may change what its agents use); under --allow-no-auth alone
// Manage is read-only (owner decision, review M3).
type Principal struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	Admin    bool   `json:"admin"`
	Open     bool   `json:"-"` // admin with no sign-in at all: the UI says so
	SignedIn bool   `json:"-"` // a session's user (sign-in is on)
}

// Label is who as the change log and every author field record it: the name, with the email when
// it adds one, or the issuer's subject when there is no email.
func (p Principal) Label() string {
	switch {
	case !p.SignedIn:
		return p.Name
	case p.Email != "" && p.Email != p.Name:
		return p.Name + " <" + p.Email + ">"
	case p.Email == "" && p.ID != p.Name:
		return p.Name + " (" + p.ID + ")"
	}
	return p.Name
}

// Who resolves a request's principal.
type Who func(*http.Request) Principal

// local is the local user of a loopback server (tests' default).
func local(*http.Request) Principal { return Principal{ID: "local", Name: "Local user", Admin: true} }

// LocalUser is every request's principal without sign-in: the local user, an admin or not; open
// marks an admin by --admin-without-auth (anyone who can reach the server).
func LocalUser(admin, open bool) Who {
	p := Principal{ID: "local", Name: "Local user", Admin: admin, Open: open && admin}
	return func(*http.Request) Principal { return p }
}

// admin guards a write: 403 forbidden unless the principal is an admin (after docs.Guard's
// cross-origin refusal, as every POST).
func admin(log *slog.Logger, who Who, h func(http.ResponseWriter, *http.Request, Principal)) http.HandlerFunc {
	return docs.Guard(log, func(w http.ResponseWriter, r *http.Request) {
		p := who(r)
		if !p.Admin {
			msg := "only an admin may change what BigLaw's agents use; everyone may view it. This server runs without sign-in (--allow-no-auth), so Manage is read-only unless its operator starts it with --admin-without-auth (README § Exposure)"
			if p.SignedIn {
				msg = "only an admin may change what BigLaw's agents use, set secrets or test custom tools; you are signed in as a member. Admins are the accounts BIGLAW_ADMINS names (README § Sign-in)"
			}
			docs.WriteJSON(w, http.StatusForbidden, map[string]string{"error": msg, "errorType": "forbidden"})
			return
		}
		h(w, r, p)
	})
}

// manageRoutes are Manage's routes: anyone who may use BigLaw reads them; only an admin writes.
func manageRoutes(mux *http.ServeMux, e *engine.Engine, who Who, log *slog.Logger, reply func(http.ResponseWriter, int, any, error)) {
	answer := func(w http.ResponseWriter, code int, v any, err error) {
		var inv *engine.InvalidError
		if errors.As(err, &inv) { // every problem, by file, for the editor to show beside its field
			docs.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": strings.TrimPrefix(err.Error(), "engine: "), "errorType": "invalid", "problems": inv.Problems})
			return
		}
		reply(w, code, v, err)
	}
	get := func(route string, fn func(*http.Request) (any, error)) {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) { v, err := fn(r); reply(w, 200, v, err) })
	}
	ws := writes{mux, log, who, answer}
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		p := who(r)
		reply(w, 200, map[string]any{"id": p.ID, "name": p.Name, "email": p.Email, "admin": p.Admin, "authEnabled": p.SignedIn, "adminWithoutAuth": p.Open, "model": e.Ready()}, nil)
	})
	get("GET /manage/items", func(r *http.Request) (any, error) { return e.Items(r.Context()) })
	get("GET /manage/items/{id}", func(r *http.Request) (any, error) { return e.ItemDetail(r.Context(), r.PathValue("id")) })
	get("GET /manage/items/{id}/history", func(r *http.Request) (any, error) {
		vs, err := e.History(r.Context(), r.PathValue("id"))
		return authors(who(r), vs), err
	})
	get("GET /manage/items/{id}/diff", func(r *http.Request) (any, error) {
		from, err := version(r, "from", 0)
		to, terr := version(r, "to", -1)
		if err = cmp.Or(err, terr); err != nil {
			return nil, err
		}
		return e.Diff(r.Context(), r.PathValue("id"), from, to)
	})
	handle(ws, "POST /manage/items/{id}/versions", http.StatusCreated, func(r *http.Request, p Principal, in engine.SaveIn) (any, error) {
		v, notes, err := e.Save(r.Context(), r.PathValue("id"), in, p.Label())
		return map[string]any{"version": v, "notes": nonNil(notes)}, err
	})
	handle(ws, "POST /manage/items/{id}/revert", http.StatusCreated, func(r *http.Request, p Principal, in struct {
		To, Base int
		Note     string
	}) (any, error) {
		v, err := e.Revert(r.Context(), r.PathValue("id"), in.To, in.Base, in.Note, p.Label())
		return map[string]any{"version": v, "notes": []string{}}, err
	})
	handle(ws, "POST /manage/items/{id}/delete", http.StatusCreated, func(r *http.Request, p Principal, in struct {
		Base int
		Note string
	}) (any, error) {
		v, err := e.Delete(r.Context(), r.PathValue("id"), in.Base, in.Note, p.Label())
		return map[string]any{"version": v, "notes": []string{}}, err
	})
	mux.HandleFunc("GET /manage/changes", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		vs, err := e.Changes(r.Context(), cmp.Or(n, 200))
		reply(w, 200, authors(who(r), vs), err)
	})
	mux.HandleFunc("GET /manage/export", func(w http.ResponseWriter, r *http.Request) { export(w, r, e, reply) })
	// Custom tools: secrets (names only, ever), an MCP server's tools, and the Test call.
	get("GET /manage/secrets", func(r *http.Request) (any, error) {
		ss, err := e.Secrets(r.Context())
		if p := who(r); p.SignedIn && !p.Admin {
			for i := range ss {
				ss[i].UpdatedBy = engine.DisplayName(ss[i].UpdatedBy)
			}
		}
		return ss, err
	})
	handle(ws, "POST /manage/secrets", http.StatusCreated, func(r *http.Request, p Principal, in struct{ Name, Value, Origin string }) (any, error) {
		return map[string]string{"name": in.Name}, e.SetSecret(r.Context(), in.Name, in.Value, in.Origin, p.Label()) // never the value
	})
	mux.HandleFunc("POST /manage/secrets/{name}/delete", admin(log, who, func(w http.ResponseWriter, r *http.Request, p Principal) {
		err := e.DeleteSecret(r.Context(), r.PathValue("name"), p.Label())
		answer(w, 200, map[string]string{"name": r.PathValue("name")}, err)
	}))
	mux.HandleFunc("POST /manage/items/{id}/discover", admin(log, who, func(w http.ResponseWriter, r *http.Request, p Principal) {
		d, err := e.Discover(r.Context(), r.PathValue("id")) // it connects out: an admin's only
		answer(w, 200, d, err)
	}))
	handle(ws, "POST /manage/items/{id}/import", http.StatusCreated, func(r *http.Request, p Principal, in struct {
		Tools       []string
		OfferToLead bool
	}) (any, error) {
		return e.Import(r.Context(), r.PathValue("id"), in.Tools, in.OfferToLead, p.Label())
	})
	handle(ws, "POST /manage/items/{id}/test", 200, func(r *http.Request, p Principal, in engine.TestIn) (any, error) {
		return e.TestCall(r.Context(), r.PathValue("id"), in, p.Label())
	})
}

// writes is what an admin's write route needs (handle).
type writes struct {
	mux    *http.ServeMux
	log    *slog.Logger
	who    Who
	answer func(http.ResponseWriter, int, any, error)
}

// handle mounts an admin's write at route: its body decoded strictly into an In, fn's answer at code.
func handle[In any](ws writes, route string, code int, fn func(*http.Request, Principal, In) (any, error)) {
	ws.mux.HandleFunc(route, admin(ws.log, ws.who, func(w http.ResponseWriter, r *http.Request, p Principal) {
		var in In
		var out any
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)) // an item's files: a skill's are at most 17 × 64 KiB
		d.DisallowUnknownFields()
		err := d.Decode(&in)
		if err == nil && d.More() {
			err = errors.New("more after the object")
		}
		if err != nil {
			err = fmt.Errorf("%w: the body is not the expected JSON object: %v", engine.ErrBadTask, err)
		} else {
			out, err = fn(r, p, in)
		}
		ws.answer(w, code, out, err)
	}))
}

// export downloads the overridden files (all, or ?item=a,b) as a patch against the repository
// (format=patch, the default: git apply or patch -p1 at the repo root) or a zip of the files at their
// repo paths (format=zip; a deleted skill's files are listed in DELETED.txt). No credentials: the
// owner opens the pull request from it.
func export(w http.ResponseWriter, r *http.Request, e *engine.Engine, reply func(http.ResponseWriter, int, any, error)) {
	var ids []string
	if s := r.URL.Query().Get("item"); s != "" {
		ids = strings.Split(s, ",")
	}
	format := cmp.Or(r.URL.Query().Get("format"), "patch")
	if format != "patch" && format != "zip" {
		reply(w, 0, nil, fmt.Errorf("%w: format is patch or zip", engine.ErrBadTask))
		return
	}
	now, built, err := e.Export(r.Context(), ids)
	if err != nil {
		reply(w, 0, nil, err)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if format == "patch" {
		w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="biglaw-overrides.patch"`)
		io.WriteString(w, engine.Patch(now, built))
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="biglaw-overrides.zip"`)
	z := zip.NewWriter(w)
	var gone []string
	for p := range built {
		if _, ok := now[p]; !ok {
			gone = append(gone, p)
		}
	}
	for _, p := range slices.Sorted(maps.Keys(now)) {
		f, err := z.Create(p)
		if err != nil {
			return
		}
		io.WriteString(f, now[p])
	}
	if len(gone) > 0 {
		f, err := z.Create("DELETED.txt")
		if err == nil {
			io.WriteString(f, "Delete these files from the repository (a skill deleted in Manage):\n"+strings.Join(slices.Sorted(slices.Values(gone)), "\n")+"\n")
		}
	}
	z.Close()
}

// version is a diff's version parameter: a number, "builtin" (0) or absent (def).
func version(r *http.Request, name string, def int) (int, error) {
	s := r.URL.Query().Get(name)
	switch s {
	case "":
		return def, nil
	case "builtin":
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s is a version number or builtin", engine.ErrBadTask, name)
	}
	return n, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// authors is vs as p may see them: under sign-in a member sees authors' display names only.
func authors(p Principal, vs []engine.Version) []engine.Version {
	if p.SignedIn && !p.Admin {
		for i := range vs {
			vs[i].Author = engine.DisplayName(vs[i].Author)
		}
	}
	return vs
}
