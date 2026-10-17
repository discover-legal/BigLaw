// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestOpenErrorHidesPassword: connection errors are logged at startup, so
// they must never carry the DSN's password.
func TestOpenErrorHidesPassword(t *testing.T) {
	for _, dsn := range []string{
		"postgres://u:SECRETPW@127.0.0.1:badport/db",
		"postgres://u:SECRETPW@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		"host=127.0.0.1 port=1 user=u password=SECRETPW sslmode=disable connect_timeout=2",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := Open(ctx, dsn)
		cancel()
		if err == nil || strings.Contains(err.Error(), "SECRETPW") {
			t.Errorf("Open(%q) error = %v", strings.ReplaceAll(dsn, "SECRETPW", "***"), err)
		}
	}
}
