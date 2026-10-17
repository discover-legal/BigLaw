// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These tests keep the repository's prose and deploy files in step with the
// code, so the README, docker-compose.yml, .env.example, render.yaml, CI and
// the Dockerfile cannot silently drift.

func readRepo(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// readmeConfigTable returns variable → "Read from" PR for every row of
// README § Configuration. A row may name several variables:
// "`BIGLAW_MODEL_LEAD` / `_SPECIALIST` / `_CRITIC`" expands each suffix
// against the first name.
func readmeConfigTable(t *testing.T) map[string]int {
	readme := readRepo(t, "README.md")
	start := strings.Index(readme, "\n## Configuration")
	if start < 0 {
		t.Fatal("README has no ## Configuration section")
	}
	section := readme[start+1:]
	if end := strings.Index(section, "\n## "); end > 0 {
		section = section[:end]
	}
	rows := map[string]int{}
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(line, "|")
		pr, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cells[len(cells)-2]), "PR")))
		if err != nil {
			t.Fatalf("README row %q: last column is not \"PR n\"", line)
		}
		names := strings.Split(strings.ReplaceAll(cells[1], "`", ""), "/")
		first := strings.TrimSpace(names[0])
		rows[first] = pr
		for _, suffix := range names[1:] {
			suffix = strings.TrimSpace(suffix)
			parts := strings.Split(first, "_")
			keep := len(parts) - strings.Count(suffix, "_")
			rows[strings.Join(parts[:keep], "_")+suffix] = pr
		}
	}
	return rows
}

var varName = regexp.MustCompile(`\b((?:BIGLAW|ALLOW)_[A-Z0-9_]+)\b`)

func TestEnvVarTablesAgree(t *testing.T) {
	declared := map[string]int{}
	for _, e := range envVars {
		declared[e.name] = e.pr
	}
	readme := readmeConfigTable(t)
	for name, pr := range declared {
		if got, ok := readme[name]; !ok || got != pr {
			t.Errorf("README § Configuration: %s has PR %d (present %v); config.go says PR %d", name, got, ok, pr)
		}
	}
	for name := range readme {
		if _, ok := declared[name]; !ok {
			t.Errorf("README § Configuration lists %s, which config.go does not declare", name)
		}
	}
	// Compose passes every variable through, plus PGPASSWORD.
	compose := readRepo(t, "docker-compose.yml")
	keys := regexp.MustCompile(`(?m)^\s+([A-Z][A-Z0-9_]+):`).FindAllStringSubmatch(compose, -1)
	inCompose := map[string]bool{}
	for _, k := range keys {
		inCompose[k[1]] = true
	}
	for name := range declared {
		if !inCompose[name] {
			t.Errorf("docker-compose.yml does not pass %s to biglaw", name)
		}
	}
	// .env.example and render.yaml carry exactly the variables an operator
	// sets by hand (README § Run it, § Exposure), no more and no fewer.
	for file, want := range map[string][]string{
		".env.example": {"BIGLAW_MODEL_URL", "BIGLAW_MODEL_KEY", "BIGLAW_MODEL_LEAD", "BIGLAW_PUBLIC_URL", "BIGLAW_OIDC_ISSUER", "BIGLAW_OIDC_CLIENT_ID", "BIGLAW_OIDC_CLIENT_SECRET"},
		"render.yaml":  {"BIGLAW_DATABASE_URL", "BIGLAW_ADDR", "BIGLAW_PUBLIC_URL", "BIGLAW_MODEL_URL", "BIGLAW_MODEL_KEY", "BIGLAW_MODEL_LEAD", "BIGLAW_OIDC_ISSUER", "BIGLAW_OIDC_CLIENT_ID", "BIGLAW_OIDC_CLIENT_SECRET"},
	} {
		got := regexp.MustCompile(`(?m)^(?:\s*- key: |)((?:BIGLAW|ALLOW)_[A-Z0-9_]+)(?:=|\s*$)`).FindAllStringSubmatch(readRepo(t, file), -1)
		var names []string
		for _, m := range got {
			names = append(names, m[1])
		}
		slices.Sort(names)
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Errorf("%s sets %v, want %v", file, names, want)
		}
	}
	// No file names a variable the code does not declare.
	for _, file := range []string{"docker-compose.yml", ".env.example", "render.yaml", "README.md", "CLAUDE.md"} {
		for _, m := range varName.FindAllString(readRepo(t, file), -1) {
			if _, ok := declared[m]; !ok && m != "BIGLAW_TEST_DSN" && m != "BIGLAW_REQUIRE_DB" {
				t.Errorf("%s names %s, which config.go does not declare", file, m)
			}
		}
	}
}

