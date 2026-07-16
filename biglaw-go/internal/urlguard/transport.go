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
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
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

func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() &&
		!ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
