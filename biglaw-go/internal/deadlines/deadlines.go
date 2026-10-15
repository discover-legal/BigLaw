// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// DeadlineEngine — pure calendar arithmetic, no LLM, no network.
// Loads YAML jurisdiction rule files and computes downstream court/filing deadlines.

package deadlines

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// Engine loads YAML rule sets and computes deadlines.
type Engine struct {
	mu    sync.RWMutex
	rules map[string]*types.JurisdictionRules // jurisdiction → rules
}

// New creates a DeadlineEngine.
func New() *Engine {
	return &Engine{rules: make(map[string]*types.JurisdictionRules)}
}

// LoadRulesDir loads all .yaml/.yml files from a directory.
func (e *Engine) LoadRulesDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("DeadlineEngine: rules directory not found", "dir", dir)
		return nil
	}
	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			slog.Warn("DeadlineEngine: failed to read rule file", "file", name, "error", err)
			continue
		}
		var ruleset types.JurisdictionRules
		if err := yaml.Unmarshal(data, &ruleset); err != nil {
			slog.Warn("DeadlineEngine: failed to parse rule file", "file", name, "error", err)
			continue
		}
		if err := e.LoadRules(&ruleset); err != nil {
			slog.Warn("DeadlineEngine: invalid ruleset", "file", name, "error", err)
			continue
		}
		loaded++
	}
	slog.Info("DeadlineEngine: loaded rule sets", "count", loaded, "dir", dir)
	return nil
}

// LoadRules loads a single jurisdiction rule set.
func (e *Engine) LoadRules(rules *types.JurisdictionRules) error {
	if rules == nil || rules.Jurisdiction == "" || len(rules.Rules) == 0 {
		return fmt.Errorf("invalid ruleset: missing required fields")
	}
	e.mu.Lock()
	e.rules[strings.ToUpper(strings.TrimSpace(rules.Jurisdiction))] = rules
	e.mu.Unlock()
	return nil
}

// ListJurisdictions returns all loaded jurisdictions.
func (e *Engine) ListJurisdictions() []map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []map[string]interface{}
	for _, r := range e.rules {
		out = append(out, map[string]interface{}{
			"jurisdiction": r.Jurisdiction,
			"name":         r.Name,
			"id":           r.ID,
			"ruleCount":    len(r.Rules),
		})
	}
	return out
}

// UnknownJurisdictionError is returned when no rule set is loaded for the
// requested jurisdiction.
type UnknownJurisdictionError struct {
	Jurisdiction string
	Valid        []string
}

func (e *UnknownJurisdictionError) Error() string {
	return fmt.Sprintf("no rules loaded for jurisdiction: %s", e.Jurisdiction)
}

// UnknownTriggerError is returned when the trigger event matches no rule in
// the jurisdiction's rule set. Valid lists every trigger the set declares so
// callers can show the user what they may ask for.
type UnknownTriggerError struct {
	Jurisdiction string
	Trigger      string
	Valid        []string
}

func (e *UnknownTriggerError) Error() string {
	return fmt.Sprintf("unknown triggerEvent %q for jurisdiction %s; valid triggers: %s",
		e.Trigger, e.Jurisdiction, strings.Join(e.Valid, ", "))
}

// InvalidDateError is returned for a malformed triggerDate.
type InvalidDateError struct{ Value string }

func (e *InvalidDateError) Error() string {
	return fmt.Sprintf("invalid triggerDate %q: expected YYYY-MM-DD", e.Value)
}

func triggersOf(rs *types.JurisdictionRules) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rs.Rules {
		t := strings.ToLower(strings.TrimSpace(r.Trigger))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func (e *Engine) jurisdictionCodes() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.rules))
	for _, r := range e.rules {
		out = append(out, r.Jurisdiction)
	}
	sort.Strings(out)
	return out
}

