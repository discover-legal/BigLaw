// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Shared SSRF-safe URL validation for audit sinks.
 *
 * Blocks RFC-1918 private ranges, loopback (127.x / ::1),
 * and link-local (169.254.x) in addition to requiring http/https.
 */

const PRIVATE_HOST_PATTERNS = [
  /^127\./,               // loopback (IPv4)
  /^10\./,                // RFC-1918 class A
  /^172\.(1[6-9]|2\d|3[01])\./, // RFC-1918 class B
  /^192\.168\./,          // RFC-1918 class C
  /^169\.254\./,          // link-local
  /^\[?::1\]?$/i,         // IPv6 loopback
  /^localhost$/i,         // hostname alias
];

export function validateSinkUrl(raw: string, sinkName: string): URL {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    throw new Error(`${sinkName}: invalid URL`);
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") {
    throw new Error(`${sinkName}: only http/https allowed`);
  }
  const host = u.hostname;
  for (const pattern of PRIVATE_HOST_PATTERNS) {
    if (pattern.test(host)) {
      throw new Error(`${sinkName}: private/loopback/link-local addresses are not permitted`);
    }
  }
  return u;
}
