// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package deadlines

import (
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func shippedEngine(t *testing.T) *Engine {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "deadlines", "rules")
	e := New()
	if err := e.LoadRulesDir(dir); err != nil {
		t.Fatal(err)
	}
	if len(e.ListJurisdictions()) < 3 {
		t.Fatalf("expected shipped rule sets in %s", dir)
	}
	return e
}

func findRule(t *testing.T, r *types.DeadlineResult, id string) types.ComputedDeadline {
	t.Helper()
	for _, d := range r.Deadlines {
		if d.RuleID == id {
			return d
		}
	}
	t.Fatalf("rule %s not in result %+v", id, r.Deadlines)
	return types.ComputedDeadline{}
}

// FRCP 6(a)(1)(C): calendar-day periods that end on a weekend or legal
// holiday roll to the next day that is neither.
func TestFRCPRollForward(t *testing.T) {
	e := shippedEngine(t)
	cases := []struct {
		name, trigger, date, rule  string
		wantDue, wantRaw, wantWarn string
	}{
		{"Christmas Friday 2026", "complaint_served", "2026-12-04", "frcp-answer-to-complaint", "2026-12-28", "2026-12-25", "2026-12-23"},
		{"lands Saturday", "complaint_served", "2026-10-03", "frcp-answer-to-complaint", "2026-10-26", "2026-10-24", "2026-10-21"},
		{"weekday no roll; warning Sat moves to Fri", "complaint_served", "2026-10-01", "frcp-answer-to-complaint", "2026-10-22", "", "2026-10-16"},
		{"Saturday then MLK Monday", "complaint_served", "2026-12-26", "frcp-answer-to-complaint", "2027-01-19", "2027-01-16", "2027-01-14"},
		{"New Year 2022 observed Fri 2021-12-31", "complaint_served", "2021-12-10", "frcp-answer-to-complaint", "2022-01-03", "2021-12-31", "2021-12-29"},
		{"Juneteenth 2026", "complaint_served", "2026-05-29", "frcp-answer-to-complaint", "2026-06-22", "2026-06-19", "2026-06-17"},
		{"no Juneteenth before 2021", "complaint_served", "2020-05-29", "frcp-answer-to-complaint", "2020-06-19", "", "2020-06-12"},
		{"July 4 2026 Saturday observed Fri Jul 3", "complaint_served", "2026-06-12", "frcp-answer-to-complaint", "2026-07-06", "2026-07-03", "2026-07-01"},
		{"Thanksgiving 2026", "complaint_served", "2026-11-05", "frcp-answer-to-complaint", "2026-11-27", "2026-11-26", "2026-11-20"},
		{"Christmas 2022 Sunday observed Mon 26", "complaint_served", "2022-12-05", "frcp-answer-to-complaint", "2022-12-27", "2022-12-26", "2022-12-22"},
		{"Columbus Day 2026", "final_judgment_entered", "2026-09-12", "frap-notice-of-appeal", "2026-10-13", "2026-10-12", "2026-10-06"},
		{"60 days lands Monday", "complaint_served_on_us_govt", "2026-10-15", "frcp-answer-govt", "2026-12-14", "", "2026-11-30"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := e.Compute("US-FED", tc.trigger, tc.date)
			if err != nil {
				t.Fatal(err)
			}
			d := findRule(t, r, tc.rule)
			if d.DueDate != tc.wantDue || d.UnadjustedDate != tc.wantRaw || d.WarningDate != tc.wantWarn {
				t.Errorf("due=%s raw=%s warn=%s; want due=%s raw=%s warn=%s (adj=%v)",
					d.DueDate, d.UnadjustedDate, d.WarningDate, tc.wantDue, tc.wantRaw, tc.wantWarn, d.Adjustments)
			}
			if (tc.wantRaw != "") != (len(d.Adjustments) > 0) {
				t.Errorf("adjustments %v inconsistent with roll-forward", d.Adjustments)
			}
			if len(r.Limitations) == 0 {
				t.Error("US result must disclose unmodelled state holidays (FRCP 6(a)(6)(C))")
			}
		})
	}
}