// Compute returns all deadlines triggered by a given event and date.
//
// Counting follows the computation rule of the rule set's legal system
// (selected by its holiday calendar):
//
//   - us_federal — FRCP 6(a)(1) / FRAP 26(a)(1): exclude the trigger day,
//     count every day, and if the last day is a Saturday, Sunday or legal
//     holiday the period runs to the next day that is none of those.
//   - uk_bank — CPR 2.8: "clear days"; a period of 5 days or less excludes
//     Saturdays, Sundays, bank holidays, Christmas Day and Good Friday
//     (2.8(4)); a longer period that ends on a day the court office is
//     closed runs to the next day it is open (2.8(5)).
//   - eu_institutions — Reg. (EEC, Euratom) 1182/71 Art. 3: a period that
//     ends on a Saturday, Sunday or public holiday ends on the next working
//     day (Art. 3(4)).
//
// Business-day rules count only working days, so their result is always a
// working day. The warning date is counted back from the (adjusted) due
// date and moved EARLIER, never later, if it lands on a non-working day.
func (e *Engine) Compute(jurisdiction, triggerEvent, triggerDate string) (*types.DeadlineResult, error) {
	e.mu.RLock()
	ruleset := e.rules[strings.ToUpper(strings.TrimSpace(jurisdiction))]
	e.mu.RUnlock()
	if ruleset == nil {
		return nil, &UnknownJurisdictionError{Jurisdiction: jurisdiction, Valid: e.jurisdictionCodes()}
	}

	tDate, err := time.Parse("2006-01-02", triggerDate)
	if err != nil {
		return nil, &InvalidDateError{Value: triggerDate}
	}
	tDate = time.Date(tDate.Year(), tDate.Month(), tDate.Day(), 0, 0, 0, 0, time.UTC)

	trigger := strings.ToLower(strings.TrimSpace(triggerEvent))
	cal := ruleset.Holidays
	deadlines := []types.ComputedDeadline{}
	matched := false

	for _, rule := range ruleset.Rules {
		if strings.ToLower(strings.TrimSpace(rule.Trigger)) != trigger {
			continue
		}
		matched = true

		dueDate, unadjusted, adjustments := computeDue(tDate, rule, cal)
		backward := rule.Days < 0

		cd := types.ComputedDeadline{
			RuleID:      rule.ID,
			Event:       rule.Event,
			DueDate:     dueDate.Format("2006-01-02"),
			Days:        rule.Days,
			DayType:     rule.DayType,
			Cite:        rule.Cite,
			Note:        rule.Note,
			Adjustments: adjustments,
		}
		if backward {
			cd.Direction = "before"
		}
		if !unadjusted.Equal(dueDate) {
			cd.UnadjustedDate = unadjusted.Format("2006-01-02")
		}
		if rule.WarningDays > 0 {
			cd.WarningDate = warningDate(tDate, dueDate, rule, cal).Format("2006-01-02")
		}
		deadlines = append(deadlines, cd)
	}

	if !matched {
		return nil, &UnknownTriggerError{
			Jurisdiction: ruleset.Jurisdiction,
			Trigger:      triggerEvent,
			Valid:        triggersOf(ruleset),
		}
	}

	sort.SliceStable(deadlines, func(i, j int) bool {
		return deadlines[i].DueDate < deadlines[j].DueDate
	})

	return &types.DeadlineResult{
		Jurisdiction:     ruleset.Jurisdiction,
		JurisdictionName: ruleset.Name,
		TriggerEvent:     triggerEvent,
		TriggerDate:      tDate.Format("2006-01-02"),
		ComputedAt:       time.Now().UTC().Format(time.RFC3339),
		HolidayCalendar:  cal,
		Deadlines:        deadlines,
		Limitations:      limitationsFor(cal),
	}, nil
}

