// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

// Rate is USD per million tokens. Cache writes cost 1.25× and cache reads 0.10× the input rate.
type Rate struct{ In, Out float64 }

func (r Rate) Cost(u Usage) float64 {
	return (float64(u.InputTokens)*r.In + float64(u.OutputTokens)*r.Out +
		float64(u.CacheWriteTokens)*r.In*1.25 + float64(u.CacheReadTokens)*r.In*0.10) / 1e6
}

// The built-in table is prices.json, read by the BIGLAW_PRICES parser (DESIGN § Revision 5, way 3).
//
//go:embed prices.json
var pricesJSON []byte

var builtinPrices = func() map[string]*Rate {
	t, err := ParsePrices(pricesJSON)
	if err != nil {
		panic("model: built-in prices.json: " + err.Error())
	}
	return t
}()

// Prices resolves a model id in the operator's table, then the built-in one: in each an exact id
// beats the longest prefix key, and "vendor/model" is also tried as "model" (a vendor-less key
// matches only that stripped form, so "deepseek/deepseek-r1" is not caught by "deepseek*").
type Prices struct {
	operator, builtin map[string]*Rate
	warned            sync.Map
}

// LoadPrices reads BIGLAW_PRICES: empty is the built-in table alone; a value starting with { or [
// is the JSON; anything else names a regular file. Errors never echo the value or the file.
func LoadPrices(value string) (*Prices, error) {
	p := &Prices{builtin: builtinPrices}
	data, src := []byte(strings.TrimSpace(value)), "the JSON value"
	if len(data) == 0 {
		return p, nil
	}
	if data[0] != '{' && data[0] != '[' {
		src = "the file it names"
		f, err := os.OpenFile(string(data), os.O_RDONLY|syscall.O_NONBLOCK, 0) // a FIFO must not block startup
		if err == nil {
			defer f.Close()
		}
		if fi, e := f.Stat(); err != nil || e != nil || !fi.Mode().IsRegular() { // no OS error text: it repeats the path
			return nil, errors.New("cannot open the file it names: missing, unreadable, or not a regular file")
		}
		if data, err = io.ReadAll(io.LimitReader(f, 1<<20+1)); err != nil || len(data) > 1<<20 {
			return nil, errors.New("cannot read the file it names, or it is over 1 MiB")
		}
	}
	var err error
	if p.operator, err = ParsePrices(data); err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	return p, nil
}

var priceKey = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/@+-]{0,199}\*?$`)

// ParsePrices parses a JSON object: model id (case-insensitive, trailing * = prefix) -> null (no
// price) or {"in": USD, "out": USD} per 1M tokens, 0–10000. Errors never quote the input.
func ParsePrices(data []byte) (map[string]*Rate, error) {
	var in map[string]*struct{ In, Out *float64 }
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // a misspelt field must not pass silently
	if dec.Decode(&in) != nil || in == nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New(`is not a JSON object of model id -> null or {"in": USD, "out": USD}`)
	}
	out := map[string]*Rate{}
	for k, r := range in {
		key := strings.ToLower(strings.TrimSpace(k))
		_, dup := out[key]
		switch {
		case !priceKey.MatchString(key):
			return nil, errors.New("a model id is not 1-200 letters, digits and . _ : / @ + -, with an optional trailing * for a prefix")
		case dup:
			return nil, errors.New("a model id is repeated (ids are case-insensitive)")
		case r == nil:
			out[key] = nil
		case r.In == nil || r.Out == nil || !(*r.In >= 0 && *r.In <= 1e4 && *r.Out >= 0 && *r.Out <= 1e4):
			return nil, errors.New(`an entry is not null or {"in": USD, "out": USD} with both rates from 0 to 10000`)
		default:
			out[key] = &Rate{*r.In, *r.Out}
		}
	}
	return out, nil
}

// Lookup returns model's rate (nil: no price), whether any entry matched, and whether it is the operator's.
func (p *Prices) Lookup(model string) (rate *Rate, known, operator bool) {
	id := strings.ToLower(strings.TrimSpace(model))
	ids := []string{id, id[strings.LastIndexByte(id, '/')+1:]} // "vendor/model" is also tried as "model"
	for i, t := range []map[string]*Rate{p.operator, p.builtin} {
		for _, x := range ids {
			if r, ok := t[x]; ok {
				return r, true, i == 0
			}
			best := -1
			for k, v := range t {
				if pre, ok := strings.CutSuffix(k, "*"); ok && len(pre) > best && strings.HasPrefix(x, pre) && (x == ids[1] || strings.Contains(pre, "/")) {
					best, rate = len(pre), v
				}
			}
			if best >= 0 {
				return rate, true, i == 0
			}
		}
	}
	return nil, false, false
}

// charge prices one call from the table, whatever the host: no price means cost unknown (nil,
// warned once), never $0. With BIGLAW_LOCAL_WATTS set the call also records watt-hours.
func (p *Prices) charge(model string, u *Usage, watts float64, log *slog.Logger) {
	if watts > 0 {
		wh := watts * u.Duration.Hours()
		u.WattHours = &wh
	}
	if r, _, _ := p.Lookup(model); r != nil {
		c := r.Cost(*u)
		u.CostUSD = &c
	} else if _, seen := p.warned.LoadOrStore(strings.ToLower(model), true); !seen {
		log.Warn("no price for this model: its cost is recorded as unknown, not $0, and the USD budget cannot count it; add it to BIGLAW_PRICES", "model", model)
	}
}
