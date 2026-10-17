// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package version is the single source of truth for the BigLaw release
// version reported by GET /health, the MCP server handshake and the CLI.
//
// Release builds stamp it at link time:
//
//	go build -ldflags "-X github.com/discover-legal/biglaw-go/internal/version.Version=1.2.3" ./cmd/biglaw
//
// (`make build VERSION=1.2.3`, or `docker build --build-arg VERSION=1.2.3`).
// Unstamped builds report the release line below.
package version

// Version is a var, not a const, so -ldflags -X can override it.
var Version = "1.0.0"
