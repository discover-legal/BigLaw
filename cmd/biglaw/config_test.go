// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
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
	if len(envVars) != 18 {
		t.Fatalf("%d runtime variables, README promises 18 (BIGLAW_MODELS replaced three; PR 8 added BIGLAW_ADMINS)", len(envVars))
	}
	// Variables read by later PRs still show their README defaults.
	out := Describe(env("BIGLAW_DATABASE_URL", dsn), nil)
	for _, want := range []string{
		"BIGLAW_ADDR                127.0.0.1:3101  [default]",
		"BIGLAW_PUBLIC_URL          http://127.0.0.1:3101  [default]",
		"BIGLAW_BUDGET_CALLS        400  [default]",
		"BIGLAW_BUDGET_USD          5  [default]",
		"BIGLAW_LOCAL_WATTS         (unset)\n",
		"BIGLAW_COLLAPSE_THRESHOLD  0.5  [default]",
		"BIGLAW_COLLAPSE_ACTION     strict  [default]",
		"BIGLAW_MODEL_KEY           (unset)\n",
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
			"https://api.anthropic\u3002com/v1", "https://api.anthropic%E3%80%82com/v1", "https://api.anthropic%2Ecom/v1", // breaker evasions
			"https://Zm9vYmFy/c2VjcmV0S0VZ+abc==@gw.example.com/v1", "https://user:/pw@gw.example.com/v1", "https://user:1234/rest@gw.example.com/v1", // r3 P1
			"http://[::1%25zone]:80/", "https://gw.example/v1/a@b",
			"https://h.com:99999", "https://.", "https://..:80/v1", "http://-_/v1"}, // r4 L1: port range, a host with no letter or digit
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
	// Ordinary endpoints stay accepted (a Compose service name may hold "_").
	for _, u := range []string{"http://ollama_1:11434/v1", "http://[::1]:11434", "https://GW.example.:8443/v1?x=1"} {
		if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", u)); err != nil {
			t.Errorf("BIGLAW_MODEL_URL=%q refused: %v", u, err)
		}
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
		if err := trip(u, "BIGLAW_MODELS", "lead=anthropic/claude-haiku-4.5"); err != nil {
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
		// r3 prosecutor P1: an unescaped "/" (or "#") in user info makes Go read the credential as host + path.
		{"model url slash in base64 key", []string{"BIGLAW_MODEL_URL", "https://Zm9vYmFy/c2VjcmV0S0VZ+SECRET==@gw.example.com/v1"}},
		{"model url empty user, slash pw", []string{"BIGLAW_MODEL_URL", "https://user:/SECRETpw@gw.example.com/v1"}},
		{"model url numeric pw prefix", []string{"BIGLAW_MODEL_URL", "https://user:1234/SECRETrest@gw.example.com/v1"}},
		{"public url numeric pw prefix", []string{"BIGLAW_PUBLIC_URL", "https://user:1234/SECRETrest@gw.example.com/v1"}},
		{"model url hash in pw", []string{"BIGLAW_MODEL_URL", "https://user:123456#SECRET@gw.example.com"}},
		{"model url key, slash, @", []string{"BIGLAW_MODEL_URL", "https://SECRETKEY/x@gw.example/"}},
		// r3 auditor B5: path parameters, IPv6 zone ids and spaces are never printed.
		{"model url path param", []string{"BIGLAW_MODEL_URL", "https://gw.example/v1;key=SECRET"}},
		{"model url zone id", []string{"BIGLAW_MODEL_URL", "http://[::1%25SECRET]:80/"}},
		{"model url space", []string{"BIGLAW_MODEL_URL", "https://gw.example/v1 SECRET"}},
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
	out := Describe(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_KEY", "k", "BIGLAW_OIDC_ISSUER", "https://idp.example/realms/firm?x=SECRET",
		"BIGLAW_MODEL_URL", "https://gw.example/v1/chat?key=k&b=2#f", "BIGLAW_PUBLIC_URL", "HTTPS://Biglaw.Example:8443",
		"BIGLAW_PRICES", "https://K@prices.example/p.json"), []string{"PGPASSWORD=x", "PGHOST=db", "PATH=/bin"})
	for _, want := range []string{
		"BIGLAW_MODEL_KEY           (set, hidden)  [env]",
		`BIGLAW_DATABASE_URL        host="db" port=5432 user="biglaw" dbname="biglaw" sslmode="disable" password=(set, hidden)  [env]`,
		"BIGLAW_MODEL_URL           https://gw.example  [env]", // host only: never path, query or fragment
		"BIGLAW_PRICES              (set, not shown: not an http(s) URL with a plain host and no @)  [env]",
		"BIGLAW_PUBLIC_URL          https://Biglaw.Example:8443  [env]",
		"BIGLAW_OIDC_ISSUER         https://idp.example  [env]", // host only, as every URL
		"lib/pq also reads these libpq variables (values not shown): PGHOST, PGPASSWORD",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe lacks %q:\n%s", want, out)
		}
	}
	if got := shown("BIGLAW_MODELS", "lead=x\nBIGLAW_FORGED y"); strings.Contains(got, "\n") { // red team: no forged output lines
		t.Errorf("a newline in a value reached the output: %q", got)
	}
	if got := shown("BIGLAW_ADDR", "127.0.0.1:3101\u202e1013:1.0.0.721"); strings.ContainsRune(got, '\u202e') { // r1 L-2: a bidi override is escaped
		t.Errorf("a format character reached the output: %q", got)
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

func TestUnknownVars(t *testing.T) {
	got := UnknownVars([]string{"BIGLAW_DATABASE_URI=x", "BIGLAW_ADDR=y", "PATH=/bin", "BIGLAW_TEST_DSN=z", "BIGLAW_MODEL_LEAD=q"})
	if strings.Join(got, ",") != "BIGLAW_DATABASE_URI,BIGLAW_MODEL_LEAD" {
		t.Fatalf("UnknownVars = %v", got)
	}
}

// TestConfigModelVars: the model variables join Config and are validated, with
// every problem reported at once and no value of a secret in any message.
func TestConfigModelVars(t *testing.T) {
	c, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "http://ollama:11434/v1", "BIGLAW_MODEL_KEY", "sk-k",
		"BIGLAW_MODELS", "lead=qwen2.5:14b,critic=anthropic/claude-haiku-4.5", "BIGLAW_LOCAL_WATTS", "65.5",
		"BIGLAW_PRICES", `{"qwen2.5:14b": {"in": 0.5, "out": 1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.ModelKey != "sk-k" || c.LocalWatts != 65.5 || c.Roles != (model.Roles{Lead: "qwen2.5:14b", Critic: "anthropic/claude-haiku-4.5"}) {
		t.Fatalf("config = %+v", c)
	}
	for role, want := range map[model.Role]string{model.Lead: "qwen2.5:14b", model.Specialist: "qwen2.5:14b", model.Critic: "anthropic/claude-haiku-4.5"} {
		if got, err := c.Roles.Model(role); err != nil || got != want {
			t.Errorf("Roles.Model(%s) = %q, %v; want %q (unset roles fall back to the lead)", role, got, err, want)
		}
	}
	if r, _, op := c.Prices.Lookup("qwen2.5:14b"); r == nil || *r != (model.Rate{In: 0.5, Out: 1}) || !op {
		t.Errorf("BIGLAW_PRICES did not reach Config: %v %v", r, op)
	}
	// Defaults: no model, no watt-hours, the built-in table.
	c, err = Load(env("BIGLAW_DATABASE_URL", dsn))
	if err != nil || c.LocalWatts != 0 || c.Roles != (model.Roles{}) || c.Prices == nil {
		t.Fatalf("defaults: %+v, %v", c, err)
	}
	if _, err := c.Roles.Model(model.Lead); !errors.Is(err, model.ErrNotConfigured) {
		t.Errorf("no lead: %v, want ErrNotConfigured", err)
	}

	bad := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(bad, []byte(`{"gpt-x": {"in": "PRICEFILESECRET"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_KEY", "sk SECRETKEY", "BIGLAW_MODELS", "specialist=a b,critic=x\nBIGLAW_FORGED",
		"BIGLAW_LOCAL_WATTS", "NaN", "BIGLAW_PRICES", bad))
	msg := fmt.Sprint(err)
	for _, want := range []string{
		"BIGLAW_MODEL_URL: is required when BIGLAW_MODEL_KEY",
		"BIGLAW_MODEL_KEY: contains white space",
		"BIGLAW_MODELS: the specialist id is empty or not a model id",
		"BIGLAW_LOCAL_WATTS: NaN is not a number of watts",
		"BIGLAW_MODELS: the critic id is empty or not a model id",
		"BIGLAW_PRICES: the file it names: is not a JSON object",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("combined error lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "SECRET") {
		t.Errorf("an error echoed a secret or the prices file:\n%s", msg)
	}
	// The URL is required by a key alone and by a role alone; any model id is accepted (an LM Studio
	// "@quant" suffix too: a wrong id fails at its first call, as a 404).
	for _, kv := range [][]string{{"BIGLAW_MODEL_KEY", "sk-k"}, {"BIGLAW_MODELS", "lead=m"}} {
		if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, kv[0], kv[1])); err == nil || !strings.Contains(err.Error(), "BIGLAW_MODEL_URL: is required") {
			t.Errorf("%s without BIGLAW_MODEL_URL: %v", kv[0], err)
		}
	}
	if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "http://lmstudio:1234/v1", "BIGLAW_MODELS", "lead=qwen2.5-14b-instruct@q4_k_m")); err != nil {
		t.Errorf("an @quant model id: %v", err)
	}
	// r2 R2-2: role ids reach the logs raw, so a URL or credentials pasted into a role is refused, unechoed.
	for _, v := range []string{"https://alice:Pa55w0rd@gw.example.com/v1", "alice:Pa55w0rd@gw.example.com", "http://Pa55w0rd.example.com"} {
		_, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "http://lmstudio:1234/v1", "BIGLAW_MODELS", "lead=ok,specialist="+v))
		if msg := fmt.Sprint(err); !strings.Contains(msg, "BIGLAW_MODELS: the specialist id is empty or not a model id") || strings.Contains(msg, "Pa55w0rd") {
			t.Errorf("role %q: %v", v, err)
		}
	}
	for _, w := range []string{"-1", "100000.5", "Inf", "1e9", "250W", "0x10"} {
		if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_LOCAL_WATTS", w)); err == nil {
			t.Errorf("BIGLAW_LOCAL_WATTS=%s accepted", w)
		}
	}
	for _, w := range []string{"0", "100000", "1e3"} {
		if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_LOCAL_WATTS", w)); err != nil {
			t.Errorf("BIGLAW_LOCAL_WATTS=%s refused: %v", w, err)
		}
	}
}

