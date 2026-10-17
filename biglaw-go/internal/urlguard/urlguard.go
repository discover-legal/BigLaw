// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package urlguard validates caller-supplied outbound URLs against SSRF: it
// rejects any URL whose host is a private, loopback, link-local, unspecified,
// or otherwise internal address. It is the single source of truth for this
// check across the codebase (webhook egress, connector endpoints, settings).
package urlguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// lookup resolves hostnames during validation; tests swap it.
var lookup resolver = net.DefaultResolver

// resolveTimeout bounds the validation-time DNS lookup.
const resolveTimeout = 3 * time.Second

// AssertPublicHTTPS validates that raw is a public https URL. Returns the
// trimmed URL on success, or an error naming label on failure.
func AssertPublicHTTPS(raw, label string) (string, error) { return assertPublic(raw, label, true) }

// AssertPublicHTTP is AssertPublicHTTPS but also permits plain http.
func AssertPublicHTTP(raw, label string) (string, error) { return assertPublic(raw, label, false) }

func assertPublic(raw, label string, requireHTTPS bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" {
		want := "http or https"
		if requireHTTPS {
			want = "https"
		}
		return "", fmt.Errorf("invalid %s '%s': must be a public %s URL", label, trimmed, want)
	}
	if requireHTTPS {
		if u.Scheme != "https" {
			return "", fmt.Errorf("%s must use https", label)
		}
	} else if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid %s '%s': must be a public http or https URL", label, trimmed)
	}
	if isPrivateHost(strings.ToLower(u.Hostname())) {
		return "", fmt.Errorf("%s '%s' resolves to a private or loopback address", label, trimmed)
	}
	return trimmed, nil
}

// isPrivateHost reports whether h (a URL hostname, lower-cased, brackets
// stripped) names an internal address.
//
// IP literals are parsed canonically — every IPv4 spelling inet_aton accepts
// (hex/octal/decimal parts, 1–4 parts: 0x7f.0.0.1, 0177.1, 2130706433) and
// every IPv6 form (zones, IPv4-mapped/compatible, 6to4 and NAT64 embeddings)
// — and checked with the same isPublicIP rule the dial-time guard uses, so
// ULA (fc00::/7, e.g. fd00::1), link-local, loopback, unspecified, CGNAT and
// reserved ranges are rejected in any encoding.
//
// Hostnames are rejected outright when they are internal by convention
// (localhost, *.localhost, *.local, *.internal — metadata.google.internal —
// *.home.arpa, and single-label intranet names), and otherwise resolved: any
// non-public answer rejects (localtest.me → 127.0.0.1). A lookup failure does
// not reject — the name may simply not resolve from here — because the
// authoritative check is at dial time (NewPublicClient), which also defeats
// DNS rebinding between validation and use.
func isPrivateHost(h string) bool {
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	if h == "" {
		return true
	}
	if ip, ok := parseIPLiteral(h); ok {
		return !isPublicIP(net.IP(ip.AsSlice()))
	}
	if h == "localhost" || !strings.Contains(h, ".") {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa", ".localdomain", ".lan", ".intranet", ".corp"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	ips, err := lookup.LookupIPAddr(ctx, h)
	if err != nil {
		return false // unresolvable from here; the dial-time guard decides
	}
	for _, a := range ips {
		if !isPublicIP(a.IP) {
			return true
		}
	}
	return false
}

// parseIPLiteral parses h as an IP address in any spelling a resolver or URL
// parser might honour. IPv6 zones are stripped.
func parseIPLiteral(h string) (netip.Addr, bool) {
	if strings.Contains(h, ":") {
		if i := strings.IndexByte(h, '%'); i >= 0 {
			h = h[:i]
		}
		a, err := netip.ParseAddr(h)
		return a, err == nil
	}
	return parseLegacyIPv4(h)
}

// parseLegacyIPv4 implements inet_aton: 1–4 dot-separated parts, each
// decimal, octal (leading 0) or hex (0x); the last part fills the remaining
// bytes. "127.1" → 127.0.0.1, "0x7f.0.0.1" → 127.0.0.1.
func parseLegacyIPv4(h string) (netip.Addr, bool) {
	parts := strings.Split(h, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	vals := make([]uint64, len(parts))
	for i, p := range parts {
		if p == "" {
			return netip.Addr{}, false
		}
		v, err := strconv.ParseUint(p, 0, 32) // base 0: 0x hex, 0 octal, else decimal
		if err != nil {
			return netip.Addr{}, false
		}
		vals[i] = v
	}
	var n uint64
	for i, v := range vals[:len(vals)-1] {
		if v > 255 {
			return netip.Addr{}, false
		}
		n |= v << (24 - 8*i)
	}
	last := vals[len(vals)-1]
	if last >= 1<<(8*(5-len(vals))) {
		return netip.Addr{}, false
	}
	n |= last
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}), true
}
