// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// bench7: the endpoint's 400 said why ("Your credit balance is too low"), and BigLaw said only "HTTP
// 400". The provider's error type and message reach the error, bounded, with the key and anything
// key-, bearer- or URL-shaped redacted.
func TestProviderErrorTypeAndMessage(t *testing.T) {
	const key = "sk-ant-test-0123456789abcdefghijklmnop"
	for _, c := range []struct{ body, want, not string }{
		{`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`,
			"HTTP 400 (invalid_request_error: Your credit balance is too low to access the Anthropic API.)", ""},
		{`{"error":{"message":"bad key ` + key + ` at https://api.example.com/v1?k=1 with Bearer abc.def","type":"invalid_request_error","code":null}}`,
			"HTTP 400 (invalid_request_error: bad key [redacted] at [redacted] with [redacted])", key},
		{`{"error":{"type":"Weird Type!","message":"` + strings.Repeat("x ", 300) + `"}}`, "HTTP 400 (x x", "Weird"},
		{`not json at all ` + key, "HTTP 400", key},
	} {
		cl, _ := endpoint(t, key, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			w.Write([]byte(c.body))
		})
		_, err := cl.Chat(context.Background(), Request{Agent: "lead", Role: Lead, Messages: []Message{{Role: "user", Content: "hi"}}})
		var me *Error
		if !errors.As(err, &me) || !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), c.want) || c.not != "" && strings.Contains(err.Error(), c.not) || len(me.Detail) > 300 {
			t.Errorf("%q: %v", c.body[:min(40, len(c.body))], err)
		}
	}
}

// bench7 r1 (verifier LOW-1): one redactor over all of what is surfaced: a key echoed as the error's
// type or code, a masked key's tail, a host with no scheme, base64 key material.
func TestProviderErrorRedactsTypeCodeTailsAndHosts(t *testing.T) {
	const key = "a1b2c3d4e5f60718293a4b5c6d7e8f90" // 32 lowercase hex, starts with a letter
	for _, c := range []struct{ name, body, leak string }{
		{"key as type", `{"error":{"type":"` + key + `","message":"denied"}}`, key},
		{"key as code", `{"error":{"code":"` + key + `","message":"denied"}}`, key},
		{"masked tail", `{"error":{"message":"Incorrect API key provided: sk-proj-abc1********************wxyz. You can find your API key at https://platform.openai.com/account/api-keys.","type":"invalid_request_error"}}`, "wxyz"},
		{"host without scheme", `{"error":{"type":"proxy_error","message":"upstream gpu-7.internal.corp:8443 refused"}}`, "gpu-7.internal.corp"},
		{"base64", `{"error":{"type":"auth_error","message":"token dGhpcyBpcyBhIHNlY3JldCBrZXk+/forbidden/9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b rejected"}}`, "dGhpcyBpcyBhIHNlY3JldCBrZXk"},
	} {
		cl, _ := endpoint(t, key, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(403)
			w.Write([]byte(c.body))
		})
		_, err := cl.Chat(context.Background(), Request{Agent: "lead", Role: Lead, Messages: []Message{{Role: "user", Content: "hi"}}})
		if err == nil || strings.Contains(err.Error(), c.leak) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// TestRedactKeepsPathsHidesCredentials (r2 L1): a request-validation error keeps the path it names
// ("messages.0.content", "tools.0.custom.input_schema") and model ids, while Basic-auth values, IP
// addresses with ports, short key fragments and key= values are hidden.
func TestRedactKeepsPathsHidesCredentials(t *testing.T) {
	const key = "sk-ant-api03-AbCdEfGh0123456789_xyzXYZ-abcdefghijklmnop"
	for _, c := range []struct{ msg, keep, leak string }{
		{"messages.0.content: Field required", "messages.0.content: Field required", ""},
		{"tools.0.custom.input_schema: JSON schema is invalid", "tools.0.custom.input_schema: JSON schema is invalid", ""},
		{"max_tokens: 10000 > 8192, the maximum for claude-haiku-4-5-20251001", "max_tokens: 10000 > 8192, the maximum for claude-haiku-4-5-20251001", ""},
		{"model: claude-3.5-sonnet", "model: claude-3.5-sonnet", ""},
		{"The model gpt-4.1-mini does not exist, e.g. check your org.", "gpt-4.1-mini does not exist, e.g. check", ""},
		{"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing.", "credit balance is too low", ""},
		{"key " + key[:20] + "... rejected", "rejected", key[:12]},
		{"key " + strings.ToUpper(key) + " rejected", "rejected", "ABCDEFGH"},
		{"Authorization: Basic dXNlcjpwYXNzd29yZA== refused", "refused", "dXNlcjpwYXNzd29yZA"},
		{"sent basic dXNlcjpwYXNzd29yZA== refused", "refused", "dXNlcjpwYXNzd29yZA"},
		{"got Bearer abc123 (truncated)", "truncated", "abc123"},
		{"x-api-key header is required; got key=AbCdEf012345 (truncated)", "x-api-key header is required", "AbCdEf012345"},
		{"upstream 10.0.3.7:8443 refused", "refused", "10.0.3.7"},
		{"upstream gpu-7.internal.corp:8443 refused", "refused", "internal.corp"},
		{"see api.example.com for status", "for status", "example.com"},
	} {
		got := redact(c.msg, key)
		if !strings.Contains(got, c.keep) || c.leak != "" && strings.Contains(got, c.leak) {
			t.Errorf("redact(%q) = %q: want %q kept and %q hidden", c.msg, got, c.keep, c.leak)
		}
	}
}
