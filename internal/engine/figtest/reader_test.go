// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package figtest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// Reader answers read_figures requests only, one label per span it was given, by the fixture texts'
// plain conventions; With overrides a span's party, measure or whole label.
func TestReaderLabelsEverySpanItWasGiven(t *testing.T) {
	in := `{"findings":[{"id":"1","parties":["Greystone"],"passages":[
		{"id":"1.q1","kind":"quote","documentId":"d","text":"Greystone claims damages of $644,910 for 3 invoices","class":null,"unit":null,"entity":null,"metric":null,"spans":[{"id":"1.q1.s1","text":"644,910"},{"id":"1.q1.s2","text":"3"}]},
		{"id":"1.m","kind":"memo","documentId":null,"text":"Served 10/21/2026; 21 days later.","class":null,"unit":null,"entity":null,"metric":null,"spans":[{"id":"1.m.s1","text":"21"}]}]}]}`
	req := model.Request{Agent: "read_figures", Messages: []model.Message{{Role: "user", Content: "Request:\n" + in}}}
	if _, ok := Reader(model.Request{Agent: "lead"}); ok {
		t.Fatal("answered another agent")
	}
	r, ok := Reader(req)
	var out struct{ Labels []map[string]any }
	if !ok || json.Unmarshal([]byte(r.Text), &out) != nil || len(out.Labels) != 3 {
		t.Fatalf("%v %s", ok, r.Text)
	}
	if l := out.Labels[0]; l["kind"] != "money" || l["currency"] != "USD" || l["party"] != "Greystone" {
		t.Errorf("$644,910: %v", l)
	}
	if l := out.Labels[2]; l["span"] != "1.m.s1" || l["kind"] != "other" { // "21 days", not the date's 21
		t.Errorf("21 days: %v", l)
	}
	r, _ = With(map[string]string{"3": "Talbot"}, nil, map[string]string{"644,910": "damages claimed"})(req)
	if !strings.Contains(r.Text, `"party":"Talbot"`) || !strings.Contains(r.Text, `"measures":"damages claimed"`) {
		t.Errorf("overrides: %s", r.Text)
	}
}
