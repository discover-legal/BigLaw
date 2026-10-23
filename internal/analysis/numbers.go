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

// refRe reads a value reference as models write it: F12.v1 (the form ledger and handles show),
// also [F12].v1, f12.V1, F12 v1, F12.value1; F12.d1 is F12's first date value; a bare F12 is its
// only value.
var (
	refRe  = regexp.MustCompile(`(?i)^\[?(F\d+)\]?(?:[.\s_-]*(v|value|d|date)\s*(\d+))?$`)
	daysRe = regexp.MustCompile(`(?i)^(-?\d+)\s*(?:calendar\s+)?days?$`)
)

// valueOf finds the value a reference names; a reference that names none is an error that lists
// the finding's values by the reference to send.
func valueOf(findings []Finding, ref string) (Value, string, error) {
	m := refRe.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return Value{}, "", fmt.Errorf("%q is not a value reference: write a finding's value as F12.v1 (ledger op get with detail full lists them)", ref)
	}
	fid := strings.ToUpper(m[1])
	i := slices.IndexFunc(findings, func(f Finding) bool { return f.FID == fid })
	if i < 0 {
		return Value{}, "", fmt.Errorf("no finding %s in this task's ledger", fid)
	} else if !findings[i].Verified {
		return Value{}, "", fmt.Errorf("%s is quarantined (its quotes did not verify): only verified values may be used", fid)
	}
	vs, n := findings[i].Values, 1
	if m[3] != "" {
		n, _ = strconv.Atoi(m[3])
	}
	j := -1
	switch kind := strings.ToLower(m[2]); {
	case kind == "" && len(vs) == 1:
		j = 0
	case strings.HasPrefix(kind, "v"):
		j = n - 1
	case kind != "": // the nth value that is a date
		for k := range vs {
			if ClassOf(vs[k]) == "Date" {
				if n--; n == 0 {
					j = k
					break
				}
			}
		}
	}
	if j >= 0 && j < len(vs) {
		return vs[j], fmt.Sprintf("%s.v%d", fid, j+1), nil
	}
	var list []string
	for j, v := range vs {
		list = append(list, fmt.Sprintf("%s.v%d = %s", fid, j+1, v.Value)+map[bool]string{true: " (as of " + v.Date + ")"}[v.Date != ""])
	}
	return Value{}, "", fmt.Errorf("%q is not a value reference here: %s has %d values, %s; send one of those refs, e.g. {\"ref\": \"%s.v1\", \"literal\": null, \"unit\": null} (an as-of date is not a value: give it as a literal with unit \"date\")",
		ref, fid, len(vs), strings.Join(list, ", "), fid)
}

// resolve reads an operand: a verified finding's value (an unknown or quarantined finding is an
// error, never zero) or a literal with its unit ("date" for a date). A literal that is plainly a
// full date or a count of days ("21 days") is read as one without its unit. Money is USD only
// when its unit says so or says nothing; another currency keeps its own unit.
func resolve(findings []Finding, o Operand) (operand, error) {
	v, name := Value{Value: deref(o.Literal), Unit: deref(o.Unit)}, ""
	if o.Ref != nil {
		var err error
		if v, name, err = valueOf(findings, *o.Ref); err != nil {
			return operand{}, err
		}
	} else if o.Literal == nil {
		return operand{}, errors.New(`an operand needs ref or literal, e.g. {"ref": "F12.v1", "literal": null, "unit": null} or {"ref": null, "literal": "21", "unit": "days"}`)
	}
	unit := cmp.Or(map[Class]string{"Percentage": "%", "Date": "date"}[ClassOf(v)], strings.ToLower(v.Unit))
	if ClassOf(v) == "Money" && slices.Contains([]string{"", "$", "usd", "dollars"}, unit) {
		unit = "USD"
	}
	if strings.Contains(unit, "day") { // "calendar days", "day"
		unit = "days"
	}
	if m := daysRe.FindStringSubmatch(strings.TrimSpace(v.Value)); m != nil && (unit == "" || unit == "days" || unit == "count") {
		v.Value, unit = m[1], "days"
	}
	if _, isNum := Number(v.Value); unit == "" && !isNum {
		if d, ok := ParseDate(v.Value); ok && d.Day != 0 {
			unit = "date"
		}
	}
	x := operand{unit: unit, text: strings.TrimPrefix(name+" = "+v.Value, " = ")}
	if d, ok := ParseDate(v.Value); unit == "date" {
		if !ok || d.Day == 0 {
			return x, fmt.Errorf("%q is not a full date (write it as 2026-11-12)", v.Value)
		}
		x.date = &d
	} else if x.num, ok = Number(v.Value); !ok {
		return x, fmt.Errorf("%q is not a number (give a date with unit \"date\", a number of days as \"21\" with unit \"days\")", v.Value)
	}
	return x, nil
}

