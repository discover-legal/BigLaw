// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Schema is a JSON Schema in the subset BigLaw uses: type (one or a list; "null" makes a field
// nullable), properties, required, additionalProperties (false only), enum, items, and the
// min/max of items, string length and numbers. Any other keyword is a compile error, so a schema
// never silently means less than it says.
type Schema struct {
	Type                 types              `json:"type"`
	Properties           map[string]*Schema `json:"properties"`
	Required             []string           `json:"required"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	Enum                 []any              `json:"enum"`
	Items                *Schema            `json:"items"`
	MinItems, MaxItems   *float64
	MinLength, MaxLength *float64
	Minimum, Maximum     *float64
	Description          string `json:"description"`
}

type types []string

func (t *types) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*t = types{one}
		return nil
	}
	return json.Unmarshal(b, (*[]string)(t))
}

// Compile parses a schema; an unknown keyword, an unknown type or an open object is an error.
func Compile(raw json.RawMessage) (*Schema, error) {
	var s Schema
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return nil, fmt.Errorf("not a schema in BigLaw's subset: %v", err)
	}
	return &s, s.check("$")
}

func (s *Schema) check(at string) error {
	for _, t := range s.Type {
		if !slices.Contains([]string{"object", "array", "string", "number", "integer", "boolean", "null"}, t) {
			return fmt.Errorf("%s: unknown type %q", at, t)
		}
	}
	switch {
	case len(s.Type) == 0 && s.Enum == nil:
		return fmt.Errorf("%s: a schema needs a type or an enum", at)
	case s.AdditionalProperties != nil && *s.AdditionalProperties:
		return fmt.Errorf("%s: additionalProperties may only be false", at)
	case s.Items != nil:
		return s.Items.check(at + "[]")
	}
	for n, p := range s.Properties {
		if err := p.check(at + "." + n); err != nil {
			return err
		}
	}
	return nil
}

// Strict reports the first place s is not strict-shaped (every object closed, every property
// required, optional fields nullable instead of absent), or "" when it is.
func (s *Schema) Strict(at string) string {
	if slices.Contains(s.Type, "object") && s.AdditionalProperties == nil {
		return at + ": an object without additionalProperties: false"
	}
	names := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !slices.Contains(s.Required, n) {
			return at + "." + n + ": not in required (make it nullable instead)"
		}
		if bad := s.Properties[n].Strict(at + "." + n); bad != "" {
			return bad
		}
	}
	if s.Items != nil {
		return s.Items.Strict(at + "[]")
	}
	return ""
}

// Validate checks data against s. The error names the first offending path and what it needs.
func (s *Schema) Validate(data json.RawMessage) error {
	var v any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&v); err != nil || d.More() {
		return fmt.Errorf("not one JSON value")
	}
	return s.validate(v, "input")
}

func (s *Schema) validate(v any, at string) error {
	if s.Enum != nil && !slices.ContainsFunc(s.Enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) && (e == nil) == (v == nil) }) {
		vals, _ := json.Marshal(s.Enum)
		return fmt.Errorf("%s must be one of: %s", at, strings.ReplaceAll(strings.Trim(string(vals), "[]"), `"`, ""))
	}
	t := typeOf(v)
	if s.Type != nil && !slices.Contains(s.Type, t) && !(t == "integer" && slices.Contains(s.Type, "number")) {
		return fmt.Errorf("%s must be %s, not %s", at, strings.Join(s.Type, " or "), t)
	}
	out := func(n float64, lo, hi *float64) bool { return lo != nil && n < *lo || hi != nil && n > *hi }
	switch x := v.(type) {
	case json.Number:
		if f, _ := x.Float64(); out(f, s.Minimum, s.Maximum) {
			return fmt.Errorf("%s is out of range", at)
		}
	case string:
		if n := utf8.RuneCountInString(x); out(float64(n), s.MinLength, s.MaxLength) {
			return fmt.Errorf("%s has %d characters, outside the allowed length", at, n)
		}
	case []any:
		if out(float64(len(x)), s.MinItems, s.MaxItems) {
			return fmt.Errorf("%s has %d items, outside the allowed number", at, len(x))
		}
		for i := 0; s.Items != nil && i < len(x); i++ {
			if err := s.Items.validate(x[i], fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, r := range s.Required {
			if _, ok := x[r]; !ok {
				return fmt.Errorf("%s.%s is missing (send null if it does not apply)", at, r)
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if p := s.Properties[k]; p != nil {
				if err := p.validate(x[k], at+"."+k); err != nil {
					return err
				}
			} else if s.AdditionalProperties != nil {
				return fmt.Errorf("%s.%s is not a field of this tool", at, k)
			}
		}
	}
	return nil
}

func typeOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return "integer"
		}
	}
	return "number"
}
