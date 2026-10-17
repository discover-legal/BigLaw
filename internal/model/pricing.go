// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
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

// Rate is USD per million tokens. Cache writes cost 1.25× and cache reads 0.10× the input rate.
type Rate struct{ In, Out float64 }

func (r Rate) Cost(u Usage) float64 {
	return (float64(u.InputTokens)*r.In + float64(u.OutputTokens)*r.Out +
		float64(u.CacheWriteTokens)*r.In*1.25 + float64(u.CacheReadTokens)*r.In*0.10) / 1e6
}

// Entry is one price-table row: Rate is nil for a model with no price; Open marks open weights,
// the only models a local endpoint runs for $0 + watt-hours.
type Entry struct {
	Rate *Rate
	Open bool
}

// The built-in table is prices.json, read by the BIGLAW_PRICES parser (DESIGN § Revision 5, way 3).
//
//go:embed prices.json
var pricesJSON []byte

var builtinPrices = func() map[string]Entry {
	t, err := ParsePrices(pricesJSON)
	if err != nil {
		panic("model: built-in prices.json: " + err.Error())
	}
	return t
}()

// Prices resolves a model id in the operator's table, then the built-in one: in each an exact id
// beats the longest prefix key, and "vendor/model" is also tried as "model".
type Prices struct {
	operator, builtin map[string]Entry
	warned            sync.Map
}

// LoadPrices reads BIGLAW_PRICES: empty is the built-in table alone; a value starting with { or [
// is the JSON; anything else names a regular file. Errors never echo the value or the file.
func LoadPrices(value string) (*Prices, error) {
	p := &Prices{builtin: builtinPrices}
	data, src, err := []byte(strings.TrimSpace(value)), "the JSON value", error(nil)
	if len(data) == 0 {
		return p, nil
	}
	if data[0] != '{' && data[0] != '[' {
		src = "the file it names"
		var f *os.File
		if fi, err := os.Stat(string(data)); err == nil && fi.Mode().IsRegular() { // a FIFO would block startup
			f, _ = os.Open(string(data))
		}
		if f == nil { // no OS error text: it repeats the path, which may name a secret
			return nil, errors.New("cannot open the file it names: missing, unreadable, or not a regular file")
		}
		defer f.Close()
		if data, err = io.ReadAll(io.LimitReader(f, 1<<20+1)); err != nil || len(data) > 1<<20 {
			return nil, errors.New("cannot read the file it names, or it is over 1 MiB")
		}
	}
	if p.operator, err = ParsePrices(data); err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	return p, nil
}

var priceKey = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/@+-]{0,199}\*?$`)

// ParsePrices parses a JSON object: model id (case-insensitive, trailing * = prefix) -> null, {"open": true}
// or {"in": USD, "out": USD[, "open": true]} per 1M tokens, 0–10000. Errors never quote the input.
func ParsePrices(data []byte) (map[string]Entry, error) {
	var in map[string]*struct {
		In, Out *float64
		Open    bool
	}
	if json.Unmarshal(data, &in) != nil || in == nil {
		return nil, errors.New(`is not a JSON object of model id -> null, {"open": true} or {"in": USD, "out": USD}`)
	}
	out := map[string]Entry{}
	for k, r := range in {
		key := strings.ToLower(strings.TrimSpace(k))
		_, dup := out[key]
		switch {
		case !priceKey.MatchString(key):
			return nil, errors.New("a model id is not 1-200 letters, digits and . _ : / @ + -, with an optional trailing * for a prefix")
		case dup:
			return nil, errors.New("a model id is repeated (ids are case-insensitive)")
		case r == nil:
			out[key] = Entry{}
		case r.In == nil && r.Out == nil && r.Open:
			out[key] = Entry{Open: true}
		case r.In == nil || r.Out == nil || !(*r.In >= 0 && *r.In <= 1e4 && *r.Out >= 0 && *r.Out <= 1e4):
			return nil, errors.New(`an entry is not null, {"open": true} or {"in": USD, "out": USD} with both rates from 0 to 10000`)
		default:
			out[key] = Entry{&Rate{*r.In, *r.Out}, r.Open}
		}
	}
	return out, nil
}

// Lookup returns model's entry, whether any entry matched, and whether the match is the operator's.
func (p *Prices) Lookup(model string) (e Entry, known, operator bool) {
	id := strings.ToLower(strings.TrimSpace(model))
	ids := []string{id, id[strings.LastIndexByte(id, '/')+1:]} // "vendor/model" is also tried as "model"
	for i, t := range []map[string]Entry{p.operator, p.builtin} {
		for _, x := range ids {
			if e, ok := t[x]; ok {
				return e, true, i == 0
			}
			best := -1
			for k, v := range t {
				if pre, ok := strings.CutSuffix(k, "*"); ok && len(pre) > best && strings.HasPrefix(x, pre) {
					best, e = len(pre), v
				}
			}
			if best >= 0 {
				return e, true, i == 0
			}
		}
	}
	return Entry{}, false, false
}

// Pricing is how calls to model are charged: free means $0 + watt-hours, which only an open-weights
// or unlisted model on a local endpoint gets (the host never makes a closed model free).
func (p *Prices) Pricing(model string, local bool) (rate *Rate, free bool) {
	e, known, _ := p.Lookup(model)
	return e.Rate, local && (!known || e.Open)
}

// charge prices one call; an unpriced model is warned about once.
func (p *Prices) charge(model string, u *Usage, local bool, watts float64, log *slog.Logger) {
	if r, free := p.Pricing(model, local); free {
		zero, wh := 0.0, watts*u.Duration.Hours()
		u.CostUSD, u.WattHours = &zero, &wh
	} else if r != nil {
		c := r.Cost(*u)
		u.CostUSD = &c
	} else if _, seen := p.warned.LoadOrStore(strings.ToLower(model), true); !seen {
		log.Warn("no price for this model: its cost is recorded as unknown, not $0, and the USD budget cannot count it; add it to BIGLAW_PRICES", "model", model)
	}
}

var localName = regexp.MustCompile(`^[^.]+$|\.(local|internal|lan|home\.arpa)$`)

// IsLocalHost: loopback, a private or link-local IP, localhost, a single-label name (a Compose
// service such as "ollama"), or a .local, .internal, .lan or .home.arpa name.
func IsLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) || ip == nil && localName.MatchString(host)
}