// TestModelsRoleMap (PLAN § PR 3b): BIGLAW_MODELS parses role=id pairs; specialist and critic fall
// back to the lead, vision and embed never do; a bad entry is a startup error that echoes no value.
func TestModelsRoleMap(t *testing.T) {
	load := func(v string) (Config, error) {
		return Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "http://ollama:11434/v1", "BIGLAW_MODELS", v))
	}
	c, err := load(" lead = qwen2.5:14b , critic=glm-4.5-air,vision=qwen2.5vl:7b,embed=nomic-embed-text")
	if err != nil {
		t.Fatal(err)
	}
	if want := (model.Roles{Lead: "qwen2.5:14b", Critic: "glm-4.5-air", Vision: "qwen2.5vl:7b", Embed: "nomic-embed-text"}); c.Roles != want {
		t.Fatalf("roles = %+v, want %+v", c.Roles, want)
	}
	for role, want := range map[model.Role]string{model.Lead: "qwen2.5:14b", model.Specialist: "qwen2.5:14b", model.Critic: "glm-4.5-air",
		model.Vision: "qwen2.5vl:7b", model.Embed: "nomic-embed-text"} {
		if got, err := c.Roles.Model(role); err != nil || got != want {
			t.Errorf("Model(%s) = %q, %v; want %q", role, got, err, want)
		}
	}
	// No fallback for vision or embed: a lead alone leaves both unset (images refused, search lexical).
	c, err = load("lead=qwen2.5:14b")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []model.Role{model.Vision, model.Embed} {
		if got, err := c.Roles.Model(role); !errors.Is(err, model.ErrNotConfigured) || got != "" || !strings.Contains(err.Error(), string(role)+"=<model id> in BIGLAW_MODELS") {
			t.Errorf("Model(%s) with only a lead = %q, %v; want ErrNotConfigured naming the variable", role, got, err)
		}
	}
	// embed alone is fine (search only); a specialist or critic without the lead is not.
	if c, err := load("embed=text-embedding-3-small"); err != nil || c.Roles != (model.Roles{Embed: "text-embedding-3-small"}) {
		t.Errorf("embed alone: %+v, %v", c.Roles, err)
	}
	for v, want := range map[string]string{
		"specialist=x":          "lead is required when specialist or critic is set",
		"lead=a,lead=b":         "the lead role is set twice",
		"lead=a,":               "entry 2 is not role=<model id>",
		"lead=a,emebd=SECRETX":  "entry 2 is not role=<model id>",
		"qwen2.5:14b":           "entry 1 is not role=<model id>",
		"lead=":                 "the lead id is empty or not a model id",
		"Lead=a":                "entry 1 is not role=<model id>",
		"lead=a;embed=SECRETXY": "the lead id is empty or not a model id",
	} {
		_, err := load(v)
		if msg := fmt.Sprint(err); !strings.Contains(msg, "BIGLAW_MODELS: "+want) || strings.Contains(msg, "SECRET") {
			t.Errorf("BIGLAW_MODELS=%q: %v; want %q, no value echoed", v, err, want)
		}
	}
	if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODELS", "embed=m")); err == nil || !strings.Contains(err.Error(), "BIGLAW_MODEL_URL: is required when BIGLAW_MODEL_KEY or BIGLAW_MODELS") {
		t.Errorf("a role without BIGLAW_MODEL_URL: %v", err)
	}
	if got := shown("BIGLAW_MODELS", "lead=qwen2.5:14b,embed=nomic-embed-text"); got != "lead=qwen2.5:14b,embed=nomic-embed-text" {
		t.Errorf("a valid map is shown as %q", got)
	}
}

