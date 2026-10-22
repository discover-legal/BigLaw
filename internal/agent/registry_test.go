// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRegistryRejectsDuplicateName(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&counter{s: goSpec("retrieve")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&counter{s: goSpec("retrieve")}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate: %v", err)
	}
	for i := 1; i < MaxTools; i++ {
		if err := r.Register(&counter{s: goSpec(fmt.Sprintf("tool_%d", i))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Register(&counter{s: goSpec("one_too_many")}); err == nil {
		t.Fatalf("a %dth tool was registered", MaxTools+1)
	}
	if _, err := r.Defs(r.Names()[:MaxVisible+1]); err == nil {
		t.Fatalf("%d visible tools were allowed", MaxVisible+1)
	}
}

// Every description: ≥ 3 sentences, a "Do not use" line, 1–3 example calls that validate.
func TestSpecsDescriptionsComplete(t *testing.T) {
	ex := "\nExample: {\"q\": \"x\", \"op\": null}"
	for name, desc := range map[string]string{
		"two sentences":  "Does a thing. Do not use it for other things." + ex,
		"no do-not-use":  "Does a thing. It is useful. It returns things." + ex,
		"no example":     "Does a thing. It is useful. Do not use it for other things.",
		"four examples":  "Does a thing. It is useful. Do not use it for other things." + ex + ex + ex + ex,
		"invalid sample": "Does a thing. It is useful. Do not use it for other things.\nExample: {\"q\": 3, \"op\": null}",
	} {
		s := goSpec("tool")
		s.Description = desc
		if err := NewRegistry().Register(&counter{s: s}); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
	if err := NewRegistry().Register(&counter{s: goSpec("tool")}); err != nil {
		t.Fatalf("a complete description was refused: %v", err)
	}
}

func TestStrictSchemasShape(t *testing.T) {
	for name, in := range map[string]string{
		"open object":        `{"type":"object","required":["q"],"properties":{"q":{"type":"string"}}}`,
		"optional field":     `{"type":"object","additionalProperties":false,"required":[],"properties":{"q":{"type":"string"}}}`,
		"nested open object": `{"type":"object","additionalProperties":false,"required":["q"],"properties":{"q":{"type":"array","items":{"type":"object","properties":{}}}}}`,
		"unknown keyword":    `{"type":"object","additionalProperties":false,"required":[],"properties":{},"pattern":"x"}`,
		"open additional":    `{"type":"object","additionalProperties":true,"required":[],"properties":{}}`,
	} {
		s := goSpec("tool")
		s.In = json.RawMessage(in)
		s.Description = "Does a thing. It is useful. Do not use it for other things.\nExample: {}"
		if err := NewRegistry().Register(&counter{s: s}); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
	r := NewRegistry()
	_ = r.Register(&counter{s: goSpec("tool")})
	defs, _ := r.Defs([]string{"tool"})
	if !defs[0].Strict {
		t.Fatal("definitions ask for strict mode; the model client sends it only where supported")
	}
}

func TestSchemaValidatorCases(t *testing.T) {
	s, err := Compile(json.RawMessage(`{"type":"object","additionalProperties":false,"required":["n","e","a"],"properties":{
		"n":{"type":["integer","null"],"minimum":1,"maximum":8},"e":{"type":"string","enum":["x","y"]},
		"a":{"type":"array","maxItems":2,"items":{"type":"string","maxLength":3}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		`{"n":null,"e":"x","a":[]}`:         "",
		`{"n":3,"e":"y","a":["abc"]}`:       "",
		`{"n":9,"e":"x","a":[]}`:            "input.n is out of range",
		`{"n":1.5,"e":"x","a":[]}`:          "input.n must be integer or null, not number",
		`{"n":1,"e":"z","a":[]}`:            "input.e must be one of: x,y",
		`{"n":1,"e":"x","a":["a","b","c"]}`: "input.a has 3 items",
		`{"n":1,"e":"x","a":["abcd"]}`:      "input.a[0] has 4 characters",
		`{"e":"x","a":[]}`:                  "input.n is missing (send null",
	} {
		err := s.Validate(json.RawMessage(in))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}
