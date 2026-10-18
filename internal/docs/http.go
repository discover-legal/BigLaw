// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/discover-legal/BigLaw/internal/store"
)

// Upload read bounds: a client must send at least minBytes in every window, and the whole body
// within total, or the read fails and the upload's slot and pool connection are released at once.
// Variables, not constants, so tests can shorten them.
var (
	uploadWindow   = 30 * time.Second
	uploadMinBytes = 64 << 10 // ≈ 2 KiB/s floor
	uploadTotal    = 15 * time.Minute
)

// Routes mounts the document file routes on mux: POST /documents/upload (multipart, one part
// named "file") and GET /documents/{id}/file (the original, with a safe download name). There is
// no authentication before PR 8; the caller is the single local partner, so the scope is the
// requested document.
func (s *Service) Routes(mux *http.ServeMux, log *slog.Logger) {
	mux.HandleFunc("POST /documents/upload", func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeError(w, log, errCrossOrigin)
			return
		}
		s.upload(w, r, log)
	})
	mux.HandleFunc("GET /documents/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		d, err := s.Get(r.Context(), []string{id}, id)
		var f store.File
		if err == nil {
			f, err = s.File(r.Context(), []string{id}, id)
		}
		if err != nil {
			writeError(w, log, err)
			return
		}
		w.Header().Set("Content-Type", d.ContentType) // detected, never the uploader's claim
		w.Header().Set("Content-Disposition", ContentDisposition(f.Name))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Write(f.Data)
	})
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request, log *slog.Logger) {
	rc := http.NewResponseController(w)
	pr := &progressReader{r: r.Body, rc: rc, end: time.Now().Add(uploadTotal)}
	pr.extend()
	r.Body = http.MaxBytesReader(w, io.NopCloser(pr), store.MaxFileSize+1<<20) // the file plus multipart framing
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, log, fmt.Errorf("%w: send multipart/form-data with one part named \"file\"", ErrBadInput))
		return
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			writeError(w, log, readError(err, pr.timedOut, `the request has no part named "file"`))
			return
		}
		if p.FormName() != "file" {
			continue
		}
		ct := p.Header.Get("Content-Type")
		if ct == "" || strings.HasPrefix(ct, "multipart/") {
			ct = "application/octet-stream"
		}
		d, err := s.Ingest(r.Context(), p.FileName(), ct, p)
		if err != nil {
			writeError(w, log, readError(err, pr.timedOut, ""))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(d)
		return
	}
}

// progressReader pushes the read deadline forward each time uploadMinBytes more have arrived,
// never past end. A stalled or trickling client hits the deadline and its read fails.
type progressReader struct {
	r        io.Reader
	rc       *http.ResponseController
	end      time.Time
	got      int
	timedOut bool // a read hit the deadline (the server then cancels the request, so later errors read "context canceled")
}

func (p *progressReader) extend() {
	d := time.Now().Add(uploadWindow)
	if d.After(p.end) {
		d = p.end
	}
	_ = p.rc.SetReadDeadline(d)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		p.timedOut = true
	}
	if p.got += n; p.got >= uploadMinBytes {
		p.got = 0
		p.extend()
	}
	return n, err
}

// readError classifies a failure while reading an upload: a read deadline is 408, an oversized
// body 413, a broken multipart stream 400.
func readError(err error, timedOut bool, missing string) error {
	var mbe *http.MaxBytesError
	switch {
	case timedOut:
		return errTimeout
	case errors.As(err, &mbe):
		return store.ErrFileTooLarge
	case errors.Is(err, io.EOF) && missing != "":
		return fmt.Errorf("%w: %s", ErrBadInput, missing)
	case missing != "", errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("%w: the multipart body is malformed or ended early", ErrBadInput)
	}
	return err
}

// sameOrigin refuses a write a browser sends from another site (a page the lawyer visits could
// otherwise POST uploads to a loopback BigLaw: multipart/form-data needs no CORS preflight).
// Clients that send neither header (curl, the drivers) pass.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	o := r.Header.Get("Origin")
	return o == "" || o == "http://"+r.Host || o == "https://"+r.Host
}

var errCrossOrigin = errors.New("docs: cross-origin upload refused: upload from BigLaw's own pages or a non-browser client")

var errTimeout = fmt.Errorf("docs: the upload was too slow: send at least %d KiB every %s and the whole file within %s",
	uploadMinBytes>>10, uploadWindow, uploadTotal)

// writeError answers with {error, errorType} and the status for err's kind. Unclassified errors are
// logged and answered with a fixed text (they may hold database detail).
func writeError(w http.ResponseWriter, log *slog.Logger, err error) {
	code, kind, msg := http.StatusInternalServerError, "internal", "internal error (logged)"
	for _, c := range []struct {
		err        error
		code       int
		kind       string
		ownMessage bool
	}{
		{ErrNotFound, 404, "not_found", false}, {ErrBadInput, 400, "bad_request", true}, {store.ErrBadName, 400, "bad_name", true},
		{store.ErrBadType, 400, "bad_content_type", true}, {store.ErrFileTooLarge, 413, "too_large", true},
		{errTimeout, 408, "upload_timeout", true}, {errCrossOrigin, 403, "cross_origin", true}, {ErrUnsupported, 415, "unsupported_type", true}, {ErrUnreadable, 422, "unreadable", true},
	} {
		if errors.Is(err, c.err) {
			code, kind, msg = c.code, c.kind, "not found"
			if c.ownMessage {
				msg = strings.TrimPrefix(strings.TrimPrefix(err.Error(), "docs: "), "store: ")
			}
			break
		}
	}
	if errors.Is(err, context.Canceled) {
		code, kind, msg = 499, "cancelled", "the request was cancelled"
	}
	if code == http.StatusInternalServerError {
		log.Error("documents request failed", "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg, "errorType": kind})
}

// ContentDisposition is an RFC 6266 attachment header for a stored name: filename* carries the
// name (UTF-8, percent-encoded), and both forms are made safe to save on Windows: reserved
// characters become "_", a reserved device name (CON, aux.txt, COM1…) gets a "_" prefix, and
// trailing dots and spaces are dropped. filename= is the ASCII fallback for old clients.
func ContentDisposition(name string) string {
	name = strings.TrimRight(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name), ". ")
	if base, _, _ := strings.Cut(name, "."); name == "" || windowsDevice.MatchString(strings.TrimRight(base, " ")) {
		name = "_" + name
	}
	var ascii, ext strings.Builder
	for _, r := range name {
		if r < 0x7f && r != '%' {
			ascii.WriteRune(r)
		} else {
			ascii.WriteByte('_')
		}
	}
	for _, c := range []byte(name) { // RFC 8187 attr-char, everything else percent-encoded
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			ext.WriteByte(c)
		} else {
			fmt.Fprintf(&ext, "%%%02X", c)
		}
	}
	return mime.FormatMediaType("attachment", map[string]string{"filename": ascii.String()}) + "; filename*=UTF-8''" + ext.String()
}

var windowsDevice = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|COM[0-9¹²³]|LPT[0-9¹²³])$`)
