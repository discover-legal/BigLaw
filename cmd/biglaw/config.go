// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
)

// builtPR is the last build PR whose variables this build reads.
const builtPR = 2

// envVar is one runtime environment variable; envVars is the complete surface (README § Configuration,
// loc.py caps it at 20). A variable is validated by the PR that first reads it (pr), warned about before.
type envVar struct {
	name, def string
	secret    bool // never printed by `biglaw version --config`
	url       bool // printed only as an http(s) URL with its credentials masked
	pr        int  // the build PR that first reads it
}

var envVars = []envVar{
	{name: "BIGLAW_DATABASE_URL", secret: true, pr: 1}, // password inside; printed redacted
	{name: "BIGLAW_ADDR", def: "127.0.0.1:3101", pr: 1},
	{name: "BIGLAW_PUBLIC_URL", def: "http://127.0.0.1:3101", url: true, pr: 8},
	{name: "BIGLAW_MODEL_URL", url: true, pr: 1}, // the vendor breaker (PR 1) and the model client (PR 2)
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
	{name: "BIGLAW_OIDC_ISSUER", url: true, pr: 8}, // or a preset: google, microsoft:<tenant>
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
	ModelURL       string
	ModelKey       string
	Roles          model.Roles // read only through roles(), so PR 3's BIGLAW_MODELS is a local change
	LocalWatts     float64
	Prices         *model.Prices
	AllowAnthropic bool
	AllowAWS       bool
}

// Load resolves and validates the configuration from getenv, reporting all problems at once; the
// vendor breaker runs last, so a valid config that couples directly to a gated vendor is refused.
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
		ModelKey:       get("BIGLAW_MODEL_KEY"),
		AllowAnthropic: truthy(get("ALLOW_ANTHROPIC")),
		AllowAWS:       truthy(get("ALLOW_AWS")),
	}
	if c.DatabaseURL == "" {
		bad("BIGLAW_DATABASE_URL", "is required: a Postgres DSN such as postgres://biglaw:PASSWORD@localhost:5432/biglaw?sslmode=disable")
	} else if err := store.CheckDSN(c.DatabaseURL); err != nil {
		bad("BIGLAW_DATABASE_URL", "%v", err)
	}
	if host, port, err := net.SplitHostPort(c.Addr); err != nil || !validPort(port) {
		bad("BIGLAW_ADDR", "%s is not host:port", shown("BIGLAW_ADDR", c.Addr))
	} else if host == "" {
		bad("BIGLAW_ADDR", "%s has no host; write 127.0.0.1:%s (or 0.0.0.0:%s with serve --allow-no-auth, see README § Exposure)", c.Addr, port, port)
	}
	if c.ModelURL != "" && httpURL(c.ModelURL) == nil {
		bad("BIGLAW_MODEL_URL", "%s must be an http(s) URL with a plain ASCII host (an internationalised name in its xn-- form), a port up to 65535 and no \"@\": "+
			"put the key in BIGLAW_MODEL_KEY, not in the URL, and write any other @ as %%40", shown("BIGLAW_MODEL_URL", c.ModelURL))
	}
	var roleErrs []error
	c.Roles, roleErrs = roles(get)
	errs = append(errs, roleErrs...)
	if c.ModelURL == "" && (c.ModelKey != "" || c.Roles != (model.Roles{})) {
		bad("BIGLAW_MODEL_URL", "is required when BIGLAW_MODEL_KEY or a BIGLAW_MODEL_* role is set (the key and models need an endpoint)")
	}
	if strings.ContainsFunc(c.ModelKey, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		bad("BIGLAW_MODEL_KEY", "contains white space or a control character (value not shown)")
	}
	w, err := strconv.ParseFloat(get("BIGLAW_LOCAL_WATTS"), 64)
	if c.LocalWatts = w; err != nil || !(w >= 0 && w <= 100000) {
		bad("BIGLAW_LOCAL_WATTS", "%s is not a number of watts from 0 to 100000", shown("BIGLAW_LOCAL_WATTS", get("BIGLAW_LOCAL_WATTS")))
	}
	if c.Prices, err = model.LoadPrices(get("BIGLAW_PRICES")); err != nil {
		bad("BIGLAW_PRICES", "%v", err)
	}
	if err := errors.Join(errs...); err != nil {
		return c, err
	}
	return c, GuardVendors(c)
}

