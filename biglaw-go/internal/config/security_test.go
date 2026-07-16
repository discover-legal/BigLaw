// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import "testing"

func TestValidateSecurity(t *testing.T) {
	valid := &Config{}
	valid.Auth.Enabled = true
	valid.Auth.SessionSecret = "0123456789abcdef0123456789abcdef"
	valid.API.APIKey = "abcdef0123456789abcdef0123456789"
	valid.API.ProfileID = "profile-1"
	if err := ValidateSecurity(valid); err != nil {
		t.Fatalf("valid security config rejected: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"default secret": func(c *Config) { c.Auth.SessionSecret = DefaultSessionSecret },
		"short secret":   func(c *Config) { c.Auth.SessionSecret = "too-short" },
		"missing key":    func(c *Config) { c.API.APIKey = "" },
		"unbound key":    func(c *Config) { c.API.ProfileID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := *valid
			mutate(&cfg)
			if err := ValidateSecurity(&cfg); err == nil {
				t.Fatal("insecure configuration was accepted")
			}
		})
	}
}
