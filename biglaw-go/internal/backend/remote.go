// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Remote serves the Backend by calling an owner process's REST API — the
// same Local.As(principal) operations, run on the far side of HTTP as the
// principal the owner resolves for this client's bearer token. It holds no
// state beyond an HTTP client, so everything created through it is the
// owner's, visible to the workbench and every other client.
type Remote struct {
	base  *url.URL
	token string
	hc    *http.Client

	// Lazy attach: the first call (or Attach) probes GET /me, retrying an
	// unreachable owner until graceUntil, and caches the principal.
	mu         sync.Mutex
	principal  *Principal
	graceUntil time.Time
}

// remoteTimeout bounds one REST call. Ingest is the slow path (chunking and
// dense embeddings run synchronously inside POST /documents).
const remoteTimeout = 2 * time.Minute

// maxRemoteBody caps a response body the client will buffer.
const maxRemoteBody = 64 << 20

// NewRemote returns a client for the BigLaw REST API at baseURL. A non-empty
// token is sent as "Authorization: Bearer <token>" on every request — the
// credential the API's auth middleware accepts from non-browser callers
// (API_KEY, bound to API_PROFILE_ID) when AUTH_ENABLED=true. Redirects are
// never followed (a 3xx would carry the bearer elsewhere), and plain http to
// a non-loopback host is warned about. grace is how long the first call
// keeps retrying an owner that is not answering yet (it may be booting).
func NewRemote(baseURL, token string, grace time.Duration) (*Remote, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil {
		return nil, fmt.Errorf("backend URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("backend URL %q: want http(s)://host[:port]", u.Redacted())
	}
	u.RawQuery, u.Fragment = "", ""
	r := &Remote{
		base:  u,
		token: token,
		hc: &http.Client{
			Timeout: remoteTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse // surfaced as an error by do
			},
		},
		graceUntil: time.Now().Add(grace),
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		slog.Warn("BigLaw backend reached over plain http on a non-loopback host: requests"+
			" (and the bearer token, if set) travel unencrypted — prefer https",
			"api", r.URL(), "token", token != "")
	}
	return r, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var _ Backend = (*Remote)(nil)

// URL is the backend base URL with any userinfo password redacted (for logs
// and error messages).
func (r *Remote) URL() string { return r.base.Redacted() }

// Principal is the identity the backend resolved for this client (GET /me).
type Principal struct {
	User        *types.SessionUser `json:"user"`
	AuthEnabled bool               `json:"authEnabled"`
}

// Ping probes the backend once: reachable, accepts this client's credentials,
// and really is BigLaw. It returns the principal the client acts as.
func (r *Remote) Ping(ctx context.Context) (*Principal, error) {
	var p Principal
	if err := r.send(ctx, http.MethodGet, "/me", nil, nil, &p); err != nil {
		switch {
		case errors.Is(err, ErrUnauthorized):
			return nil, wrapKind(err, fmt.Sprintf("BigLaw backend at %s rejected this client: %v — it runs with "+
				"AUTH_ENABLED=true; set BIG_MICHAEL_API_TOKEN (or API_KEY) to the backend's API_KEY", r.URL(), err))
		case errors.Is(err, ErrUnavailable):
			return nil, wrapKind(err, fmt.Sprintf("no BigLaw backend reachable at %s: %v — start one "+
				"(BIG_MICHAEL_MODE=backend, or the Docker stack) or point BIG_MICHAEL_API at it", r.URL(), err))
		default:
			return nil, wrapKind(err, fmt.Sprintf("%s does not look like a BigLaw backend: GET /me: %v", r.URL(), err))
		}
	}
	if p.User == nil {
		return nil, newErr(ErrInternal, fmt.Sprintf("%s does not look like a BigLaw backend: GET /me returned no principal", r.URL()))
	}
	return &p, nil
}

// wrapKind keeps err's kind under a more helpful message.
func wrapKind(err error, msg string) error {
	var be *Error
	if errors.As(err, &be) {
		return &Error{Kind: be.Kind, Msg: msg}
	}
	return newErr(ErrInternal, msg)
}

// Attach makes sure the backend has been probed successfully, probing now if
// not. While the grace window is open an unreachable backend is retried;
// after it, one attempt per call. Auth and protocol failures are reported at
// once. Every operation calls Attach first, so the MCP server can answer
// initialize immediately and a backend that comes up later is picked up.
func (r *Remote) Attach(ctx context.Context) (*Principal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.principal != nil {
		return r.principal, nil
	}
	for {
		p, err := r.Ping(ctx)
		if err == nil {
			r.principal = p
			slog.Info("MCP thin client attached", "api", r.URL(),
				"principal", p.User.ProfileID, "role", p.User.Role, "authEnabled", p.AuthEnabled)
			return p, nil
		}
		if !errors.Is(err, ErrUnavailable) || !time.Now().Before(r.graceUntil) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (r *Remote) SubmitTask(ctx context.Context, in SubmitTaskInput) (*types.Task, error) {
	var t types.Task
	if err := r.do(ctx, http.MethodPost, "/tasks", nil, in, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *Remote) GetTask(ctx context.Context, taskID string) (*types.Task, error) {
	var t types.Task
	if err := r.do(ctx, http.MethodGet, "/tasks/"+seg(taskID), nil, nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *Remote) ListTasks(ctx context.Context) ([]*types.Task, error) {
	out := []*types.Task{}
	if err := r.do(ctx, http.MethodGet, "/tasks", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ApproveGate(ctx context.Context, taskID, gateID, note string) error {
	return r.do(ctx, http.MethodPost, "/tasks/"+seg(taskID)+"/gates/"+seg(gateID)+"/approve", nil, gateNote{note}, nil)
}

func (r *Remote) RejectGate(ctx context.Context, taskID, gateID, reason string) error {
	return r.do(ctx, http.MethodPost, "/tasks/"+seg(taskID)+"/gates/"+seg(gateID)+"/reject", nil, gateReason{reason}, nil)
}

func (r *Remote) SubmitFromTemplate(ctx context.Context, in SubmitFromTemplateInput) (*types.Task, error) {
	var t types.Task
	if err := r.do(ctx, http.MethodPost, "/tasks/from-template", nil, in, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *Remote) ListTemplates(ctx context.Context) ([]types.TaskTemplate, error) {
	out := []types.TaskTemplate{}
	if err := r.do(ctx, http.MethodGet, "/templates", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) GetRound(ctx context.Context, taskID string, round int) (*types.RoundState, error) {
	var rs types.RoundState
	path := "/tasks/" + seg(taskID) + "/rounds/" + strconv.Itoa(round)
	if err := r.do(ctx, http.MethodGet, path, nil, nil, &rs); err != nil {
		return nil, err
	}
	return &rs, nil
}

func (r *Remote) IngestDocument(ctx context.Context, in IngestInput) (*types.Document, error) {
	var d types.Document
	if err := r.do(ctx, http.MethodPost, "/documents", nil, in, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *Remote) SearchKnowledge(ctx context.Context, query string, topK int) ([]types.SearchResult, error) {
	q := url.Values{"q": {query}, "topK": {strconv.Itoa(clampTopK(topK))}}
	out := []types.SearchResult{}
	if err := r.do(ctx, http.MethodGet, "/documents/search", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ListAgents(ctx context.Context) ([]types.AgentDefinition, error) {
	out := []types.AgentDefinition{}
	if err := r.do(ctx, http.MethodGet, "/agents", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ReadAudit(ctx context.Context, aq AuditQuery) ([]audit.AuditEntry, error) {
	q := url.Values{"limit": {strconv.Itoa(clampAuditLimit(aq.Limit))}}
	for k, v := range map[string]string{"taskId": aq.TaskID, "actorId": aq.ActorID, "event": aq.Event} {
		if v != "" {
			q.Set(k, v)
		}
	}
	out := []audit.AuditEntry{}
	if err := r.do(ctx, http.MethodGet, "/audit", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ListPlugins(ctx context.Context) ([]PluginSummary, error) {
	out := []PluginSummary{}
	if err := r.do(ctx, http.MethodGet, "/plugins", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Remote) ListTimeEntries(ctx context.Context, f timekeeping.TimeFilter) ([]types.TimeEntry, error) {
	q := url.Values{}
	for k, v := range map[string]string{
		"profileId":    f.ProfileID,
		"taskId":       f.TaskID,
		"matterNumber": f.MatterNumber,
		"from":         timeParam(f.From),
		"to":           timeParam(f.To),
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	out := []types.TimeEntry{}
	if err := r.do(ctx, http.MethodGet, "/time-entries", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ─── Transport ────────────────────────────────────────────────────────────────

// seg escapes one path segment; the API routes on the raw (escaped) path,
// so an ID containing "/" or ".." stays a single segment.
func seg(s string) string { return url.PathEscape(s) }

// do attaches (lazily probing the backend) and performs one REST call.
func (r *Remote) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	if _, err := r.Attach(ctx); err != nil {
		return err
	}
	return r.send(ctx, method, path, query, in, out)
}

// send performs one REST call. A non-2xx status becomes an *Error whose kind
// follows the status and whose message is the API's {"error": …} text, so a
// remote failure reads exactly like the in-process one.
func (r *Remote) send(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := *r.base
	escaped := strings.TrimRight(u.EscapedPath(), "/") + path // path is pre-escaped (seg)
	unescaped, err := url.PathUnescape(escaped)
	if err != nil {
		return newErr(ErrInvalid, "bad request path: "+err.Error())
	}
	u.Path, u.RawPath = unescaped, escaped
	u.RawQuery = query.Encode()

	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return newErr(ErrInvalid, "encode request: "+err.Error())
		}
		// The operation's own size rule (checkSize), applied before sending:
		// the call fails exactly as it would in-process, and the API never
		// sees a body it would have to cut off.
		if len(raw) > MaxRequestBytes {
			return newErr(ErrInvalid, fmt.Sprintf("request too large: %d bytes exceeds the %d MiB limit",
				len(raw), MaxRequestBytes>>20))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return newErr(ErrInternal, err.Error())
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.hc.Do(req)
	if err != nil {
		return newErr(ErrUnavailable, "backend unreachable: "+err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteBody))
	if err != nil {
		return newErr(ErrUnavailable, "read response: "+err.Error())
	}

	if resp.StatusCode >= 300 && resp.StatusCode <= 399 {
		return newErr(ErrInternal, fmt.Sprintf("backend answered %s %s with a redirect (HTTP %d to %q); "+
			"redirects are not followed — point BIG_MICHAEL_API at the final URL",
			method, path, resp.StatusCode, resp.Header.Get("Location")))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return newErr(ErrInternal, fmt.Sprintf("decode %s %s response: %v", method, path, err))
	}
	return nil
}

func statusError(status int, raw []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &e) == nil {
		msg = e.Error
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch status {
	case http.StatusBadRequest:
		return newErr(ErrInvalid, msg)
	case http.StatusUnauthorized:
		return newErr(ErrUnauthorized, msg)
	case http.StatusForbidden:
		return newErr(ErrForbidden, msg)
	case http.StatusNotFound:
		return newErr(ErrNotFound, msg)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return newErr(ErrUnavailable, msg)
	case http.StatusInternalServerError:
		return newErr(ErrInternal, msg)
	default:
		return newErr(ErrInternal, fmt.Sprintf("HTTP %d: %s", status, msg))
	}
}