// roles reads the model id per role: an unset specialist or critic uses the lead's, so neither may
// be set without it. Ids are not checked here: a wrong one fails at its first call (404).
func roles(get func(string) string) (model.Roles, []error) {
	r := model.Roles{Lead: get("BIGLAW_MODEL_LEAD"), Specialist: get("BIGLAW_MODEL_SPECIALIST"), Critic: get("BIGLAW_MODEL_CRITIC")}
	if r.Lead == "" && (r.Specialist != "" || r.Critic != "") {
		return r, []error{errors.New("BIGLAW_MODEL_LEAD: is required when BIGLAW_MODEL_SPECIALIST or _CRITIC is set (unset roles fall back to the lead)")}
	}
	return r, nil
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

// vendors is v1's self-imposed vendor breaker: endpoint markers (anywhere in the URL: fails closed)
// coupling BigLaw DIRECTLY to a gated vendor, and the opt-in; a gateway in between is allowed.
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
	allowed := map[string]bool{"ALLOW_ANTHROPIC": c.AllowAnthropic, "ALLOW_AWS": c.AllowAWS}
	var errs []error
	for _, v := range vendors {
		for _, m := range v.markers {
			if strings.Contains(haystack, m) && !allowed[v.optIn] {
				errs = append(errs, fmt.Errorf("vendor breaker tripped: BIGLAW_MODEL_URL %s couples BigLaw directly to %s, a gated closed vendor; "+
					"set %s=1 to opt in deliberately, or reach the model through a gateway", shown("BIGLAW_MODEL_URL", c.ModelURL), v.name, v.optIn))
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

// NotYetRead lists declared variables that are set but first read by a later PR (warned about).
func NotYetRead(getenv func(string) string) []string {
	var out []string
	for _, e := range envVars {
		if e.pr > builtPR && strings.TrimSpace(getenv(e.name)) != "" {
			out = append(out, e.name)
		}
	}
	return out
}

// Describe renders the configuration for `biglaw version --config`: every value through shown, and
// the PG* variables lib/pq also reads by name only.
func Describe(getenv func(string) string, environ []string) string {
	var b strings.Builder
	for _, e := range envVars {
		raw := strings.TrimSpace(getenv(e.name))
		val, src := raw, "env"
		if raw == "" {
			val, src = e.def, "default"
		}
		if val == "" {
			val, src = "(unset)", ""
		} else {
			val = shown(e.name, val)
		}
		if e.pr > builtPR {
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

// shown is the only form in which a config value is output. Total rule: secrets hidden, the DSN via
// store.DescribeDSN, URLs as scheme://host only if httpURL accepts them, anything URL- or user:pw@-shaped hidden.
func shown(name, val string) string {
	e := envVars[slices.IndexFunc(envVars, func(e envVar) bool { return e.name == name })]
	switch {
	case e.secret && name != "BIGLAW_DATABASE_URL":
		return "(set, hidden)"
	case name == "BIGLAW_DATABASE_URL":
		return store.DescribeDSN(val)
	case name == "BIGLAW_OIDC_ISSUER" && oidcPreset.MatchString(val):
		return val
	case e.url || strings.Contains(val, "://"):
		if u := httpURL(val); u != nil {
			return u.Scheme + "://" + u.Host
		}
		return "(set, not shown: not an http(s) URL with a plain host and no @)"
	case userinfoShape.MatchString(val):
		return "(set, not shown: it looks like user:password@host)"
	case strings.ContainsFunc(val, unicode.IsControl):
		return strconv.Quote(val) // a newline must not forge a line of output
	}
	return val
}

var (
	oidcPreset    = regexp.MustCompile(`^(google|microsoft:[A-Za-z0-9.-]+)$`)
	userinfoShape = regexp.MustCompile(`:[^@]*@`)
	plainHost     = regexp.MustCompile(`^([A-Za-z0-9._-]+|\[[0-9A-Fa-f:.]+\])(:[0-9]+)?$`)
)

// httpURL parses raw if it is an absolute http(s) URL with a plain ASCII host (no zone, no escape) and no
// "@" anywhere (an "@" after an unescaped / ? # would hide credentials in the path); else nil.
func httpURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || !plainHost.MatchString(u.Host) || strings.Contains(raw, "@") ||
		!strings.ContainsFunc(u.Hostname(), func(r rune) bool { return r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) }) ||
		u.Port() != "" && !validPort(u.Port()) {
		return nil
	}
	return u
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
