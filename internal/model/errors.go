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

// redact replaces in s the key, and anything shaped like a key, a token or a credential (a key
// prefix and what follows it, a run of 32 or more key or base64 characters, a masked key's tail, a
// bearer or basic value, a "key=…" or "Authorization: …" value), a URL, an IPv4 address and a host
// name, with "[redacted]". A dotted path such as "messages.0.content" or "tools.1.input_schema" is
// not a host name and is kept: it is what a request-validation error names (r2 L1).
func redact(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "[redacted]")
	}
	s = secretish.ReplaceAllString(s, "[redacted]")
	return dotted.ReplaceAllStringFunc(s, func(m string) string {
		if hostLike(m) {
			return "[redacted]"
		}
		return m
	})
}

// hostLike: dotted labels of letters, digits and hyphens, none all digits, ending in a top-level
// domain shape (2 to 24 letters), with an optional port.
func hostLike(m string) bool {
	m, _, _ = strings.Cut(m, ":")
	labels := strings.Split(m, ".")
	for _, l := range labels {
		if l == "" || strings.Trim(l, "0123456789") == "" || strings.ContainsRune(l, '_') {
			return false
		}
	}
	return tld.MatchString(labels[len(labels)-1])
}

var (
	errType = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// secretish is what an error message may hold that must never be logged: a URL, a bearer or
	// basic value, a credential after "key=" or "Authorization:", a key prefix and its fragment
	// ("sk-ant-…", "sk-…****wxyz"), a masked tail, any run of 32 or more key, token or base64
	// characters, and an IPv4 address with its port.
	secretish = regexp.MustCompile(`(?i)https?://\S+|\b(?:authorization|proxy-authorization)\s*:\s*(?:(?:basic|bearer|token)\s+)?\S+|` +
		`\b(?:bearer|basic)\s+\S+|\b(?:x-api-key|api[_-]?key|key|token|secret|password|passwd)\s*[=:]\s*\S+|` +
		`\b(?:sk|pk|rk|ak)-[A-Za-z0-9_*.-]+|[A-Za-z0-9_-]*\*{3,}[A-Za-z0-9_*-]*|[A-Za-z0-9_+/=-]{32,}|\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`)
	// dotted is a candidate host name (hostLike decides): two or more dotted labels, a port optional.
	dotted = regexp.MustCompile(`[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)+(?::\d+)?`)
	tld    = regexp.MustCompile(`^[A-Za-z]{2,24}$`)
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
