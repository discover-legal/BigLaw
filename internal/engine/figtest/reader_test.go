// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package figtest

import (
	"encoding/json"
	"math/rand/v2"
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
	if l := out.Labels[2]; l["span"] != "1.m.s1" || l["kind"] != "duration" || l["unit"] != "days" { // "21 days", not the date's 21
		t.Errorf("21 days: %v", l)
	}
	r, _ = With(map[string]string{"3": "Talbot"}, nil, map[string]string{"644,910": "damages claimed"})(req)
	if !strings.Contains(r.Text, `"party":"Talbot"`) || !strings.Contains(r.Text, `"measures":"damages claimed"`) {
		t.Errorf("overrides: %s", r.Text)
	}
}

// The windows oracleRef and oracleMonth match on (refWindow, monthWindow) answer as the whole text
// does, on texts built from the pieces either pattern reads: month and reference words, their
// case-folded letters (ſ, the Kelvin sign), digits, dots, commas, white space and other runes.
func TestWindowsMatchAsTheWholeText(t *testing.T) {
	pieces := []string{"Jan", "jan", "MARCH", "Sep", "ſep", "dec", "uary", "x", "K", "ſ", "Section", "ſection", "§", "no.", "No.", "#", "item", "Paragraph",
		"para", "graph", " ", "  ", "\t", "\n", ".", ",", "1", "12", "123", "2026", "$", "(", "é", "\xff", "\xc3", "a", "q", "-", ":", "ARTICLE", "ticle"}
	r := rand.New(rand.NewPCG(3, 4))
	for range 30000 {
		var b strings.Builder
		for range r.IntN(12) {
			b.WriteString(pieces[r.IntN(len(pieces))])
		}
		s := b.String()
		if got, want := oracleRef.MatchString(refWindow(s)), oracleRef.MatchString(s); got != want {
			t.Fatalf("oracleRef on %q: window %q says %v, the whole text %v", s, refWindow(s), got, want)
		}
		if got, want := oracleMonth.MatchString(monthWindow(s)), oracleMonth.MatchString(s); got != want {
			t.Fatalf("oracleMonth on %q: window %q says %v, the whole text %v", s, monthWindow(s), got, want)
		}
	}
}
