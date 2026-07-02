// SPDX-License-Identifier: AGPL-3.0-only
package orchestrator

import (
	"strings"
	"testing"
)

func TestDefenseIssuesFor(t *testing.T) {
	auth := "Section 206(1) of the Advisers Act | Section 206(2) | Section 209(e) | Rule 204-2(a)(3) | Section 207"
	got := defenseIssuesFor(auth)
	blob := strings.ToLower(strings.Join(got, "\n"))
	for _, want := range []string{"scienter", "negligence", "18 u.s.c. § 1519", "statute of limitations", "204-2"} {
		if !strings.Contains(blob, strings.ToLower(want)) {
			t.Errorf("expected a defense issue mentioning %q; got:\n%s", want, strings.Join(got, "\n---\n"))
		}
	}
	// The explicit 206(1)-vs-206(2) scienter DISTINCTION (a named rubric point) must fire when both charged.
	if !strings.Contains(blob, "both section 206(1)") {
		t.Error("expected the explicit 206(1)-vs-206(2) scienter distinction")
	}
	if defenseIssuesFor("") != nil {
		t.Error("empty authorities should yield no issues")
	}
	// 206(2) alone must NOT emit the both-charged distinction.
	if strings.Contains(strings.ToLower(strings.Join(defenseIssuesFor("Section 206(2)"), " ")), "both section 206(1)") {
		t.Error("the distinction should require BOTH provisions")
	}
}
