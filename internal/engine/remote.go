// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// Remote tools (README § Manage, Custom tools): tools an admin adds with no code, either imported
// from a remote MCP server over Streamable HTTP (JSON-RPC 2.0) or a typed mapping to an HTTPS
// endpoint. Every request goes through remoteClient: https only, no credentials in the URL, every
// address it connects to checked when it connects (so a name that resolves, or a redirect that
// leads, to a private, loopback, link-local or metadata address is refused: DNS rebinding included),
// at most 3 redirects, ports 443 and 8443 only, 30 s for the whole call (every request of it; also
// a Discover/Import), a 1 MB answer cap and 64 KB of headers, no proxy (a proxy would connect for it,
// past the address check; HTTPS_PROXY is ignored by design). Secrets live server-side (secrets
// table), each bound to the origin it was set for (secretRef: never sent to another scheme, host or
// port), added as one header, and redacted from everything the remote end sends back (its value with
// and without its prefix, base64, URL and JSON forms); they never reach the API, a log line, an
// error, the snapshot, the export or a model. An answer is untrusted data from outside the matter,
// labelled so in its output. With no signed-in admin (--allow-no-auth) all of it is off (Off).

// Remote call bounds.
const (
	remoteTimeout   = 30 * time.Second
	remoteMaxBytes  = 1 << 20
	remoteMaxHeader = 64 << 10
	remoteRedirects = 3
	mcpProtocol     = "2025-06-18"
)

// mcpProtocols are the MCP protocol versions BigLaw speaks (the server's initialize answer must
// name one of them: its requests and answers are then the shapes remote.go reads).
var mcpProtocols = []string{"2025-06-18", "2025-03-26"}

// remotePorts are the ports a custom tool or server may use (L4: a port policy, so a custom tool is
// never a port scanner; the refusal's words are the same whatever the reason).
var remotePorts = []string{"443", "8443"}

// ErrForbidden is a refusal of who is asking, not of what (403): custom outbound calls and secrets
// with no signed-in admin.
var ErrForbidden = errors.New("engine: forbidden")

// errRemoteOff is the refusal of every secret and custom outbound call when the server runs with
// no signed-in admin (RemotePolicy.Off).
var errRemoteOff = fmt.Errorf("%w: secrets and custom tools' outbound calls need a signed-in admin, and this server runs without sign-in (--allow-no-auth): until PR 8's sign-in they work only when BigLaw runs on loopback without that flag (README § Exposure)", ErrForbidden)

// errRemoteDeadline is the cause of a call's own 30 s deadline (withRemoteDeadline).
var errRemoteDeadline = errors.New("remote: no answer within 30 s (the whole call)")

// withRemoteDeadline is ctx bounded by one call's 30 s (every request of an MCP call or of a
// Discover/Import shares it).
func withRemoteDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, remoteTimeout, errRemoteDeadline)
}

// RemotePolicy is what remote calls may reach. The zero value is production's: public addresses
// over https only. Tests (a local fake server) allow loopback and trust their server's certificate.
type RemotePolicy struct {
	Off           bool           // no signed-in admin (serve --allow-no-auth): no secret is set or sent and no custom call made
	AllowLoopback bool           // tests only: 127.0.0.0/8 and ::1 on any port (never set by cmd/biglaw)
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
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: remoteTimeout, MaxResponseHeaderBytes: remoteMaxHeader, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second},
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
	if err != nil || c.deniedAddr(ip) != "" {
		return fmt.Errorf("remote: %w", errRemoteAddr)
	}
	return nil
}

// errRemoteAddr is every refusal of where a request would go (a host, an address or a port): one
// text whatever the reason, so the refusals tell nothing about a host's ports (L4). The reason is
// for the server's own log only (deniedAddr).
var errRemoteAddr = errors.New("the URL is not one BigLaw may reach (https to a public host on port 443 or 8443)")

