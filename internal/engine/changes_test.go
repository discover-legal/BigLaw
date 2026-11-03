// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// TestChangeSetRenderedEventFiresWebhook (AC-26): a webhook rule on change_set_rendered receives one
// POST per render with the task, change set, kind, counts and hash; a condition on the event's
// fields filters; a limitation is a note; every outcome is a hookrun row keyed by the change set; a
// rule that cannot send is recorded, never an error; block, approve and check are refused at save,
// as is a match other than *.
func TestChangeSetRenderedEventFiresWebhook(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/nowhere" {
			w.WriteHeader(500)
			return
		}
		var ev map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &ev)
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer srv.Close()
	e := remoteEngine(f, newFake(nil), srv, &syncBuf{})
	save(t, e, "rule.notify_dms", 0, ruleFile(rule("notify_dms", 0, eventChangeSet, "*", nil, HookAction{Type: "webhook", Webhook: &Webhook{URL: srv.URL + "/hook", Wait: true}}, "closed")))
	save(t, e, "rule.compares", 0, ruleFile(rule("compares", 1, eventChangeSet, "*", &Cond{"kind", "eq", "compare"}, HookAction{Type: "limitation", Message: "A compare was rendered."}, "open")))
	save(t, e, "rule.failing", 0, ruleFile(rule("failing", 2, eventChangeSet, "", nil, HookAction{Type: "webhook", Webhook: &Webhook{URL: srv.URL + "/nowhere", Wait: true}}, "closed")))
	task := f.submit(0)
	ctx := context.Background()
	ev := ChangeSetEvent{ChangeSetID: "cs-1", DocumentID: f.ids()[0], Kind: "edits", Author: "Horatiu", Applied: 3, Failed: 1, SHA256: "abc"}
	if err := e.ChangeSetRendered(ctx, task.ID, ev); err != nil {
		t.Fatal(err)
	}
	ev.ChangeSetID, ev.Kind = "cs-2", "compare"
	if err := e.ChangeSetRendered(ctx, task.ID, ev); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0]["event"] != eventChangeSet || got[0]["taskId"] != task.ID || got[0]["subject"] != "change_set" || got[1]["textBytes"] == nil {
		t.Fatalf("the webhook got %v", got)
	}
	if o := outcomes(t, e, task.ID); !slices.Equal(o, []string{"notify_dms:sent", "failing:failed-closed", "notify_dms:sent", "compares:noted", "failing:failed-closed"}) {
		t.Fatalf("outcomes: %v", o)
	}
	notes, err := load[string](ctx, e, task.ID, kNote)
	if err != nil || !slices.Contains(notes, "A compare was rendered.") {
		t.Errorf("the limitation note: %v %v", notes, err)
	}
	for name, r := range map[string]HookRule{
		"block":   rule("r1", 0, eventChangeSet, "*", nil, HookAction{Type: "block", Message: "m"}, "closed"),
		"approve": rule("r1", 0, eventChangeSet, "*", nil, HookAction{Type: "approve", Message: "m"}, "closed"),
		"check":   rule("r1", 0, eventChangeSet, "*", nil, HookAction{Type: "check", Agent: "screen", Message: "m"}, "closed"),
		"match":   rule("r1", 0, eventChangeSet, "retrieve", nil, HookAction{Type: "limitation", Message: "m"}, "open"),
	} {
		if _, _, err := e.Save(ctx, "rule.r1", SaveIn{Files: ruleFile(r)}, "t"); err == nil {
			t.Errorf("%s was saved: %v", name, err)
		}
	}
	if err := e.ChangeSetRendered(ctx, "00000000-0000-0000-0000-000000000000", ev); err == nil {
		t.Error("an unknown task")
	}
}
