// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package jsonsafe

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
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

func TestNilCollectionsEncodeEmptyEverywhere(t *testing.T) {
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
		`"any":{"list":[],"task":{"tags":[]}}`, `"bytes":""`, `"custom":"custom"`,
		`"arr":[{"tags":[]},{"tags":[]}]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, "omitted") || strings.Contains(s, "Skipped") || strings.Contains(s, "raw") {
		t.Errorf("omitempty / '-' semantics changed: %s", s)
	}
}

func TestTopLevelValues(t *testing.T) {
	var nilList []inner
	if got, _ := Marshal(nilList); string(got) != "[]" {
		t.Fatalf("top-level nil slice = %s", got)
	}
	if got, _ := Marshal(map[string][]int(nil)); string(got) != "{}" {
		t.Fatalf("top-level nil map = %s", got)
	}
	if got, _ := Marshal(&outer{}); !bytes.Contains(got, []byte(`"rounds":[]`)) {
		t.Fatalf("pointer to struct not normalized: %s", got)
	}
	if got, _ := Marshal(nil); string(got) != "null" {
		t.Fatalf("nil = %s", got)
	}
}

// A cyclic value must be an error, never a stack overflow that kills the
// process (gin's recovery cannot catch a fatal stack overflow).
type cyc struct {
	Name   string `json:"name"`
	Parent *cyc   `json:"parent"`
	Kids   []any  `json:"kids"`
}

func TestCycleIsAnError(t *testing.T) {
	n := &cyc{Name: "a"}
	n.Parent = n
	if _, err := Marshal(n); err == nil {
		t.Fatal("pointer cycle: want error")
	}
	m := map[string]any{}
	m["self"] = m
	if _, err := Marshal(m); err == nil {
		t.Fatal("map cycle: want error")
	}
}

// Encoding reads only what encoding/json reads: unexported fields (mutexes,
// counters) of live state are never touched. Run with -race.
type live struct {
	mu    sync.Mutex
	priv  int
	Items []string `json:"items"`
}

func TestDoesNotReadUnexportedFields(t *testing.T) {
	l := &live{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 500 {
			l.mu.Lock()
			l.priv++
			l.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			if _, err := Marshal(l); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
}

func TestEncoderEscapeHTML(t *testing.T) {
	var buf bytes.Buffer
	e := NewEncoder(&buf)
	_ = e.Encode("<a>")
	e.SetEscapeHTML(false)
	_ = e.Encode("<a>")
	if buf.String() != "\"\\u003ca\\u003e\"\n\"<a>\"\n" {
		t.Fatalf("got %q", buf.String())
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