func TestGoVersionPinnedEverywhere(t *testing.T) {
	find := func(file, pattern string) []string {
		var out []string
		for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(readRepo(t, file), -1) {
			out = append(out, m[1])
		}
		return out
	}
	toolchain := find("go.mod", `(?m)^toolchain go(\S+)$`)
	if len(toolchain) != 1 {
		t.Fatalf("go.mod has %d toolchain lines, want 1", len(toolchain))
	}
	want := toolchain[0]
	if got := find("Dockerfile", `(?m)^FROM golang:(\S+?)-alpine`); !slices.Equal(got, []string{want}) {
		t.Errorf("Dockerfile builds with Go %v, go.mod pins %s", got, want)
	}
	ci := find(".github/workflows/ci.yml", `go-version: "([^"]+)"`)
	if len(ci) == 0 || slices.ContainsFunc(ci, func(v string) bool { return v != want }) {
		t.Errorf("CI uses Go %v, go.mod pins %s", ci, want)
	}
	if strings.Contains(readRepo(t, ".github/workflows/ci.yml"), "go-version-file") {
		t.Error("CI must pin go-version, not read it from go.mod's go line")
	}
}

// TestDeployFilesFollowExposureRule: only Compose, which publishes on the
// host's loopback, may start the server with --allow-no-auth; the image and
// Render may not. Compose's ports are checked twice: in the text (so an
// interpolated host such as "${BIND:-127.0.0.1}" is refused, whatever the
// current environment) and, where docker is installed (it is in CI), in the
// model `docker compose config` resolves (so YAML the text check does not
// understand cannot hide a port).
func TestDeployFilesFollowExposureRule(t *testing.T) {
	compose := readRepo(t, "docker-compose.yml")
	if !strings.Contains(compose, `command: ["serve", "--allow-no-auth"]`) {
		t.Error("docker-compose.yml must start biglaw with --allow-no-auth (it listens on 0.0.0.0 in the container)")
	}
	ports, err := composePublished(compose)
	if err != nil {
		t.Error(err)
	}
	if len(ports) == 0 {
		t.Error("no published ports found in docker-compose.yml")
	}
	for _, p := range ports {
		if !loopbackHost(p.host) {
			t.Errorf("docker-compose.yml: %s", p)
		}
	}
	for _, p := range resolvedPublished(t, "../../docker-compose.yml") {
		if !loopbackHost(p.host) {
			t.Errorf("docker compose config: %s", p)
		}
	}
	for _, file := range []string{"Dockerfile", "render.yaml"} {
		body := readRepo(t, file)
		for _, line := range strings.Split(body, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "allow-no-auth") {
				t.Errorf("%s passes --allow-no-auth: %q", file, line)
			}
		}
	}
	if !strings.Contains(readRepo(t, "Dockerfile"), `CMD ["serve"]`) {
		t.Error(`Dockerfile default command must be ["serve"] (no --allow-no-auth)`)
	}
}

