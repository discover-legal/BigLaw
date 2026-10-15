// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package jsonsafe enforces one wire-contract rule for every JSON body the
// REST API emits: a collection field (list or object map) is never null.
//
// encoding/json writes a nil slice or map as `null`, and idiomatic Go
// produces both everywhere (`var out []T` + append, early returns, zero-value
// structs). Clients — the workbench UI first among them — reasonably treat a
// field typed `T[]` as an array, so a single nil slice anywhere in a response
// graph (a queued task's `rounds`, a fallback headnote report's
// `practiceAreas`, a review's `legend` map, …) crashes the page that renders
// it.
//
// Fixing that type-by-type means every constructor, early return and future
// struct must remember; one miss is a crash. Instead the API layer routes all
// encoding through Normalize, which returns a copy of the value in which every
// nil slice reachable through exported, JSON-visible fields is replaced by an
// empty one, and every nil map by an empty map. The original value is never
// mutated (handlers often serialise live, shared state). Types that implement
// json.Marshaler encode themselves and are passed through untouched, as are
// []byte (base64 in JSON) and nil pointers / interfaces (those mean
// "absent", not "empty collection").
package jsonsafe

import (
	"encoding"
	"encoding/json"
	"io"
	"reflect"
	"sync"
)

// Marshal is json.Marshal with nil slices encoded as [] and nil maps as {}.
func Marshal(v any) ([]byte, error) { return json.Marshal(Normalize(v)) }

// MarshalIndent is json.MarshalIndent with nil slices/maps encoded as []/{}.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	return json.MarshalIndent(Normalize(v), prefix, indent)
}

// Normalize returns v, or a copy of v in which every nil slice or map
// reachable through JSON-visible fields, map values, array/slice elements,
// pointers and interfaces is replaced by an empty one of the same type. Values
// whose type cannot contain such a collection are returned as-is, uncopied.
func Normalize(v any) any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if !mayHoldNilCollection(rv.Type()) {
		return v
	}
	return normalize(rv).Interface()
}

var (
	marshalerType     = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	bytesKind         = reflect.TypeFor[byte]().Kind()
)

// selfEncoding reports whether encoding/json delegates t's encoding to a
// method on t (or *t), in which case its internals are not ours to touch.
func selfEncoding(t reflect.Type) bool {
	if t.Implements(marshalerType) || t.Implements(textMarshalerType) {
		return true
	}
	if t.Kind() != reflect.Pointer {
		pt := reflect.PointerTo(t)
		return pt.Implements(marshalerType) || pt.Implements(textMarshalerType)
	}
	return false
}

func isByteSlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == bytesKind
}

// mayHoldNilCollection is a cached, conservative answer to "could a value of type t
// contain a nil slice or map that would encode as null?". Interfaces answer true
// (the dynamic type decides). It lets the hot path skip copying flat values.
var holdCache sync.Map // reflect.Type → bool

func mayHoldNilCollection(t reflect.Type) bool {
	if v, ok := holdCache.Load(t); ok {
		return v.(bool)
	}
	// Recursive types: assume true while computing; corrected below.
	holdCache.Store(t, true)
	res := computeMayHold(t)
	holdCache.Store(t, res)
	return res
}

func computeMayHold(t reflect.Type) bool {
	if selfEncoding(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Slice:
		return !isByteSlice(t)
	case reflect.Interface, reflect.Map:
		return true
	case reflect.Pointer, reflect.Array:
		return mayHoldNilCollection(t.Elem())
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if !jsonVisible(f) {
				continue
			}
			if mayHoldNilCollection(f.Type) {
				return true
			}
		}
	}
	return false
}

// jsonVisible mirrors encoding/json's field selection closely enough for our
// purpose: exported fields (or embedded structs, whose exported fields are
// promoted) that are not tagged "-".
func jsonVisible(f reflect.StructField) bool {
	if tag := f.Tag.Get("json"); tag == "-" {
		return false
	}
	if f.IsExported() {
		return true
	}
	if f.Anonymous {
		t := f.Type
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		return t.Kind() == reflect.Struct
	}
	return false
}

func normalize(v reflect.Value) reflect.Value {
	t := v.Type()
	if !mayHoldNilCollection(t) {
		return v
	}
	switch t.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return reflect.MakeSlice(t, 0, 0)
		}
		if !mayHoldNilCollection(t.Elem()) {
			return v
		}
		out := reflect.MakeSlice(t, v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(normalize(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(t).Elem()
		for i := range v.Len() {
			out.Index(i).Set(normalize(v.Index(i)))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(t.Elem())
		out.Elem().Set(normalize(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		inner := normalize(v.Elem())
		out := reflect.New(t).Elem()
		out.Set(inner)
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.MakeMap(t)
		}
		if !mayHoldNilCollection(t.Elem()) {
			return v
		}
		out := reflect.MakeMapWithSize(t, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), normalize(iter.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(t).Elem()
		out.Set(v) // copies every field, unexported ones included
		for i := range t.NumField() {
			f := t.Field(i)
			if !jsonVisible(f) || !mayHoldNilCollection(f.Type) {
				continue
			}
			dst := out.Field(i)
			if !dst.CanSet() {
				// Unexported embedded struct: its promoted fields can't be set
				// through reflection. Rare in API types; leave it as-is.
				continue
			}
			dst.Set(normalize(v.Field(i)))
		}
		return out
	}
	return v
}

// ─── stream encoder ───────────────────────────────────────────────────────────

// Encoder wraps json.Encoder, normalizing each value before encoding.
type Encoder struct{ enc *json.Encoder }

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{enc: json.NewEncoder(w)} }

// SetEscapeHTML mirrors json.Encoder.SetEscapeHTML.
func (e *Encoder) SetEscapeHTML(on bool) { e.enc.SetEscapeHTML(on) }

// Encode writes the normalized JSON encoding of v followed by a newline.
func (e *Encoder) Encode(v any) error { return e.enc.Encode(Normalize(v)) }
