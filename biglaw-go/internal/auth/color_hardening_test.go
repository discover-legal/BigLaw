// SPDX-License-Identifier: Apache-2.0
package auth

import "testing"

func TestColorOrPickRejectsCSSValues(t *testing.T) {
	if got := colorOrPick("url(https://tracker.invalid/pixel)", "alice"); got == "url(https://tracker.invalid/pixel)" {
		t.Fatal("unsafe CSS value accepted")
	}
	if got := colorOrPick("#a1b2c3", "alice"); got != "#A1B2C3" {
		t.Fatalf("valid color normalized to %q", got)
	}
}