// TestComposeExposureCheckCatchesBypasses runs both Compose checks on the
// spellings the r2 committee used to publish beyond loopback while the old
// regex stayed green. Each must be refused by the text check and, where
// docker is installed, by the resolved model too.
func TestComposeExposureCheckCatchesBypasses(t *testing.T) {
	const head = "services:\n  biglaw:\n    image: biglaw:local\n    command: [\"serve\", \"--allow-no-auth\"]\n"
	bad := map[string]string{
		"extra unquoted":       "    ports:\n      - \"127.0.0.1:3101:3101\"\n      - 3102:3101\n",
		"8080 unquoted":        "    ports:\n      - \"127.0.0.1:3101:3101\"\n      - 8080:3101\n",
		"network_mode host":    "    network_mode: host\n",
		"ports + network_mode": "    ports:\n      - \"127.0.0.1:3101:3101\"\n    network_mode: host\n",
		"PORT:PORT":            "    ports:\n      - \"3101:3101\"\n",
		"container port only":  "    ports:\n      - \"3101\"\n",
		"0.0.0.0":              "    ports:\n      - \"0.0.0.0:3101:3101\"\n",
		"single-quoted":        "    ports:\n      - '0.0.0.0:3101:3101'\n",
		"[::]":                 "    ports:\n      - \"[::]:3101:3101\"\n",
		"LAN IP":               "    ports:\n      - \"192.168.1.5:3101:3101/tcp\"\n",
		"long, no host_ip":     "    ports:\n      - target: 3101\n        published: \"3101\"\n",
		"long, 0.0.0.0":        "    ports:\n      - target: 3101\n        host_ip: 0.0.0.0\n        published: \"3101\"\n",
		"long, ::":             "    ports:\n      - published: \"3101\"\n        target: 3101\n        host_ip: \"::\"\n",
		"interpolated host":    "    ports:\n      - \"${BIND:-127.0.0.1}:3101:3101\"\n",
		"interpolated long":    "    ports:\n      - target: 3101\n        host_ip: ${BIND:-127.0.0.1}\n",
		"flow list":            "    ports: [\"3101:3101\"]\n",
		"flow list mixed":      "    ports: [\"127.0.0.1:3101:3101\", \"3102:3101\"]\n",
		"second service":       "    ports:\n      - \"127.0.0.1:3101:3101\"\n  postgres:\n    image: postgres:16-alpine\n    ports:\n      - 5432:5432\n",
		"merge key":            "    <<: *exposed\n",
		"quoted ports key":     "    \"ports\":\n      - 3102:3101\n", // red team: the text check read only a bare key
	}
	for name, tail := range bad {
		body := head + tail
		ports, err := composePublished(body)
		if err == nil && !slices.ContainsFunc(ports, func(p published) bool { return !loopbackHost(p.host) }) {
			t.Errorf("%s: text check passed %v:\n%s", name, ports, body)
		}
		if strings.Contains(body, "<<:") || strings.Contains(body, "${") {
			continue // not valid / resolved by the environment: the text check alone owns these
		}
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(body), 0o600)
		if r := resolvedPublished(t, filepath.Join(dir, "compose.yml")); r != nil && !slices.ContainsFunc(r, func(p published) bool { return !loopbackHost(p.host) }) {
			t.Errorf("%s: resolved check passed %v", name, r)
		}
	}
	good := map[string]string{
		"quoted loopback":   "    ports:\n      - \"127.0.0.1:3101:3101\"   # comment\n",
		"unquoted loopback": "    ports:\n      - 127.0.0.1:3101:3101\n",
		"ipv6 loopback":     "    ports:\n      - \"[::1]:3101:3101/tcp\"\n",
		"range":             "    ports:\n      - \"127.0.0.1:3101-3102:3101-3102\"\n",
		"long loopback":     "    ports:\n      - target: 3101\n        host_ip: 127.0.0.1\n        published: \"3101\"\n",
		"flow loopback":     "    ports: [\"127.0.0.1:3101:3101\"]\n",
	}
	for name, tail := range good {
		ports, err := composePublished(head + tail)
		if err != nil || len(ports) == 0 || slices.ContainsFunc(ports, func(p published) bool { return !loopbackHost(p.host) }) {
			t.Errorf("%s: refused (%v, %v):\n%s", name, ports, err, head+tail)
		}
	}
}

type published struct{ host, spec string } // host "" = every interface

func (p published) String() string {
	if p.host == "" {
		return fmt.Sprintf("port %q is published on every host interface", p.spec)
	}
	return fmt.Sprintf("port %q is published on %s, not the host's loopback", p.spec, p.host)
}

