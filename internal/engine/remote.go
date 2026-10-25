// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// Remote tools (README § Manage, Custom tools): tools an admin adds with no code, either imported
// from a remote MCP server over Streamable HTTP (JSON-RPC 2.0) or a typed mapping to an HTTPS
// endpoint. Every request goes through remoteClient: https only, no credentials in the URL, every
// address it connects to checked when it connects (so a name that resolves, or a redirect that
// leads, to a private, loopback, link-local or metadata address is refused: DNS rebinding included),
// at most 3 redirects, a 30 s timeout, a 1 MB answer cap, no proxy (a proxy would connect for it,
// past the address check). Secrets live server-side (secrets table) and are added as one header;
// they never reach the API, a log line, an error, the snapshot or the export. An answer is untrusted
// data from outside the matter, labelled so in its output.

// Remote call bounds.
const (
	remoteTimeout   = 30 * time.Second
	remoteMaxBytes  = 1 << 20
	remoteRedirects = 3
	mcpProtocol     = "2025-06-18"
)

// RemotePolicy is what remote calls may reach. The zero value is production's: public addresses
// over https only. Tests (a local fake server) allow loopback and trust their server's certificate.
type RemotePolicy struct {
	AllowLoopback bool           // tests only: 127.0.0.0/8 and ::1 (never set by cmd/biglaw)
	RootCAs       *x509.CertPool // tests only: the httptest server's certificate
}

// SetRemotePolicy replaces the remote calls' policy (tests: a local fake MCP or HTTP server).
func (e *Engine) SetRemotePolicy(p RemotePolicy) { e.remote = newRemoteClient(p) }

type remoteClient struct {
	policy RemotePolicy
	http   *http.Client
}

func newRemoteClient(p RemotePolicy) *remoteClient {
	c := &remoteClient{policy: p}
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error { return c.dialCheck(address) }}
	c.http = &http.Client{
		Timeout: remoteTimeout,
		Transport: &http.Transport{Proxy: nil, DialContext: d.DialContext, TLSClientConfig: &tls.Config{RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: remoteTimeout, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error { // the same host only: a secret is for the host it was configured for
			if len(via) >= remoteRedirects || req.URL.Host != via[0].URL.Host {
				return errors.New("remote: redirect refused")
			}
			return c.checkURL(req.URL.String())
		},
	}
	return c
}

// dialCheck refuses a connection to an address that is not public, whatever name led to it.
func (c *remoteClient) dialCheck(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("remote: %w", errRemoteAddr)
	}
	if why := c.deniedAddr(ip); why != "" {
		return fmt.Errorf("remote: %w (%s)", errRemoteAddr, why)
	}
	return nil
}

var errRemoteAddr = errors.New("the address is not a public one (private, loopback, link-local, metadata or reserved)")

// deniedAddr is why ip may not be reached ("" when it may).
func (c *remoteClient) deniedAddr(ip netip.Addr) string {
	ip = ip.Unmap()
	switch {
	case ip.IsLoopback():
		if c.policy.AllowLoopback {
			return ""
		}
		return "loopback"
	case ip.IsPrivate():
		return "private"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return "link-local (cloud metadata lives there)"
	case ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast():
		return "unspecified or multicast"
	}
	for _, p := range reserved {
		if p.Contains(ip) {
			return "reserved (" + p.String() + ")"
		}
	}
	return ""
}

var reserved = func() (out []netip.Prefix) {
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"255.255.255.255/32", "64:ff9b::/96", "100::/64", "2001:db8::/32", "fd00:ec2::254/128"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// checkURL is what a remote tool's or server's URL must be when it is saved and when it is
// called: absolute https, a host, no user info, no fragment, and not a literal or named address
// that is never public (an IP in a denied range, localhost). Names are checked when they connect.
func (c *remoteClient) checkURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || !u.IsAbs():
		return errors.New("remote: not an absolute URL")
	case u.Scheme != "https":
		return errors.New("remote: the URL must be https")
	case u.User != nil:
		return errors.New("remote: no credentials in the URL (store the key as a secret)")
	case u.Hostname() == "" || u.Fragment != "":
		return errors.New("remote: a URL needs a host and no fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || host == "metadata.google.internal" {
		return errors.New("remote: " + host + " is not a public host")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if why := c.deniedAddr(ip); why != "" {
			return fmt.Errorf("remote: %s is %s, never reachable from BigLaw", host, why)
		}
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return errors.New("remote: bad port")
		}
	}
	return nil
}

