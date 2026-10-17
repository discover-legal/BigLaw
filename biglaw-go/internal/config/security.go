// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import (
	"fmt"
	"strings"
)

// MinIntakeSecretLen is the minimum length of each INTAKE_HMAC_SECRET entry.
const MinIntakeSecretLen = 32

// ValidateSecurity rejects authentication settings that would allow session
// forgery or let a shared bearer credential choose its own principal, and
// guessable intake signing secrets.
func ValidateSecurity(cfg *Config) error {
	// INTAKE_HMAC_SECRET may list several comma-separated secrets (key
	// rotation); every one must be long enough to resist guessing.
	for _, s := range strings.Split(cfg.Intake.HMACSecret, ",") {
		if s = strings.TrimSpace(s); s != "" && len(s) < MinIntakeSecretLen {
			return fmt.Errorf("config: every INTAKE_HMAC_SECRET entry must be at least %d characters (generate with: openssl rand -hex 32)", MinIntakeSecretLen)
		}
	}
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