// computeDue applies the counting rule. Positive Days count forward from the
// trigger ("within N days after service"); negative Days count backward from
// it ("at least N days before the hearing"), and a backward period that ends
// on a non-working day continues BACKWARD — FRCP 6(a)(5) defines the "next
// day" for a backward-counted period as the preceding day — which is also
// the only safe reading under CPR and Reg. 1182/71 (rolling a "before" date
// forward would move it past the latest permitted day).
//
// CPR 2.8(2)-(3): periods are clear days — neither the day of the event nor
// the day on which the period ends is counted — so "at least 7 days before
// the hearing" on Friday 20th means by Thursday 12th (8 days earlier); a
// period of 5 days or less also skips weekends and bank holidays (2.8(4)),
// e.g. 3 days before Friday 20th → Monday 16th (CPR 2.8 worked example).
func computeDue(t time.Time, rule types.DeadlineRule, cal types.HolidayCalendar) (due, unadjusted time.Time, adjustments []string) {
	n := rule.Days
	backward := n < 0
	if backward {
		n = -n
	}
	ukClear := cal == types.HolidaysUKBank
	note := func(why []string, rule string) {
		for _, w := range why {
			adjustments = append(adjustments, w+" — "+rule)
		}
	}
	switch {
	case rule.DayType == types.DayTypeBusiness:
		if backward {
			due = subBusinessDays(t, n, cal)
		} else {
			due = addBusinessDays(t, n, cal)
		}
		return due, due, nil
	case ukClear && n <= 5:
		adjustments = append(adjustments,
			"CPR 2.8(4): period of 5 days or less excludes Saturdays, Sundays, bank holidays, Christmas Day and Good Friday")
		if !backward {
			due = addBusinessDays(t, n, cal)
			return due, due, adjustments
		}
		// n clear working days strictly between the act and the event; the
		// act is due on the day before the earliest of them.
		unadjusted = subBusinessDays(t, n, cal).AddDate(0, 0, -1)
		due, why := rollBackward(unadjusted, cal)
		note(why, rollRule(cal, true))
		return due, unadjusted, adjustments
	case backward:
		unadjusted = t.AddDate(0, 0, -n)
		if ukClear {
			unadjusted = unadjusted.AddDate(0, 0, -1)
			adjustments = append(adjustments,
				"CPR 2.8(3): clear days — neither the day of the event nor the day the period ends is counted")
		}
		due, why := rollBackward(unadjusted, cal)
		note(why, rollRule(cal, true))
		return due, unadjusted, adjustments
	default:
		unadjusted = t.AddDate(0, 0, n)
		due, why := rollForward(unadjusted, cal)
		note(why, rollRule(cal, false))
		return due, unadjusted, adjustments
	}
}

// warningDate counts the warning lead time back from the adjusted due date
// (business days for business-day rules, calendar days otherwise). A warning
// that lands on a non-working day moves to the PRECEDING working day — a
// reminder must never slip later, and is always on or before the due date.
func warningDate(trigger, due time.Time, rule types.DeadlineRule, cal types.HolidayCalendar) time.Time {
	var w time.Time
	if rule.DayType == types.DayTypeBusiness {
		w = subBusinessDays(due, rule.WarningDays, cal)
	} else {
		w = due.AddDate(0, 0, -rule.WarningDays)
		w, _ = rollBackward(w, cal)
	}
	// A forward period's reminder never precedes the trigger itself. A
	// backward period's trigger (the hearing) is after the due date, so no
	// such floor applies — the reminder is simply before the due date.
	if rule.Days >= 0 && w.Before(trigger) {
		w = trigger
	}
	return w
}

func rollRule(cal types.HolidayCalendar, backward bool) string {
	if backward {
		switch cal {
		case types.HolidaysUSFederal:
			return "a backward-counted period continues to the PRECEDING day that is not a Saturday, Sunday or legal holiday (FRCP 6(a)(5), 6(a)(1)(C))"
		case types.HolidaysUKBank:
			return "moved to the preceding working day: the act must be done on or before the last permitted day (CPR 2.8)"
		case types.HolidaysEUInstitutions:
			return "moved to the preceding working day (a time limit before an event cannot be extended past it)"
		default:
			return "moved to the preceding day that is not a Saturday or Sunday"
		}
	}
	switch cal {
	case types.HolidaysUSFederal:
		return "period continues to the next day that is not a Saturday, Sunday or legal holiday (FRCP 6(a)(1)(C); FRAP 26(a)(1)(C))"
	case types.HolidaysUKBank:
		return "court office closed; act is in time on the next day it is open (CPR 2.8(5))"
	case types.HolidaysEUInstitutions:
		return "period ends on the next working day (Reg. 1182/71 Art. 3(4))"
	default:
		return "period continues to the next day that is not a Saturday or Sunday"
	}
}

