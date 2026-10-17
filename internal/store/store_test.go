// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lib/pq"
)

// TestCheckDSN: the validator and lib/pq must read a DSN the same way, and
// no error may carry the password (marked SECRET in every case).
func TestCheckDSN(t *testing.T) {
	cases := []struct {
		dsn  string
		ok   bool
		want string // substring of the error
	}{
		{"postgres://u:SECRET@h:5432/db?sslmode=disable", true, ""},
		{"postgresql://u:SECRET@h/db", true, ""},
		{"postgres:///biglaw?sslmode=disable", true, ""},
		{"postgres://u:SE%2FCRET%3F%23%25%40@h/db", true, ""}, // percent-encoded reserved characters
		{"postgres://u:SE@CRET@h/db", true, ""},               // '@' inside the password: Go and lib/pq both take the last '@'
		{"host=h user=u password='SECRET pw' dbname=d", true, ""},
		{"host=h user=u password=SECRET dbname=d sslmode=disable", true, ""},

		{"POSTGRES://u:SECRET@h/db?sslmode=disable", false, "lowercase"},
		{"Postgresql://u:SECRET@h:5433/db?sslmode=disable", false, "lowercase"},
		{"mysql://u:SECRET@h/db", false, "postgres://"},
		{"postgres://biglaw:p@ss/w0rdSECRET@127.0.0.1:5433/x", false, "percent-encode"},
		{"postgres://u:SECRET/x@h/db", false, "percent-encode"},
		{"postgres://u:SE#CRET@h/db", false, "percent-encode"},
		{"postgres://u:SE?CRET@h/db", false, "percent-encode"},
		{"postgres://u:SE CRET@h/db", false, "percent-encode"},
		{"postgres://u:SECRET%zz@h/db", false, "percent-encode"},
		{"postgres://u:SECRET@h:badport/db", false, "percent-encode"},
		// An "@" after the host is refused even where it is harmless (r2: the
		// message names the database name and query, and %40 works).
		{"postgres://u:SECRET@h/db?sslmode=disable&application_name=ops@corp", false, "query value"},
		{"postgres://u:SECRET@h/db@2026?sslmode=disable", false, "database name"},
		{"postgres://u:SECRET@h/db?sslmode=disable&application_name=ops%40corp", true, ""},
		{"postgres://u:SECRET@h/db?sslmode=bogus&sslpassword=SECRET", false, "sslmode"},
		{"host=h user=u password=SECRET PW dbname=d", false, "not shown"},
		{"host=h user=u password='SECRET dbname=d", false, "unterminated"},
	}
	for _, tc := range cases {
		err := CheckDSN(tc.dsn)
		if tc.ok != (err == nil) || err != nil && !strings.Contains(err.Error(), tc.want) {
			t.Errorf("CheckDSN(%q) = %v, want ok=%v containing %q", tc.dsn, err, tc.ok, tc.want)
		}
		if err != nil && (strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "CRET")) {
			t.Errorf("CheckDSN(%q) error leaks the password: %v", tc.dsn, err)
		}
	}
	t.Setenv("PGGSSENCMODE", "disable") // lib/pq refuses some libpq variables outright
	if err := CheckDSN("postgres://h/db"); err == nil || !strings.Contains(err.Error(), "PGGSSENCMODE") {
		t.Errorf("unsupported PG variable: %v", err)
	}
}