// loopbackHost reports whether host is a literal loopback IP (Docker binds
// IPs, not names).
func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// composePublished lists every port a compose file publishes, from its text:
// short syntax (quoted or not, block or flow list) and long syntax
// (target/published/host_ip). An interpolated host ("${…}") is reported with
// the raw text as its host, so it never counts as loopback. It returns an
// error for a construct that can publish or share ports in a way it does not
// read: network_mode, extends, include, YAML anchors and merge keys.
func composePublished(body string) ([]published, error) {
	var out []published
	var long *published // the long-syntax item being read
	portsIndent := -1
	flush := func() {
		if long != nil {
			out = append(out, *long)
			long = nil
		}
	}
	for _, raw := range strings.Split(body, "\n") {
		line := raw
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		text := strings.TrimSpace(line)
		for _, bad := range []string{"network_mode:", "extends:", "include:", "<<:"} {
			if strings.HasPrefix(text, bad) || strings.HasPrefix(text, "- "+bad) {
				return out, fmt.Errorf("docker-compose.yml uses %q, which this check cannot follow; publish ports only through ports: on the host's loopback", bad)
			}
		}
		if regexp.MustCompile(`(^|[\s:\[,])[&*][A-Za-z_]`).MatchString(text) {
			return out, fmt.Errorf("docker-compose.yml uses a YAML anchor or alias (%q), which this check cannot follow", text)
		}
		if portsIndent >= 0 && indent <= portsIndent {
			flush()
			portsIndent = -1
		}
		if key, rest, ok := strings.Cut(text, ":"); ok && strings.Trim(key, `"'`) == "ports" {
			portsIndent = indent
			if flow := strings.TrimSpace(rest); strings.HasPrefix(flow, "[") {
				for _, item := range strings.Split(strings.Trim(flow, "[]"), ",") {
					if item = strings.TrimSpace(item); item != "" {
						out = append(out, shortPort(item))
					}
				}
				portsIndent = -1
			}
			continue
		}
		if portsIndent < 0 {
			continue
		}
		item, isItem := strings.CutPrefix(text, "- ")
		if isItem {
			flush()
			if k, v, ok := strings.Cut(item, ":"); ok && regexp.MustCompile(`^[a-z_]+$`).MatchString(k) && (v == "" || v[0] == ' ') {
				long = &published{spec: item}
				text = item // the first key of a long-syntax item
			} else {
				out = append(out, shortPort(item))
				continue
			}
		}
		if long != nil {
			if k, v, ok := strings.Cut(text, ":"); ok && strings.TrimSpace(k) == "host_ip" {
				long.host = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	flush()
	return out, nil
}

// shortPort reads a short-syntax spec: [HOST_IP:][HOST_PORT:]CONTAINER_PORT[/PROTO],
// with an IPv6 HOST_IP in brackets. Without a HOST_IP Docker publishes on
// every interface.
func shortPort(spec string) published {
	s := strings.Trim(strings.TrimSpace(spec), `"'`)
	s, _, _ = strings.Cut(s, "/")
	p := published{spec: s}
	switch parts := strings.Split(s, ":"); {
	case strings.Contains(s, "$"):
		p.host = s // interpolated: cannot be proven loopback
	case strings.HasPrefix(s, "["):
		if i := strings.Index(s, "]:"); i > 0 {
			p.host = s[1:i]
		} else {
			p.host = s
		}
	case len(parts) == 3:
		p.host = parts[0]
	case len(parts) > 3:
		p.host = s // an unbracketed IPv6 address or worse: not understood
	}
	return p
}

// resolvedPublished returns the ports `docker compose config` resolves for
// file, or nil (logged) when docker is not installed. CI installs it; there
// make compose-check fails without docker.
func resolvedPublished(t *testing.T, file string) []published {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Log("docker not installed: resolved-model exposure check skipped (CI runs it)")
		return nil
	}
	cmd := exec.Command("docker", "compose", "-f", file, "config", "--format", "json")
	cmd.Env = append(os.Environ(), "POSTGRES_PASSWORD=exposure-check")
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker compose config %s: %v", file, err)
	}
	var cfg struct {
		Services map[string]struct {
			NetworkMode string `json:"network_mode"`
			Ports       []struct {
				HostIP    string `json:"host_ip"`
				Published string `json:"published"`
				Target    int    `json:"target"`
			} `json:"ports"`
		} `json:"services"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	out := []published{}
	for name, svc := range cfg.Services {
		if svc.NetworkMode != "" {
			out = append(out, published{host: "network_mode " + svc.NetworkMode, spec: name})
		}
		for _, p := range svc.Ports {
			out = append(out, published{host: p.HostIP, spec: fmt.Sprintf("%s %s:%d", name, p.Published, p.Target)})
		}
	}
	return out
}

// TestImageShipsLicenseAndNotice: the image redistributes the binary (and
// lib/pq inside it), so it carries LICENSE and NOTICE, and NOTICE carries
// lib/pq's MIT permission notice.
func TestImageShipsLicenseAndNotice(t *testing.T) {
	stages := strings.Split(readRepo(t, "Dockerfile"), "\nFROM ")
	if final := stages[len(stages)-1]; !regexp.MustCompile(`(?m)^COPY LICENSE NOTICE /usr/share/doc/biglaw/$`).MatchString(final) {
		t.Error("the Dockerfile's final stage does not COPY LICENSE NOTICE /usr/share/doc/biglaw/")
	}
	notice := readRepo(t, "NOTICE")
	for _, want := range []string{"github.com/lib/pq", "Permission is hereby granted, free of charge", `THE SOFTWARE IS PROVIDED "AS IS"`} {
		if !strings.Contains(notice, want) {
			t.Errorf("NOTICE lacks %q", want)
		}
	}
}
