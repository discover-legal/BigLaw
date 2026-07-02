// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package urlguard

import "testing"

func TestAssertPublicHTTPS(t *testing.T) {
	reject := []string{
		"http://example.com",                // not https
		"https://localhost",                 // loopback name
		"https://127.0.0.1",                 // loopback IPv4
		"https://10.0.0.5",                  // RFC1918
		"https://192.168.1.1",               // RFC1918
		"https://172.16.0.1",                // RFC1918
		"https://169.254.169.254",           // link-local / cloud metadata
		"https://100.64.0.1",                // CGNAT
		"https://0x7f000001",                // hex-encoded loopback
		"https://2130706433",                // decimal-encoded loopback
		"https://[::1]",                     // IPv6 loopback
		"https://[::ffff:127.0.0.1]",        // IPv4-mapped IPv6 loopback
		"ftp://example.com",                 // wrong scheme
		"not a url",                         // unparseable / no host
	}
	for _, raw := range reject {
		if _, err := AssertPublicHTTPS(raw, "test"); err == nil {
			t.Errorf("AssertPublicHTTPS(%q) = nil error, want rejection", raw)
		}
	}

	accept := []string{
		"https://example.com",
		"https://my-tenant.webhook.office.com/webhookb2/abc",
		"https://hooks.slack.com/services/T/B/x",
	}
	for _, raw := range accept {
		if _, err := AssertPublicHTTPS(raw, "test"); err != nil {
			t.Errorf("AssertPublicHTTPS(%q) = %v, want accepted", raw, err)
		}
	}
}

func TestAssertPublicHTTPAllowsHTTP(t *testing.T) {
	if _, err := AssertPublicHTTP("http://example.com", "test"); err != nil {
		t.Errorf("AssertPublicHTTP should allow public http, got %v", err)
	}
	if _, err := AssertPublicHTTP("http://127.0.0.1", "test"); err == nil {
		t.Errorf("AssertPublicHTTP must still reject loopback")
	}
}