// TestRoleIDRefusesQueryCharacters (PR 2 L-1): role ids reach the logs raw, so an id that could
// carry a secret (a query string, white space, a URL, user:password@) is refused at startup and
// never shown, by Load, by `version --config` or by a `serve` refusal.
func TestRoleIDRefusesQueryCharacters(t *testing.T) {
	for _, id := range []string{"gw.example.com/v1?key=PWMARK1", "m&token=PWMARK2", "m?PWMARK3", "PWMARK4 extra", "PWMARK5\tx",
		"https://gw.example.com/PWMARK6", "alice:PWMARK7@gw", "x\nPWMARK8", "m\u00a0PWMARK9", "a\u202ePWMARK10", "a\u200bPWMARK11",
		"a\u2066PWMARK12", "sk-proj-PWMARK13", "AKIAPWMARK14", "ghp_PWMARK15", "xPWMARK16a0b1c2d3e4f5g6h7i8j9", "m" + strings.Repeat("-PWMARK17", 23)} {
		for _, role := range []string{"lead", "embed"} {
			v := "lead=ok," + role + "=" + id
			if role == "lead" {
				v = "lead=" + id
			}
			e := env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "http://ollama:11434/v1", "BIGLAW_MODELS", v)
			_, err := Load(e)
			var stdout, stderr bytes.Buffer
			run(context.Background(), []string{"version", "--config"}, e, nil, &stdout, &stderr)
			out := fmt.Sprint(err) + stdout.String() + stderr.String()
			if err == nil || !strings.Contains(fmt.Sprint(err), "the "+role+" id is empty or not a model id") || strings.Contains(out, "PWMARK") {
				t.Errorf("%s=%q: err %v; output leaked or accepted:\n%s", role, id, err, out)
			}
			if !strings.Contains(stdout.String(), "BIGLAW_MODELS              (set, not shown") {
				t.Errorf("version --config shows BIGLAW_MODELS=%q:\n%s", v, stdout.String())
			}
		}
	}
	// Ordinary ids pass: Ollama tags, LM Studio @quant, OpenRouter vendor/model, Bedrock-style ids.
	for _, id := range []string{"qwen2.5:14b", "qwen2.5-14b-instruct@q4_k_m", "deepseek/deepseek-r1", "us.anthropic.claude-haiku-4-5-v1:0", "text-embedding-3-small"} {
		if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "http://ollama:11434/v1", "BIGLAW_MODELS", "lead="+id)); err != nil {
			t.Errorf("%q refused: %v", id, err)
		}
	}
}

