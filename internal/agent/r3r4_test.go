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

// TestClipJSONMeasuresEncodedBytes (lab-r3 r3 verifier L4): a string of characters JSON escapes is
// cut by its encoded size, not emptied; a one-item array over the cut keeps its item, cut; "&" and
// "<" stay as the tool wrote them; and no cut result passes its bound, the note included.
func TestClipJSONMeasuresEncodedBytes(t *testing.T) {
	esc := strings.Repeat("<>&\"\n", 6_000) // 30 KB raw, about 60 KB encoded
	raw, _ := json.Marshal(map[string]any{"text": esc, "next": 3})
	got := clipResult(toolOut{"r:\n", raw}, 16_000)
	body, _, _ := strings.Cut(strings.TrimPrefix(got, "r:\n"), "\n[result cut")
	var o struct {
		Text string
		Next int
	}
	if len(got) > 16_000 || json.Unmarshal([]byte(body), &o) != nil || o.Next != 3 || len(o.Text) < 6_000 || strings.Contains(body, `\u0026`) {
		t.Errorf("escaped string: %d bytes, kept %d raw bytes, next %d: %.120q", len(got), len(o.Text), o.Next, got)
	}
	one, _ := json.Marshal(map[string]any{"findings": []string{strings.Repeat("a & b ", 5_000)}, "next": nil})
	got = clipResult(toolOut{"r:\n", one}, 8_000)
	body, _, _ = strings.Cut(strings.TrimPrefix(got, "r:\n"), "\n[result cut")
	var p struct{ Findings []string }
	if len(got) > 8_000 || json.Unmarshal([]byte(body), &p) != nil || len(p.Findings) != 1 || len(p.Findings[0]) < 5_000 || !strings.Contains(p.Findings[0], "a & b") {
		t.Errorf("one-item array: %d bytes, %d items: %.160q", len(got), len(p.Findings), got)
	}
	var many []map[string]string
	for i := range 300 { // several fields cut, so several notes
		many = append(many, map[string]string{"claim": strings.Repeat("x", 40+i%7)})
	}
	nested, _ := json.Marshal(map[string]any{"a": many, "b": many, "c": strings.Repeat("y", 9_000), "d": many})
	for _, n := range []int{2_000, 5_000, 16_000} {
		if got := clipResult(toolOut{"Result of ledger (step 9):\n", nested}, n); len(got) > n {
			t.Errorf("cut to %d: %d bytes", n, len(got))
		}
		if got := clip(strings.Repeat("z", 3*n), n); len(got) > n {
			t.Errorf("clip to %d: %d bytes", n, len(got))
		}
	}
}

// TestCompactionRelaxesAfterRetry (lab-r3 r3 verifier L5): after an over-long retry the older
// results are compacted for the next turn too, then the compaction relaxes a turn at a time (keep
// stayed at 1 for the rest of the run).
func TestCompactionRelaxesAfterRetry(t *testing.T) {
	var script []model.Reply
	for range 5 {
		script = append(script, calls(call("echo", q)))
	}
	script = append(script, model.Reply{Err: &model.Error{Kind: model.ErrUnavailable, Detail: "HTTP 400", Status: 400}})
	for range 6 {
		script = append(script, calls(call("echo", q)))
	}
	script = append(script, calls(call("finish", q)))
	rt, f := rig(t, map[string][]model.Reply{"lead": script}, &counter{s: goSpec("echo")}, newFinish())
	if _, err := rt.Run(context.Background(), lead("echo", "finish"), Call{Input: json.RawMessage(`"go"`)}, NewBudget(50, 1, 0)); err != nil {
		t.Fatal(err)
	}
	full := func(i int) int {
		n := 0
		for _, m := range f.Calls()[i].Messages {
			if c, ok := m.Content.(string); ok && m.Role == "tool" && !strings.HasPrefix(c, "[step ") {
				n++
			}
		}
		return n
	}
	if a, b := full(7), full(len(f.Calls())-1); a != 1 || b != keepTurns {
		t.Errorf("results in full: %d the turn after the retry (want 1), %d at the end (want %d)", a, b, keepTurns)
	}
}