func limitationsFor(cal types.HolidayCalendar) []string {
	switch cal {
	case types.HolidaysUSFederal:
		return []string{
			"Legal holidays modelled: the 11 federal holidays of 5 U.S.C. 6103(a) (incl. Juneteenth from 2021) and their Friday/Monday observed dates when they fall on a weekend.",
			"NOT modelled: state holidays where the district court sits (FRCP 6(a)(6)(C); for appeals FRAP 26(a)(6)(C)), ad-hoc days declared a holiday by the President or Congress (FRCP 6(a)(6)(B), e.g. executive-order closures), Inauguration Day (5 U.S.C. 6103(c)), and clerk's-office inaccessibility (FRCP 6(a)(3)). Any of these can push the true deadline later — never earlier — than shown.",
			"NOT modelled: the 3 extra days for service by mail, leaving with the clerk or other consented means (FRCP 6(d)); add them when they apply, then re-apply the weekend/holiday roll-forward.",
			"Local rules, standing orders and court orders may set different periods. Verify every date against the governing rules.",
		}
	case types.HolidaysUKBank:
		return []string{
			"Bank holidays modelled: England and Wales (incl. substitute days and the 2012/2020/2022/2023 one-off changes). Scotland and Northern Ireland calendars are NOT modelled.",
			"CPR 2.8(5) roll-forward applies only to acts done at the court office; for acts not done at the court (e.g. service between parties) a period ending on a closed day is NOT extended — verify.",
			"Deemed-service rules (CPR 6.14, 6.26) are not applied: the trigger date must already be the date of (deemed) service.",
		}
	case types.HolidaysEUInstitutions:
		return []string{
			"Holidays modelled are a conservative subset of the institutions' holidays (New Year, Easter Monday, 1 May, Ascension, Whit Monday, 25-26 December). The authoritative Commission/Court holiday lists are published annually in the Official Journal and contain more days (so true deadlines may be LATER, never earlier, than shown).",
			"Reg. 1182/71 Art. 3(5) (a period of two days or more includes at least two working days) and the 10-day extension on account of distance for CJEU/General Court actions are not applied.",
		}
	default:
		return []string{"No holiday calendar configured for this rule set: only weekends are skipped."}
	}
}

// ─── Calendar helpers ─────────────────────────────────────────────────────────

// rollForward moves d forward past Saturdays, Sundays and holidays, returning
// the first working day and one human-readable reason per day skipped.
func rollForward(d time.Time, cal types.HolidayCalendar) (time.Time, []string) {
	return roll(d, cal, 1)
}

// rollBackward moves d back to the closest preceding working day.
func rollBackward(d time.Time, cal types.HolidayCalendar) (time.Time, []string) {
	return roll(d, cal, -1)
}

func roll(d time.Time, cal types.HolidayCalendar, step int) (time.Time, []string) {
	var why []string
	for {
		if name, ok := holidayName(d, cal); ok {
			r := fmt.Sprintf("%s is %s", d.Format("2006-01-02 (Monday)"), name)
			if strings.HasSuffix(name, "(observed)") && step > 0 {
				r += " [in-lieu observed date: federal courts are closed, but a court could read FRCP 6(a)(6)(A) as the statutory date only — the conservative course is to file by the previous court day]"
			}
			why = append(why, r)
		} else if dow := d.Weekday(); dow == time.Saturday || dow == time.Sunday {
			why = append(why, fmt.Sprintf("%s is a %s", d.Format("2006-01-02"), dow))
		} else {
			return d, why
		}
		d = d.AddDate(0, 0, step)
	}
}

func addBusinessDays(start time.Time, days int, holidays types.HolidayCalendar) time.Time {
	current := start
	remaining := days
	for remaining > 0 {
		current = current.AddDate(0, 0, 1)
		if isBusinessDay(current, holidays) {
			remaining--
		}
	}
	return current
}

func subBusinessDays(start time.Time, days int, holidays types.HolidayCalendar) time.Time {
	current := start
	remaining := days
	for remaining > 0 {
		current = current.AddDate(0, 0, -1)
		if isBusinessDay(current, holidays) {
			remaining--
		}
	}
	return current
}

