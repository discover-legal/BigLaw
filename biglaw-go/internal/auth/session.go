// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Stateless signed session cookies. The session is a JSON payload
// (SessionUser + jti + expiry) → base64url → HMAC-SHA256 signature keyed on
// cfg.Auth.SessionSecret — no server-side session store. Logout revokes the
// per-session token ID (jti) in an in-process set; the set clears on restart,
// which is acceptable for the 12-hour session lifetime (restarting the server
// is a natural fence). Also signs the short-lived OAuth state cookie.

package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	// SessionCookieName holds the signed session payload.
	SessionCookieName = "bm_session"
	// StateCookieName holds the signed OAuth anti-CSRF state during a login.
	StateCookieName = "bm_oauth_state"
	// SessionMaxAge mirrors the TS implementation's 12-hour session lifetime.
	SessionMaxAge = 12 * time.Hour
	// StateMaxAge bounds the login round-trip to the provider.
	StateMaxAge = 10 * time.Minute
)

// sessionPayload is what actually lives inside the cookie. JTI enables
// revocation on logout; Exp is the absolute expiry as a Unix timestamp.
type sessionPayload struct {
	types.SessionUser
	JTI string `json:"jti"`
	Exp int64  `json:"exp"`
}

// In-process revocation set keyed on per-session token IDs (jti), capped with
// FIFO eviction so a logout flood cannot grow it unboundedly.
const maxRevokedJTIs = 100_000

var (
	revokedMu    sync.Mutex
	revokedJTIs  = map[string]struct{}{}
	revokedOrder []string
)

func revokeJTI(jti string) {
	if jti == "" {
		return
	}
	revokedMu.Lock()
	defer revokedMu.Unlock()
	if _, dup := revokedJTIs[jti]; dup {
		return
	}
	if len(revokedOrder) >= maxRevokedJTIs {
		delete(revokedJTIs, revokedOrder[0])
		revokedOrder = revokedOrder[1:]
	}
	revokedJTIs[jti] = struct{}{}
	revokedOrder = append(revokedOrder, jti)
}

func isRevoked(jti string) bool {
	revokedMu.Lock()
	defer revokedMu.Unlock()
	_, ok := revokedJTIs[jti]
	return ok
}

// MintSession builds a signed session cookie value for u, valid for ttl.
func MintSession(secret string, u types.SessionUser, ttl time.Duration) string {
	payload := sessionPayload{
		SessionUser: u,
		JTI:         uuid.New().String(),
		Exp:         time.Now().Add(ttl).Unix(),
	}
	raw, _ := json.Marshal(payload)
	return signValue(secret, raw)
}

// ParseSession verifies a session cookie value and returns the SessionUser,
// or nil when the signature is invalid, the session has expired, or the
// token was revoked by logout.
func ParseSession(secret, value string) *types.SessionUser {
	raw, ok := verifyValue(secret, value)
	if !ok {
		return nil
	}
	var payload sessionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	if payload.Exp == 0 || time.Now().Unix() > payload.Exp {
		return nil // expired
	}
	if isRevoked(payload.JTI) {
		return nil
	}
	u := payload.SessionUser
	return &u
}

// RevokeSession marks the cookie's jti as revoked so replayed copies of the
// cookie are rejected for the rest of the session lifetime. Only validly
// signed cookies are revoked — a forged value cannot fill the set.
func RevokeSession(secret, value string) {
	raw, ok := verifyValue(secret, value)
	if !ok {
		return
	}
	var payload sessionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	revokeJTI(payload.JTI)
}

// SignState signs a short-lived plain string (the OAuth state cookie).
func SignState(secret, state string) string {
	return signValue(secret, []byte(state))
}

// VerifyState verifies a signed state cookie and returns the original value.
func VerifyState(secret, signed string) (string, bool) {
	raw, ok := verifyValue(secret, signed)
	if !ok {
		return "", false
	}
	return string(raw), true
}

// ─── Signing primitives ──────────────────────────────────────────────────────
// Cookie value format: base64url(payload) + "." + base64url(hmac-sha256(b64)).

func signValue(secret string, raw []byte) string {
	b64 := base64.RawURLEncoding.EncodeToString(raw)
	return b64 + "." + base64.RawURLEncoding.EncodeToString(sign(secret, b64))
}

func verifyValue(secret, value string) ([]byte, bool) {
	b64, sigB64, found := strings.Cut(value, ".")
	if !found {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, false
	}
	if subtle.ConstantTimeCompare(sig, sign(secret, b64)) != 1 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, false
	}
	return raw, true
}

func sign(secret, b64 string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(b64))
	return mac.Sum(nil)
}
