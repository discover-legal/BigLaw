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
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Remote serves the Backend by calling an owner process's REST API. It holds
// no state beyond an HTTP client: every call is a request to the owner, so
// tasks and documents created through it are the owner's, visible to the
// workbench and every other client.
type Remote struct {
	base  *url.URL
	token string
	hc    *http.Client
}

// remoteTimeout bounds one REST call. Ingest is the slow path (chunking and
// dense embeddings run synchronously inside POST /documents).
const remoteTimeout = 2 * time.Minute

// maxRemoteBody caps a response body the client will buffer.
const maxRemoteBody = 64 << 20

// NewRemote returns a client for the BigLaw REST API at baseURL. A non-empty
// token is sent as "Authorization: Bearer <token>" on every request — the
// credential the API's auth middleware accepts from non-browser callers
// (API_KEY, bound to API_PROFILE_ID) when AUTH_ENABLED=true; the API ignores
// it when auth is off.
func NewRemote(baseURL, token string) (*Remote, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil {
		return nil, fmt.Errorf("backend URL %q: %w", baseURL, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("backend URL %q: want http(s)://host[:port]", baseURL)
	}
	return &Remote{base: u, token: token, hc: &http.Client{Timeout: remoteTimeout}}, nil
}

var _ Backend = (*Remote)(nil)

// URL is the backend base URL (for logs and error messages).
func (r *Remote) URL() string { return r.base.String() }

// Principal is the identity the backend resolved for this client (GET /me).
type Principal struct {
	User        *types.SessionUser `json:"user"`
	AuthEnabled bool               `json:"authEnabled"`
}

// Ping checks that a BigLaw backend is reachable at the URL and accepts this
// client's credentials, returning the principal it acts as. It is the
// startup probe for mcp mode: an error here means no tool call can work.
func (r *Remote) Ping(ctx context.Context) (*Principal, error) {
	var p Principal
	if err := r.do(ctx, http.MethodGet, "/me", nil, nil, &p); err != nil {
		switch {
		case errors.Is(err, ErrUnauthorized):
			return nil, fmt.Errorf("BigLaw backend at %s rejected this client: %w — it runs with "+
				"AUTH_ENABLED=true; set BIG_MICHAEL_API_TOKEN (or API_KEY) to the backend's API_KEY", r.URL(), err)
		case errors.Is(err, ErrUnavailable):
			return nil, fmt.Errorf("no BigLaw backend reachable at %s: %w — start one "+
				"(BIG_MICHAEL_MODE=backend, or the Docker stack) or point BIG_MICHAEL_API at it", r.URL(), err)
		default:
			return nil, fmt.Errorf("%s does not look like a BigLaw backend: GET /me: %w", r.URL(), err)
		}
	}
	if p.User == nil {
		return nil, fmt.Errorf("%s does not look like a BigLaw backend: GET /me returned no principal", r.URL())
	}
	return &p, nil
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
	body := struct {
		Note string `json:"note"`
	}{note}
	return r.do(ctx, http.MethodPost, "/tasks/"+seg(taskID)+"/gates/"+seg(gateID)+"/approve", nil, body, nil)
}

func (r *Remote) RejectGate(ctx context.Context, taskID, gateID, reason string) error {
	body := struct {
		Reason string `json:"reason"`
	}{reason}
	return r.do(ctx, http.MethodPost, "/tasks/"+seg(taskID)+"/gates/"+seg(gateID)+"/reject", nil, body, nil)
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
	if aq.TaskID != "" {
		q.Set("taskId", aq.TaskID)
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

// seg escapes one path segment (task and gate IDs are UUIDs, but never trust
// a caller-supplied ID not to contain "/" or "..").
func seg(s string) string { return url.PathEscape(s) }

// do performs one REST call. A non-2xx status becomes an *Error whose kind
// follows the status and whose message is the API's {"error": …} text, so a
// remote failure reads exactly like the local one.
func (r *Remote) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	// path arrives already escaped (see seg); keep it verbatim as RawPath.
	u := *r.base
	escaped := strings.TrimRight(u.EscapedPath(), "/") + path
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
			return newErr(ErrInternal, "encode request: "+err.Error())
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
	switch {
	case status == http.StatusBadRequest:
		return newErr(ErrInvalid, msg)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return newErr(ErrUnauthorized, msg)
	case status == http.StatusNotFound:
		return newErr(ErrNotFound, msg)
	case status == http.StatusServiceUnavailable:
		return newErr(ErrUnavailable, msg)
	case status == http.StatusInternalServerError:
		return newErr(ErrInternal, msg)
	default:
		return newErr(ErrInternal, fmt.Sprintf("HTTP %d: %s", status, msg))
	}
}
