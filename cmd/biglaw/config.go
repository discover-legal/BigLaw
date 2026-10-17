// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// envVar is one runtime environment variable. This table is the complete
// configuration surface (README § Configuration); scripts/loc.py counts the
// distinct names read in Go and fails CI above 20.
type envVar struct {
	name, def string
	secret    bool // never printed by `biglaw version --config`
}

var envVars = []envVar{
	{name: "BIGLAW_DATABASE_URL", secret: true}, // password inside; printed redacted
	{name: "BIGLAW_ADDR", def: "127.0.0.1:3101"},
	{name: "BIGLAW_PUBLIC_URL", def: "http://127.0.0.1:3101"},
	{name: "BIGLAW_MODEL_URL"},
	{name: "BIGLAW_MODEL_KEY", secret: true},
	{name: "BIGLAW_MODEL_LEAD"},
	{name: "BIGLAW_MODEL_SPECIALIST"},
	{name: "BIGLAW_MODEL_CRITIC"},
	{name: "BIGLAW_BUDGET_CALLS", def: "150"},
	{name: "BIGLAW_BUDGET_USD", def: "5"},
	{name: "BIGLAW_LOCAL_WATTS", def: "250"},
	{name: "BIGLAW_PRICES"},
	{name: "BIGLAW_COLLAPSE_THRESHOLD", def: "0.5"},
	{name: "BIGLAW_COLLAPSE_ACTION", def: "strict"},
	{name: "BIGLAW_OIDC_ISSUER"},
	{name: "BIGLAW_OIDC_CLIENT_ID"},
	{name: "BIGLAW_OIDC_CLIENT_SECRET", secret: true},
	{name: "BIGLAW_AI_RATE"},
	{name: "ALLOW_ANTHROPIC"},
}

// Config is the resolved runtime configuration.
type Config struct {
	DatabaseURL string
	Addr        string
	PublicURL   string // no trailing slash

	ModelURL, ModelKey                      string
	ModelLead, ModelSpecialist, ModelCritic string // specialist/critic fall back to lead

	BudgetCalls int
	BudgetUSD   float64
	LocalWatts  float64
	Prices      string // JSON object or file path; parsed by the model package

	CollapseThreshold float64
	CollapseAction    string // strict | warn

	OIDCIssuer, OIDCClientID, OIDCClientSecret string

	AIRate         *float64 // nil: AI time is non-billable
	AllowAnthropic bool
}

// Load resolves the configuration from getenv (os.Getenv in production) and
// validates every value. All problems are reported at once. The vendor
// breaker runs last, so a valid config that couples directly to a gated
// vendor is still refused.
func Load(getenv func(string) string) (Config, error) {
	get := func(name string) string {
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
	var errs []error
	bad := func(name, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{name}, args...)...))
	}

	c := Config{
		DatabaseURL:      get("BIGLAW_DATABASE_URL"),
		Addr:             get("BIGLAW_ADDR"),
		PublicURL:        strings.TrimRight(get("BIGLAW_PUBLIC_URL"), "/"),
		ModelURL:         get("BIGLAW_MODEL_URL"),
		ModelKey:         get("BIGLAW_MODEL_KEY"),
		ModelLead:        get("BIGLAW_MODEL_LEAD"),
		ModelSpecialist:  get("BIGLAW_MODEL_SPECIALIST"),
		ModelCritic:      get("BIGLAW_MODEL_CRITIC"),
		Prices:           get("BIGLAW_PRICES"),
		CollapseAction:   strings.ToLower(get("BIGLAW_COLLAPSE_ACTION")),
		OIDCIssuer:       get("BIGLAW_OIDC_ISSUER"),
		OIDCClientID:     get("BIGLAW_OIDC_CLIENT_ID"),
		OIDCClientSecret: get("BIGLAW_OIDC_CLIENT_SECRET"),
		AllowAnthropic:   truthy(get("ALLOW_ANTHROPIC")),
	}

	if c.DatabaseURL == "" {
		bad("BIGLAW_DATABASE_URL", "is required: a Postgres DSN such as postgres://biglaw:PASSWORD@localhost:5432/biglaw?sslmode=disable")
	} else if strings.Contains(c.DatabaseURL, "://") {
		if u, err := url.Parse(c.DatabaseURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			bad("BIGLAW_DATABASE_URL", "is not a valid postgres:// URL (value not shown: it may contain a password)")
		}
	}
	if host, port, err := net.SplitHostPort(c.Addr); err != nil || !validPort(port) {
		bad("BIGLAW_ADDR", "%q is not host:port", c.Addr)
	} else if host == "" {
		bad("BIGLAW_ADDR", "%q has no host; write 0.0.0.0:%s to listen on every interface", c.Addr, port)
	}
	if err := checkURL(c.PublicURL, false); err != nil {
		bad("BIGLAW_PUBLIC_URL", "%v", err)
	}
	if c.ModelURL != "" {
		if err := checkURL(c.ModelURL, true); err != nil {
			bad("BIGLAW_MODEL_URL", "%v", err)
		}
	}
	if c.ModelSpecialist == "" {
		c.ModelSpecialist = c.ModelLead
	}
	if c.ModelCritic == "" {
		c.ModelCritic = c.ModelLead
	}

	if n, err := strconv.Atoi(get("BIGLAW_BUDGET_CALLS")); err != nil || n < 1 || n > 100000 {
		bad("BIGLAW_BUDGET_CALLS", "%q must be a whole number from 1 to 100000", get("BIGLAW_BUDGET_CALLS"))
	} else {
		c.BudgetCalls = n
	}
	c.BudgetUSD = number(get, "BIGLAW_BUDGET_USD", 0, math.MaxFloat64, bad)
	c.LocalWatts = number(get, "BIGLAW_LOCAL_WATTS", 0, 100000, bad)
	c.CollapseThreshold = number(get, "BIGLAW_COLLAPSE_THRESHOLD", 0, 1, bad)
	if f, err := strconv.ParseFloat(get("BIGLAW_COLLAPSE_THRESHOLD"), 64); err == nil && f == 0 {
		bad("BIGLAW_COLLAPSE_THRESHOLD", "must be above 0 (0 would fail every task)")
	}
	if c.CollapseAction != "strict" && c.CollapseAction != "warn" {
		bad("BIGLAW_COLLAPSE_ACTION", "%q must be strict or warn", c.CollapseAction)
	}
	if v := get("BIGLAW_AI_RATE"); v != "" {
		r := number(get, "BIGLAW_AI_RATE", 0, 1e6, bad)
		c.AIRate = &r
	}

	switch {
	case c.OIDCIssuer != "":
		if err := checkIssuer(c.OIDCIssuer); err != nil {
			bad("BIGLAW_OIDC_ISSUER", "%v", err)
		}
		if c.OIDCClientID == "" || c.OIDCClientSecret == "" {
			bad("BIGLAW_OIDC_ISSUER", "is set, so BIGLAW_OIDC_CLIENT_ID and BIGLAW_OIDC_CLIENT_SECRET are required")
		}
	case c.OIDCClientID != "" || c.OIDCClientSecret != "":
		bad("BIGLAW_OIDC_CLIENT_ID", "an OIDC client is set but BIGLAW_OIDC_ISSUER is not")
	}

	if err := errors.Join(errs...); err != nil {
		return c, err
	}
	return c, GuardVendors(c)
}

