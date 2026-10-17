// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocBudget runs scripts/loc.py on synthetic trees, one per budget, and
// on this repository.
func TestLocBudget(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("python3 is required in CI for the budget check")
		}
		t.Skip("python3 not installed")
	}
	script, _ := filepath.Abs("../../scripts/loc.py")
	loc := func(root string) (int, string) {
		out, err := exec.Command(py, script, "--root", root).CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		} else if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}
	write := func(root, rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := func() string {
		root := t.TempDir()
		write(root, "go.mod", "module x\n\ngo 1.25\n\nrequire github.com/lib/pq v1.12.3\n")
		write(root, "internal/a/a.go", "package a\n")
		write(root, "internal/a/a_test.go", "package a\n")
		return root
	}

	if code, out := loc(base()); code != 0 {
		t.Fatalf("minimal tree: exit %d\n%s", code, out)
	}
	if code, out := loc("../.."); code != 0 {
		t.Fatalf("this repository is over budget: exit %d\n%s", code, out)
	}

	over := map[string]func(root string){
		"go_prod_loc": func(r string) {
			write(r, "internal/a/big.go", "package a\n"+strings.Repeat("// x\n", 14000))
		},
		"internal_packages": func(r string) {
			for i := 0; i < 12; i++ { // 12 more on top of internal/a
				write(r, fmt.Sprintf("internal/p%d/p.go", i), "package p\n")
				write(r, fmt.Sprintf("internal/p%d/p_test.go", i), "package p\n")
			}
		},
		"direct_go_deps": func(r string) {
			write(r, "go.mod", "module x\n\ngo 1.25\n\nrequire (\n\ta.example/a v1.0.0\n\tb.example/b v1.0.0\n\tc.example/c v1.0.0 // indirect\n\td.example/d v1.0.0\n)\n\nrequire e.example/e v1.0.0\n")
		},
		"env_vars": func(r string) {
			var b strings.Builder
			b.WriteString("package a\n\nimport \"os\"\n\nfunc f() {\n")
			for i := 0; i < 21; i++ {
				fmt.Fprintf(&b, "\t_ = os.Getenv(\"X_%d\")\n", i)
			}
			b.WriteString("}\n")
			write(r, "internal/a/env.go", b.String())
		},
		"ui_runtime_deps": func(r string) {
			write(r, "ui/package.json", `{"dependencies":{"react":"18","react-dom":"18","left-pad":"1"}}`)
		},
		"untested_packages": func(r string) {
			write(r, "internal/b/b.go", "package b\n")
		},
		"non-literal": func(r string) {
			write(r, "internal/a/dyn.go", "package a\n\nimport \"os\"\n\nfunc g(n string) string { return os.Getenv(n) }\n")
		},
		"test-only": func(r string) {
			write(r, "internal/a/t.go", "package a\n\nimport \"os\"\n\nvar _ = os.Getenv(\"BIGLAW_TEST_DSN\")\n")
		},
	}
	for name, mutate := range over {
		root := base()
		mutate(root)
		code, out := loc(root)
		named := false
		for _, line := range strings.Split(out, "\n") {
			named = named || strings.HasPrefix(line, "FAIL:") && strings.Contains(line, name)
		}
		if code == 0 || !named {
			t.Errorf("%s over budget: exit %d\n%s", name, code, out)
		}
	}
}