// remoteAnswer is what one remote request returned.
type remoteAnswer struct {
	status  int
	header  http.Header
	body    []byte
	elapsed time.Duration
}

// do sends one request, the secret (if any) as header: value. Errors are BigLaw's words only (the
// URL, the secret and the answer's body are never in them).
func (c *remoteClient) do(ctx context.Context, method, rawURL string, header http.Header, body []byte, secretHeader, secret string) (remoteAnswer, error) {
	if err := c.checkURL(rawURL); err != nil {
		return remoteAnswer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return remoteAnswer{}, errors.New("remote: the request could not be built")
	}
	maps.Copy(req.Header, header)
	req.Header.Set("User-Agent", "BigLaw")
	if secretHeader != "" && secret != "" {
		req.Header.Set(secretHeader, secret)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		switch {
		case ctx.Err() != nil:
			return remoteAnswer{}, ctx.Err()
		case errors.Is(err, errRemoteAddr):
			return remoteAnswer{}, fmt.Errorf("remote: %w", errRemoteAddr)
		case errors.As(err, &ue) && ue.Timeout():
			return remoteAnswer{}, errors.New("remote: no answer within 30 s")
		case errors.As(err, &ue) && strings.Contains(ue.Err.Error(), "redirect"):
			return remoteAnswer{}, errors.New("remote: a redirect was refused (to another host, too many, or to a URL BigLaw may not reach)")
		}
		return remoteAnswer{}, errors.New("remote: the server could not be reached")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, remoteMaxBytes+1))
	if err != nil {
		return remoteAnswer{}, errors.New("remote: the answer could not be read")
	}
	if len(data) > remoteMaxBytes {
		return remoteAnswer{}, errors.New("remote: the answer is over 1 MB")
	}
	return remoteAnswer{resp.StatusCode, resp.Header, data, time.Since(start)}, nil
}

// mcpSession is one MCP connection: initialize, then requests on its session id.
type mcpSession struct {
	c                    *remoteClient
	url, header, secret  string
	id                   string
	next                 int
	ServerName, Protocol string
}

// openMCP is the initialize handshake (and the initialized notification) with a server.
func (c *remoteClient) openMCP(ctx context.Context, rawURL, secretHeader, secret string) (*mcpSession, error) {
	s := &mcpSession{c: c, url: rawURL, header: secretHeader, secret: secret}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := s.call(ctx, "initialize", map[string]any{"protocolVersion": mcpProtocol, "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "BigLaw", "version": "2"}}, &init); err != nil {
		return nil, err
	}
	s.ServerName, s.Protocol = init.ServerInfo.Name, init.ProtocolVersion
	if err := s.notify(ctx, "notifications/initialized"); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *mcpSession) headers() http.Header {
	h := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}
	if s.id != "" {
		h.Set("Mcp-Session-Id", s.id)
	}
	if s.Protocol != "" {
		h.Set("MCP-Protocol-Version", s.Protocol)
	}
	return h
}

func (s *mcpSession) notify(ctx context.Context, method string) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	a, err := s.c.do(ctx, "POST", s.url, s.headers(), body, s.header, s.secret)
	if err == nil && a.status/100 != 2 {
		err = fmt.Errorf("remote: the MCP server answered %s with HTTP %d", method, a.status)
	}
	return err
}

