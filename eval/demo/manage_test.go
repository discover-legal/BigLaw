// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package demo

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/discover-legal/BigLaw/internal/engine"
)

const managePath = "../../ui/src/recorded/manage.json"

// TestManageRecording writes (DEMO_RECORD=1, make demo-record) or checks ui/src/recorded/manage.json:
// the Manage screen's catalogue as built into this binary (engine.Builtins: every agent, tool, pack,
// skill and hook with its contents, no overrides and no usage), which the recorded demo shows
// read-only. No database. A change to any spec, prompt, pack or skill file makes it stale.
func TestManageRecording(t *testing.T) {
	t.Parallel()
	items := engine.Builtins()
	for i := range items {
		items[i].Original = nil // the same as Content for a built-in: the recorded transport restores it
	}
	rec := map[string]any{
		"provenance": "The built-in catalogue of this build, recorded from engine.Builtins (eval/demo/manage_test.go): the files every task runs on " +
			"when no admin has overridden them. The demo is read-only: nothing is saved and no usage is recorded.",
		"items": items,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(rec); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DEMO_RECORD") == "1" {
		if err := os.WriteFile(managePath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", managePath, buf.Len())
		return
	}
	old, err := os.ReadFile(managePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old, buf.Bytes()) {
		t.Fatalf("%s is stale: a spec, prompt, pack or skill changed; re-record with make demo-record (DEMO_RECORD=1 go test -run TestManageRecording ./eval/demo, no database needed) and review the diff", managePath)
	}
}
