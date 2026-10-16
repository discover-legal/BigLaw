// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package urlguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fec0::/10"), // deprecated site-local
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64 local-use (RFC 8215)
	netip.MustParsePrefix("2001::/32"),      // Teredo: tunnels to an embedded IPv4
}

type resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// NewPublicClient returns an HTTP client that validates DNS answers at dial
// time, dials the validated address directly, and revalidates redirects.
func NewPublicClient(timeout time.Duration, requireHTTPS bool) *http.Client {
	return newPublicClient(timeout, requireHTTPS, net.DefaultResolver)
}

func newPublicClient(timeout time.Duration, requireHTTPS bool, r resolver) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("urlguard: invalid dial address: %w", err)
		}
		ips, err := r.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("urlguard: resolve %s: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("urlguard: %s resolved to no addresses", host)
		}
		for _, resolved := range ips {
			if !isPublicIP(resolved.IP) {
				return nil, fmt.Errorf("urlguard: %s resolved to a private or reserved address", host)
			}
		}
		var lastErr error
		for _, resolved := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("urlguard: too many redirects")
			}
			_, err := assertPublic(req.URL.String(), "redirect URL", requireHTTPS)
			return err
		},
	}
}

// isPublicIP reports whether ip is a globally routable unicast address.
// IPv4 embedded in IPv6 — mapped (::ffff:a.b.c.d), compatible (::a.b.c.d),
// 6to4 (2002:AABB:CCDD::) and NAT64 (64:ff9b::a.b.c.d) — is judged by the
// embedded IPv4 address, so a private target can't hide behind an IPv6 form.
func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if addr.Is6() {
		b := addr.As16()
		switch {
		case nat64.Contains(addr), ipv4Compatible.Contains(addr) && addr != netip.IPv6Unspecified() && addr != netip.IPv6Loopback():
			return isPublicIP(net.IP(b[12:16]))
		case sixToFour.Contains(addr):
			return isPublicIP(net.IP(b[2:6]))
		}
	}
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	ip = net.IP(addr.AsSlice())
	return ip.IsGlobalUnicast() && !ip.IsPrivate() &&
		!ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() &&
		!ip.IsMulticast()
}

var (
	nat64          = netip.MustParsePrefix("64:ff9b::/96")
	ipv4Compatible = netip.MustParsePrefix("::/96")
	sixToFour      = netip.MustParsePrefix("2002::/16")
)
