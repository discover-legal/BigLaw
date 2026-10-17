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

func TestDescribeRedactsSecrets(t *testing.T) {
	secrets := []string{"s3cret-pw", "sk-live-abc123", "oidc-shh", "gwkey999", "SECRETKEY", "PUBSECRET", "ISSPW", "QSECRET", "FRAGSECRET", "SSLPW", "PGSECRET"}
	cases := []struct{ name, dsn string }{
		{"url", dsn},
		{"url sslpassword", "postgres://u:s3cret-pw@h/db?sslmode=disable&sslpassword=SSLPW"},
		{"url fragment", "postgres://u:s3cret-pw@h/db#FRAGSECRET"},
		{"kv quoted", `host=db user=biglaw password='s3cret-pw a\'b' dbname=x`},
		{"kv escaped space", `host=db password=s3cret-pw\ PUBSECRET dbname=x`},
		{"kv sslpassword", "host=db sslpassword=SSLPW password = s3cret-pw"},
		{"kv broken", "host=db password=s3cret-pw PUBSECRET dbname=x"},   // unquoted space: invalid
		{"upper scheme", "POSTGRES://biglaw:s3cret-pw@127.0.0.1:5433/x"}, // lib/pq would misread it
		{"slash in password", "postgres://biglaw:s3cret/pw@127.0.0.1/x"}, // would leak to DNS
		{"slash in pw, @ later", "postgres://biglaw:p@ss/s3cret-pw@h/x"}, // host "ss" in Go's reading
	}
	for _, tc := range cases {
		e := env("BIGLAW_DATABASE_URL", tc.dsn, "BIGLAW_MODEL_KEY", "sk-live-abc123", "BIGLAW_OIDC_CLIENT_SECRET", "oidc-shh",
			"BIGLAW_MODEL_URL", "https://SECRETKEY@gw.example/v1?key=gwkey999",
			"BIGLAW_PUBLIC_URL", "http://admin:PUBSECRET@h.example",
			"BIGLAW_OIDC_ISSUER", "https://u:ISSPW@idp.example/?x=QSECRET#FRAGSECRET")
		out := Describe(e, []string{"PGPASSWORD=PGSECRET", "PGHOST=db", "PATH=/bin"})
		var stdout, stderr bytes.Buffer
		run(context.Background(), []string{"version", "--config"}, e, nil, &stdout, &stderr)
		for _, secret := range secrets {
			if strings.Contains(out, secret) || strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Errorf("%s: version --config leaked %q:\n%s%s", tc.name, secret, stdout.String(), stderr.String())
			}
		}
		if !strings.Contains(out, "lib/pq also reads these libpq variables (values not shown): PGHOST, PGPASSWORD") {
			t.Errorf("%s: PG* variables not listed:\n%s", tc.name, out)
		}
	}
	out := Describe(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_KEY", "k"), nil)
	for _, want := range []string{"BIGLAW_MODEL_KEY           (set, hidden)  [env, read from PR 2]", "biglaw:xxxxx@db:5432/biglaw"} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe lacks %q:\n%s", want, out)
		}
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
