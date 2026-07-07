// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tests for the signed session cookie: round-trip, tamper rejection,
// wrong-secret rejection, expiry, revocation, and the OAuth state cookie.

package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

const testSecret = "unit-test-secret"

func testUser() types.SessionUser {
	return types.SessionUser{
		ProfileID: "p-123",
		Name:      "Ada Counsel",
		Email:     "ada@firm.example",
		Role:      types.RolePartner,
		Mode:      types.ModeAdmin,
	}
}

func TestSessionRoundTrip(t *testing.T) {
	value := MintSession(testSecret, testUser(), time.Hour)
	got := ParseSession(testSecret, value)
	if got == nil {
		t.Fatal("expected valid session, got nil")
	}
	want := testUser()
	if *got != want {
		t.Errorf("round-trip mismatch: got %+v want %+v", *got, want)
	}
}

func TestSessionTamperedSignatureRejected(t *testing.T) {
	value := MintSession(testSecret, testUser(), time.Hour)

	// Flip a character in the signature half.
	dot := strings.LastIndex(value, ".")
	sig := []byte(value[dot+1:])
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	if ParseSession(testSecret, value[:dot+1]+string(sig)) != nil {
		t.Error("tampered signature accepted")
	}

	// Tamper with the payload half but keep the original signature.
	if ParseSession(testSecret, "e30"+value[3:]) != nil {
		t.Error("tampered payload accepted")
	}

	// Garbage values must not parse.
	if ParseSession(testSecret, "not-a-cookie") != nil {
		t.Error("malformed value accepted")
	}
}

func TestSessionWrongSecretRejected(t *testing.T) {
	value := MintSession(testSecret, testUser(), time.Hour)
	if ParseSession("a-different-secret", value) != nil {
		t.Error("session signed with another secret accepted")
	}
}

func TestSessionExpiredRejected(t *testing.T) {
	value := MintSession(testSecret, testUser(), -time.Second)
	if ParseSession(testSecret, value) != nil {
		t.Error("expired session accepted")
	}
}

func TestSessionRevocation(t *testing.T) {
	value := MintSession(testSecret, testUser(), time.Hour)
	RevokeSession(testSecret, value)
	if ParseSession(testSecret, value) != nil {
		t.Error("revoked session accepted")
	}
}

func TestStateSignVerify(t *testing.T) {
	signed := SignState(testSecret, "state-abc")
	got, ok := VerifyState(testSecret, signed)
	if !ok || got != "state-abc" {
		t.Errorf("state round-trip failed: got %q ok=%v", got, ok)
	}
	if _, ok := VerifyState("other-secret", signed); ok {
		t.Error("state signed with another secret accepted")
	}
}
