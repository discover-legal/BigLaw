// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"cmp"
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

	"github.com/discover-legal/BigLaw/internal/engine"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
)

// envVar is one runtime environment variable; envVars is the complete surface (README § Configuration,
// loc.py caps it at 20). A variable is declared in the PR that first reads it.
type envVar struct {
	name, def string
	secret    bool // never printed by `biglaw version --config`
	url       bool // printed only as an http(s) URL with its credentials masked
}

var envVars = []envVar{
	{name: "BIGLAW_DATABASE_URL", secret: true}, // password inside; printed redacted
	{name: "BIGLAW_ADDR", def: "127.0.0.1:3101"},
	{name: "BIGLAW_PUBLIC_URL", def: "http://127.0.0.1:3101", url: true},
	{name: "BIGLAW_MODEL_URL", url: true}, // the vendor breaker (PR 1) and the model client (PR 2)
	{name: "BIGLAW_MODEL_KEY", secret: true},
	{name: "BIGLAW_MODELS"}, // role=model id pairs: lead, specialist, critic, vision, embed
	{name: "BIGLAW_BUDGET_CALLS", def: "400"},
	{name: "BIGLAW_BUDGET_USD", def: "5"},
	{name: "BIGLAW_LOCAL_WATTS"},
	{name: "BIGLAW_PRICES"},
	{name: "BIGLAW_COLLAPSE_THRESHOLD", def: "0.5"},
	{name: "BIGLAW_COLLAPSE_ACTION", def: "strict"},
	{name: "BIGLAW_OIDC_ISSUER", url: true}, // the issuer URL exactly as its discovery document states it
	{name: "BIGLAW_OIDC_CLIENT_ID"},
	{name: "BIGLAW_OIDC_CLIENT_SECRET", secret: true},
	{name: "BIGLAW_ADMINS"}, // comma-separated verified emails or subs: sign-in's admins
	{name: "ALLOW_ANTHROPIC"},
	{name: "ALLOW_AWS"},
}

// Config is the configuration this build reads. Variables first read by a
// later PR join it (and its validation) in that PR.
type Config struct {
	DatabaseURL    string
	Addr           string
	ModelURL       string
	ModelKey       string
	Roles          model.Roles // BIGLAW_MODELS
	LocalWatts     float64
	Prices         *model.Prices
	Engine         engine.Config // BIGLAW_BUDGET_CALLS/_USD, BIGLAW_COLLAPSE_THRESHOLD/_ACTION
	AllowAnthropic bool
	AllowAWS       bool
	OIDC           OIDC // sign-in: on when Issuer is set
}

