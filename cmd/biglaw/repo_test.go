// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"os"
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
// host's loopback, may start the server with --allow-no-auth.
func TestDeployFilesFollowExposureRule(t *testing.T) {
	compose := readRepo(t, "docker-compose.yml")
	if !strings.Contains(compose, `command: ["serve", "--allow-no-auth"]`) {
		t.Error("docker-compose.yml must start biglaw with --allow-no-auth (it listens on 0.0.0.0 in the container)")
	}
	ports := regexp.MustCompile(`(?m)^\s+- "([^"]+)"`).FindAllStringSubmatch(compose, -1)
	if len(ports) == 0 {
		t.Error("no published ports found in docker-compose.yml")
	}
	for _, p := range ports {
		if !strings.HasPrefix(p[1], "127.0.0.1:") {
			t.Errorf("docker-compose.yml publishes %q beyond the host's loopback", p[1])
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
