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
	// The README § Configuration defaults.
	if c.Addr != "127.0.0.1:3101" || c.PublicURL != "http://127.0.0.1:3101" ||
		c.BudgetCalls != 150 || c.BudgetUSD != 5 || c.LocalWatts != 250 ||
		c.CollapseThreshold != 0.5 || c.CollapseAction != "strict" ||
		c.AIRate != nil || c.AllowAnthropic ||
		c.ModelURL != "" || c.OIDCIssuer != "" || c.Prices != "" {
		t.Fatalf("defaults differ from README: %+v", c)
	}
	if len(envVars) != 19 {
		t.Fatalf("%d runtime variables, README promises 19", len(envVars))
	}

	// A missing database URL refuses to start, with a clear error.
	_, err = Load(env())
	if err == nil || !strings.Contains(err.Error(), "BIGLAW_DATABASE_URL: is required") {
		t.Fatalf("empty env: err = %v", err)
	}
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"serve"}, env(), nil, &out, &errb); code != 2 ||
		!strings.Contains(errb.String(), "refusing to start") || !strings.Contains(errb.String(), "BIGLAW_DATABASE_URL") {
		t.Fatalf("serve with empty env: code %d, stderr %q", code, errb.String())
	}
}

func TestConfigRolesFallBackToLead(t *testing.T) {
	c, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_LEAD", "qwen2.5:14b", "BIGLAW_MODEL_CRITIC", "glm-4.5-air"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ModelSpecialist != "qwen2.5:14b" || c.ModelCritic != "glm-4.5-air" {
		t.Fatalf("roles: %+v", c)
	}
	c, err = Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_AI_RATE", "120",
		"BIGLAW_PUBLIC_URL", "https://law.example.com/", "BIGLAW_COLLAPSE_ACTION", "WARN",
		"BIGLAW_OIDC_ISSUER", "microsoft:0b1c-22", "BIGLAW_OIDC_CLIENT_ID", "id", "BIGLAW_OIDC_CLIENT_SECRET", "sec"))
	if err != nil {
		t.Fatal(err)
	}
	if c.AIRate == nil || *c.AIRate != 120 || c.PublicURL != "https://law.example.com" || c.CollapseAction != "warn" {
		t.Fatalf("parsed: %+v", c)
	}
}

func TestConfigRejectsBadValues(t *testing.T) {
	cases := map[string][]string{
		"BIGLAW_ADDR":               {"3101", ":3101", "localhost:99999", "h:p"},
		"BIGLAW_PUBLIC_URL":         {"ftp://x", "law.example.com", "https://x/?a=b", "https://u:p@x"},
		"BIGLAW_MODEL_URL":          {"localhost:11434", "file:///etc/passwd", "http://user:pw@h/v1"},
		"BIGLAW_BUDGET_CALLS":       {"0", "-1", "1.5", "abc", "100001"},
		"BIGLAW_BUDGET_USD":         {"-1", "NaN", "Inf", "five"},
		"BIGLAW_LOCAL_WATTS":        {"-5", "NaN"},
		"BIGLAW_COLLAPSE_THRESHOLD": {"0", "1.5", "-0.1", "NaN"},
		"BIGLAW_COLLAPSE_ACTION":    {"ignore", "off"},
		"BIGLAW_AI_RATE":            {"-1", "Inf", "x"},
		"BIGLAW_OIDC_ISSUER":        {"http://idp.example.com", "microsoft:", "microsoft:../x", "okta"},
	}
	for name, values := range cases {
		for _, v := range values {
			e := env("BIGLAW_DATABASE_URL", dsn, name, v, "BIGLAW_OIDC_CLIENT_ID", "id", "BIGLAW_OIDC_CLIENT_SECRET", "s",
				"BIGLAW_OIDC_ISSUER", map[bool]string{true: v, false: "google"}[name == "BIGLAW_OIDC_ISSUER"])
			if _, err := Load(e); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s=%q: err = %v, want an error naming %s", name, v, err, name)
			}
		}
	}
	// An OIDC issuer without a client, and a client without an issuer.
	if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_OIDC_ISSUER", "google")); err == nil {
		t.Error("issuer without client accepted")
	}
	if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_OIDC_CLIENT_ID", "id")); err == nil {
		t.Error("client without issuer accepted")
	}
	// Every problem is reported at once.
	_, err := Load(env("BIGLAW_BUDGET_CALLS", "0", "BIGLAW_COLLAPSE_ACTION", "x"))
	for _, want := range []string{"BIGLAW_DATABASE_URL", "BIGLAW_BUDGET_CALLS", "BIGLAW_COLLAPSE_ACTION"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("combined error %v lacks %s", err, want)
		}
	}
}

