// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Model-failure kinds (README G6). A failed Chat matches exactly one under
// errors.Is (or context.Canceled); a failure is never "no finding".
var (
	ErrNotConfigured = errors.New("model not configured")            // 503 model_not_configured
	ErrRateLimited   = errors.New("model provider rate limited")     // 503 model_rate_limited
	ErrUnavailable   = errors.New("model call failed")               // 502 model_unavailable
	ErrTimeout       = errors.New("model call timed out")            // 504 model_timeout
	ErrNoContent     = errors.New("model returned no usable output") // 422 no_content
)

// Error is a classified failure. Detail is BigLaw's own fixed text and status codes, and of an error
// answer only the provider's error type and message, redacted (providerError): never the URL, the
// key, a header or the rest of anything the endpoint sent, so it is safe to log.
type Error struct {
	Kind       error
	Detail     string
	RetryAfter time.Duration // the provider's Retry-After on a 429; 0 if none
	Usage      *Usage        // a 200 the provider billed but BigLaw refused: its usage and cost
	Status     int           // the endpoint's HTTP status (0: none); a 4xx other than 401/403/404/429 refused this request
	Truncated  bool          // an ErrNoContent answer that stopped at MaxTokens before any text or whole tool call
}

func (e *Error) Error() string { return e.Kind.Error() + ": " + e.Detail }
func (e *Error) Unwrap() error { return e.Kind }

func fail(kind error, detail string) *Error { return &Error{Kind: kind, Detail: detail} }

// providerError is an error answer's type and message as the provider states them (OpenAI's
// {"error": {"type", "message"}} or Anthropic's {"type": "error", "error": {...}}), its control
// characters dropped, the whole of it redacted (redact) and then cut to 240 bytes; "" when the body
// states neither.
func providerError(body io.Reader, key string) string {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(body, 16<<10))
	if json.Unmarshal(data, &e) != nil {
		return ""
	}
	typ := e.Error.Type
	if typ == "" {
		if s, ok := e.Error.Code.(string); ok {
			typ = s
		}
	}
	if !errType.MatchString(typ) {
		typ = ""
	}
	msg := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, e.Error.Message)
	out := redact(strings.TrimSpace(strings.Trim(typ+": "+strings.Join(strings.Fields(msg), " "), ": ")), key) // one redactor over all of it, type and code too (r1 LOW-1), before it is cut
	if len(out) > 240 {
		out = strings.ToValidUTF8(out[:240], "") + "…"
	}
	return out
}

// redact replaces in s the key, and anything shaped like a key, a token (a long run of key or base64
// characters, a masked key's tail), a bearer value, a URL or a host name, with "[redacted]".
func redact(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "[redacted]")
	}
	return secretish.ReplaceAllString(s, "[redacted]")
}

var (
	errType = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// secretish is what an error message may hold that must never be logged: a URL, a bearer value, a
	// masked key and its tail ("sk-…****wxyz"), any long run of key, token or base64 characters, and a
	// host name with or without a scheme or port ("gpu-7.internal.corp:8443").
	secretish = regexp.MustCompile(`(?i)https?://\S+|bearer\s+\S+|\b(?:sk|pk|rk|ak)-[A-Za-z0-9_*-]+|[A-Za-z0-9_-]*\*{3,}[A-Za-z0-9_*-]*|[A-Za-z0-9_+/=-]{32,}|\b(?:[a-z0-9-]+\.)+[a-z][a-z0-9-]+(?::\d+)?\b`)
)

// HTTPStatus maps a model failure to its HTTP status (500 if none); the API (PR 4b) owns the rest.
func HTTPStatus(err error) int {
	for i, kind := range []error{context.Canceled, ErrNotConfigured, ErrRateLimited, ErrTimeout, ErrNoContent, ErrUnavailable} {
		if errors.Is(err, kind) {
			return []int{499, 503, 503, 504, 422, 502}[i]
		}
	}
	return http.StatusInternalServerError
}
