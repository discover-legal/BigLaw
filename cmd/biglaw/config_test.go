// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

const dsn = "postgres://biglaw:s3cret-pw@db:5432/biglaw?sslmode=disable"

func TestConfigDefaults(t *testing.T) {
	c, err := Load(env("BIGLAW_DATABASE_URL", dsn))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:3101" || c.ModelURL != "" || c.AllowAnthropic || c.AllowAWS {
		t.Fatalf("defaults differ from README: %+v", c)
	}
	if len(envVars) != 20 {
		t.Fatalf("%d runtime variables, README promises 20", len(envVars))
	}
	// Variables read by later PRs still show their README defaults.
	out := Describe(env("BIGLAW_DATABASE_URL", dsn), nil)
	for _, want := range []string{
		"BIGLAW_ADDR                127.0.0.1:3101  [default]",
		"BIGLAW_PUBLIC_URL          http://127.0.0.1:3101  [default, read from PR 8]",
		"BIGLAW_BUDGET_CALLS        150  [default, read from PR 4]",
		"BIGLAW_BUDGET_USD          5  [default, read from PR 4]",
		"BIGLAW_LOCAL_WATTS         250  [default, read from PR 2]",
		"BIGLAW_COLLAPSE_THRESHOLD  0.5  [default, read from PR 4]",
		"BIGLAW_COLLAPSE_ACTION     strict  [default, read from PR 4]",
		"BIGLAW_MODEL_KEY           (unset)  [read from PR 2]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe lacks %q:\n%s", want, out)
		}
	}

	// A missing database URL refuses to start, with a clear error.
	_, err = Load(env())
	if err == nil || !strings.Contains(err.Error(), "BIGLAW_DATABASE_URL: is required") {
		t.Fatalf("empty env: err = %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"serve"}, env(), nil, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "refusing to start") || !strings.Contains(stderr.String(), "BIGLAW_DATABASE_URL") {
		t.Fatalf("serve with empty env: code %d, stderr %q", code, stderr.String())
	}
}

func TestConfigRejectsBadValues(t *testing.T) {
	cases := map[string][]string{
		"BIGLAW_ADDR": {"3101", ":3101", "localhost:99999", "h:p", "127.0.0.1:+3109", "127.0.0.1:-0"},
		"BIGLAW_MODEL_URL": {"localhost:11434", "file:///etc/passwd", "http://user:pw@h/v1", "https://KEY@gw.example/v1",
			"https://api.anthropic\u3002com/v1", "https://api.anthropic%E3%80%82com/v1", "https://api.anthropic%2Ecom/v1"}, // breaker evasions
		"BIGLAW_DATABASE_URL": {"POSTGRES://u:p@h/db", "mysql://u:p@h/db", "postgres://u:p/w@h/db"},
	}
	for name, values := range cases {
		for _, v := range values {
			e := map[string]string{"BIGLAW_DATABASE_URL": dsn, name: v}
			if _, err := Load(func(k string) string { return e[k] }); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s=%q: err = %v, want an error naming %s", name, v, err, name)
			}
		}
	}
	// Every problem is reported at once.
	_, err := Load(env("BIGLAW_ADDR", "x", "BIGLAW_MODEL_URL", "ftp://h"))
	for _, want := range []string{"BIGLAW_DATABASE_URL", "BIGLAW_ADDR", "BIGLAW_MODEL_URL"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("combined error %v lacks %s", err, want)
		}
	}
	// Variables a later PR reads are not validated yet (that PR validates them).
	if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_BUDGET_CALLS", "abc", "BIGLAW_OIDC_ISSUER", "okta")); err != nil {
		t.Errorf("a variable this build does not read was validated: %v", err)
	}
}