// deniedAddr is why ip may not be reached ("" when it may): loopback, private, link-local, any
// address of the IANA special-purpose registries that is not globally reachable, and IPv6 forms that
// carry an IPv4 address (mapped, translated, compatible, NAT64, 6to4, Teredo) when it is denied or
// the form is one BigLaw has no reason to dial.
func (c *remoteClient) deniedAddr(ip netip.Addr) string {
	ip = ip.WithZone("")
	if ip.Is4In6() { // ::ffff:a.b.c.d is a.b.c.d on the wire
		ip = ip.Unmap()
	}
	if ip.Is6() {
		b := ip.As16()
		if p := netip.PrefixFrom(ip, 16); p.Masked().Addr() == netip.MustParseAddr("2002::") { // 6to4: the IPv4 in bits 16–48
			if why := c.deniedAddr(netip.AddrFrom4([4]byte(b[2:6]))); why != "" {
				return "6to4 over " + why
			}
		}
	}
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

// reserved are the special-purpose blocks (IANA's IPv4 and IPv6 Special-Purpose Address Registries,
// RFC 6890 and updates) that are not globally reachable, plus the IPv6 forms that embed an IPv4
// address and the deprecated site-local block; loopback, private (RFC 1918, fc00::/7), link-local and
// multicast are refused by kind above.
var reserved = func() (out []netip.Prefix) {
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"240.0.0.0/4", "255.255.255.255/32",
		"::/96",             // IPv4-compatible (deprecated): ::a.b.c.d
		"::ffff:0:0:0/96",   // IPv4-translated (SIIT): ::ffff:0:a.b.c.d
		"64:ff9b::/96",      // NAT64 well-known prefix
		"64:ff9b:1::/48",    // NAT64 local-use prefix
		"100::/64",          // discard-only
		"100:0:0:1::/64",    // dummy prefix
		"2001::/23",         // IETF protocol assignments: Teredo 2001::/32, benchmarking, ORCHID
		"2001:db8::/32",     // documentation
		"3fff::/20",         // documentation
		"5f00::/16",         // SRv6 SIDs
		"fec0::/10",         // site-local (deprecated)
		"fd00:ec2::254/128", // a cloud metadata address (inside fc00::/7, named for the log)
	} {
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
	labels := strings.Split(host, ".")
	ip, ipErr := netip.ParseAddr(host)
	loop := ipErr == nil && ip.IsLoopback() && c.policy.AllowLoopback
	switch {
	case host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".localdomain") || strings.HasPrefix(host, "ip6-"):
	case ipErr == nil && c.deniedAddr(ip) != "":
	case ipErr != nil && numericLabel.MatchString(labels[len(labels)-1]): // 2130706433, 0x7f.1, 127.1: an address, not a name
	case !loop && !slices.Contains(remotePorts, cmpStr(u.Port() != "", u.Port(), "443")):
	default:
		return nil
	}
	return fmt.Errorf("remote: %w", errRemoteAddr)
}

// untrusted is third-party text (an MCP server's tool description, name or protocol) fit to show:
// control and format characters (bidi overrides, zero-width marks) and line breaks become spaces, so
// it cannot open a line of its own in a description (no smuggled "Example:" or instruction line),
// runs of space fold to one, and it is cut to at most max characters.
func untrustedText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
	return truncate(strings.Join(strings.Fields(s), " "), max)
}

func untrusted(s string) string { return untrustedText(s, 200) }

var numericLabel = regexp.MustCompile(`^(0x[0-9a-f]*|[0-9]+)$`)

// originOf is a URL's origin as a secret is bound to it: https://host:port, the host lower-cased
// and the port explicit ("" when raw is not an absolute URL).
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), cmpStr(u.Port() != "", u.Port(), "443"))
}

// secretRef is a secret as a call sends it: the header, its value (prefix + secret), the secret's
// name (for messages) and the origin it is bound to.
type secretRef struct {
	name, header, prefix, value, origin string
}

// forms are the ways the secret may come back in an answer: the header value, the bare secret,
// and each in base64 (standard and URL alphabets, padded or not), URL-escaped and JSON-escaped; a
// bare secret that is itself base64 of user:password adds the decoded pair and its password.
func (s *secretRef) forms() []string {
	if s == nil {
		return nil
	}
	bare := strings.TrimPrefix(s.value, s.prefix)
	base := []string{s.value, bare}
	if dec, err := base64.StdEncoding.DecodeString(bare); err == nil && utf8.Valid(dec) {
		if u, pw, ok := strings.Cut(string(dec), ":"); ok && u != "" {
			base = append(base, string(dec))
			if len(pw) >= 6 {
				base = append(base, pw)
			}
		}
	}
	var out []string
	for _, v := range base {
		j, _ := json.Marshal(v)
		out = append(out, v, base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v)),
			base64.URLEncoding.EncodeToString([]byte(v)), base64.RawURLEncoding.EncodeToString([]byte(v)),
			url.QueryEscape(v), url.PathEscape(v), string(j[1:len(j)-1]))
	}
	slices.SortFunc(out, func(a, b string) int { return len(b) - len(a) }) // longest first: a form inside another goes with it
	return slices.Compact(out)
}

// redact is text with every form of the secret replaced (a remote end that echoes its request).
func (s *secretRef) redact(text string) string {
	for _, f := range s.forms() {
		if len(f) >= 6 {
			text = strings.ReplaceAll(text, f, "[secret redacted]")
		}
	}
	return text
}

