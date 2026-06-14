// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package config

import (
	"strings"
	"testing"
)

func qwenStack() *Config {
	c := &Config{}
	c.Model = ModelConfig{
		Stack: "qwen", PrimaryURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		Heavy: "qwen-max", Mid: "qwen-plus", Light: "qwen-turbo", Vision: "qwen-vl-max",
	}
	c.Blob.Backend = "disk"
	return c
}

func TestGuardAllowsCleanQwen(t *testing.T) {
	if err := GuardVendors(qwenStack()); err != nil {
		t.Errorf("clean Qwen config should pass, got: %v", err)
	}
}

// A Claude model reached through a non-Anthropic wrapper (e.g. OpenRouter) is
// allowed — the breaker keys on the endpoint, not the model name.
func TestGuardAllowsClaudeViaWrapper(t *testing.T) {
	c := qwenStack()
	c.Model.PrimaryURL = "https://openrouter.ai/api/v1"
	c.Model.Heavy = "anthropic/claude-3.7-sonnet"
	if err := GuardVendors(c); err != nil {
		t.Errorf("Claude via a non-Anthropic wrapper should be allowed, got: %v", err)
	}
}

// Pointing an endpoint directly at Anthropic's own API trips the breaker.
func TestGuardTripsOnAnthropicEndpoint(t *testing.T) {
	c := qwenStack()
	c.Model.PrimaryURL = "https://api.anthropic.com/v1"
	err := GuardVendors(c)
	if err == nil || !strings.Contains(err.Error(), "Anthropic") {
		t.Fatalf("expected the direct Anthropic endpoint to trip, got: %v", err)
	}
	// Active opt-in disarms it.
	t.Setenv("ALLOW_ANTHROPIC", "1")
	if err := GuardVendors(c); err != nil {
		t.Errorf("ALLOW_ANTHROPIC=1 should disarm, got: %v", err)
	}
}

func TestGuardTripsOnAWSEndpoint(t *testing.T) {
	c := qwenStack()
	c.Local.LocalInferenceURL = "https://bedrock-runtime.us-east-1.amazonaws.com"
	err := GuardVendors(c)
	if err == nil || !strings.Contains(err.Error(), "AWS") {
		t.Fatalf("expected AWS breaker to trip, got: %v", err)
	}
	t.Setenv("ALLOW_AWS", "1")
	if err := GuardVendors(c); err != nil {
		t.Errorf("ALLOW_AWS=1 should disarm, got: %v", err)
	}
}

func TestGuardTripsOnS3Backend(t *testing.T) {
	c := qwenStack()
	c.Blob.Backend = "s3"
	if err := GuardVendors(c); err == nil || !strings.Contains(err.Error(), "AWS") {
		t.Fatalf("expected BLOB_BACKEND=s3 to trip the AWS breaker, got: %v", err)
	}
}

// The open, non-Amazon object stores must pass the breaker.
func TestGuardAllowsOpenBlobBackends(t *testing.T) {
	for _, backend := range []string{"disk", "webdav", "supabase", "oci"} {
		c := qwenStack()
		c.Blob.Backend = backend
		if err := GuardVendors(c); err != nil {
			t.Errorf("BLOB_BACKEND=%s should be allowed, got: %v", backend, err)
		}
	}
}
