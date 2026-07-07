// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package settings

import "testing"

func TestAssertPublicHTTPURL(t *testing.T) {
	rejected := []string{
		// Pre-existing blocklist
		"http://localhost:3101",
		"http://127.0.0.1",
		"http://[::1]:8080",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.5",
		"http://172.16.0.1",
		"http://172.31.255.255",
		"http://192.168.1.1",
		"http://[fc00::1]",
		"http://[fe80::1]",
		// Expanded blocklist (port of TS 3428a26 / f9f5bad)
		"http://0.0.0.0",                // IPv4 unspecified
		"http://0.1.2.3",                // 0.0.0.0/8 "this network"
		"http://[::]",                   // IPv6 unspecified
		"http://0x7f000001",             // hex-encoded 127.0.0.1
		"http://0X7F000001",             // hex, upper-case
		"http://2130706433",             // bare-decimal 127.0.0.1
		"http://100.64.0.1",             // 100.64.0.0/10 CGNAT, low edge
		"http://100.127.255.255",        // 100.64.0.0/10 CGNAT, high edge
		"http://[::ffff:127.0.0.1]",     // IPv4-mapped IPv6 loopback
		"http://[::ffff:10.0.0.1]:8080", // IPv4-mapped IPv6 RFC-1918
		// Not http(s) / unparseable
		"ftp://example.com",
		"not a url",
		"",
	}
	for _, raw := range rejected {
		if _, err := assertPublicHTTPURL(raw, "test"); err == nil {
			t.Errorf("assertPublicHTTPURL(%q) = nil error, want rejection", raw)
		}
	}

	accepted := []string{
		"https://example.com",
		"http://example.com:8080/path",
		"https://8.8.8.8",
		"http://100.63.255.255", // just below CGNAT range
		"http://100.128.0.0",    // just above CGNAT range
		"https://api.anthropic.com/v1",
	}
	for _, raw := range accepted {
		if _, err := assertPublicHTTPURL(raw, "test"); err != nil {
			t.Errorf("assertPublicHTTPURL(%q) = %v, want accepted", raw, err)
		}
	}
}
