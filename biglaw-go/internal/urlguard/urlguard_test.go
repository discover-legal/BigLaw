// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package urlguard

import (
	"context"
	"net"
	"testing"
	"time"
)

type staticResolver []net.IP

func (r staticResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	out := make([]net.IPAddr, len(r))
	for i, ip := range r {
		out[i] = net.IPAddr{IP: ip}
	}
	return out, nil
}

func TestAssertPublicHTTPS(t *testing.T) {
	reject := []string{
		"http://example.com",         // not https
		"https://localhost",          // loopback name
		"https://127.0.0.1",          // loopback IPv4
		"https://10.0.0.5",           // RFC1918
		"https://192.168.1.1",        // RFC1918
		"https://172.16.0.1",         // RFC1918
		"https://169.254.169.254",    // link-local / cloud metadata
		"https://100.64.0.1",         // CGNAT
		"https://0x7f000001",         // hex-encoded loopback
		"https://2130706433",         // decimal-encoded loopback
		"https://[::1]",              // IPv6 loopback
		"https://[::ffff:127.0.0.1]", // IPv4-mapped IPv6 loopback
		"ftp://example.com",          // wrong scheme
		"not a url",                  // unparseable / no host
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

func TestPublicClientRejectsPrivateDNSResolution(t *testing.T) {
	client := newPublicClient(time.Second, true, staticResolver{net.ParseIP("127.0.0.1")})
	_, err := client.Get("https://public-looking.example/")
	if err == nil {
		t.Fatal("client accepted a hostname that resolved to loopback")
	}
}

func TestPublicClientRejectsMixedPublicAndPrivateAnswers(t *testing.T) {
	client := newPublicClient(time.Second, true, staticResolver{
		net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.1"),
	})
	_, err := client.Get("https://public-looking.example/")
	if err == nil {
		t.Fatal("client accepted a DNS answer set containing a private address")
	}
}

func TestReservedAddressesAreNotPublic(t *testing.T) {
	for _, raw := range []string{"192.0.2.1", "198.18.0.1", "203.0.113.1", "2001:db8::1"} {
		if isPublicIP(net.ParseIP(raw)) {
			t.Errorf("isPublicIP(%s) = true for reserved address", raw)
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
