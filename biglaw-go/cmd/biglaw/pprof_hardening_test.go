// SPDX-License-Identifier: Apache-2.0
package main

import "testing"

func TestStartLocalPprofRejectsPublicBind(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:6060", ":6060", "192.0.2.10:6060", "bad-address"} {
		if err := startLocalPprof(addr); err == nil {
			t.Fatalf("startLocalPprof(%q) accepted a non-loopback bind", addr)
		}
	}
}