func TestVendorBreakerRefusesDirectAnthropic(t *testing.T) {
	trip := func(u string, kv ...string) error {
		_, err := Load(env(append([]string{"BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", u}, kv...)...))
		return err
	}
	for _, u := range []string{
		"https://api.anthropic.com/v1", "https://API.ANTHROPIC.COM/v1/", "https://eu.api.anthropic.com",
		"https://api.anthropic.com./v1",
	} {
		if err := trip(u); err == nil || !strings.Contains(err.Error(), "vendor breaker") || !strings.Contains(err.Error(), "ALLOW_ANTHROPIC") {
			t.Errorf("%s without opt-in: err = %v", u, err)
		}
		for _, opt := range []string{"1", "true", "YES"} {
			if err := trip(u, "ALLOW_ANTHROPIC", opt); err != nil {
				t.Errorf("%s with ALLOW_ANTHROPIC=%s: %v", u, opt, err)
			}
		}
		if err := trip(u, "ALLOW_ANTHROPIC", "0"); err == nil {
			t.Errorf("%s with ALLOW_ANTHROPIC=0 accepted", u)
		}
	}
	// The AWS breaker (v1's ALLOW_AWS) works the same way.
	for _, u := range []string{"https://bedrock-runtime.us-east-1.amazonaws.com", "https://gw.example/BEDROCK/v1"} {
		if err := trip(u); err == nil || !strings.Contains(err.Error(), "ALLOW_AWS") || strings.Contains(err.Error(), "ALLOW_ANTHROPIC") {
			t.Errorf("%s without ALLOW_AWS: err = %v", u, err)
		}
		if err := trip(u, "ALLOW_AWS", "on"); err != nil {
			t.Errorf("%s with ALLOW_AWS: %v", u, err)
		}
		if err := trip(u, "ALLOW_ANTHROPIC", "1"); err == nil {
			t.Errorf("%s: ALLOW_ANTHROPIC disarmed the AWS breaker", u)
		}
	}
	// A URL that couples to both names both.
	if err := trip("https://bedrock.amazonaws.com/anthropic.com"); err == nil ||
		!strings.Contains(err.Error(), "ALLOW_AWS") || !strings.Contains(err.Error(), "ALLOW_ANTHROPIC") {
		t.Errorf("double coupling: err = %v", err)
	}
	// A Claude model behind a third-party gateway is not a direct coupling;
	// neither is an IP literal (the breaker keys on names, see Limitations).
	for _, u := range []string{"https://openrouter.ai/api/v1", "http://192.168.1.20:11434/v1"} {
		if err := trip(u, "BIGLAW_MODEL_LEAD", "anthropic/claude-haiku-4.5"); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	// The breaker message does not echo a key carried in the URL.
	if err := trip("https://api.anthropic.com/v1?key=sk-ant-zzz"); err == nil || strings.Contains(err.Error(), "sk-ant-zzz") {
		t.Errorf("breaker error leaked the key: %v", err)
	}
}

// TestDescribeRedactsSecrets: whatever shape a value has, no secret in it
// reaches `version --config` or a `serve` refusal. Every secret below
// contains "SECRET"; the inputs include the r2 critics' exact repros.
func TestDescribeRedactsSecrets(t *testing.T) {
	const base = "postgres://biglaw:SECRET0@127.0.0.1:1/biglaw?sslmode=disable&connect_timeout=1"
	cases := []struct {
		name string
		kv   []string
	}{
		{"url dsn", nil},
		{"url sslpassword", []string{"BIGLAW_DATABASE_URL", "postgres://u:SECRET1@127.0.0.1:1/db?sslmode=disable&sslpassword=SECRET2"}},
		{"url fragment", []string{"BIGLAW_DATABASE_URL", "postgres://u:SECRET1@127.0.0.1:1/db?sslmode=disable#SECRET3"}},
		{"kv quoted", []string{"BIGLAW_DATABASE_URL", `host=127.0.0.1 port=1 user=biglaw password='SECRET a\'b' dbname=x sslmode=disable`}},
		{"kv escaped space", []string{"BIGLAW_DATABASE_URL", `host=127.0.0.1 port=1 password=SECRET\ PW dbname=x sslmode=disable`}},
		{"kv sslpassword", []string{"BIGLAW_DATABASE_URL", "host=127.0.0.1 port=1 sslpassword=SECRET1 password = SECRET2 sslmode=disable"}},
		{"kv broken", []string{"BIGLAW_DATABASE_URL", "host=db password=SECRET PW dbname=x"}}, // unquoted space: invalid
		// r2 prosecutor F2 / reproducer: lib/pq reads an unquoted value up to white space, quotes included.
		{"kv apostrophe inside", []string{"BIGLAW_DATABASE_URL", "host=127.0.0.1 port=1 user=postgres password=ab'SECRETcd dbname=x sslmode=disable"}},
		{"kv trailing quotes", []string{"BIGLAW_DATABASE_URL", "host=127.0.0.1 port=1 password=SECRET'' dbname=x sslmode=disable"}},
		{"kv escaped quote", []string{"BIGLAW_DATABASE_URL", `host=127.0.0.1 port=1 password=a\'SECRET dbname=x sslmode=disable`}},
		{"kv empty value eats next", []string{"BIGLAW_DATABASE_URL", "host=127.0.0.1 port=1 password= SECRETdb=x sslmode=disable"}}, // pq: password is "SECRETdb=x"
		{"upper scheme", []string{"BIGLAW_DATABASE_URL", "POSTGRES://biglaw:SECRET@127.0.0.1:5433/x"}},
		{"slash in password", []string{"BIGLAW_DATABASE_URL", "postgres://biglaw:SECRET/pw@127.0.0.1/x"}},
		{"slash in pw, @ later", []string{"BIGLAW_DATABASE_URL", "postgres://biglaw:p@ss/SECRET@h/x"}},
		// r2 prosecutor F1: scheme-less and malformed URLs.
		{"model url no scheme", []string{"BIGLAW_MODEL_URL", "user:sk-SECRETKEY1@api.example.com"}},
		{"model url key in query, no scheme", []string{"BIGLAW_MODEL_URL", "api.example.com/v1?key=SECRETKEY2"}},
		{"model url scheme-relative", []string{"BIGLAW_MODEL_URL", "//sk-SECRETKEY3@api.example.com/v1"}},
		{"model url https:/ typo", []string{"BIGLAW_MODEL_URL", "https:/u:SECRETKEY4@api.example.com"}},
		{"model url backslash", []string{"BIGLAW_MODEL_URL", `https://gw.example\SECRET@x/v1`}},
		{"model url userinfo, query, fragment", []string{"BIGLAW_MODEL_URL", "https://SECRET1@gw.example/v1?key=SECRET2#SECRET3"}},
		{"model url bare query key", []string{"BIGLAW_MODEL_URL", "https://gw.example/v1?sk-SECRET"}}, // red team: a query name can be the key
		{"model url key named like a param", []string{"BIGLAW_MODEL_URL", "https://gw.example/v1?SECRET=1&x"}},
		{"breaker with key in query", []string{"BIGLAW_MODEL_URL", "https://api.anthropic.com/v1?key=sk-ant-SECRET"}},
		{"public url no scheme", []string{"BIGLAW_PUBLIC_URL", "admin:SECRETKEY5@biglaw.example.com"}},
		{"public url userinfo", []string{"BIGLAW_PUBLIC_URL", "http://admin:SECRET@h.example"}},
		{"issuer no scheme, query", []string{"BIGLAW_OIDC_ISSUER", "accounts.google.com?client_secret=SECRETKEY6"}},
		{"issuer full", []string{"BIGLAW_OIDC_ISSUER", "https://u:SECRET1@idp.example/?x=SECRET2#SECRET3"}},
		{"issuer opaque", []string{"BIGLAW_OIDC_ISSUER", "microsoft:SECRET@tenant"}},
		{"addr userinfo", []string{"BIGLAW_ADDR", "u:SECRET@h:1"}},
		{"addr userinfo, valid host:port", []string{"BIGLAW_ADDR", "[u:SECRET@h]:1"}},
		{"plain var holding a url", []string{"BIGLAW_PRICES", "https://u:SECRET@prices.example/x.json"}},
		{"secrets", []string{"BIGLAW_MODEL_KEY", "sk-live-SECRET", "BIGLAW_OIDC_CLIENT_SECRET", "oidc-SECRET"}},
	}
	for _, tc := range cases {
		e := env(append([]string{"BIGLAW_DATABASE_URL", base}, tc.kv...)...)
		for _, args := range [][]string{{"version", "--config"}, {"serve"}} {
			var stdout, stderr bytes.Buffer
			run(context.Background(), args, e, []string{"PGPASSWORD=PGSECRET", "PGHOST=db", "PATH=/bin"}, &stdout, &stderr)
			out := strings.ReplaceAll(stdout.String()+stderr.String(), "BIGLAW_OIDC_CLIENT_SECRET", "") // the name is not a secret
			if strings.Contains(out, "SECRET") {
				t.Errorf("%s: biglaw %v leaked a secret:\n%s", tc.name, args, out)
			}
		}
	}
	out := Describe(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_KEY", "k", "BIGLAW_OIDC_ISSUER", "microsoft:contoso.onmicrosoft.com",
		"BIGLAW_MODEL_URL", "https://K@gw.example/v1/chat?key=k&b=2#f", "BIGLAW_PUBLIC_URL", "HTTPS://Biglaw.Example:8443"), []string{"PGPASSWORD=x", "PGHOST=db", "PATH=/bin"})
	for _, want := range []string{
		"BIGLAW_MODEL_KEY           (set, hidden)  [env, read from PR 2]",
		`BIGLAW_DATABASE_URL        host="db" port=5432 user="biglaw" dbname="biglaw" sslmode="disable" password=(set, hidden)  [env]`,
		"BIGLAW_MODEL_URL           https://xxxxx@gw.example/v1/chat?(2 query parameters, not shown)#xxxxx  [env]",
		"BIGLAW_PUBLIC_URL          https://Biglaw.Example:8443  [env, read from PR 8]",
		"BIGLAW_OIDC_ISSUER         microsoft:contoso.onmicrosoft.com  [env, read from PR 8]",
		"lib/pq also reads these libpq variables (values not shown): PGHOST, PGPASSWORD",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe lacks %q:\n%s", want, out)
		}
	}
	if got := shown("BIGLAW_MODEL_LEAD", "x\nBIGLAW_FORGED y"); strings.Contains(got, "\n") { // red team: no forged output lines
		t.Errorf("a newline in a value reached the output: %q", got)
	}
	if got := shown("BIGLAW_DATABASE_URL", "postgres:///x?sslmode=disable&application_name=a&search_path=s"); !strings.Contains(got, "(+1 other parameters, not shown)") {
		t.Errorf("runtime parameters: %s", got)
	}
}

