// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sync"
)

// rowCache is a running task's decoded ledger rows (load), by kind, Go type and key. A long run
// reads its findings hundreds of times (the SEC proof run: 393 loads, 390 MB of JSON), and decoding
// them was a fifth of its CPU. The cache never decides freshness: every load still reads the rows
// (Postgres and the step buffer), and a row is reused only when its bytes equal those it was decoded
// from. So a write from any path (a step's commit, a gate decision, another process that took the
// lease) is seen at the next load, and the cache needs no invalidation. It lives on the run, so it
// goes when the run ends; each load keeps only the rows it read (rows are never deleted).
type rowCache struct {
	mu sync.Mutex
	m  map[cacheKind]map[string]decoded // a published map is never written again
}

type cacheKind struct {
	kind string
	t    reflect.Type
}

// decoded is one row's bytes and their value; the value is never handed out, only its copies.
type decoded struct {
	raw json.RawMessage
	v   any
}

func (c *rowCache) get(k cacheKind) map[string]decoded {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[k]
}

func (c *rowCache) put(k cacheKind, rows map[string]decoded) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[cacheKind]map[string]decoded{}
	}
	c.m[k] = rows
}

// load decodes one kind of rows into T. In a running task a row decoded before is reused when its
// bytes are unchanged (rowCache). Callers get deep copies, so they may change what load returns
// (several append to a finding's citations or notes) without reaching the cache or each other.
func load[T any](ctx context.Context, e *Engine, taskID, kind string) ([]T, error) {
	rs, err := e.rows(ctx, taskID, kind)
	var memo, next map[string]decoded
	r, k := e.active(taskID), cacheKind{kind, reflect.TypeFor[T]()}
	if r != nil {
		memo, next = r.rowCache.get(k), make(map[string]decoded, len(rs))
	}
	out := make([]T, 0, len(rs))
	for _, row := range rs {
		var v T
		if err == nil {
			if d, ok := memo[row.Key]; ok && bytes.Equal(d.raw, row.Data) {
				v = d.v.(T)
			} else {
				err = json.Unmarshal(row.Data, &v)
			}
			if err == nil && next != nil {
				next[row.Key] = decoded{row.Data, v}
				v = deepCopy(reflect.ValueOf(&v).Elem()).Interface().(T)
			}
		}
		out = append(out, v)
	}
	if err == nil && next != nil {
		r.rowCache.put(k, next)
	}
	return out, err
}

// deepCopy copies v so that no slice, map or pointer reachable from it is shared, keeping nil and
// empty apart (they marshal differently). Strings are immutable and shared; unexported fields are
// copied as they are (JSON never sets them).
func deepCopy(v reflect.Value) reflect.Value {
	t := v.Type()
	if !hasRefs(t) {
		return v
	}
	switch t.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		n := reflect.New(t.Elem())
		n.Elem().Set(deepCopy(v.Elem()))
		return n
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		n := reflect.New(t).Elem()
		n.Set(deepCopy(v.Elem()))
		return n
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		n := reflect.MakeSlice(t, v.Len(), v.Len())
		if !hasRefs(t.Elem()) {
			reflect.Copy(n, v)
			return n
		}
		for i := range v.Len() {
			n.Index(i).Set(deepCopy(v.Index(i)))
		}
		return n
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		n := reflect.MakeMapWithSize(t, v.Len())
		for it := v.MapRange(); it.Next(); {
			n.SetMapIndex(it.Key(), deepCopy(it.Value()))
		}
		return n
	case reflect.Array, reflect.Struct:
		n := reflect.New(t).Elem()
		n.Set(v)
		if t.Kind() == reflect.Array {
			for i := range v.Len() {
				n.Index(i).Set(deepCopy(v.Index(i)))
			}
			return n
		}
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() && hasRefs(f.Type) {
				n.Field(i).Set(deepCopy(v.Field(i)))
			}
		}
		return n
	}
	return v // chan, func: JSON decodes neither
}

var refTypes sync.Map // reflect.Type → bool

// hasRefs is whether a value of t can share memory with its copy: a slice, map, pointer or
// interface, or an array or struct (exported fields) holding one.
func hasRefs(t reflect.Type) bool {
	if b, ok := refTypes.Load(t); ok {
		return b.(bool)
	}
	b := false
	switch t.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map:
		b = true
	case reflect.Array:
		b = hasRefs(t.Elem())
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if !hasRefs(f.Type) {
				continue
			}
			if !f.IsExported() { // deepCopy cannot reach it (an embedded unexported struct, an UnmarshalJSON's own state): a shared copy, never silent
				panic("ledger cache: " + t.String() + "." + f.Name + " is unexported and holds references")
			}
			b = true
		}
	}
	refTypes.Store(t, b)
	return b
}