func isBusinessDay(d time.Time, holidays types.HolidayCalendar) bool {
	dow := d.Weekday()
	if dow == time.Saturday || dow == time.Sunday {
		return false
	}
	_, hol := holidayName(d, holidays)
	return !hol
}

// holidayName reports whether d is a holiday in the calendar and its name.
func holidayName(d time.Time, cal types.HolidayCalendar) (string, bool) {
	if cal == types.HolidaysNone || cal == "" {
		return "", false
	}
	name, ok := getHolidays(d.Year(), cal)[d.Format("2006-01-02")]
	return name, ok
}

var (
	holidayMu    sync.Mutex
	holidayCache = map[string]map[string]string{}
)

// getHolidays returns every holiday (date → name) that FALLS in the given
// calendar year. Observed/substitute days can cross a year boundary (New
// Year's Day 2022, a Saturday, was observed Friday 2021-12-31), so the
// generators for year and year+1 are both run and filtered by actual year.
func getHolidays(year int, calendar types.HolidayCalendar) map[string]string {
	key := fmt.Sprintf("%d:%s", year, calendar)
	holidayMu.Lock()
	defer holidayMu.Unlock()
	if h, ok := holidayCache[key]; ok {
		return h
	}
	var gen func(int) map[string]string
	switch calendar {
	case types.HolidaysUSFederal:
		gen = usFederalHolidays
	case types.HolidaysUKBank:
		gen = ukBankHolidays
	case types.HolidaysEUInstitutions:
		gen = euInstitutionHolidays
	default:
		gen = func(int) map[string]string { return map[string]string{} }
	}
	h := map[string]string{}
	prefix := fmt.Sprintf("%04d-", year)
	for _, y := range []int{year - 1, year, year + 1} {
		for k, v := range gen(y) {
			if strings.HasPrefix(k, prefix) {
				if prev, ok := h[k]; ok && prev != v {
					v = prev + " / " + v
				}
				h[k] = v
			}
		}
	}
	holidayCache[key] = h
	return h
}

func date(year, month, day int) time.Time {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

func nthWeekday(year, month int, weekday time.Weekday, n int) time.Time {
	d := date(year, month, 1)
	for d.Weekday() != weekday {
		d = d.AddDate(0, 0, 1)
	}
	return d.AddDate(0, 0, (n-1)*7)
}

func lastWeekday(year, month int, weekday time.Weekday) time.Time {
	last := time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC)
	for last.Weekday() != weekday {
		last = last.AddDate(0, 0, -1)
	}
	return last
}

// usFederalHolidays lists the legal public holidays of 5 U.S.C. 6103(a) for
// a year, plus the in-lieu observed day (Executive Order 11582 / 5 U.S.C.
// 6103(b)): a holiday on Saturday is observed the preceding Friday, one on
// Sunday the following Monday. Federal courts close on the observed day, so
// both the statutory and observed dates are treated as legal holidays.
func usFederalHolidays(year int) map[string]string {
	h := map[string]string{}
	add := func(d time.Time, name string) { h[d.Format("2006-01-02")] = name }
	fixed := func(month, day int, name string) {
		d := date(year, month, day)
		add(d, name)
		switch d.Weekday() {
		case time.Saturday:
			add(d.AddDate(0, 0, -1), name+" (observed)")
		case time.Sunday:
			add(d.AddDate(0, 0, 1), name+" (observed)")
		}
	}
	fixed(1, 1, "New Year's Day")
	if year >= 1986 {
		add(nthWeekday(year, 1, time.Monday, 3), "Birthday of Martin Luther King, Jr.")
	}
	add(nthWeekday(year, 2, time.Monday, 3), "Washington's Birthday")
	add(lastWeekday(year, 5, time.Monday), "Memorial Day")
	if year >= 2021 {
		fixed(6, 19, "Juneteenth National Independence Day")
	}
	fixed(7, 4, "Independence Day")
	add(nthWeekday(year, 9, time.Monday, 1), "Labor Day")
	add(nthWeekday(year, 10, time.Monday, 2), "Columbus Day")
	fixed(11, 11, "Veterans Day")
	add(nthWeekday(year, 11, time.Thursday, 4), "Thanksgiving Day")
	fixed(12, 25, "Christmas Day")
	return h
}

