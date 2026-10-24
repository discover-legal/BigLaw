// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// TestR2VShrinkSticksAndHidesNext (lab-r3 r2 verifier M5; failed on 7a5d6be): after one over-long
// retry, shrink stayed on for the rest of the run. Now the refusal is for the refused request only:
// a later turn's page reaches the lead whole, its next intact.
func TestR2VShrinkSticksAndHidesNext(t *testing.T) {
	page := json.RawMessage(`{"findings":"` + strings.Repeat("x", 60_000) + `","next":57}`)
	refused := model.Reply{Err: &model.Error{Kind: model.ErrUnavailable, Detail: "HTTP 400", Status: 400}}
	rt, f := rig(t, map[string][]model.Reply{"lead": {calls(call("echo", q)), refused, refused, calls(call("echo", q)), calls(call("finish", q))}},
		&counter{s: Spec{Name: "echo", Kind: "go", Description: testDesc, In: testIn, Class: "free",
			Out: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["findings","next"],"properties":{"findings":{"type":"string"},"next":{"type":"integer"}}}`)}, out: page}, newFinish())
	if _, err := rt.Run(context.Background(), lead("echo", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatalf("run: %v", err)
	}
	newest := func(r model.Request) string {
		var s string
		for _, m := range r.Messages {
			if m.Role == "tool" {
				s = m.Content.(string)
			}
		}
		return s
	}
	calls := f.Calls()
	if last := newest(calls[len(calls)-1]); !strings.Contains(last, `"next":57`) || len(last) < 60_000 {
		t.Errorf("a later turn's 60 KB page reached the lead as %d bytes: …%.220q", len(last), last[max(0, len(last)-220):])
	}
	if r := newest(calls[3]); len(r) > ShrunkResult || !strings.Contains(r, `"code":"result_too_large"`) || strings.Contains(r, "xxxx") {
		t.Errorf("the retry that succeeded holds %d bytes, want the typed refusal and no part of the page: %.300q", len(r), r)
	}
}
