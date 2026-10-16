// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
)

func TestTightenStatePerms(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Persistence.TimeFile = filepath.Join(dir, "time.json")
	cfg.Persistence.PreBillsFile = filepath.Join(dir, "real-prebills.json")
	cfg.Audit.LogFile = filepath.Join(dir, "audit.jsonl")
	cfg.LPM.ReportDir = filepath.Join(dir, "reports")
	cfg.Database.Backend, cfg.Database.SQLitePath = "sqlite", filepath.Join(dir, "biglaw.db")
	report := filepath.Join(cfg.LPM.ReportDir, "M-1", "2026-10-16.docx")
	_ = os.MkdirAll(filepath.Dir(report), 0o755)
	loose := []string{cfg.Persistence.TimeFile, cfg.Persistence.PreBillsFile, cfg.Audit.LogFile, report,
		cfg.Database.SQLitePath + "-wal"}
	for _, f := range loose {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A symlinked store path: the target is tightened.
	link := filepath.Join(dir, "prebills-link.json")
	_ = os.Symlink(cfg.Persistence.PreBillsFile, link)
	cfg.Persistence.PreBillsFile = link

	tightenStatePerms(cfg)
	for _, f := range loose {
		if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s perm = %v", filepath.Base(f), fi.Mode().Perm())
		}
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced")
	}
}
