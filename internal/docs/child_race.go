// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build race

package docs

// childEnv is the reader child's whole environment. Only a race-detector build (tests: a release
// binary is built with CGO_ENABLED=0, and -race needs cgo) passes one variable, so that the child,
// a race-built copy of the test binary, does not sleep ThreadSanitizer's atexit_sleep_ms (a second)
// as it exits after every PDF or .docx it reads. It holds no secret and changes nothing it reads.
var childEnv = []string{"GORACE=atexit_sleep_ms=0"}

// RaceEnabled reports a race-detector build (tests only): its shadow memory and slowdown distort
// memory and timing tests, which read it to skip or relax themselves.
const RaceEnabled = true
