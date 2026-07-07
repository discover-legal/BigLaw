// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package urlguard validates caller-supplied outbound URLs against SSRF: it
// rejects any URL whose host is a private, loopback, link-local, unspecified,
// or otherwise internal address. It is the single source of truth for this
// check across the codebase (webhook egress, connector endpoints, settings).
package urlguard

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	rfc1918_172 = regexp.MustCompile(`^172\.(1[6-9]|2\d|3[01])\.`)
	cgnat100_64 = regexp.MustCompile(`^100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.`)
	hexIPv4     = regexp.MustCompile(`^0x[0-9a-f]+$`)
	decimalIPv4 = regexp.MustCompile(`^\d+$`)
)

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

// isPrivateHost reports whether h is a loopback/private/link-local/unspecified
// or obfuscated-internal host (hex/decimal IPv4, IPv4-mapped IPv6, ULA, etc.).
func isPrivateHost(h string) bool {
	return h == "localhost" ||
		h == "::1" || // IPv6 loopback
		h == "::" || // IPv6 unspecified
		h == "0.0.0.0" || // IPv4 unspecified
		strings.HasPrefix(h, "0.") || // 0.0.0.0/8 "this network"
		hexIPv4.MatchString(h) || // hex-encoded IPv4 (e.g. 0x7f000001)
		decimalIPv4.MatchString(h) || // decimal-encoded IPv4 (e.g. 2130706433)
		strings.HasPrefix(h, "127.") ||
		strings.HasPrefix(h, "169.254.") ||
		strings.HasPrefix(h, "10.") ||
		rfc1918_172.MatchString(h) ||
		strings.HasPrefix(h, "192.168.") ||
		cgnat100_64.MatchString(h) || // 100.64.0.0/10 IANA shared address space
		strings.HasPrefix(h, "::ffff:") || // IPv4-mapped IPv6
		strings.HasPrefix(h, "fc00:") ||
		strings.HasPrefix(h, "fe80:")
}