// TestConfigEngineVars (moved from PR 1): the budget and grounding-collapse variables join Config,
// validated: whole calls 1–100000, a plain positive dollar amount (no $0, no hex, sign or
// exponent), a threshold from 0.01 to 1 (1e-300 refused), strict or warn.
func TestConfigEngineVars(t *testing.T) {
	c, err := Load(env("BIGLAW_DATABASE_URL", dsn))
	if err != nil || c.Engine.BudgetCalls != 400 || c.Engine.BudgetUSD != 5 || c.Engine.CollapseThreshold != 0.5 || c.Engine.CollapseAction != "strict" {
		t.Fatalf("defaults: %+v, %v", c.Engine, err)
	}
	c, err = Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_BUDGET_CALLS", "400", "BIGLAW_BUDGET_USD", "2.50", "BIGLAW_COLLAPSE_THRESHOLD", "0.3", "BIGLAW_COLLAPSE_ACTION", "warn"))
	if err != nil || c.Engine.BudgetCalls != 400 || c.Engine.BudgetUSD != 2.5 || c.Engine.CollapseThreshold != 0.3 || c.Engine.CollapseAction != "warn" {
		t.Fatalf("set: %+v, %v", c.Engine, err)
	}
	for name, values := range map[string][]string{
		"BIGLAW_BUDGET_CALLS":       {"0", "-5", "100001", "1.5", "abc", "1e3"},
		"BIGLAW_BUDGET_USD":         {"0", "0.00", "-1", "0x1p4", "+5", "1e2", "inf", "NaN", "abc", "2000000"},
		"BIGLAW_COLLAPSE_THRESHOLD": {"0", "1e-300", "0.001", "1.5", "-0.5", "abc"},
		"BIGLAW_COLLAPSE_ACTION":    {"fail", "STRICT", "off"},
	} {
		for _, v := range values {
			if _, err := Load(env("BIGLAW_DATABASE_URL", dsn, name, v)); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s=%q: err = %v, want an error naming it", name, v, err)
			}
		}
	}
}