// GuardVendors is the self-imposed vendor breaker ported from v1
// (biglaw-go/internal/config/guard.go). It refuses a model endpoint that
// couples BigLaw DIRECTLY to Anthropic's own API unless ALLOW_ANTHROPIC is
// set. It keys on the endpoint, not the model name: a Claude model reached
// through a third-party OpenAI-compatible gateway is allowed. Like v1 it
// matches "anthropic.com" anywhere in the URL, so it fails closed.
func GuardVendors(c Config) error {
	if strings.Contains(strings.ToLower(c.ModelURL), "anthropic.com") && !c.AllowAnthropic {
		return fmt.Errorf("vendor breaker tripped: BIGLAW_MODEL_URL %s couples BigLaw directly to Anthropic, a gated closed vendor; "+
			"set ALLOW_ANTHROPIC=1 to opt in deliberately, or reach the model through a gateway", redactURL(c.ModelURL))
	}
	return nil
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
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "BIGLAW_") && !known[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Describe renders the resolved configuration for `biglaw version --config`.
// Secrets print only as set/unset; the database password is masked.
func Describe(getenv func(string) string) string {
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
			val = redactDSN(val)
		case e.secret:
			val = "(set, hidden)"
		case e.name == "BIGLAW_MODEL_URL":
			val = redactURL(val)
		}
		fmt.Fprintf(&b, "%-26s %s", e.name, val)
		if src != "" {
			fmt.Fprintf(&b, "  [%s]", src)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func number(get func(string) string, name string, lo, hi float64, bad func(string, string, ...any)) float64 {
	v := get(name)
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || f < lo || f > hi {
		bad(name, "%q must be a number from %g to %g", v, lo, hi)
		return 0
	}
	return f
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 0 && n <= 65535
}

func checkURL(raw string, allowPath bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q must be an http(s) URL with a host", redactURL(raw))
	}
	if u.User != nil {
		return fmt.Errorf("must not carry credentials in the URL")
	}
	if !allowPath && (u.RawQuery != "" || u.Fragment != "") {
		return fmt.Errorf("%q must not have a query or fragment", raw)
	}
	return nil
}

var tenantRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)

// checkIssuer accepts the two presets or an https issuer URL. Resolving and
// discovering the issuer is the OIDC flow's job (PR 8).
func checkIssuer(v string) error {
	switch {
	case v == "google":
		return nil
	case strings.HasPrefix(v, "microsoft:"):
		if t := strings.TrimPrefix(v, "microsoft:"); !tenantRe.MatchString(t) {
			return fmt.Errorf("%q: write microsoft:<tenant-id>", v)
		}
		return nil
	}
	if u, err := url.Parse(v); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%q must be google, microsoft:<tenant-id> or an https issuer URL", v)
	}
	return nil
}

// redactURL hides a password in userinfo and every query value (some
// gateways take the key as ?key=…).
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable URL)"
	}
	q := u.Query()
	for k := range q {
		q.Set(k, "xxxxx")
	}
	u.RawQuery = q.Encode()
	return u.Redacted()
}

var kvPassword = regexp.MustCompile(`(?i)(password\s*=\s*)('(?:[^'\\]|\\.)*'|\S+)`)

func redactDSN(dsn string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "(unparseable DSN)"
		}
		if q := u.Query(); q.Has("password") {
			q.Set("password", "xxxxx")
			u.RawQuery = q.Encode()
		}
		return u.Redacted()
	}
	return kvPassword.ReplaceAllString(dsn, "${1}xxxxx")
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