func TestUSFederalHolidays2026(t *testing.T) {
	got := keys(getHolidays(2026, types.HolidaysUSFederal))
	want := []string{
		"2026-01-01", "2026-01-19", "2026-02-16", "2026-05-25", "2026-06-19",
		"2026-07-03", "2026-07-04", "2026-09-07", "2026-10-12", "2026-11-11",
		"2026-11-26", "2026-12-25",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("2026 federal holidays\n got %v\nwant %v", got, want)
	}
	// Cross-year observance: New Year's Day 2022 (Sat) observed 2021-12-31.
	if n, ok := holidayName(date(2021, 12, 31), types.HolidaysUSFederal); !ok || n != "New Year's Day (observed)" {
		t.Errorf("2021-12-31 = %q,%v; want New Year's Day (observed)", n, ok)
	}
	// Juneteenth 2021 fell on Saturday → observed Friday 2021-06-18.
	if _, ok := holidayName(date(2021, 6, 18), types.HolidaysUSFederal); !ok {
		t.Error("2021-06-18 should be Juneteenth (observed)")
	}
}

func TestUKBankHolidays(t *testing.T) {
	cases := []struct {
		day  string
		want bool
	}{
		{"2022-01-03", true},  // New Year (Sat) substitute is the following Monday
		{"2021-12-31", false}, // ...not the preceding Friday (US-style)
		{"2026-04-03", true},  // Good Friday 2026
		{"2026-04-06", true},  // Easter Monday 2026
		{"2026-12-28", true},  // Boxing Day 2026 (Sat) substitute
		{"2022-12-27", true},  // Christmas 2022 (Sun) substitute
		{"2022-06-03", true},  // Platinum Jubilee
		{"2022-05-30", false}, // Spring BH moved to 2 June in 2022
		{"2020-05-08", true},  // VE Day move
		{"2020-05-04", false},
		{"2023-05-08", true}, // Coronation
	}
	for _, tc := range cases {
		d, _ := time.Parse("2006-01-02", tc.day)
		if _, ok := holidayName(d, types.HolidaysUKBank); ok != tc.want {
			t.Errorf("%s holiday=%v want %v", tc.day, ok, tc.want)
		}
	}
}

func TestUKCPR(t *testing.T) {
	e := shippedEngine(t)
	// 14 days from Fri 2026-12-11 = Fri 25 Dec; 28 Dec is the Boxing Day
	// substitute, so CPR 2.8(5) carries the filing to Tue 29 Dec.
	r, err := e.Compute("UK", "claim_form_served", "2026-12-11")
	if err != nil {
		t.Fatal(err)
	}
	if d := findRule(t, r, "cpr-acknowledgment-of-service"); d.DueDate != "2026-12-29" {
		t.Errorf("AoS due %s want 2026-12-29 (%v)", d.DueDate, d.Adjustments)
	}
	// CPR 2.8(4): periods of 5 days or less exclude weekends/bank holidays.
	short := New()
	_ = short.LoadRules(&types.JurisdictionRules{Jurisdiction: "UK-T", Holidays: types.HolidaysUKBank,
		Rules: []types.DeadlineRule{{ID: "s", Trigger: "x", Event: "y", Days: 3, DayType: types.DayTypeCalendar}}})
	r, _ = short.Compute("UK-T", "x", "2026-04-02") // Thu before Easter
	if r.Deadlines[0].DueDate != "2026-04-09" {     // skips Good Fri, Sat, Sun, Easter Mon
		t.Errorf("CPR 2.8(4) short period due %s want 2026-04-09", r.Deadlines[0].DueDate)
	}
}

func TestEUBusinessDays(t *testing.T) {
	e := shippedEngine(t)
	r, err := e.Compute("EU-COMP", "dawn_raid_conducted", "2026-04-01")
	if err != nil {
		t.Fatal(err)
	}
	d := findRule(t, r, "ec-dawn-raid-response")
	// Easter Monday 2026-04-06 is skipped: 10 working days → Thu 16 April.
	if d.DueDate != "2026-04-16" || d.WarningDate != "2026-04-14" {
		t.Errorf("due %s warn %s; want 2026-04-16 / 2026-04-14", d.DueDate, d.WarningDate)
	}
}

func TestUnknownTriggerAndJurisdiction(t *testing.T) {
	e := shippedEngine(t)
	_, err := e.Compute("US-FED", "complaint_serverd", "2026-12-04")
	var ute *UnknownTriggerError
	if !errors.As(err, &ute) {
		t.Fatalf("want UnknownTriggerError, got %v", err)
	}
	if len(ute.Valid) == 0 || ute.Valid[0] > ute.Valid[len(ute.Valid)-1] {
		t.Errorf("valid triggers should be a sorted non-empty list: %v", ute.Valid)
	}
	var uje *UnknownJurisdictionError
	if _, err := e.Compute("XX", "complaint_served", "2026-12-04"); !errors.As(err, &uje) {
		t.Fatalf("want UnknownJurisdictionError, got %v", err)
	}
	var ide *InvalidDateError
	if _, err := e.Compute("US-FED", "complaint_served", "2026-13-01"); !errors.As(err, &ide) {
		t.Fatalf("want InvalidDateError, got %v", err)
	}
}

