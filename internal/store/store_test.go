// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
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
		{"host=h user=u password=SECRET PW dbname=d", false, "key=value"},
		{"host=h user=u password='SECRET dbname=d", false, "key=value"},
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
