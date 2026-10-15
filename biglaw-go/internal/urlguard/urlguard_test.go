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

// Every internal-address spelling found in review — and the rest of the
// inet_aton / IPv6 family — must be rejected by the validator.
func TestValidatorRejectsInternalAddressesInAnyEncoding(t *testing.T) {
	old := lookup
	lookup = staticResolver{net.ParseIP("127.0.0.1")} // what localtest.me resolves to
	defer func() { lookup = old }()
	for _, raw := range []string{
		"http://[fd00::1]", "http://[fc00::1]", "http://[fe80::1%25eth0]", "http://[fec0::1]",
		"http://0x7f.0.0.1", "http://0177.0.0.1", "http://127.1", "http://0x7f.1",
		"http://2130706433", "http://017700000001", "http://0", "http://0.0.0.0",
		"http://[::ffff:7f00:1]", "http://[::127.0.0.1]", "http://[64:ff9b::a00:1]",
		"http://[2002:a00:1::]", "http://[2002:7f00:1::]", "http://100.64.0.1",
		"http://169.254.169.254", "http://[::ffff:169.254.169.254]",
		"http://localtest.me", "http://metadata.google.internal", "http://metadata.google.internal.",
		"http://foo.localhost", "http://printer.local", "http://docuseal",
		"http://224.0.0.1",
	} {
		if _, err := AssertPublicHTTP(raw, "test"); err == nil {
			t.Errorf("AssertPublicHTTP(%q) accepted an internal address", raw)
		}
	}
}

func TestValidatorAcceptsPublicAddresses(t *testing.T) {
	old := lookup
	lookup = staticResolver{net.ParseIP("93.184.216.34")}
	defer func() { lookup = old }()
	for _, raw := range []string{
		"https://example.com", "https://8.8.8.8", "https://0x8.0x8.0x8.0x8",
		"https://[2606:4700:4700::1111]", "https://[2002:808:808::]", "http://100.63.255.255",
	} {
		if _, err := AssertPublicHTTP(raw, "test"); err != nil {
			t.Errorf("AssertPublicHTTP(%q) = %v, want accepted", raw, err)
		}
	}
}

// A hostname that resolves to any private address is rejected at validation
// time; an unresolvable one is left to the dial-time guard.
func TestValidatorResolvesHostnames(t *testing.T) {
	old := lookup
	defer func() { lookup = old }()
	lookup = staticResolver{net.ParseIP("93.184.216.34"), net.ParseIP("10.1.2.3")}
	if _, err := AssertPublicHTTPS("https://rebind.example.com", "t"); err == nil {
		t.Error("mixed public/private DNS answer accepted")
	}
	lookup = failingResolver{}
	if _, err := AssertPublicHTTPS("https://offline.example.com", "t"); err != nil {
		t.Errorf("unresolvable host rejected at validation: %v", err)
	}
}

type failingResolver struct{}

func (failingResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
}
