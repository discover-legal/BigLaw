// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/model"
)

type src []Finding

func (s src) Findings(context.Context, string) ([]Finding, error) { return s, nil }

// invoke runs one tool through the runtime's dispatch path (schema checks included).
func invoke(t *testing.T, name, input string) (agent.Result, error) {
	t.Helper()
	rt := &agent.Runtime{Chat: model.NewFake(nil), Reg: agent.NewRegistry()}
	for _, tl := range Tools(src(ledger())) {
		if err := rt.Reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	a := agent.Agent{Name: "lead", Visible: []string{"numbers", "join_discrepancies"}}
	return rt.Invoke(context.Background(), a, agent.Call{TaskID: "t"}, name, json.RawMessage(input), agent.NewBudget(10, 1, 0), false, 0)
}

// Trap: op calc without its fields is an actionable error naming them.
func TestNumbersOpFieldsChecked(t *testing.T) {
	_, err := invoke(t, "numbers", `{"op":"calc","fn":"sum","operands":null,"holidays":null,"fids":null,"salientTop":null}`)
	var f *agent.Failure
	if !errors.As(err, &f) || !strings.Contains(err.Error(), "operands[]") || !strings.Contains(err.Error(), "call again") {
		t.Fatalf("calc without operands: %v", err)
	}
	if _, err := invoke(t, "numbers", `{"op":"calc","fn":"cube","operands":[],"holidays":null,"fids":null,"salientTop":null}`); !errors.Is(err, agent.ErrToolInput) || !strings.Contains(err.Error(), "input.fn must be one of") {
		t.Fatalf("unknown fn: %v", err)
	}
	res, err := invoke(t, "numbers", `{"op":"calc","fn":"sum","operands":[{"ref":"F1.v1","literal":null,"unit":null},{"ref":"F2.v1","literal":null,"unit":null}],"holidays":null,"fids":null,"salientTop":null}`)
	if err != nil || !strings.Contains(string(res.Output), `"result":"8238000"`) {
		t.Fatalf("calc: %s %v", res.Output, err)
	}
	res, err = invoke(t, "numbers", `{"op":"handles","fn":null,"operands":null,"holidays":null,"fids":null,"salientTop":2}`)
	if err != nil || strings.Contains(string(res.Output), "7,800,000") || !strings.Contains(string(res.Output), `"handle":"{Zephyr}"`) {
		t.Fatalf("handles (no values in the output): %s %v", res.Output, err)
	}
	if res, err = invoke(t, "numbers", `{"op":"table","fn":null,"operands":null,"holidays":null,"fids":["F2"],"salientTop":null}`); err != nil || !strings.Contains(string(res.Output), "$438,000") {
		t.Fatalf("table: %s %v", res.Output, err)
	}
}

func TestJoinRefusesUnverifiedInputs(t *testing.T) {
	if _, err := invoke(t, "join_discrepancies", `{"fids":["F1","F3"],"aliases":null}`); err == nil || !strings.Contains(err.Error(), "F3 is not a verified finding") {
		t.Fatalf("a quarantined finding was joined: %v", err)
	}
	res, err := invoke(t, "join_discrepancies", `{"fids":null,"aliases":null}`)
	if err != nil || string(res.Output) != `{"conflicts":[],"truncated":false,"unreadDocuments":[]}` {
		t.Fatalf("all verified: %s %v", res.Output, err)
	}
}

// r2 verifier N1: an empty fids list means every finding, as null does (and as in numbers);
// [] once picked nothing and silently reported no discrepancy.
func TestJoinEmptyFidsMeansAll(t *testing.T) {
	fs := src(read([]Finding{
		fh("$9.0 million", "Respondent paid $9.0 million in fees.", "docA", "Acme", "fees paid"),
		fh("$9.4 million", "Acme paid $9.4 million in fees.", "docB", "Acme", "fees paid"),
	}))
	rt := &agent.Runtime{Chat: model.NewFake(nil), Reg: agent.NewRegistry()}
	for _, tl := range Tools(fs) {
		if err := rt.Reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	run := func(in string) string {
		a := agent.Agent{Name: "lead", Visible: []string{"join_discrepancies"}}
		res, err := rt.Invoke(context.Background(), a, agent.Call{TaskID: "t"}, "join_discrepancies", json.RawMessage(in), agent.NewBudget(10, 1, 0), false, 0)
		if err != nil {
			t.Fatal(err)
		}
		return string(res.Output)
	}
	null, empty := run(`{"fids":null,"aliases":null}`), run(`{"fids":[],"aliases":null}`)
	if !strings.Contains(null, "9.4") || empty != null {
		t.Fatalf("fids [] reports %s, fids null reports %s", empty, null)
	}
}
