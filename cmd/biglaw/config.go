// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/discover-legal/BigLaw/internal/store"
)

// envVar is one runtime environment variable. This table is the complete
// configuration surface (README § Configuration); scripts/loc.py counts the
// distinct names read in Go and fails CI above 20. Each variable is
// validated by the PR that first reads it (pr); later ones are declared here
// so the surface is fixed, shown by `version --config`, and warned about
// when set but not yet read.
type envVar struct {
	name, def string
	secret    bool // never printed by `biglaw version --config`
	pr        int  // the build PR that first reads it
}

var envVars = []envVar{
	{name: "BIGLAW_DATABASE_URL", secret: true, pr: 1}, // password inside; printed redacted
	{name: "BIGLAW_ADDR", def: "127.0.0.1:3101", pr: 1},
	{name: "BIGLAW_PUBLIC_URL", def: "http://127.0.0.1:3101", pr: 8},
	{name: "BIGLAW_MODEL_URL", pr: 1}, // vendor breaker today; model client in PR 2
	{name: "BIGLAW_MODEL_KEY", secret: true, pr: 2},
	{name: "BIGLAW_MODEL_LEAD", pr: 2},
	{name: "BIGLAW_MODEL_SPECIALIST", pr: 2},
	{name: "BIGLAW_MODEL_CRITIC", pr: 2},
	{name: "BIGLAW_BUDGET_CALLS", def: "150", pr: 4},
	{name: "BIGLAW_BUDGET_USD", def: "5", pr: 4},
	{name: "BIGLAW_LOCAL_WATTS", def: "250", pr: 2},
	{name: "BIGLAW_PRICES", pr: 2},
	{name: "BIGLAW_COLLAPSE_THRESHOLD", def: "0.5", pr: 4},
	{name: "BIGLAW_COLLAPSE_ACTION", def: "strict", pr: 4},
	{name: "BIGLAW_OIDC_ISSUER", pr: 8},
	{name: "BIGLAW_OIDC_CLIENT_ID", pr: 8},
	{name: "BIGLAW_OIDC_CLIENT_SECRET", secret: true, pr: 8},
	{name: "BIGLAW_AI_RATE", pr: 9},
	{name: "ALLOW_ANTHROPIC", pr: 1},
	{name: "ALLOW_AWS", pr: 1},
}

// Config is the configuration this build reads. Variables first read by a
// later PR join it (and its validation) in that PR.
type Config struct {
	DatabaseURL    string
	Addr           string
	ModelURL       string // checked by the vendor breaker only, until PR 2
	AllowAnthropic bool
	AllowAWS       bool
}

// Load resolves and validates the configuration from getenv (os.Getenv in
// production). All problems are reported at once. The vendor breaker runs
// last, so a valid config that couples directly to a gated vendor is still
// refused.
func Load(getenv func(string) string) (Config, error) {
	get := func(name string) string { return value(getenv, name) }
	var errs []error
	bad := func(name, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{name}, args...)...))
	}
	c := Config{
		DatabaseURL:    get("BIGLAW_DATABASE_URL"),
		Addr:           get("BIGLAW_ADDR"),
		ModelURL:       get("BIGLAW_MODEL_URL"),
		AllowAnthropic: truthy(get("ALLOW_ANTHROPIC")),
		AllowAWS:       truthy(get("ALLOW_AWS")),
	}
	if c.DatabaseURL == "" {
		bad("BIGLAW_DATABASE_URL", "is required: a Postgres DSN such as postgres://biglaw:PASSWORD@localhost:5432/biglaw?sslmode=disable")
	} else if err := store.CheckDSN(c.DatabaseURL); err != nil {
		bad("BIGLAW_DATABASE_URL", "%v", err)
	}
	if host, port, err := net.SplitHostPort(c.Addr); err != nil || !validPort(port) {
		bad("BIGLAW_ADDR", "%q is not host:port", c.Addr)
	} else if host == "" {
		bad("BIGLAW_ADDR", "%q has no host; write 127.0.0.1:%s (or 0.0.0.0:%s with serve --allow-no-auth, see README § Exposure)", c.Addr, port, port)
	}
	if c.ModelURL != "" {
		if u, err := url.Parse(c.ModelURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("BIGLAW_MODEL_URL", "%s must be an http(s) URL with a host", redactURL(c.ModelURL))
		} else if u.User != nil {
			bad("BIGLAW_MODEL_URL", "must not carry credentials in the URL; put the key in BIGLAW_MODEL_KEY")
		} else if h := u.Hostname(); !isASCII(h) {
			bad("BIGLAW_MODEL_URL", "host must be ASCII; write an internationalised host name in its xn-- form")
		}
	}
	if err := errors.Join(errs...); err != nil {
		return c, err
	}
	return c, GuardVendors(c)
}

// value is getenv(name) trimmed, or the table default when empty.
func value(getenv func(string) string, name string) string {
	if v := strings.TrimSpace(getenv(name)); v != "" {
		return v
	}
	for _, e := range envVars {
		if e.name == name {
			return e.def
		}
	}
	panic("config: unknown variable " + name) // programming error
}