func TestEasterDates(t *testing.T) {
	for year, want := range map[int]string{2024: "2024-03-31", 2025: "2025-04-20", 2026: "2026-04-05", 2027: "2027-03-28"} {
		if got := easterDate(year).Format("2006-01-02"); got != want {
			t.Errorf("Easter %d = %s want %s", year, got, want)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Backward-counted periods ("at least N days before the hearing"): negative
// days count back from the trigger, a non-working end day moves to the
// PRECEDING working day (FRCP 6(a)(5)), the warning is on or before the due
// date, and CPR counts clear days (2.8(3)) with the 2.8(4) short-period rule.
func TestBackwardPeriods(t *testing.T) {
	type rule struct {
		days, warn int
		dt         types.DayType
	}
	cases := []struct {
		name     string
		cal      types.HolidayCalendar
		r        rule
		event    string
		due, raw string
		warn     string
	}{
		{"US 14 before Sunday hearing → Sat/Sun roll back", types.HolidaysUSFederal, rule{-14, 3, types.DayTypeCalendar}, "2026-12-27", "2026-12-11", "2026-12-13", "2026-12-08"},
		{"US 3 before, weekday", types.HolidaysUSFederal, rule{-3, 0, types.DayTypeCalendar}, "2026-12-27", "2026-12-24", "", ""},
		{"US lands on Veterans Day → back to Tue", types.HolidaysUSFederal, rule{-7, 0, types.DayTypeCalendar}, "2026-11-18", "2026-11-10", "2026-11-11", ""},
		{"US 5 business days before, across Christmas", types.HolidaysUSFederal, rule{-5, 0, types.DayTypeBusiness}, "2026-12-28", "2026-12-18", "", ""},
		{"CPR 2.8 worked example: 3 days before Fri 20 Oct → Mon 16 Oct", types.HolidaysUKBank, rule{-3, 0, types.DayTypeCalendar}, "2000-10-20", "2000-10-16", "", ""},
		{"CPR 24.5(1): 7 clear days before Fri 20 Nov → Thu 12 Nov", types.HolidaysUKBank, rule{-7, 2, types.DayTypeCalendar}, "2026-11-20", "2026-11-12", "", "2026-11-10"},
		{"CPR clear days back over Christmas", types.HolidaysUKBank, rule{-7, 0, types.DayTypeCalendar}, "2027-01-04", "2026-12-24", "2026-12-27", ""},
		{"EU 10 before → Easter Monday → back", types.HolidaysEUInstitutions, rule{-10, 0, types.DayTypeCalendar}, "2026-04-16", "2026-04-03", "2026-04-06", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := New()
			_ = e.LoadRules(&types.JurisdictionRules{Jurisdiction: "T", Holidays: tc.cal, Rules: []types.DeadlineRule{
				{ID: "r", Trigger: "hearing", Event: "x", Days: tc.r.days, DayType: tc.r.dt, WarningDays: tc.r.warn}}})
			res, err := e.Compute("T", "hearing", tc.event)
			if err != nil {
				t.Fatal(err)
			}
			d := res.Deadlines[0]
			if d.DueDate != tc.due || d.UnadjustedDate != tc.raw || d.WarningDate != tc.warn || d.Direction != "before" {
				t.Errorf("due=%s raw=%s warn=%s dir=%q; want due=%s raw=%s warn=%s (adj %v)",
					d.DueDate, d.UnadjustedDate, d.WarningDate, d.Direction, tc.due, tc.raw, tc.warn, d.Adjustments)
			}
			if d.DueDate >= tc.event {
				t.Errorf("a 'before' deadline must precede the event: %s vs %s", d.DueDate, tc.event)
			}
			if d.WarningDate != "" && d.WarningDate > d.DueDate {
				t.Errorf("warning %s after due %s", d.WarningDate, d.DueDate)
			}
		})
	}
}

// The shipped CPR 24.5(1) rule is modelled backward from the hearing.
func TestShippedCPRSummaryJudgmentRule(t *testing.T) {
	e := shippedEngine(t)
	r, err := e.Compute("UK", "summary_judgment_hearing", "2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	if d := findRule(t, r, "cpr-summary-judgment-response"); d.DueDate != "2026-11-12" {
		t.Errorf("CPR 24.5(1) evidence due %s, want 2026-11-12", d.DueDate)
	}
}