// TestCheckDSNWithoutPasswdEntry: a DSN that names its user is accepted by
// a process whose uid has no passwd entry and no USER/PGUSER (docker run
// --user, Kubernetes runAsUser), as it is by lib/pq when connecting (r3
// auditor B6: a pre-check of the bare libpq environment refused it). The
// test re-runs itself as such a uid, so it needs root; CI's docker job runs
// the image with --user for the same reason.
func TestCheckDSNWithoutPasswdEntry(t *testing.T) {
	if os.Getenv("STORE_TEST_NO_PASSWD") == "1" {
		if _, err := pq.NewConfig(""); err == nil {
			t.Fatal("the child resolved a default user: the condition under test is not reproduced")
		}
		if err := CheckDSN("postgres://biglaw@db:5432/biglaw?sslmode=disable"); err != nil {
			t.Fatalf("CheckDSN of a DSN naming its user: %v", err)
		}
		// r4 verifier L2: one that names no user gets a hint, not just os/user's error.
		err := CheckDSN("postgres://:SECRETPW@db:5432/biglaw?sslmode=disable")
		if err == nil || !strings.Contains(err.Error(), "name the user in the database URL") || strings.Contains(err.Error(), "SECRETPW") {
			t.Fatalf("CheckDSN of a DSN naming no user: %v", err)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root to run a child as a uid without a passwd entry")
	}
	dir := t.TempDir()
	bin, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "store.test"), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for d := dir; len(d) > len(filepath.Clean(os.TempDir())); d = filepath.Dir(d) {
		os.Chmod(d, 0o755) // t.TempDir's directories are 0700
	}
	cmd := exec.Command(filepath.Join(dir, "store.test"), "-test.run=^TestCheckDSNWithoutPasswdEntry$", "-test.v")
	cmd.Env = []string{"STORE_TEST_NO_PASSWD=1", "HOME=/"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 54321, Gid: 54321}}
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "--- PASS") {
		t.Fatalf("as uid 54321: %v\n%s", err, out)
	}
}

// TestNoDefaultUser: both os/user failures (static build without cgo, and
// with cgo) are recognised; other parse errors are not.
func TestNoDefaultUser(t *testing.T) {
	for err, want := range map[error]bool{
		errors.New("user: Current requires cgo or $USER set in environment"): true,
		user.UnknownUserIdError(54321):                                       true,
		fmt.Errorf("wrapped: %w", user.UnknownUserIdError(1)):                true,
		errors.New(`pq: unsupported sslmode "x"`):                            false,
	} {
		if got := noDefaultUser(err); got != want {
			t.Errorf("noDefaultUser(%v) = %v, want %v", err, got, want)
		}
	}
}

// TestHideDSN tests the last line of defence directly: CheckDSN now refuses
// every DSN known to make lib/pq echo itself, so Open never reaches these.
func TestHideDSN(t *testing.T) {
	const dsn = "postgres://u:SECRETPW@h/db"
	for _, err := range []error{
		&url.Error{Op: "parse", URL: dsn, Err: errors.New("bad")},
		errors.New("pq: cannot use " + dsn),
		errors.New(`missing "=" after "SECRETPW" in connection info string`),
	} {
		if got := hideDSN(err, dsn); strings.Contains(got.Error(), "SECRETPW") || !strings.Contains(got.Error(), "not shown") {
			t.Errorf("hideDSN(%v) = %v", err, got)
		}
	}
	plain := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	if got := hideDSN(plain, dsn); got != plain {
		t.Errorf("an error without the DSN was replaced: %v", got)
	}
}

// TestOpenErrorHidesPassword: connection errors are logged at startup, so
// they must never carry the DSN's password.
func TestOpenErrorHidesPassword(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:SECRETPW@127.0.0.1:badport/db",
		"postgres://u:SECRETPW@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		"host=127.0.0.1 port=1 user=u password=SECRETPW sslmode=disable connect_timeout=2",
		"host=127.0.0.1 port=1 user=u password=SECRETPW PW sslmode=disable",
		"POSTGRES://u:SECRETPW@127.0.0.1:1/db?sslmode=disable",
		"postgres://u:p@ss/SECRETPW@127.0.0.1:1/db",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := Open(ctx, dsn, nil)
		cancel()
		if err == nil || strings.Contains(err.Error(), "SECRETPW") || strings.Contains(err.Error(), "ss") && strings.Contains(err.Error(), "lookup") {
			t.Errorf("Open(%q) error = %v", strings.ReplaceAll(dsn, "SECRETPW", "***"), err)
		}
	}
}