func abs(n int64) int64 { return max(n, -n) }

// cmpExample is a call of fn that works, for its error.
func cmpExample(fn string) string {
	second := map[string]string{"date_add": `{"ref": null, "literal": "21", "unit": "days"}`, "business_days": `{"ref": null, "literal": "10", "unit": "days"}`,
		"date_diff": `{"ref": null, "literal": "2026-11-12", "unit": "date"}`, "product": `{"ref": null, "literal": "35", "unit": "%"}`}[fn]
	return `{"op": "calc", "fn": "` + fn + `", "operands": [{"ref": "F12.v1", "literal": null, "unit": null}, ` + cmp.Or(second, `{"ref": "F7.v2", "literal": null, "unit": null}`) +
		`], "holidays": ` + map[bool]string{true: `["2026-11-11"]`, false: "null"}[fn == "date_add" || fn == "business_days"] + `, "fids": null, "salientTop": null}`
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// takes says what each fn accepts, and shows a call that works, for the error that names it.
var takes = map[string]string{"sum": "two or more numbers in one unit", "diff": "two numbers in one unit", "ratio": "two numbers", "pct": "two numbers",
	"product":  "two or more numbers, at most one of them not a percentage (35% times $100,000 is $35,000)",
	"date_add": "a full date and a whole number of days (negative counts back)", "date_diff": "two full dates",
	"business_days": "two full dates, or a full date and a whole number of days (negative counts back)"}

// Calc runs one calculation exactly (decimal, never floating point): sum, diff, ratio, pct,
// product, date_add, date_diff (days), business_days (date to date: a count; date + n: a date;
// weekends and the caller's holidays skipped). date_add with holidays set (even []) rolls a result
// that falls on a weekend or holiday to the next day that is not, backward for a negative count
// (FRCP 6(a)(1)(C), 6(a)(5)); the worksheet shows the raw date. The worksheet id is a hash of the
// steps: same inputs, same id.
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
	if shape == "nd" && (fn == "date_add" || fn == "business_days") { // a count then a date: unambiguous
		xs[0], xs[1], shape = xs[1], xs[0], "dn"
	}
	days := len(xs) == 2 && xs[1].num != nil && xs[1].num.IsInt() && xs[1].num.Num().IsInt64() && abs(xs[1].num.Num().Int64()) <= 10000 &&
		(xs[1].unit == "" || xs[1].unit == "days")
	pcts := 0
	for _, x := range xs {
		pcts += map[bool]int{true: 1}[x.unit == "%"]
	}
	if !map[string]bool{"sum": same && len(shape) >= 2 && !strings.Contains(shape, "d"), "diff": same && shape == "nn", "ratio": shape == "nn", "pct": shape == "nn",
		"product":  len(shape) >= 2 && !strings.Contains(shape, "d") && pcts >= len(xs)-1,
		"date_add": shape == "dn" && days, "date_diff": shape == "dd", "business_days": shape == "dd" || shape == "dn" && days}[fn] {
		if takes[fn] == "" {
			return Worksheet{}, fmt.Errorf("unknown fn %q", fn)
		}
		got := strings.NewReplacer("d", " a date,", "n", " a number,").Replace(shape)
		return Worksheet{}, fmt.Errorf("%s takes %s; it got%s so send, e.g., %s", fn, takes[fn], strings.TrimSuffix(cmp.Or(got, " no operands,"), ","),
			cmpExample(fn))
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
	case "product":
		r.SetInt64(1)
		for _, x := range xs {
			r.Mul(r, map[bool]*big.Rat{true: new(big.Rat).Quo(x.num, big.NewRat(100, 1)), false: x.num}[x.unit == "%"])
		}
		w.Unit = "%"
		for _, x := range xs {
			if x.unit != "%" {
				w.Unit = x.unit
			}
		}
		if w.Unit == "%" { // percentages only: the product is a percentage again
			r.Mul(r, big.NewRat(100, 1))
		}
		w.Result = Decimal(r)
	case "date_add":
		n := xs[1].num.Num().Int64()
		t := xs[0].date.time().AddDate(0, 0, int(n))
		if holidays != nil && !workday(t) {
			w.Steps = append(w.Steps, SheetStep{"raw date", t.Format(time.DateOnly) + " (" + t.Weekday().String() + map[bool]string{true: ", a weekend day)", false: ", a holiday)"}[t.Weekday()%6 == 0]})
			for step := map[bool]int{true: -1, false: 1}[n < 0]; !workday(t); t = t.AddDate(0, 0, step) {
			}
			w.Steps = append(w.Steps, SheetStep{map[bool]string{true: "rolled back", false: "rolled forward"}[n < 0], t.Format(time.DateOnly) + " (" + t.Weekday().String() + ")"})
		}
		w.Result, w.Unit = t.Format(time.DateOnly), "date"
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
		n, step := xs[1].num.Num().Int64(), 1
		if n < 0 {
			n, step = -n, -1
		}
		for ; n > 0; n-- {
			for t = t.AddDate(0, 0, step); !workday(t); t = t.AddDate(0, 0, step) {
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
// whatever fids a call asked for; kept are codenames given before (a run's), which keep their
// figures, a new figure taking the next codename none has used, so a codename never changes meaning
// as the ledger grows.
func Handles(findings []Finding, fids []string, top int, kept ...Handle) []Handle {
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
	name, used, next := map[string]string{}, map[string]bool{}, 0
	for _, k := range kept {
		name[k.Value], used[k.Handle] = k.Handle, true
	}
	var named []Handle
	for _, h := range hs {
		if h.Handle = name[h.Value]; h.Handle == "" {
			for next < len(codenames) && used["{"+codenames[next]+"}"] {
				next++
			}
			if next == len(codenames) { // the pool is spent: the figure gets no handle
				continue
			}
			h.Handle, used["{"+codenames[next]+"}"] = "{"+codenames[next]+"}", true
		}
		named = append(named, h)
	}
	hs = slices.DeleteFunc(named, func(h Handle) bool { return len(fids) > 0 && !slices.Contains(fids, h.FID) })
	if top <= 0 {
		top = MaxHandles
	}
	return hs[:min(len(hs), top)]
}

// handleRe matches a {Codename} or a worksheet's {W0123456789} (its wid).
var (
	handleRe = regexp.MustCompile(`\{([A-Za-z]+|W[0-9a-f]{10})\}`)
	widRe    = regexp.MustCompile(`^\{W[0-9a-f]{10}\}$`)
)

// Substitute replaces every {Codename} and {W…} with its figure's exact value, literally (a "$7" is
// data, never a template). A handle with no figure is left as written and reported, never guessed;
// other braces are text.
func Substitute(text string, hs []Handle) (string, error) {
	var unknown []string
	out := handleRe.ReplaceAllStringFunc(text, func(m string) string {
		for _, h := range hs {
			if strings.EqualFold(h.Handle, m) {
				return h.Value
			}
		}
		if widRe.MatchString(m) || slices.ContainsFunc(codenames, func(c string) bool { return strings.EqualFold("{"+c+"}", m) }) {
			unknown = append(unknown, m)
		}
		return m
	})
	if len(unknown) > 0 {
		return out, fmt.Errorf("unknown figure handle %s: it names no verified figure or worksheet", strings.Join(unknown, ", "))
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
