// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"encoding/json"
	"io"

	"github.com/discover-legal/biglaw-go/internal/jsonsafe"
	ginjson "github.com/gin-gonic/gin/codec/json"
)

// Every JSON body this API writes (c.JSON, c.IndentedJSON, c.PureJSON, …)
// goes through gin's pluggable codec. Installing jsonsafe there is the single
// choke point that guarantees list fields are encoded as [] and never null —
// see package jsonsafe for why this is enforced at the wire rather than
// type-by-type. SSE payloads, which are marshalled by hand, call
// jsonsafe.Marshal directly.
func init() { ginjson.API = nullSafeCodec{} }

type nullSafeCodec struct{}

func (nullSafeCodec) Marshal(v any) ([]byte, error) { return jsonsafe.Marshal(v) }
func (nullSafeCodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
func (nullSafeCodec) MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	return jsonsafe.MarshalIndent(v, prefix, indent)
}
func (nullSafeCodec) NewEncoder(w io.Writer) ginjson.Encoder { return jsonsafe.NewEncoder(w) }
func (nullSafeCodec) NewDecoder(r io.Reader) ginjson.Decoder { return json.NewDecoder(r) }