// ukBankHolidays lists England & Wales bank holidays (Banking and Financial
// Dealings Act 1971 Sch. 1 + royal proclamations), plus Good Friday and
// Christmas Day (common-law holidays that CPR 2.8 names expressly).
// Substitute days move to the next weekday — never to the preceding Friday.
func ukBankHolidays(year int) map[string]string {
	h := map[string]string{}
	add := func(d time.Time, name string) { h[d.Format("2006-01-02")] = name }
	easter := easterDate(year)

	// New Year's Day — substitute the following Monday if on a weekend.
	ny := date(year, 1, 1)
	switch ny.Weekday() {
	case time.Saturday:
		add(ny.AddDate(0, 0, 2), "New Year's Day (substitute day)")
	case time.Sunday:
		add(ny.AddDate(0, 0, 1), "New Year's Day (substitute day)")
	default:
		add(ny, "New Year's Day")
	}
	add(easter.AddDate(0, 0, -2), "Good Friday")
	add(easter.AddDate(0, 0, 1), "Easter Monday")

	earlyMay := nthWeekday(year, 5, time.Monday, 1)
	spring := lastWeekday(year, 5, time.Monday)
	// One-off changes by royal proclamation.
	switch year {
	case 2012:
		spring = date(2012, 6, 4)
		add(date(2012, 6, 5), "Queen's Diamond Jubilee")
	case 2020:
		earlyMay = date(2020, 5, 8) // VE Day 75th anniversary
	case 2022:
		spring = date(2022, 6, 2)
		add(date(2022, 6, 3), "Platinum Jubilee bank holiday")
		add(date(2022, 9, 19), "State Funeral of Queen Elizabeth II")
	case 2023:
		add(date(2023, 5, 8), "Coronation of King Charles III")
	case 2011:
		add(date(2011, 4, 29), "Royal Wedding")
	}
	add(earlyMay, "Early May bank holiday")
	add(spring, "Spring bank holiday")
	add(lastWeekday(year, 8, time.Monday), "Summer bank holiday")

	// Christmas Day and Boxing Day with substitute days.
	dec25 := date(year, 12, 25)
	switch dec25.Weekday() {
	case time.Friday: // Boxing Day Saturday → Monday 28th
		add(dec25, "Christmas Day")
		add(date(year, 12, 28), "Boxing Day (substitute day)")
	case time.Saturday:
		add(date(year, 12, 27), "Christmas Day (substitute day)")
		add(date(year, 12, 28), "Boxing Day (substitute day)")
	case time.Sunday:
		add(date(year, 12, 26), "Boxing Day")
		add(date(year, 12, 27), "Christmas Day (substitute day)")
	default:
		add(dec25, "Christmas Day")
		add(date(year, 12, 26), "Boxing Day")
	}
	// Christmas Day itself is a CPR 2.8 non-counting day even when a
	// substitute is given.
	if _, ok := h[dec25.Format("2006-01-02")]; !ok {
		add(dec25, "Christmas Day")
	}
	return h
}

// euInstitutionHolidays is a conservative subset of the EU institutions'
// holidays (see limitationsFor): only days on which every institution is
// closed every year.
func euInstitutionHolidays(year int) map[string]string {
	h := map[string]string{}
	add := func(d time.Time, name string) { h[d.Format("2006-01-02")] = name }
	easter := easterDate(year)
	add(date(year, 1, 1), "New Year's Day")
	add(easter.AddDate(0, 0, 1), "Easter Monday")
	add(date(year, 5, 1), "Labour Day")
	add(easter.AddDate(0, 0, 39), "Ascension Day")
	add(easter.AddDate(0, 0, 50), "Whit Monday")
	add(date(year, 12, 25), "Christmas Day")
	add(date(year, 12, 26), "26 December")
	return h
}

// easterDate computes Easter Sunday using the Butcher/Meeus algorithm.
func easterDate(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := int(math.Floor(float64(h+l-7*m+114) / 31))
	day := ((h + l - 7*m + 114) % 31) + 1
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}
