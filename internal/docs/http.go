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
	"strconv"
	"strings"
	"time"

	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
)

// Transfer bounds: an upload must send, and a download receive, at least uploadMinBytes in every
// window and the whole file within uploadTotal, or the transfer fails and its slots and pool
// connection are released at once. Variables, not constants, so tests can shorten them.
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
		id, scope, none := r.PathValue("id"), []string{r.PathValue("id")}, func(string, store.File) error { return nil }
		if err := s.WriteFile(r.Context(), scope, id, none, io.Discard); err != nil { // verify first: never send corrupt bytes
			writeError(w, log, err)
			return
		}
		err := s.WriteFile(r.Context(), scope, id, func(contentType string, f store.File) error {
			w.Header().Set("Content-Type", contentType) // detected, never the uploader's claim
			w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
			w.Header().Set("Content-Disposition", ContentDisposition(f.Name))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "sandbox")
			return nil
		}, floorWriter{w, http.NewResponseController(w), time.Now().Add(uploadTotal)})
		if err != nil { // mid-body (a client below the floor, the file deleted meanwhile): cut the connection
			log.Warn("document download cut", "err", err)
			panic(http.ErrAbortHandler)
		}
	})
}

// floorWriter writes at most uploadMinBytes at a time, each under its own deadline (one
// uploadWindow, never past end): a client that stops reading is cut within a window, so a
// stalled download holds its read slot and one chunk for at most that long.
type floorWriter struct {
	w   io.Writer
	rc  *http.ResponseController
	end time.Time
}

func (f floorWriter) Write(b []byte) (n int, err error) {
	for len(b) > 0 && err == nil {
		if err = f.rc.SetWriteDeadline(windowEnd(f.end)); err != nil {
			return n, err // a writer that cannot take a deadline (a wrapper without Unwrap): fail closed
		}
		k := min(len(b), uploadMinBytes)
		k, err = f.w.Write(b[:k])
		n, b = n+k, b[k:]
	}
	return n, err
}

func windowEnd(end time.Time) time.Time {
	if d := time.Now().Add(uploadWindow); d.Before(end) {
		return d
	}
	return end
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request, log *slog.Logger) {
	rc := http.NewResponseController(w)
	pr := &progressReader{r: r.Body, rc: rc, end: time.Now().Add(uploadTotal)}
	if err := rc.SetReadDeadline(windowEnd(pr.end)); err != nil { // fail closed: never an upload without a deadline
		writeError(w, log, fmt.Errorf("docs: upload: %w", err))
		return
	}
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
		if p.FileName() == "" {
			writeError(w, log, fmt.Errorf(`%w: the part named "file" has no file name`, ErrBadInput))
			return
		}
		ct := p.Header.Get("Content-Type") // kept as metadata only: the served type is the detected one
		if ct == "" || strings.HasPrefix(ct, "multipart/") || strings.Contains(ct, "*") {
			ct = "application/octet-stream"
		}
		d, err := s.Ingest(r.Context(), p.FileName(), ct, p)
		if errors.Is(context.Cause(r.Context()), ErrStopping) {
			err = ErrStopping
		}
		if errors.Is(err, ErrVisionNotConfigured) { // stored, to be transcribed: 503 with the document
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]any{"error": strings.TrimPrefix(err.Error(), "docs: "), "errorType": "vision_not_configured", "document": d})
			return
		} else if err != nil {
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

func (p *progressReader) extend() { _ = p.rc.SetReadDeadline(windowEnd(p.end)) }

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
		{ErrBusy, 503, "busy", true}, {ErrStopping, 503, "stopping", true}, {ErrUnreliable, 422, "transcription_unreliable", true},
		{ErrNoContent, 422, "no_content", true}, {ErrScanEncoding, 422, "scan_encoding_unsupported", true}, {ErrTooManyPages, 413, "too_many_pages", true},
		{model.ErrNotConfigured, 503, "model_not_configured", true}, {model.ErrRateLimited, 503, "model_rate_limited", true}, // G6: fixed text only
		{model.ErrUnavailable, 502, "model_unavailable", true}, {model.ErrTimeout, 504, "model_timeout", true}, {model.ErrNoContent, 422, "no_content", true},
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
