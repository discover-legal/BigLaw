// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An audit log left 0644 by an earlier version is tightened to 0600 on the
// next append (O_CREATE's mode only applies to a new file).
func TestAppendTightensExistingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l := &Logger{maxBuffer: 100, lastHash: "genesis", logFile: path, enabled: true}
	l.Write(WriteRequest{Event: "a", ActorID: "x"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		fi, err := os.Stat(path)
		if err == nil && fi.Size() > 0 {
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("perm = %v", fi.Mode().Perm())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("entry never written")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
