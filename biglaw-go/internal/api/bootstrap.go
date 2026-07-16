// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"log/slog"

	"github.com/discover-legal/biglaw-go/internal/blob"
	"github.com/discover-legal/biglaw-go/internal/config"
)

func newBlobStore(cfg *config.Config) blob.Store {
	bs, err := blob.Open(cfg.Blob)
	if err != nil {
		slog.Warn("attachment blob store unavailable; originals will not be retained",
			"backend", cfg.Blob.Backend, "err", err)
		return nil
	}
	slog.Info("attachment blob store ready", "backend", bs.Backend())
	return bs
}