// OIDC is sign-in's configuration (internal/auth): BIGLAW_OIDC_*, BIGLAW_PUBLIC_URL and BIGLAW_ADMINS.
type OIDC struct {
	Issuer, ClientID, ClientSecret, PublicURL string
	Admins                                    []string
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
		bad("BIGLAW_MODEL_URL", "is required when BIGLAW_MODEL_KEY or BIGLAW_MODELS is set (the key and models need an endpoint)")
	}
	if strings.ContainsFunc(c.ModelKey, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		bad("BIGLAW_MODEL_KEY", "contains white space or a control character (value not shown)")
	}
	w, err := strconv.ParseFloat(cmp.Or(get("BIGLAW_LOCAL_WATTS"), "0"), 64)
	if c.LocalWatts = w; err != nil || !(w >= 0 && w <= 100000) {
		bad("BIGLAW_LOCAL_WATTS", "%s is not a number of watts from 0 to 100000", shown("BIGLAW_LOCAL_WATTS", get("BIGLAW_LOCAL_WATTS")))
	}
	if c.Prices, err = model.LoadPrices(get("BIGLAW_PRICES")); err != nil {
		bad("BIGLAW_PRICES", "%v", err)
	}
	c.Engine.BudgetCalls, err = strconv.Atoi(get("BIGLAW_BUDGET_CALLS"))
	if err != nil || c.Engine.BudgetCalls < 1 || c.Engine.BudgetCalls > 100000 {
		bad("BIGLAW_BUDGET_CALLS", "%s is not a whole number of model calls from 1 to 100000", shown("BIGLAW_BUDGET_CALLS", get("BIGLAW_BUDGET_CALLS")))
	}
	c.Engine.BudgetUSD, err = strconv.ParseFloat(get("BIGLAW_BUDGET_USD"), 64)
	if !plainDecimal.MatchString(get("BIGLAW_BUDGET_USD")) || err != nil || !(c.Engine.BudgetUSD > 0 && c.Engine.BudgetUSD <= 1e6) {
		bad("BIGLAW_BUDGET_USD", "%s is not a dollar amount above 0 (a plain decimal such as 5 or 2.50, at most 1000000; $0 would fail every task)", shown("BIGLAW_BUDGET_USD", get("BIGLAW_BUDGET_USD")))
	}
	c.Engine.CollapseThreshold, err = strconv.ParseFloat(get("BIGLAW_COLLAPSE_THRESHOLD"), 64)
	if !plainDecimal.MatchString(get("BIGLAW_COLLAPSE_THRESHOLD")) || err != nil || !(c.Engine.CollapseThreshold >= 0.01 && c.Engine.CollapseThreshold <= 1) {
		bad("BIGLAW_COLLAPSE_THRESHOLD", "%s is not a share of unverified findings from 0.01 to 1 (default 0.5)", shown("BIGLAW_COLLAPSE_THRESHOLD", get("BIGLAW_COLLAPSE_THRESHOLD")))
	}
	if c.Engine.CollapseAction = get("BIGLAW_COLLAPSE_ACTION"); c.Engine.CollapseAction != "strict" && c.Engine.CollapseAction != "warn" {
		bad("BIGLAW_COLLAPSE_ACTION", "%s is not strict (the task fails) or warn (it goes on, alerted)", shown("BIGLAW_COLLAPSE_ACTION", c.Engine.CollapseAction))
	}
	var oidcErrs []error
	c.OIDC, oidcErrs = signIn(get)
	errs = append(errs, oidcErrs...)
	if err := errors.Join(errs...); err != nil {
		return c, err
	}
	return c, GuardVendors(c)
}

// signIn reads sign-in's variables (README § Sign-in): the issuer and client id together (the secret
// too for a confidential client), an https issuer and public URL (plain http only on loopback, for
// local work: the session cookie must not cross a network in the clear), and BIGLAW_ADMINS only with
// sign-in, as comma-separated emails or subjects without white space.
func signIn(get func(string) string) (OIDC, []error) {
	o := OIDC{Issuer: get("BIGLAW_OIDC_ISSUER"), ClientID: get("BIGLAW_OIDC_CLIENT_ID"), ClientSecret: get("BIGLAW_OIDC_CLIENT_SECRET"), PublicURL: get("BIGLAW_PUBLIC_URL")}
	var errs []error
	bad := func(name, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{name}, a...)...))
	}
	plain := func(v string) bool {
		return !strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsGraphic(r) })
	}
	secure := func(u *url.URL) bool {
		return u.Scheme == "https" || isLoopback(net.JoinHostPort(strings.Trim(u.Hostname(), "[]"), "1"))
	}
	if o.Issuer != "" || o.ClientID != "" || o.ClientSecret != "" {
		if o.Issuer == "" || o.ClientID == "" {
			bad("BIGLAW_OIDC_ISSUER", "and BIGLAW_OIDC_CLIENT_ID are both needed for sign-in (and BIGLAW_OIDC_CLIENT_SECRET for a confidential client); set them all, or none")
		}
		if u := httpURL(o.Issuer); o.Issuer != "" && (u == nil || u.RawQuery != "" || u.Fragment != "" || !secure(u)) {
			bad("BIGLAW_OIDC_ISSUER", "%s must be the issuer's https URL as its discovery document states it, with no query (plain http only on loopback)", shown("BIGLAW_OIDC_ISSUER", o.Issuer))
		}
		if u := httpURL(o.PublicURL); u == nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || !secure(u) {
			bad("BIGLAW_PUBLIC_URL", "%s must be https://host[:port], how browsers reach BigLaw, with no path, when sign-in is on (plain http only on loopback: the session cookie must not travel in the clear)", shown("BIGLAW_PUBLIC_URL", o.PublicURL))
		}
		if !plain(o.ClientID) || len(o.ClientID) > 255 {
			bad("BIGLAW_OIDC_CLIENT_ID", "has white space or a control character, or is over 255 bytes")
		}
		if !plain(o.ClientSecret) {
			bad("BIGLAW_OIDC_CLIENT_SECRET", "has white space or a control character (value not shown)")
		}
	}
	if v := get("BIGLAW_ADMINS"); v != "" {
		if o.Issuer == "" {
			bad("BIGLAW_ADMINS", "needs sign-in (BIGLAW_OIDC_ISSUER): without it no request carries a user to name")
		}
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a == "" {
				continue
			} else if !plain(a) || len(a) > 255 || strings.Contains(a, "@") && strings.ContainsFunc(a, func(r rune) bool { return r > 0x7e }) {
				bad("BIGLAW_ADMINS", "entry %d is not an email or a subject (white space, a control character, over 255 bytes, or an email that is not plain ASCII)", len(o.Admins)+1)
			} else {
				o.Admins = append(o.Admins, a)
			}
		}
	}
	return o, errs
}

