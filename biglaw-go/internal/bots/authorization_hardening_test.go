// SPDX-License-Identifier: Apache-2.0
package bots

import "testing"

func TestIDAllowedFailsClosed(t *testing.T) {
	if idAllowed("", "U1") || idAllowed("U1,U2", "") || idAllowed("U1,U2", "U3") {
		t.Fatal("bot identity allowlist did not fail closed")
	}
	if !idAllowed("U1, U2", "U2") {
		t.Fatal("listed bot identity was rejected")
	}
}
