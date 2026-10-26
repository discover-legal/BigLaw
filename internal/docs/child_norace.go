// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build !race

package docs

// childEnv is the reader child's whole environment: none, so it never holds the server's secrets
// (child_race.go: what a race-detector test build passes instead).
var childEnv = []string{}
