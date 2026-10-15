// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package jsonsafe

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type inner struct {
	Tags []string `json:"tags"`
}

type custom struct{ Items []int }

func (custom) MarshalJSON() ([]byte, error) { return []byte(`"custom"`), nil }

type Embedded struct {
	Promoted []string `json:"promoted"`
}

type outer struct {
	Embedded
	Name    string            `json:"name"`
	Rounds  []inner           `json:"rounds"`
	Nested  []inner           `json:"nested"`
	Ptr     *inner            `json:"ptr"`
	NilPtr  *inner            `json:"nilPtr"`
	ByID    map[string]inner  `json:"byId"`
	NilMap  map[string]string `json:"nilMap"`
	Any     any               `json:"any"`
	Bytes   []byte            `json:"bytes"`
	Custom  custom            `json:"custom"`
	When    time.Time         `json:"when"`
	Skipped []string          `json:"-"`
	Omitted []string          `json:"omitted,omitempty"`
	Raw     json.RawMessage   `json:"raw,omitempty"`
	Arr     [2]inner          `json:"arr"`
	private []string
}

func TestNormalizeReplacesNilSlicesEverywhere(t *testing.T) {
	src := outer{
		Name:   "x",
		Nested: []inner{{}, {Tags: []string{"a"}}},
		Ptr:    &inner{},
		ByID:   map[string]inner{"k": {}},
		Any:    map[string]any{"list": []string(nil), "task": inner{}},
	}
	got, err := Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		`"promoted":[]`, `"rounds":[]`, `"nested":[{"tags":[]},{"tags":["a"]}]`,
		`"ptr":{"tags":[]}`, `"nilPtr":null`, `"byId":{"k":{"tags":[]}}`, `"nilMap":{}`,
		`"any":{"list":[],"task":{"tags":[]}}`, `"bytes":null`, `"custom":"custom"`,
		`"arr":[{"tags":[]},{"tags":[]}]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, "omitted") || strings.Contains(s, "Skipped") || strings.Contains(s, "raw") {
		t.Errorf("omitempty / '-' semantics changed: %s", s)
	}
	// The original must not be mutated — handlers serialise live state.
	if src.Rounds != nil || src.Nested[0].Tags != nil || src.Ptr.Tags != nil ||
		src.ByID["k"].Tags != nil || src.Any.(map[string]any)["list"].([]string) != nil {
		t.Fatal("Normalize mutated its input")
	}
}

func TestNormalizeTopLevelAndFlatValues(t *testing.T) {
	var nilList []inner
	if got, _ := Marshal(nilList); string(got) != "[]" {
		t.Fatalf("top-level nil slice = %s", got)
	}
	if got, _ := Marshal(&outer{}); !bytes.Contains(got, []byte(`"rounds":[]`)) {
		t.Fatalf("pointer to struct not normalized: %s", got)
	}
	if got, _ := Marshal(map[string][]int(nil)); string(got) != "{}" {
		t.Fatalf("top-level nil map = %s", got)
	}
	if Normalize(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	type flat struct{ A int }
	f := flat{A: 1}
	if Normalize(f) != any(f) {
		t.Fatal("flat values should pass through")
	}
}

type node struct {
	Children []*node `json:"children"`
}

func TestRecursiveTypes(t *testing.T) {
	got, err := Marshal(node{Children: []*node{{}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"children":[{"children":[]}]}` {
		t.Fatalf("got %s", got)
	}
}

func TestEncoder(t *testing.T) {
	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(inner{}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "{\"tags\":[]}\n" {
		t.Fatalf("got %q", buf.String())
	}
}
