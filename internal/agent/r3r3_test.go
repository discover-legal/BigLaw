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
// retry, shrink stayed on for the rest of the run and a byte cut dropped a ledger page's "next". Now
// the cut is for the refused request only, and it keeps the JSON valid with its scalar fields.
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
	cut := newest(calls[3]) // the retry that succeeded: cut, but valid JSON with its next
	body, note, _ := strings.Cut(cut[strings.Index(cut, "\n")+1:], "\n[result cut")
	var o struct {
		Findings string
		Next     *int
	}
	if len(cut) > ShrunkResult || json.Unmarshal([]byte(body), &o) != nil || o.Next == nil || *o.Next != 57 || !strings.Contains(note, "findings: its first") {
		t.Errorf("the cut result (%d bytes) is not valid JSON with its next: %.300q … %.300q", len(cut), cut, cut[max(0, len(cut)-300):])
	}
}

// TestClipJSONDropsTrailingItems: an over-long JSON object loses trailing array items, never its
// scalar fields, and stays valid; a non-object is cut at a rune boundary with the note.
func TestClipJSONDropsTrailingItems(t *testing.T) {
	var items []string
	for range 200 {
		items = append(items, `{"fid":"F1","claim":"`+strings.Repeat("é", 50)+`"}`)
	}
	raw := json.RawMessage(`{"findings":[` + strings.Join(items, ",") + `],"next":120,"ok":null}`)
	got := clipResult(toolOut{"Result of ledger (step 3):\n", raw}, 8_000)
	body, _, _ := strings.Cut(strings.TrimPrefix(got, "Result of ledger (step 3):\n"), "\n[result cut")
	var o struct {
		Findings []struct{ FID string }
		Next     int
	}
	if len(got) > 8_000 || json.Unmarshal([]byte(body), &o) != nil || o.Next != 120 || len(o.Findings) == 0 || len(o.Findings) == 200 || !strings.Contains(got, "of 200 items") {
		t.Errorf("%d bytes, %d items, next %d: %.200q", len(got), len(o.Findings), o.Next, got)
	}
	if s := clipResult(toolOut{"x: ", json.RawMessage(`"` + strings.Repeat("é", 9000) + `"`)}, 1000); len(s) > 1400 || !strings.Contains(s, "[result cut at") {
		t.Errorf("a non-object: %d bytes", len(s))
	}
}