// TestConfigSignInVars (PR 8): sign-in needs the issuer and client id together, an https issuer
// with no query and an https public URL with no path (plain http on loopback only), and
// BIGLAW_ADMINS only with sign-in, as emails or subjects; no message shows the client secret.
func TestConfigSignInVars(t *testing.T) {
	on := []string{"BIGLAW_DATABASE_URL", dsn, "BIGLAW_OIDC_ISSUER", "https://idp.example/realms/firm", "BIGLAW_OIDC_CLIENT_ID", "biglaw",
		"BIGLAW_OIDC_CLIENT_SECRET", "s3cret-VALUE", "BIGLAW_PUBLIC_URL", "https://biglaw.firm.example/", "BIGLAW_ADMINS", " ada@firm.example , u-root ,"}
	c, err := Load(env(on...))
	if err != nil {
		t.Fatal(err)
	}
	if c.OIDC.Issuer != "https://idp.example/realms/firm" || c.OIDC.ClientSecret != "s3cret-VALUE" || strings.Join(c.OIDC.Admins, "|") != "ada@firm.example|u-root" {
		t.Fatalf("sign-in config: %+v", c.OIDC)
	}
	if c, err := Load(env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_OIDC_ISSUER", "http://127.0.0.1:8080/realms/dev", "BIGLAW_OIDC_CLIENT_ID", "dev")); err != nil || c.OIDC.PublicURL != "http://127.0.0.1:3101" {
		t.Fatalf("loopback development: %v %+v", err, c.OIDC)
	}
	with := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(on); i += 2 {
			m[on[i]] = on[i+1]
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, c := range []struct {
		name string
		env  func(string) string
		want string
	}{
		{"no client id", with("BIGLAW_OIDC_CLIENT_ID", ""), "both needed"},
		{"a secret alone", env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_OIDC_CLIENT_SECRET", "s3cret-VALUE"), "both needed"},
		{"plain http issuer", with("BIGLAW_OIDC_ISSUER", "http://idp.example"), "BIGLAW_OIDC_ISSUER"},
		{"issuer query", with("BIGLAW_OIDC_ISSUER", "https://idp.example/?x=1"), "BIGLAW_OIDC_ISSUER"},
		{"a bare name", with("BIGLAW_OIDC_ISSUER", "idp"), "BIGLAW_OIDC_ISSUER"},
		{"plain http public URL", with("BIGLAW_PUBLIC_URL", "http://biglaw.firm.example"), "BIGLAW_PUBLIC_URL"},
		{"public URL path", with("BIGLAW_PUBLIC_URL", "https://firm.example/biglaw"), "BIGLAW_PUBLIC_URL"},
		{"default public URL off loopback", with("BIGLAW_PUBLIC_URL", "http://0.0.0.0:3101"), "BIGLAW_PUBLIC_URL"},
		{"admins without sign-in", env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_ADMINS", "ada@firm.example"), "needs sign-in"},
		{"admin with a space", with("BIGLAW_ADMINS", "ada lovelace"), "entry 1"},
		{"admin email not ASCII", with("BIGLAW_ADMINS", "u-root,\u212Aate@firm.example"), "entry 2"},
		{"secret with a space", with("BIGLAW_OIDC_CLIENT_SECRET", "s3cret VALUE"), "white space"},
	} {
		_, err := Load(c.env)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "VALUE") {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if got := Describe(with(), nil); strings.Contains(got, "s3cret") || !strings.Contains(got, "BIGLAW_ADMINS              ada@firm.example , u-root ,  [env]") {
		t.Errorf("Describe: %s", got)
	}
}