func TestNonLoopbackRefusedWithoutAuth(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:3101", "127.9.9.9:1", "[::1]:3101", "localhost:3101"} {
		if !isLoopback(addr) {
			t.Errorf("isLoopback(%q) = false", addr)
		}
	}
	unreachable := "postgres://u:p@127.0.0.1:1/x?sslmode=disable&connect_timeout=2"
	for _, addr := range []string{"0.0.0.0:3101", "[::]:3101", "192.0.2.10:3101", "myhost:3101"} {
		if isLoopback(addr) {
			t.Errorf("isLoopback(%q) = true", addr)
		}
		e := env("BIGLAW_DATABASE_URL", unreachable, "BIGLAW_ADDR", addr)
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"serve"}, e, nil, &stdout, &stderr); code != 2 ||
			!strings.Contains(stderr.String(), "not a loopback address") || !strings.Contains(stderr.String(), "--allow-no-auth") {
			t.Errorf("serve on %s without --allow-no-auth: code %d, stderr %q", addr, code, stderr.String())
		}
	}
	// With the override it gets past the exposure rule (and fails later, at
	// the unreachable database), warning that there is no authentication.
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"serve", "--allow-no-auth"}, env("BIGLAW_DATABASE_URL", unreachable, "BIGLAW_ADDR", "0.0.0.0:0"), nil, &stdout, &stderr)
	if code != 1 || strings.Contains(stderr.String(), "not a loopback") || !strings.Contains(stderr.String(), "WITHOUT authentication") ||
		!strings.Contains(stderr.String(), "store: connect") {
		t.Errorf("serve --allow-no-auth: code %d, stderr %q", code, stderr.String())
	}
}

func TestUnknownAndNotYetReadVars(t *testing.T) {
	got := UnknownVars([]string{"BIGLAW_DATABASE_URI=x", "BIGLAW_ADDR=y", "PATH=/bin", "BIGLAW_TEST_DSN=z", "BIGLAW_MODEL_LAED=q"})
	if strings.Join(got, ",") != "BIGLAW_DATABASE_URI,BIGLAW_MODEL_LAED" {
		t.Fatalf("UnknownVars = %v", got)
	}
	got = NotYetRead(env("BIGLAW_ADDR", "x", "BIGLAW_MODEL_KEY", "k", "BIGLAW_AI_RATE", "100", "ALLOW_AWS", "1"))
	if strings.Join(got, ",") != "BIGLAW_MODEL_KEY,BIGLAW_AI_RATE" {
		t.Fatalf("NotYetRead = %v", got)
	}
}
