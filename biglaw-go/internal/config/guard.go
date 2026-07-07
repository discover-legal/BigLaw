// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Self-imposed vendor breaker. BigLaw concentrates support and compatibility on
// open, privacy-respecting vendors and gates high-risk closed ones. This guard
// trips at startup if the resolved configuration couples the running system
// DIRECTLY to Anthropic's own API or to AWS/Amazon, and refuses to start.
//
// It keys on the endpoint, not the model name: a Claude model reached through a
// third-party OpenAI-compatible wrapper (OpenRouter, LiteLLM, a gateway) is
// allowed — that depends on the wrapper, not on Anthropic. Likewise non-Amazon
// object storage is fine; only AWS S3 (the Amazon service) is gated.
//
// Using a gated vendor directly is possible but must be an ACTIVE, explicit
// effort: set ALLOW_ANTHROPIC=1 or ALLOW_AWS=1 to disarm the respective breaker.
package config

import (
	"fmt"
	"os"
	"strings"
)

// vendorOptIn reports whether an explicit override env var is set truthy.
func vendorOptIn(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// anthropicCoupling returns descriptions of every config value that would couple
// the system DIRECTLY to Anthropic's own API. It deliberately keys on the
// ENDPOINT, not the model name: a Claude model reached through a third-party
// OpenAI-compatible wrapper (OpenRouter, LiteLLM, a gateway) depends on that
// wrapper, not on Anthropic, and is allowed — Claude stays an option, just not
// the default and not a direct dependency.
func anthropicCoupling(c *Config) []string {
	var hits []string
	for _, u := range []string{c.Model.PrimaryURL, c.Local.LocalInferenceURL, c.Local.OllamaURL} {
		if strings.Contains(strings.ToLower(u), "anthropic.com") {
			hits = append(hits, "endpoint "+u)
		}
	}
	return hits
}

// awsCoupling returns descriptions of every config value pointing at AWS/Amazon.
func awsCoupling(c *Config) []string {
	var hits []string
	for _, u := range []string{c.Model.PrimaryURL, c.Local.LocalInferenceURL, c.Local.OllamaURL} {
		lu := strings.ToLower(u)
		if strings.Contains(lu, "amazonaws.com") || strings.Contains(lu, "bedrock") {
			hits = append(hits, "endpoint "+u)
		}
	}
	if strings.EqualFold(strings.TrimSpace(c.Blob.Backend), "s3") {
		hits = append(hits, "BLOB_BACKEND=s3")
	}
	return hits
}

// GuardVendors trips the self-imposed breaker. It returns a non-nil error when
// the config couples to Anthropic or AWS without the matching opt-in, so the
// caller can refuse to start. The error names exactly what tripped and how to
// override.
func GuardVendors(c *Config) error {
	var problems []string
	if hits := anthropicCoupling(c); len(hits) > 0 && !vendorOptIn("ALLOW_ANTHROPIC") {
		problems = append(problems, fmt.Sprintf(
			"configuration is coupled to Anthropic/Claude [%s], a gated high-risk closed vendor; overriding this gate is deliberate (see internal/config/guard.go)",
			strings.Join(hits, "; ")))
	}
	if hits := awsCoupling(c); len(hits) > 0 && !vendorOptIn("ALLOW_AWS") {
		problems = append(problems, fmt.Sprintf(
			"configuration is coupled to AWS/Amazon [%s], a gated high-risk closed vendor; overriding this gate is deliberate (see internal/config/guard.go)",
			strings.Join(hits, "; ")))
	}
	if len(problems) > 0 {
		return fmt.Errorf("vendor breaker tripped — BigLaw concentrates on open, privacy-respecting vendors: %s", strings.Join(problems, " || "))
	}
	return nil
}

// DisarmedVendorBreakers reports which breakers an operator has actively
// disarmed (for a prominent startup warning). Empty when both are armed.
func DisarmedVendorBreakers() []string {
	var out []string
	if vendorOptIn("ALLOW_ANTHROPIC") {
		out = append(out, "ALLOW_ANTHROPIC")
	}
	if vendorOptIn("ALLOW_AWS") {
		out = append(out, "ALLOW_AWS")
	}
	return out
}
