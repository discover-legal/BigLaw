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
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
)

// Rate is USD per million tokens. Cache writes cost 1.25× and cache reads
// 0.10× the input rate (v1 cost/cost.go).
type Rate struct{ In, Out float64 }

func (r Rate) Cost(u Usage) float64 {
	return (float64(u.InputTokens)*r.In + float64(u.OutputTokens)*r.Out +
		float64(u.CacheWriteTokens)*r.In*1.25 + float64(u.CacheReadTokens)*r.In*0.10) / 1e6
}

// The built-in table is data in the BIGLAW_PRICES format, read by the same
// parser (DESIGN § Revision 5, way 3), ported from v1's cost.go.
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

// Prices resolves a model id: the operator's BIGLAW_PRICES table first,
// then the built-in one. In each an exact id beats the longest prefix key
// ("deepseek*"), and "vendor/model" is also tried as "model". A null entry
// is a known model without a price: its cost is unknown (nil), never $0,
// and it keeps a shorter prefix from pricing it.
type Prices struct {
	operator, builtin map[string]*Rate
	warned            sync.Map
}

// LoadPrices reads BIGLAW_PRICES: empty is the built-in table alone; a
// value starting with { or [ is the JSON; anything else is a file path.
// Errors never echo the value or the file (a wrong path may name a secret).
func LoadPrices(value string) (*Prices, error) {
	p := &Prices{builtin: builtinPrices}
	data, src := []byte(strings.TrimSpace(value)), "the JSON value"
	if len(data) == 0 {
		return p, nil
	}
	if data[0] != '{' && data[0] != '[' {
		src = "the file it names"
		f, err := os.Open(string(data))
		if err != nil {
			var pe *os.PathError
			if errors.As(err, &pe) {
				err = pe.Err // the path is the value: do not repeat it
			}
			return nil, fmt.Errorf("cannot open the file it names: %v", err)
		}
		defer f.Close()
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

// ParsePrices parses one JSON object mapping a model id (case-insensitive;
// a trailing * makes it a prefix) to null or {"in": USD, "out": USD} per
// million tokens, both required and 0–10000. Unknown fields and repeated
// ids are refused. Errors give an entry number and byte offset, never text.
func ParsePrices(data []byte) (map[string]*Rate, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("is not a JSON object")
	}
	out := map[string]*Rate{}
	for n := 1; ; n++ {
		bad := func(why string) error { return fmt.Errorf("entry %d (near byte %d): %s", n, dec.InputOffset(), why) }
		t, err := dec.Token()
		if t == json.Delim('}') {
			break
		}
		var raw json.RawMessage
		key, _ := t.(string)
		if key = strings.ToLower(strings.TrimSpace(key)); err != nil || dec.Decode(&raw) != nil {
			return nil, bad("is not valid JSON")
		}
		if !priceKey.MatchString(key) {
			return nil, bad("the model id must be 1-200 letters, digits and . _ : / @ + -, with an optional trailing * for a prefix")
		}
		if _, dup := out[key]; dup {
			return nil, bad("repeats an earlier model id (ids are case-insensitive)")
		}
		var r struct{ In, Out *float64 }
		rd := json.NewDecoder(bytes.NewReader(raw))
		rd.DisallowUnknownFields()
		if string(raw) == "null" {
			out[key] = nil
		} else if rd.Decode(&r) != nil || r.In == nil || r.Out == nil {
			return nil, bad(`must be null or {"in": USD, "out": USD} per million tokens`)
		} else if !(*r.In >= 0 && *r.In <= 1e4 && *r.Out >= 0 && *r.Out <= 1e4) {
			return nil, bad("rates must be from 0 to 10000 (USD per million tokens)")
		} else {
			out[key] = &Rate{*r.In, *r.Out}
		}
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("has data after the closing }")
	}
	return out, nil
}

// Lookup returns model's rate (nil if known but unpriced), whether any
// entry matched, and whether the match is the operator's.
func (p *Prices) Lookup(model string) (rate *Rate, known, operator bool) {
	id := strings.ToLower(strings.TrimSpace(model))
	ids := []string{id}
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		ids = append(ids, id[i+1:])
	}
	for i, t := range []map[string]*Rate{p.operator, p.builtin} {
		for _, x := range ids {
			if r, ok := t[x]; ok {
				return r, true, i == 0
			}
			best := -1
			for k, r := range t {
				if pre, ok := strings.CutSuffix(k, "*"); ok && len(pre) > best && strings.HasPrefix(x, pre) {
					best, rate = len(pre), r
				}
			}
			if best >= 0 {
				return rate, true, i == 0
			}
		}
	}
	return nil, false, false
}

// Cost is usage's USD cost on model, or nil if the model has no price.
func (p *Prices) Cost(model string, u Usage) *float64 {
	if r, _, _ := p.Lookup(model); r != nil {
		c := r.Cost(u)
		return &c
	}
	return nil
}

// charge prices one call. A local endpoint's call costs $0 and is metered
// in watt-hours, even when the name matches a hosted price, unless the
// operator's BIGLAW_PRICES names the model (a local gateway that forwards
// to a cloud vendor). An unpriced model is warned about once.
func (p *Prices) charge(model string, u *Usage, local bool, watts float64, log *slog.Logger) {
	if _, _, op := p.Lookup(model); local && !op {
		zero, wh := 0.0, watts*u.Duration.Hours()
		u.CostUSD, u.WattHours = &zero, &wh
	} else if u.CostUSD = p.Cost(model, *u); u.CostUSD == nil {
		if _, seen := p.warned.LoadOrStore(strings.ToLower(model), true); !seen {
			log.Warn("no price for this model: its cost is recorded as unknown, not $0, and the USD budget cannot count it; add it to BIGLAW_PRICES", "model", model)
		}
	}
}

// isLocalHost: loopback, a private or link-local IP, localhost, a
// single-label name (a Compose service such as "ollama"), or a .local,
// .internal, .lan or .home.arpa name.
func isLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	for _, s := range []string{".local", ".internal", ".lan", ".home.arpa"} {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return host != "" && !strings.Contains(host, ".")
}
