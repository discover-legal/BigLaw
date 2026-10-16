// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package bots

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// signTeams signs a body exactly as Microsoft Teams does for an Outgoing
// Webhook: key = base64-decode(security token); header = "HMAC " +
// base64(HMAC-SHA256(key, body)).
func signTeams(token, body string) string {
	key, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		panic(err)
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(body))
	return "HMAC " + base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func TestVerifyTeamsSignature(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	body := `{"type":"message","text":"<at>BigMichael</at> help"}`
	good := signTeams(token, body)

	// The pre-fix implementation keyed the HMAC with the token's ASCII
	// bytes; such a signature must NOT verify.
	legacy := hmac.New(sha256.New, []byte(token))
	legacy.Write([]byte(body))
	legacyHdr := "HMAC " + base64.StdEncoding.EncodeToString(legacy.Sum(nil))

	cases := []struct {
		name, body, header, secret string
		want                       bool
	}{
		{"spec-signed", body, good, token, true},
		{"lowercase scheme", body, "hmac " + good[5:], token, true},
		{"token with surrounding whitespace", body, good, " " + token + "\n", true},
		{"tampered body", body + " ", good, token, false},
		{"wrong token", body, good, base64.StdEncoding.EncodeToString([]byte("another-key")), false},
		{"raw-token-keyed (pre-fix) signature", body, legacyHdr, token, false},
		{"missing scheme", body, good[5:], token, false},
		{"bearer scheme", body, "Bearer " + good[5:], token, false},
		{"garbage signature", body, "HMAC !!!", token, false},
		{"empty header", body, "", token, false},
		{"non-base64 token", body, good, "not base64 !!", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyTeamsSignature(tc.body, tc.header, tc.secret); got != tc.want {
				t.Errorf("VerifyTeamsSignature = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHandleTeamsWebhookSignedPerSpec(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("teams-security-token-bytes"))
	t.Setenv("TEAMS_WEBHOOK_SECRET", token)
	body := `{"type":"message","text":"<at>BigMichael</at> help"}`
	if _, status, _ := HandleTeamsWebhook(body, signTeams(token, body), nil); status != 200 {
		t.Fatalf("spec-signed request: status %d, want 200", status)
	}
	if _, status, _ := HandleTeamsWebhook(body, "HMAC AAAA", nil); status != 401 {
		t.Fatalf("bad signature: status %d, want 401", status)
	}
}
