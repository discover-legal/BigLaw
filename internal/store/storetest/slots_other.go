// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

//go:build !unix

package storetest

// takeSlot does not bound tests where flock is unavailable.
func takeSlot(string, int) (func(), error) { return func() {}, nil }