// plainDecimal is a plain decimal number: no sign, exponent, hex or infinity.
var plainDecimal = regexp.MustCompile(`^\d+(\.\d+)?$`)

var roleNames = []model.Role{model.Lead, model.Specialist, model.Critic, model.Vision, model.Embed}

// roles parses BIGLAW_MODELS: comma-separated role=id entries, each role at most once. An unset
// specialist or critic uses the lead's, so neither may be set without it; vision and embed have no
// fallback. Ids are otherwise unchecked: a wrong one fails at its first call (404).
func roles(get func(string) string) (model.Roles, []error) {
	ids := map[model.Role]string{}
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf("BIGLAW_MODELS: "+format, a...)) }
	if v := get("BIGLAW_MODELS"); v != "" {
		for i, entry := range strings.Split(v, ",") {
			role, id, _ := strings.Cut(entry, "=")
			r, id := model.Role(strings.TrimSpace(role)), strings.TrimSpace(id)
			switch _, dup := ids[r]; {
			case !slices.Contains(roleNames, r): // the text is not echoed: it may be anything
				bad("entry %d is not role=<model id> with a role of lead, specialist, critic, vision or embed", i+1)
			case dup:
				bad("the %s role is set twice", r)
			case id == "" || len(id) > 200 || strings.Contains(id, "://") || userinfoShape.MatchString(id) || keyShape.MatchString(id) || strings.ContainsAny(id, "?=&") ||
				strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsGraphic(r) }): // ids reach logs raw (PR 2 L-1)
				bad("the %s id is empty or not a model id (a URL, user:pw@, a key's shape, over 200 bytes, white space, ? = & or an invisible character: value not shown)", r)
			default:
				ids[r] = id
			}
		}
	}
	rs := model.Roles{Lead: ids[model.Lead], Specialist: ids[model.Specialist], Critic: ids[model.Critic], Vision: ids[model.Vision], Embed: ids[model.Embed]}
	if rs.Lead == "" && (rs.Specialist != "" || rs.Critic != "") {
		bad("lead is required when specialist or critic is set (unset roles fall back to the lead)")
	}
	return rs, errs
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
	case name == "BIGLAW_MODELS":
		if _, errs := roles(func(string) string { return val }); errs != nil {
			return "(set, not shown: not lead=<model id>,… with safe ids)"
		}
		return val
	case e.url || strings.Contains(val, "://"):
		if u := httpURL(val); u != nil {
			return u.Scheme + "://" + u.Host
		}
		return "(set, not shown: not an http(s) URL with a plain host and no @)"
	case userinfoShape.MatchString(val):
		return "(set, not shown: it looks like user:password@host)"
	case strings.ContainsFunc(val, func(r rune) bool { return !unicode.IsPrint(r) }):
		return strconv.Quote(val) // a newline or a bidi override must not forge or disguise output
	}
	return val
}

var (
	userinfoShape = regexp.MustCompile(`:[^@]*@`)
	keyShape      = regexp.MustCompile(`^(sk-|sk_|AKIA|ASIA|AIza|xox[abpr]-|gh[pousr]_)|[A-Za-z0-9]{24}`) // a pasted API key
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
