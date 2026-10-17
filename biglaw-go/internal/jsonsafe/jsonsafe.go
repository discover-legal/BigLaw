// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package jsonsafe is the REST API's JSON encoder. It enforces one wire
// contract rule for every body the API emits: a collection (list or object
// map) is never null.
//
// encoding/json writes a nil slice or map as `null`, and idiomatic Go produces
// both everywhere (`var out []T` + append, early returns, zero-value structs).
// Clients — the workbench UI first among them — reasonably treat a field typed
// `T[]` as an array, so a single nil slice anywhere in a response graph (a
// queued task's `rounds`, a review's `legend`, …) crashes the page rendering
// it. Fixing that type-by-type means every constructor and future struct must
// remember; one miss is a crash.
//
// Instead the encoding itself is configured: this package encodes with
// github.com/go-json-experiment/json (the encoding/json/v2 implementation)
// under the full set of v1-compatibility options — so output matches
// encoding/json (field naming, omitempty, `,string`, HTML escaping, legacy
// MarshalJSON call semantics, map-key sorting) — plus
// FormatNilSliceAsNull(false) and FormatNilMapAsNull(false). There is no
// value copying: nothing is mutated or read beyond what encoding/json would
// read, MarshalJSON methods are honoured, and a cyclic value is reported as
// an error rather than overflowing the stack.
//
// Two deliberate differences from encoding/json, both from the same option:
// a nil slice encodes as [] (a nil []byte as "", its empty base64 form) and
// a nil map as {}. Nil pointers and interfaces still encode as null — they
// mean "absent", not "empty collection". The golden test in this package pins
// byte-identity with encoding/json for every API response type otherwise.
package jsonsafe

import (
	"bytes"
	stdjson "encoding/json"
	"io"

	json "github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	jsonv1 "github.com/go-json-experiment/json/v1"
)

// options is encoding/json behaviour with nil collections encoded as empty.
var options = json.JoinOptions(
	jsonv1.DefaultOptionsV1(),
	json.FormatNilSliceAsNull(false),
	json.FormatNilMapAsNull(false),
)

// Marshal is encoding/json.Marshal with nil slices encoded as [] and nil maps
// as {}.
func Marshal(v any) ([]byte, error) { return json.Marshal(v, options) }

// MarshalIndent is encoding/json.MarshalIndent with nil collections encoded
// as empty.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	b, err := Marshal(v)
	if err != nil {
		return nil, err
	}
	// encoding/json's own MarshalIndent is Marshal + Indent; reuse its Indent
	// so arbitrary prefix/indent strings behave exactly as before (jsontext
	// only accepts whitespace there).
	var out bytes.Buffer
	if err := stdjson.Indent(&out, b, prefix, indent); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Encoder mirrors encoding/json.Encoder (SetEscapeHTML, newline-terminated
// Encode) on top of Marshal.
type Encoder struct {
	w        io.Writer
	noEscape bool
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

// SetEscapeHTML mirrors encoding/json.Encoder.SetEscapeHTML (default on).
func (e *Encoder) SetEscapeHTML(on bool) { e.noEscape = !on }

// Encode writes the JSON encoding of v followed by a newline.
func (e *Encoder) Encode(v any) error {
	b, err := json.Marshal(v, options, jsontext.EscapeForHTML(!e.noEscape))
	if err != nil {
		return err
	}
	_, err = e.w.Write(append(b, '\n'))
	return err
}
