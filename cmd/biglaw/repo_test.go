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
// "`BIGLAW_OIDC_CLIENT_ID` / `_CLIENT_SECRET`" expands each suffix
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
		".env.example": {"BIGLAW_MODEL_URL", "BIGLAW_MODEL_KEY", "BIGLAW_MODELS", "BIGLAW_PUBLIC_URL", "BIGLAW_OIDC_ISSUER", "BIGLAW_OIDC_CLIENT_ID", "BIGLAW_OIDC_CLIENT_SECRET", "BIGLAW_ADMINS"},
		"render.yaml":  {"BIGLAW_DATABASE_URL", "BIGLAW_ADDR", "BIGLAW_PUBLIC_URL", "BIGLAW_MODEL_URL", "BIGLAW_MODEL_KEY", "BIGLAW_MODELS", "BIGLAW_OIDC_ISSUER", "BIGLAW_OIDC_CLIENT_ID", "BIGLAW_OIDC_CLIENT_SECRET", "BIGLAW_ADMINS"},
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
	// The language version is the same release line (go 1.N.0 with toolchain go1.N.x),
	// and the README names both.
	minor := regexp.MustCompile(`^\d+\.\d+`).FindString(want)
	if got := find("go.mod", `(?m)^go (\S+)$`); !slices.Equal(got, []string{minor + ".0"}) {
		t.Errorf("go.mod's go line is %v, want [%s.0] for toolchain go%s", got, minor, want)
	}
	readme := readRepo(t, "README.md")
	for _, s := range []string{"golang:" + want + "-alpine", "(Go " + minor + "+,"} {
		if !strings.Contains(readme, s) {
			t.Errorf("README.md does not name %q (go.mod pins toolchain go%s)", s, want)
		}
	}
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
// Render may not. Compose's ports are read from the model `docker compose
// config` resolves the way `docker compose up` does (from the repo root, no
// -f, so override files count; every profile; nothing interpolated, so a
// host such as "${BIND:-127.0.0.1}" is never taken for loopback).
func TestDeployFilesFollowExposureRule(t *testing.T) {
	compose := readRepo(t, "docker-compose.yml")
	if !strings.Contains(compose, `command: ["serve", "--allow-no-auth"]`) {
		t.Error("docker-compose.yml must start biglaw with --allow-no-auth (it listens on 0.0.0.0 in the container)")
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
	// Compose would read (or prefer) any of these; only docker-compose.yml is allowed.
	for _, glob := range []string{"compose.y*ml", "docker-compose.yaml", "*.override.y*ml"} {
		if m, _ := filepath.Glob("../../" + glob); len(m) > 0 {
			t.Errorf("%v: Compose would read it too; keep the stack in docker-compose.yml alone", m)
		}
	}
	ports, err := composePublished(t, "../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) == 0 {
		t.Error("no published ports found in docker-compose.yml")
	}
	for _, p := range ports {
		if !loopbackHost(p.host) {
			t.Errorf("docker compose config: %s", p)
		}
	}
}

// TestComposeExposureCheckCatchesBypasses runs the check on the spellings
// the r2 and r3 committees used to publish beyond loopback.
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
		"merge key":            "    ports: [\"127.0.0.1:3101:3101\"]\n  base: &exposed\n    image: x\n    network_mode: host\n  side:\n    <<: *exposed\n",
		"quoted ports key":     "    \"ports\":\n      - 3102:3101\n",
		"flow-mapped service":  "    ports: [\"127.0.0.1:3101:3101\"]\n  side: {image: \"alpine\", ports: [\"3102:3101\"]}\n",            // r3 P2
		"profiled service":     "    ports: [\"127.0.0.1:3101:3101\"]\n  debug: {profiles: [debug], image: x, ports: [\"3102:3101\"]}\n", // r3 R3-1
		"override file":        "    ports: [\"127.0.0.1:3101:3101\"]\n" + "\x00services:\n  biglaw:\n    ports: [\"3101:3101\"]\n",      // r3 R3-1
	}
	good := map[string]string{
		"quoted loopback":   "    ports:\n      - \"127.0.0.1:3101:3101\"   # comment\n",
		"unquoted loopback": "    ports:\n      - 127.0.0.1:3101:3101\n",
		"ipv6 loopback":     "    ports:\n      - \"[::1]:3101:3101/tcp\"\n",
		"range":             "    ports:\n      - \"127.0.0.1:3101-3102:3101-3102\"\n",
		"long loopback":     "    ports:\n      - target: 3101\n        host_ip: 127.0.0.1\n        published: \"3101\"\n",
		"flow loopback":     "    ports: [\"127.0.0.1:3101:3101\"]\n",
	}
	check := func(name, tail string) ([]published, error) {
		dir := t.TempDir()
		main, override, _ := strings.Cut(head+tail, "\x00")
		os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(main), 0o600)
		if override != "" {
			os.WriteFile(filepath.Join(dir, "docker-compose.override.yml"), []byte(override), 0o600)
		}
		return composePublished(t, dir)
	}
	for name, tail := range bad {
		ports, err := check(name, tail)
		if err == nil && !slices.ContainsFunc(ports, func(p published) bool { return !loopbackHost(p.host) }) {
			t.Errorf("%s: passed %v", name, ports)
		}
	}
	for name, tail := range good {
		ports, err := check(name, tail)
		if err != nil || len(ports) == 0 || slices.ContainsFunc(ports, func(p published) bool { return !loopbackHost(p.host) }) {
			t.Errorf("%s: refused (%v, %v)", name, ports, err)
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

// composePublished lists every port (and network_mode) of every service, in
// every profile, that `docker compose config --no-interpolate` resolves in
// dir. A port Compose leaves as text (it does when the list holds an
// interpolation) is reported with that text as its host, so it never counts
// as loopback. Without docker the test is skipped, or fails where CI is set
// (GitHub sets it), like make compose-check.
func composePublished(t *testing.T, dir string) ([]published, error) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("docker is required when CI is set: the Compose exposure check needs docker compose config")
		}
		t.Skip("docker not installed: Compose exposure check skipped (CI runs it)")
	}
	cmd := exec.Command("docker", "compose", "--profile", "*", "config", "--no-interpolate", "--format", "json")
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker compose config in %s: %v", dir, err)
	}
	var cfg struct {
		Services map[string]struct {
			NetworkMode string            `json:"network_mode"`
			Ports       []json.RawMessage `json:"ports"`
		} `json:"services"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	out := []published{}
	for name, svc := range cfg.Services {
		if svc.NetworkMode != "" {
			out = append(out, published{host: "network_mode " + svc.NetworkMode, spec: name})
		}
		for _, raw := range svc.Ports {
			var p struct {
				HostIP    string `json:"host_ip"`
				Published string `json:"published"`
				Target    int    `json:"target"`
			}
			if json.Unmarshal(raw, &p) != nil {
				out = append(out, published{host: string(raw), spec: name}) // unresolved text
				continue
			}
			out = append(out, published{host: p.HostIP, spec: fmt.Sprintf("%s %s:%d", name, p.Published, p.Target)})
		}
	}
	return out, nil
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
