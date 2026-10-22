// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Operand is a calc input: a verified finding value ("F12.v1") or a literal with its unit.
type Operand struct {
	Ref     *string `json:"ref"`
	Literal *string `json:"literal"`
	Unit    *string `json:"unit"`
}

// Worksheet is a calc result the deliverable can cite by WID; the same inputs give the same WID.
type Worksheet struct {
	WID             string      `json:"wid"`
	Result          string      `json:"result"`
	Unit            string      `json:"unit"`
	Steps           []SheetStep `json:"worksheet"`
	LiteralOperands bool        `json:"literalOperands"` // some operand was typed, not a verified value
}

type SheetStep struct {
	Step  string `json:"step"`
	Value string `json:"value"`
}

type operand struct {
	num  *big.Rat
	unit string
	date *Date // a full date
	text string
}

var refRe = regexp.MustCompile(`^(F\d+)\.v(\d+)$`)

// resolve reads an operand: a verified finding's value (an unknown or quarantined finding is an
// error, never zero) or a literal with its unit ("date" for a date). Money is USD only when its
// unit says so or says nothing; another currency keeps its own unit.
func resolve(findings []Finding, o Operand) (operand, error) {
	v := Value{Value: deref(o.Literal), Unit: deref(o.Unit)}
	if o.Ref != nil {
		m := refRe.FindStringSubmatch(*o.Ref)
		if m == nil {
			return operand{}, fmt.Errorf("%q is not a value reference such as F12.v1", *o.Ref)
		}
		i := slices.IndexFunc(findings, func(f Finding) bool { return f.FID == m[1] })
		n, _ := strconv.Atoi(m[2])
		switch {
		case i < 0:
			return operand{}, fmt.Errorf("no finding %s in this task's ledger", m[1])
		case !findings[i].Verified:
			return operand{}, fmt.Errorf("%s is quarantined (its quotes did not verify): only verified values may be used", m[1])
		case n < 1 || n > len(findings[i].Values):
			return operand{}, fmt.Errorf("%s has %d values, so %s does not exist", m[1], len(findings[i].Values), *o.Ref)
		}
		v = findings[i].Values[n-1]
	} else if o.Literal == nil {
		return operand{}, errors.New("an operand needs ref or literal")
	}
	unit := cmp.Or(map[Class]string{"Percentage": "%", "Date": "date"}[ClassOf(v)], strings.ToLower(v.Unit))
	if ClassOf(v) == "Money" && slices.Contains([]string{"", "$", "usd", "dollars"}, unit) {
		unit = "USD"
	}
	x := operand{unit: unit, text: strings.TrimPrefix(deref(o.Ref)+" = "+v.Value, " = ")}
	if d, ok := ParseDate(v.Value); unit == "date" {
		if !ok || d.Day == 0 {
			return x, fmt.Errorf("%q is not a full date", v.Value)
		}
		x.date = &d
	} else if x.num, ok = Number(v.Value); !ok {
		return x, fmt.Errorf("%q is not a number (give a date with unit \"date\")", v.Value)
	}
	return x, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// takes says what each fn accepts, for the error that names it.
var takes = map[string]string{"sum": "two or more numbers in one unit", "diff": "two numbers in one unit", "ratio": "two numbers", "pct": "two numbers",
	"date_add": "a full date and a whole number of days", "date_diff": "two full dates", "business_days": "two full dates, or a full date and a whole number of days"}

// Calc runs one calculation exactly (decimal, never floating point): sum, diff, ratio, pct,
// date_add, date_diff (days), business_days (date to date: a count; date + n: a date; weekends and
// the caller's holidays skipped). The worksheet id is a hash of the steps: same inputs, same id.
func Calc(findings []Finding, fn string, ops []Operand, holidays []string) (Worksheet, error) {
	var w Worksheet
	var xs []operand
	shape, same := "", true
	for i, o := range ops {
		x, err := resolve(findings, o)
		if err != nil {
			return Worksheet{}, fmt.Errorf("operand %d: %w", i+1, err)
		}
		same = same && (i == 0 || x.unit == xs[0].unit)
		xs, shape = append(xs, x), shape+map[bool]string{true: "d", false: "n"}[x.date != nil]
		w.LiteralOperands = w.LiteralOperands || o.Ref == nil
		w.Steps = append(w.Steps, SheetStep{fmt.Sprintf("operand %d", i+1), strings.TrimSpace(x.text + " " + x.unit)})
	}
	days := len(xs) == 2 && xs[1].num != nil && xs[1].num.IsInt() && xs[1].num.Num().IsInt64() && xs[1].num.Num().Int64() >= 0 && xs[1].num.Num().Int64() <= 10000
	if !map[string]bool{"sum": same && len(shape) >= 2 && !strings.Contains(shape, "d"), "diff": same && shape == "nn", "ratio": shape == "nn", "pct": shape == "nn",
		"date_add": shape == "dn" && days, "date_diff": shape == "dd", "business_days": shape == "dd" || shape == "dn" && days}[fn] {
		if takes[fn] == "" {
			return Worksheet{}, fmt.Errorf("unknown fn %q", fn)
		}
		return Worksheet{}, fmt.Errorf("%s takes %s", fn, takes[fn])
	}
	off := map[string]bool{}
	for _, h := range holidays {
		d, ok := ParseDate(h)
		if !ok || d.Day == 0 {
			return Worksheet{}, fmt.Errorf("holiday %q is not a full date", h)
		}
		off[d.Key()] = true
	}
	workday := func(t time.Time) bool { return t.Weekday()%6 != 0 && !off[t.Format(time.DateOnly)] }
	r := new(big.Rat)
	switch fn {
	case "sum", "diff":
		r.Set(xs[0].num)
		for _, x := range xs[1:] {
			r.Add(r, map[bool]*big.Rat{true: x.num, false: new(big.Rat).Neg(x.num)}[fn == "sum"])
		}
		w.Result, w.Unit = Decimal(r), xs[0].unit
	case "ratio", "pct":
		if xs[1].num.Sign() == 0 {
			return Worksheet{}, errors.New(fn + ": division by zero")
		}
		r.Quo(xs[0].num, xs[1].num)
		if fn == "pct" {
			r.Mul(r, big.NewRat(100, 1))
			w.Unit = "%"
		}
		w.Result = Decimal(r)
	case "date_add":
		w.Result, w.Unit = xs[0].date.time().AddDate(0, 0, int(xs[1].num.Num().Int64())).Format(time.DateOnly), "date"
	case "date_diff":
		w.Result, w.Unit = strconv.FormatInt((xs[1].date.time().Unix()-xs[0].date.time().Unix())/86400, 10), "days"
	default: // business_days
		t := xs[0].date.time()
		if shape == "dd" { // the working days after the first date, up to and including the second
			n, end := 0, xs[1].date.time()
			if end.Before(t) {
				return Worksheet{}, errors.New("business_days: the second date is before the first; put the earlier date first")
			}
			for t = t.AddDate(0, 0, 1); !t.After(end) && n < 1e6; t = t.AddDate(0, 0, 1) {
				n += map[bool]int{true: 1}[workday(t)]
			}
			w.Result, w.Unit = strconv.Itoa(n), "business days"
			break
		}
		for n := xs[1].num.Num().Int64(); n > 0; n-- {
			for t = t.AddDate(0, 0, 1); !workday(t); t = t.AddDate(0, 0, 1) {
			}
		}
		w.Result, w.Unit = t.Format(time.DateOnly), "date"
	}
	w.Steps = append(w.Steps, SheetStep{fn, w.Result + " " + w.Unit})
	sort.Strings(holidays)
	key, _ := json.Marshal([]any{fn, w.Steps, holidays})
	sum := sha256.Sum256(key)
	w.WID = "W" + hex.EncodeToString(sum[:5])
	return w, nil
}

// Handle is a neutral codename for one verified figure. Drafters write {Codename}; the renderer
// substitutes the exact value, so a model never types a digit (G8). The braces keep a party that
// happens to share a codename's word from ever being rewritten.
type Handle struct {
	Handle string `json:"handle"`
	FID    string `json:"fid"`
	Ref    string `json:"ref"`
	Label  string `json:"label"`
	Value  string `json:"-"`
}

var codenames = strings.Fields(`Zephyr Quasar Nimbus Halcyon Obsidian Saffron Tundra Vesper Cobalt Juniper Marigold Onyx
	Peregrine Quill Sorrel Tamarind Umber Verdigris Wren Yarrow Azimuth Basalt Cinder Dahlia Ember Fennel Garnet Heron Indigo
	Jasper Kestrel Lichen Magpie Nectar Opal Pumice Quartz Russet Sable Thistle Ursa Vellum Willow Xenon Zinnia Alder Bramble
	Cedar Drift Egret Flint Gorse Hazel Iris Kelp Lark Moss Nettle Osprey Plume Rook Sedge Tansy Vireo`)

// MaxHandles is how many figures can have a handle at once (the codename pool).
var MaxHandles = len(codenames)

// Salience ranks a figure for the key-figures guarantee: big money, large counts and major rates
// first; small bare numbers (exhibit noise) last (v1's figureSalience).
func Salience(v string) int {
	x, _ := Number(v)
	f := 0.0
	if x != nil {
		f, _ = x.Float64()
	}
	dollar, pct := strings.Contains(v, "$"), strings.Contains(v, "%")
	switch {
	case dollar && f >= 100000:
		return 100
	case strings.Contains(v, ",") && !dollar && f >= 1000:
		return 95
	case dollar && f >= 10000:
		return 90
	case dollar && f >= 1000:
		return 70
	case pct && f >= 40:
		return 60
	case pct:
		return 45
	case f >= 1000:
		return 40
	case dollar:
		return 30
	}
	return 10
}

// Handles gives a codename to each distinct verified figure (money, percentages, counts,
// identifiers; never a date), the most salient first, and returns those of fids (all findings when
// empty), the top first. Codenames are assigned over the whole ledger, so one names one figure
// whatever fids a call asked for.
func Handles(findings []Finding, fids []string, top int) []Handle {
	var hs []Handle
	seen := map[string]bool{}
	for _, f := range findings {
		if !f.Verified {
			continue
		}
		for i, v := range f.Values {
			c := ClassOf(v)
			if c == "" || c == "Date" || !c.IsA("Quantity") && c != "Authority" || seen[v.Value] || strings.TrimSpace(v.Value) == "" {
				continue
			}
			seen[v.Value] = true
			hs = append(hs, Handle{FID: f.FID, Ref: fmt.Sprintf("%s.v%d", f.FID, i+1), Label: strings.TrimSpace(v.Entity + " " + v.Metric), Value: v.Value})
		}
	}
	slices.SortStableFunc(hs, func(a, b Handle) int { return Salience(b.Value) - Salience(a.Value) })
	hs = hs[:min(len(hs), MaxHandles)]
	for i := range hs {
		hs[i].Handle = "{" + codenames[i] + "}"
	}
	hs = slices.DeleteFunc(hs, func(h Handle) bool { return len(fids) > 0 && !slices.Contains(fids, h.FID) })
	if top <= 0 {
		top = MaxHandles
	}
	return hs[:min(len(hs), top)]
}

var handleRe = regexp.MustCompile(`\{([A-Za-z]+)\}`)

// Substitute replaces every {Codename} with its figure's exact value, literally (a "$7" is data,
// never a template). A codename with no figure is an error, never a guess; other braces are text.
func Substitute(text string, hs []Handle) (string, error) {
	var unknown []string
	out := handleRe.ReplaceAllStringFunc(text, func(m string) string {
		for _, h := range hs {
			if strings.EqualFold(h.Handle, m) {
				return h.Value
			}
		}
		if slices.ContainsFunc(codenames, func(c string) bool { return strings.EqualFold("{"+c+"}", m) }) {
			unknown = append(unknown, m)
		}
		return m
	})
	if len(unknown) > 0 {
		return text, fmt.Errorf("unknown figure handle %s: it names no verified figure", strings.Join(unknown, ", "))
	}
	return out, nil
}

// KeyFigures is a Markdown table built only from verified values of fids (all when empty).
func KeyFigures(findings []Finding, fids []string) string {
	esc := strings.NewReplacer("|", `\|`, "\n", " ")
	rows := []string{"| Figure | Value | Finding |", "|---|---|---|"}
	for _, f := range findings {
		if !f.Verified || len(fids) > 0 && !slices.Contains(fids, f.FID) {
			continue
		}
		for _, v := range f.Values {
			if ClassOf(v) != "" && strings.TrimSpace(v.Value) != "" {
				rows = append(rows, "| "+esc.Replace(strings.TrimSpace(v.Entity+" "+v.Metric))+" | "+esc.Replace(v.Value)+" | ["+f.FID+"] |")
			}
		}
	}
	return strings.Join(rows, "\n")
}