// in says whether text holds any form of the secret.
func (s *secretRef) in(text string) bool {
	return s.redact(text) != text
}

// remoteAnswer is what one remote request returned.
type remoteAnswer struct {
	status  int
	header  http.Header
	body    []byte
	elapsed time.Duration
}

// do sends one request, the secret (if any) as its header, only to the secret's own origin. Errors
// are BigLaw's words only (the URL, the secret and the answer's body are never in them).
func (c *remoteClient) do(ctx context.Context, method, rawURL string, header http.Header, body []byte, sec *secretRef) (remoteAnswer, error) {
	if c.policy.Off {
		return remoteAnswer{}, errRemoteOff
	}
	if err := c.checkURL(rawURL); err != nil {
		return remoteAnswer{}, err
	}
	if sec != nil && (sec.origin == "" || originOf(rawURL) != sec.origin) {
		return remoteAnswer{}, fmt.Errorf("remote: the secret %q was set for another origin (scheme, host and port), so it is not sent here: an admin sets it again for this URL in Manage", sec.name)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return remoteAnswer{}, errors.New("remote: the request could not be built")
	}
	maps.Copy(req.Header, header)
	req.Header.Set("User-Agent", "BigLaw")
	if sec != nil {
		req.Header.Set(sec.header, sec.value)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		switch {
		case errors.Is(context.Cause(ctx), errRemoteDeadline):
			return remoteAnswer{}, errRemoteDeadline
		case ctx.Err() != nil:
			return remoteAnswer{}, ctx.Err()
		case errors.Is(err, errRemoteAddr):
			return remoteAnswer{}, fmt.Errorf("remote: %w", errRemoteAddr)
		case errors.As(err, &ue) && strings.Contains(ue.Err.Error(), "redirect"):
			return remoteAnswer{}, errors.New("remote: a redirect was refused (to another host, too many, or to a URL BigLaw may not reach)")
		}
		return remoteAnswer{}, errors.New("remote: the server could not be reached, or did not answer in time") // one text: no port or timing oracle
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, remoteMaxBytes+1))
	if errors.Is(context.Cause(ctx), errRemoteDeadline) {
		return remoteAnswer{}, errRemoteDeadline
	} else if err != nil {
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
	url                  string
	sec                  *secretRef
	id                   string
	next                 int
	ServerName, Protocol string
}

// openMCP is the initialize handshake (and the initialized notification) with a server; the server
// must speak a protocol version BigLaw does (mcpProtocols). Close it when done.
func (c *remoteClient) openMCP(ctx context.Context, rawURL string, sec *secretRef) (*mcpSession, error) {
	s := &mcpSession{c: c, url: rawURL, sec: sec}
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
	if !slices.Contains(mcpProtocols, s.Protocol) {
		s.Close(ctx)
		return nil, fmt.Errorf("remote: the MCP server speaks protocol version %q; BigLaw speaks %s", truncate(untrusted(s.Protocol), 40), strings.Join(mcpProtocols, " or "))
	}
	if err := s.notify(ctx, "notifications/initialized"); err != nil {
		s.Close(ctx)
		return nil, err
	}
	return s, nil
}

// Close ends the session on the server (HTTP DELETE with its session id; Streamable HTTP), at most
// 5 s, whatever ctx's state: a failure or a 405 (the server keeps no sessions) changes nothing.
func (s *mcpSession) Close(ctx context.Context) {
	if s == nil || s.id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = s.c.do(ctx, "DELETE", s.url, s.headers(), nil, s.sec) // best effort: the session times out on the server anyway
	s.id = ""
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
	a, err := s.c.do(ctx, "POST", s.url, s.headers(), body, s.sec)
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
	a, err := s.c.do(ctx, "POST", s.url, s.headers(), body, s.sec)
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
// (convertSchema) or why it could not be; its description is third-party text, cleaned and capped
// (untrustedText). Item and Previous are a tool already imported from it and the text an admin
// accepted for it (Discover).
type RemoteTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	In          json.RawMessage `json:"in,omitempty"`
	Problem     string          `json:"problem,omitempty"`
	Item        string          `json:"item,omitempty"`
	Previous    string          `json:"previous,omitempty"`
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
			rt := RemoteTool{Name: truncate(t.Name, 128), Description: untrustedText(t.Description, maxImportedBytes)}
			if rt.Name != untrusted(t.Name) || rt.Name == "" {
				rt.Name, rt.Problem = untrusted(t.Name), "its name is empty, over 128 characters or holds control or format characters"
			} else if in, err := convertSchema(t.InputSchema); err != nil {
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
