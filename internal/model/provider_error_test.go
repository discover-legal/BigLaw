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
