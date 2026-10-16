// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/discover-legal/biglaw-go/internal/config"
)

// tightenStatePerms brings state files written by older builds (0644) to
// 0600 at startup. Writers create new files 0600, but a file that is only
// read at boot, or appended to, keeps the mode it was created with until
// then; client data must not stay world-readable across an upgrade. Symlinks
// are followed (the target is tightened). Owner process only.
func tightenStatePerms(cfg *config.Config) {
	p := cfg.Persistence
	files := []string{
		p.TasksFile, p.SettingsFile, p.ProfilesFile, p.ClientsFile, p.TimeFile,
		p.TimeFile + ".migrations.json", p.LearningFile, p.OcgFile, p.JobsFile,
		p.PreBillsFile, p.CostFile, p.PlaybooksFile, p.ClientVoiceFile,
		cfg.Audit.LogFile, cfg.LPM.CorpusFile, cfg.LPM.RoutedFile, cfg.LPM.PendingFile,
		cfg.LPM.BackfillCursorFile, cfg.Monitors.DocketsFile,
	}
	if db := cfg.Database.SQLitePath; db != "" && cfg.Database.Backend == "sqlite" {
		files = append(files, db, db+"-wal", db+"-shm", db+"-journal")
	}
	if f := os.Getenv("CLIO_TOKENS_FILE"); f != "" {
		files = append(files, f)
	}
	n := 0
	for _, f := range files {
		if f != "" && tighten(f) {
			n++
		}
	}
	// Rendered status reports (json/md/docx per matter).
	if dir := cfg.LPM.ReportDir; dir != "" {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() && tighten(path) {
				n++
			}
			return nil
		})
	}
	if n > 0 {
		slog.Info("state files tightened to 0600 (written by an older build)", "count", n)
	}
}

// tighten sets a regular file readable by group/other to 0600.
func tighten(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 == 0 {
		return false
	}
	if err := os.Chmod(path, 0o600); err != nil {
		slog.Warn("could not tighten state file to 0600", "path", path, "err", err)
		return false
	}
	return true
}