// vendors is the self-imposed vendor breaker ported from v1
// (biglaw-go/internal/config/guard.go): endpoints that couple BigLaw
// DIRECTLY to a gated closed vendor, and the variable that opts in. It keys
// on the endpoint, not the model name: a Claude model behind a third-party
// OpenAI-compatible gateway is allowed. Like v1 it matches the marker
// anywhere in the URL (and in the decoded host name), so it fails closed.
var vendors = []struct {
	name, optIn string
	markers     []string
}{
	{"Anthropic", "ALLOW_ANTHROPIC", []string{"anthropic.com"}},
	{"AWS/Amazon", "ALLOW_AWS", []string{"amazonaws.com", "bedrock"}},
}

// GuardVendors returns an error naming every tripped breaker and its opt-in.
func GuardVendors(c Config) error {
	if c.ModelURL == "" {
		return nil
	}
	haystack := strings.ToLower(c.ModelURL)
	if u, err := url.Parse(c.ModelURL); err == nil {
		haystack += " " + strings.ToLower(u.Hostname()) // decodes %2E and the like
	}
	allowed := map[string]bool{"ALLOW_ANTHROPIC": c.AllowAnthropic, "ALLOW_AWS": c.AllowAWS}
	var errs []error
	for _, v := range vendors {
		for _, m := range v.markers {
			if strings.Contains(haystack, m) && !allowed[v.optIn] {
				errs = append(errs, fmt.Errorf("vendor breaker tripped: BIGLAW_MODEL_URL %s couples BigLaw directly to %s, a gated closed vendor; "+
					"set %s=1 to opt in deliberately, or reach the model through a gateway", redactURL(c.ModelURL), v.name, v.optIn))
				break
			}
		}
	}
	return errors.Join(errs...)
}

// isLoopback reports whether addr (host:port) listens on loopback only.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// UnknownVars lists BIGLAW_* names in environ that BigLaw does not read
// (typos such as BIGLAW_DATABASE_URI), for a startup warning.
func UnknownVars(environ []string) []string {
	known := map[string]bool{"BIGLAW_TEST_DSN": true, "BIGLAW_REQUIRE_DB": true}
	for _, e := range envVars {
		known[e.name] = true
	}
	var out []string
	for _, kv := range environ {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "BIGLAW_") && !known[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// NotYetRead lists declared variables that are set but first read by a
// later PR, so an operator is not misled into thinking they took effect.
func NotYetRead(getenv func(string) string) []string {
	var out []string
	for _, e := range envVars {
		if e.pr > 1 && strings.TrimSpace(getenv(e.name)) != "" {
			out = append(out, e.name)
		}
	}
	return out
}

// Describe renders the configuration for `biglaw version --config`.
// Secrets print only as set/unset; the database password and every URL's
// user info and query values are masked; a database URL that does not pass
// CheckDSN is not shown at all. The PG* variables lib/pq also reads are
// listed by name only.
func Describe(getenv func(string) string, environ []string) string {
	var b strings.Builder
	for _, e := range envVars {
		raw := strings.TrimSpace(getenv(e.name))
		val, src := raw, "env"
		if raw == "" {
			val, src = e.def, "default"
		}
		switch {
		case val == "":
			val, src = "(unset)", ""
		case e.name == "BIGLAW_DATABASE_URL":
			if store.CheckDSN(val) != nil {
				val = "(set, not shown: invalid, see below)"
			} else {
				val = redactDSN(val)
			}
		case e.secret:
			val = "(set, hidden)"
		case strings.Contains(val, "://"):
			val = redactURL(val)
		}
		if e.pr > 1 {
			src = strings.TrimPrefix(src+", ", ", ") + "read from PR " + strconv.Itoa(e.pr)
		}
		fmt.Fprintf(&b, "%-26s %s", e.name, val)
		if src != "" {
			fmt.Fprintf(&b, "  [%s]", src)
		}
		b.WriteByte('\n')
	}
	var pg []string
	for _, kv := range environ {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "PG") {
			pg = append(pg, name)
		}
	}
	if len(pg) > 0 {
		sort.Strings(pg)
		fmt.Fprintf(&b, "lib/pq also reads these libpq variables (values not shown): %s\n", strings.Join(pg, ", "))
	}
	return b.String()
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 0 && n <= 65535 && p[0] >= '0' && p[0] <= '9' // no sign
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// redactURL hides all user info (a key is sometimes the user name) and every
// query value and fragment (some gateways take the key as ?key=…).
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URL, not shown)"
	}
	if u.User != nil {
		u.User = url.User("xxxxx")
	}
	q := u.Query()
	for k := range q {
		q.Set(k, "xxxxx")
	}
	u.RawQuery = q.Encode()
	if u.Fragment != "" {
		u.Fragment = "xxxxx"
	}
	return u.String()
}

// kvPassword matches password=… and sslpassword=… in a key=value DSN, quoted
// or unquoted (an unquoted value may hold backslash escapes).
var kvPassword = regexp.MustCompile(`(?i)(password\s*=\s*)('(?:[^'\\]|\\.)*'|(?:[^\s'\\]|\\.)+)`)

// redactDSN masks the password of a DSN that passed store.CheckDSN.
func redactDSN(dsn string) string {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return kvPassword.ReplaceAllString(dsn, "${1}xxxxx")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "(unparseable DSN, not shown)"
	}
	q := u.Query()
	for k := range q {
		if strings.Contains(strings.ToLower(k), "password") {
			q.Set(k, "xxxxx")
		}
	}
	u.RawQuery, u.Fragment = q.Encode(), ""
	return u.Redacted()
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