// call is one JSON-RPC request; its result decodes into out. The answer may be JSON or an SSE
// stream (Streamable HTTP): the event whose id is the request's is the answer.
func (s *mcpSession) call(ctx context.Context, method string, params any, out any) error {
	s.next++
	id := s.next
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	a, err := s.c.do(ctx, "POST", s.url, s.headers(), body, s.header, s.secret)
	if err != nil {
		return err
	}
	if a.status/100 != 2 {
		return fmt.Errorf("remote: the MCP server answered %s with HTTP %d", method, a.status)
	}
	if sid := a.header.Get("Mcp-Session-Id"); sid != "" && method == "initialize" {
		s.id = sid
	}
	msg := a.body
	if strings.HasPrefix(a.header.Get("Content-Type"), "text/event-stream") {
		msg = nil
		sc := bufio.NewScanner(bytes.NewReader(a.body))
		sc.Buffer(make([]byte, 64<<10), remoteMaxBytes)
		var data strings.Builder
		for sc.Scan() { // events are blank-line separated; data lines join with "\n"
			ln := sc.Text()
			if d, ok := strings.CutPrefix(ln, "data:"); ok {
				data.WriteString(strings.TrimPrefix(d, " ") + "\n")
				continue
			}
			if ln == "" && data.Len() > 0 {
				var probe struct{ ID any }
				if json.Unmarshal([]byte(data.String()), &probe) == nil && fmt.Sprint(probe.ID) == strconv.Itoa(id) {
					msg = []byte(data.String())
					break
				}
				data.Reset()
			}
		}
		if msg == nil && data.Len() > 0 {
			msg = []byte(data.String())
		}
	}
	var rpc struct {
		ID     any             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(msg, &rpc) != nil || fmt.Sprint(rpc.ID) != strconv.Itoa(id) {
		return fmt.Errorf("remote: the MCP server's answer to %s is not a JSON-RPC response to it", method)
	}
	if rpc.Error != nil {
		return fmt.Errorf("remote: the MCP server refused %s (JSON-RPC error %d)", method, rpc.Error.Code)
	}
	if json.Unmarshal(rpc.Result, out) != nil {
		return fmt.Errorf("remote: the MCP server's result for %s does not decode", method)
	}
	return nil
}

// RemoteTool is one tool an MCP server lists, its input schema converted to BigLaw's subset
// (convertSchema) or why it could not be.
type RemoteTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	In          json.RawMessage `json:"in,omitempty"`
	Problem     string          `json:"problem,omitempty"`
}

