// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import "fmt"

// ValidateSecurity rejects authentication settings that would allow session
// forgery or let a shared bearer credential choose its own principal.
func ValidateSecurity(cfg *Config) error {
	if !cfg.Auth.Enabled {
		return nil
	}
	if cfg.Auth.SessionSecret == DefaultSessionSecret || len(cfg.Auth.SessionSecret) < 32 {
		return fmt.Errorf("config: AUTH_ENABLED=true requires a non-default SESSION_SECRET of at least 32 characters")
	}
	if len(cfg.API.APIKey) < 32 || cfg.API.ProfileID == "" {
		return fmt.Errorf("config: AUTH_ENABLED=true requires API_KEY of at least 32 characters and API_PROFILE_ID")
	}
	return nil
}