func TestVendorBreakerRefusesDirectAnthropic(t *testing.T) {
	for _, u := range []string{"https://api.anthropic.com/v1", "https://API.ANTHROPIC.COM/v1/", "https://eu.api.anthropic.com"} {
		_, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", u))
		if err == nil || !strings.Contains(err.Error(), "vendor breaker") || !strings.Contains(err.Error(), "ALLOW_ANTHROPIC") {
			t.Errorf("%s without opt-in: err = %v", u, err)
		}
		for _, opt := range []string{"1", "true", "YES"} {
			if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", u, "ALLOW_ANTHROPIC", opt)); err != nil {
				t.Errorf("%s with ALLOW_ANTHROPIC=%s: %v", u, opt, err)
			}
		}
		if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", u, "ALLOW_ANTHROPIC", "0")); err == nil {
			t.Errorf("%s with ALLOW_ANTHROPIC=0 accepted", u)
		}
	}
	// A Claude model behind a third-party gateway is not a direct coupling.
	if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "https://openrouter.ai/api/v1", "BIGLAW_MODEL_LEAD", "anthropic/claude-haiku-4.5")); err != nil {
		t.Errorf("gateway refused: %v", err)
	}
}

func TestDescribeRedactsSecrets(t *testing.T) {
	e := env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_KEY", "sk-live-abc123", "BIGLAW_OIDC_CLIENT_SECRET", "oidc-shh",
		"BIGLAW_MODEL_URL", "https://gw.example/v1?key=gwkey999")
	out := Describe(e)
	for _, secret := range []string{"s3cret-pw", "sk-live-abc123", "oidc-shh", "gwkey999"} {
		if strings.Contains(out, secret) {
			t.Errorf("Describe leaked %q:\n%s", secret, out)
		}
	}
	for _, want := range []string{"BIGLAW_ADDR                127.0.0.1:3101  [default]", "BIGLAW_MODEL_KEY           (set, hidden)  [env]", "db:5432/biglaw"} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe lacks %q:\n%s", want, out)
		}
	}
	if got := redactDSN("host=db user=biglaw password='p w\\'d' dbname=x"); strings.Contains(got, "p w") {
		t.Errorf("key=value DSN leaked: %s", got)
	}
	if got := redactDSN("postgres://db/x?password=qp"); strings.Contains(got, "qp") {
		t.Errorf("query password leaked: %s", got)
	}
	// The breaker message does not echo a key carried in the URL.
	_, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "https://api.anthropic.com/v1?key=sk-ant-zzz"))
	if err == nil || strings.Contains(err.Error(), "sk-ant-zzz") {
		t.Errorf("breaker error leaked the key: %v", err)
	}
}

func TestUnknownVars(t *testing.T) {
	got := UnknownVars([]string{"BIGLAW_DATABASE_URI=x", "BIGLAW_ADDR=y", "PATH=/bin", "BIGLAW_TEST_DSN=z", "BIGLAW_MODEL_LAED=q"})
	if strings.Join(got, ",") != "BIGLAW_DATABASE_URI,BIGLAW_MODEL_LAED" {
		t.Fatalf("UnknownVars = %v", got)
	}
}

func TestConfigDatabaseURLNotEchoed(t *testing.T) {
	_, err := Load(env("BIGLAW_DATABASE_URL", "postgres://u:SECRETPW@h:badport/db"))
	if err == nil || strings.Contains(err.Error(), "SECRETPW") || !strings.Contains(err.Error(), "BIGLAW_DATABASE_URL") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Load(env("BIGLAW_DATABASE_URL", "mysql://u:p@h/db")); err == nil {
		t.Fatal("non-postgres URL accepted")
	}
	if _, err := Load(env("BIGLAW_DATABASE_URL", "host=h dbname=d sslmode=disable")); err != nil {
		t.Fatalf("key=value DSN refused: %v", err)
	}
}