// listTools is a server's tools (tools/list, following its cursor, at most 10 pages).
func (s *mcpSession) listTools(ctx context.Context) ([]RemoteTool, error) {
	var out []RemoteTool
	cursor := ""
	for range 10 {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := s.call(ctx, "tools/list", params, &page); err != nil {
			return nil, err
		}
		for _, t := range page.Tools {
			rt := RemoteTool{Name: truncate(t.Name, 128), Description: truncate(t.Description, 4000)}
			if in, err := convertSchema(t.InputSchema); err != nil {
				rt.Problem = err.Error()
			} else {
				rt.In = in
			}
			out = append(out, rt)
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// callTool is tools/call: the text of its content parts and whether the server marked it an error.
func (s *mcpSession) callTool(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	var res struct {
		Content []struct {
			Type, Text string
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := s.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &res); err != nil {
		return "", false, err
	}
	var b strings.Builder
	for _, p := range res.Content {
		if p.Type == "text" {
			b.WriteString(p.Text)
		} else {
			fmt.Fprintf(&b, "[a %s part, not shown]", truncate(p.Type, 40))
		}
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n"), res.IsError, nil
}

// convertSchema is an MCP tool's input schema in BigLaw's subset, strict-shaped: every object
// closed, every property required (an optional one nullable, sent only when not null), and the
// keywords the subset lacks dropped where they only describe (title, format, default, examples,
// $schema) or refused where they change what is valid (anyOf, oneOf, allOf, not, $ref, pattern
// properties, an open object).
func convertSchema(raw json.RawMessage) (json.RawMessage, error) {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return nil, errors.New("its input schema is not a JSON object")
	}
	out, err := strictify(v, "$", true)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(out)
	if c, err := agent.Compile(b); err != nil || c.Strict("in") != "" {
		return nil, fmt.Errorf("its input schema does not fit BigLaw's subset (%v)", cmpErr(err, errors.New(c.Strict("in"))))
	}
	return b, nil
}

func strictify(v map[string]any, at string, required bool) (map[string]any, error) {
	for _, k := range []string{"anyOf", "oneOf", "allOf", "not", "$ref", "patternProperties", "if", "then", "else", "dependentSchemas", "const"} {
		if _, ok := v[k]; ok {
			return nil, fmt.Errorf("%s uses %s, which BigLaw's schemas cannot express: write the input schema by hand", at, k)
		}
	}
	out := map[string]any{}
	types := []string{}
	switch t := v["type"].(type) {
	case string:
		types = append(types, t)
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				types = append(types, s)
			}
		}
	case nil:
		if _, ok := v["properties"]; ok {
			types = append(types, "object")
		} else if _, ok := v["enum"]; !ok {
			return nil, fmt.Errorf("%s has no type", at)
		}
	}
	for _, k := range []string{"description", "enum", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems"} {
		if x, ok := v[k]; ok {
			out[k] = x
		}
	}
	if slices.Contains(types, "object") {
		props, _ := v["properties"].(map[string]any)
		if len(props) == 0 {
			return nil, fmt.Errorf("%s is an object with no properties (an open object BigLaw cannot validate)", at)
		}
		req := map[string]bool{}
		if rs, ok := v["required"].([]any); ok {
			for _, r := range rs {
				if s, ok := r.(string); ok {
					req[s] = true
				}
			}
		}
		ps := map[string]any{}
		names := slices.Sorted(maps.Keys(props))
		for _, n := range names {
			p, ok := props[n].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s.%s is not a schema", at, n)
			}
			s, err := strictify(p, at+"."+n, req[n])
			if err != nil {
				return nil, err
			}
			ps[n] = s
		}
		out["properties"], out["required"], out["additionalProperties"] = ps, names, false
	}
	if slices.Contains(types, "array") {
		it, ok := v["items"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s is an array with no items schema", at)
		}
		s, err := strictify(it, at+"[]", true)
		if err != nil {
			return nil, err
		}
		out["items"] = s
	}
	if !required && !slices.Contains(types, "null") && len(types) > 0 { // optional: nullable instead of absent
		types = append(types, "null")
		if e, ok := out["enum"].([]any); ok {
			out["enum"] = append(e, nil)
		}
	}
	if len(types) > 0 {
		out["type"] = types
	}
	return out, nil
}

// exampleFor is an input that validates against a strict schema: the first enum value, null where
// allowed, else a placeholder of the type (within its bounds).
func exampleFor(s *agent.Schema) any {
	switch {
	case s.Enum != nil:
		return s.Enum[0]
	case slices.Contains(s.Type, "null"):
		return nil
	case slices.Contains(s.Type, "object"):
		o := map[string]any{}
		for n, p := range s.Properties {
			o[n] = exampleFor(p)
		}
		return o
	case slices.Contains(s.Type, "array"):
		if s.MinItems != nil && *s.MinItems > 0 && s.Items != nil {
			return []any{exampleFor(s.Items)}
		}
		return []any{}
	case slices.Contains(s.Type, "boolean"):
		return false
	case slices.Contains(s.Type, "integer"), slices.Contains(s.Type, "number"):
		if s.Minimum != nil {
			return *s.Minimum
		}
		return 1
	}
	n := 7
	if s.MinLength != nil && int(*s.MinLength) > n {
		n = int(*s.MinLength)
	}
	if s.MaxLength != nil && int(*s.MaxLength) < n {
		n = int(*s.MaxLength)
	}
	return strings.Repeat("x", n)
}

// dropNulls is v without its null-valued object fields (an optional field the caller left out).
func dropNulls(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, y := range x {
			if y != nil {
				out[k] = dropNulls(y)
			}
		}
		return out
	case []any:
		for i := range x {
			x[i] = dropNulls(x[i])
		}
	}
	return v
}
