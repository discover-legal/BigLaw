// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

// TestNoForbiddenVendorSDKs is the dependency-level half of the self-imposed
// vendor breaker: the compiled binary must not depend on the Anthropic or
// AWS/Amazon SDKs. Because this test lives in package main, its build info
// covers the whole application's module graph — re-introducing either SDK
// anywhere fails this test, so doing so requires an active, visible effort
// (deleting or editing this guard).
func TestNoForbiddenVendorSDKs(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("build info unavailable")
	}
	forbidden := []string{
		"github.com/anthropics/anthropic-sdk-go",
		"github.com/aws/aws-sdk-go",   // covers aws-sdk-go and aws-sdk-go-v2
		"github.com/aws/smithy-go",    // aws-sdk-go-v2 core runtime
		"github.com/aws/aws-sdk-go-v2",
	}
	for _, dep := range info.Deps {
		for _, bad := range forbidden {
			if strings.HasPrefix(dep.Path, bad) {
				t.Errorf("forbidden vendor SDK dependency present: %s\n"+
					"This build self-imposes no Anthropic/AWS dependency. If this is intentional, "+
					"remove this guard deliberately.", dep.Path)
			}
		}
	}
}
