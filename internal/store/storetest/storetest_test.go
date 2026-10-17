// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package storetest

import (
	"fmt"
	"strings"
	"testing"
)

// fakeT records how DSNFrom ended. Skip and Fatalf stop the caller the way
// the real ones do (runtime.Goexit), here by panicking with a sentinel.
type fakeT struct {
	skipped, failed bool
	msg             string
	cleanups        []func()
}

type stop struct{}

func (f *fakeT) Helper()           {}
func (f *fakeT) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }
func (f *fakeT) Skip(args ...any) {
	f.skipped, f.msg = true, fmt.Sprint(args...)
	panic(stop{})
}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed, f.msg = true, fmt.Sprintf(format, args...)
	panic(stop{})
}

func run(env map[string]string) (f *fakeT, dsn string) {
	f = &fakeT{}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stop); !ok {
				panic(r)
			}
		}
	}()
	dsn = DSNFrom(f, func(k string) string { return env[k] })
	return f, dsn
}

func TestStoretestSkipsWithoutDSN(t *testing.T) {
	for _, req := range []string{"", "0", "false"} {
		f, dsn := run(map[string]string{"BIGLAW_REQUIRE_DB": req})
		if !f.skipped || f.failed || dsn != "" {
			t.Fatalf("REQUIRE_DB=%q: skipped=%v failed=%v dsn=%q, want a skip", req, f.skipped, f.failed, dsn)
		}
		if !strings.Contains(f.msg, "BIGLAW_TEST_DSN") {
			t.Errorf("skip message %q should name BIGLAW_TEST_DSN", f.msg)
		}
	}
}

func TestStoretestFailsWhenRequired(t *testing.T) {
	for _, req := range []string{"1", "true", "YES", " on "} {
		f, _ := run(map[string]string{"BIGLAW_REQUIRE_DB": req})
		if !f.failed || f.skipped {
			t.Fatalf("REQUIRE_DB=%q: skipped=%v failed=%v, want a failure", req, f.skipped, f.failed)
		}
		if !strings.Contains(f.msg, "BIGLAW_REQUIRE_DB") {
			t.Errorf("failure message %q should name BIGLAW_REQUIRE_DB", f.msg)
		}
	}
	// A DSN that is set but unreachable fails even without REQUIRE_DB.
	f, _ := run(map[string]string{"BIGLAW_TEST_DSN": "postgres://nobody@127.0.0.1:1/x?sslmode=disable&connect_timeout=2"})
	if !f.failed {
		t.Fatalf("unreachable DSN: failed=%v skipped=%v, want a failure", f.failed, f.skipped)
	}
}

func TestWithSearchPath(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@h:5432/db?sslmode=disable":           "postgres://u:p@h:5432/db?search_path=t_x&sslmode=disable",
		"postgresql://h/db":                                  "postgresql://h/db?search_path=t_x",
		"postgres://h/db?search_path=public&sslmode=disable": "postgres://h/db?search_path=t_x&sslmode=disable",
		"host=h dbname=db sslmode=disable":                   "host=h dbname=db sslmode=disable search_path=t_x",
	}
	for in, want := range cases {
		if got := WithSearchPath(in, "t_x"); got != want {
			t.Errorf("WithSearchPath(%q) = %q, want %q", in, got, want)
		}
	}
}
