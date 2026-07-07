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
	e.rules[strings.ToUpper(rules.Jurisdiction)] = rules
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

// Compute returns all deadlines triggered by a given event and date.
func (e *Engine) Compute(jurisdiction, triggerEvent, triggerDate string) (*types.DeadlineResult, error) {
	e.mu.RLock()
	ruleset := e.rules[strings.ToUpper(jurisdiction)]
	e.mu.RUnlock()
	if ruleset == nil {
		return nil, fmt.Errorf("no rules loaded for jurisdiction: %s", jurisdiction)
	}

	tDate, err := time.Parse("2006-01-02", triggerDate)
	if err != nil {
		return nil, fmt.Errorf("invalid triggerDate %q: expected YYYY-MM-DD", triggerDate)
	}
	tDate = time.Date(tDate.Year(), tDate.Month(), tDate.Day(), 0, 0, 0, 0, time.UTC)

	trigger := strings.ToLower(strings.TrimSpace(triggerEvent))
	var deadlines []types.ComputedDeadline

	for _, rule := range ruleset.Rules {
		if strings.ToLower(strings.TrimSpace(rule.Trigger)) != trigger {
			continue
		}

		var dueDate time.Time
		if rule.DayType == types.DayTypeBusiness {
			dueDate = addBusinessDays(tDate, rule.Days, ruleset.Holidays)
		} else {
			dueDate = tDate.AddDate(0, 0, rule.Days)
		}

		cd := types.ComputedDeadline{
			RuleID:  rule.ID,
			Event:   rule.Event,
			DueDate: dueDate.Format("2006-01-02"),
			Days:    rule.Days,
			DayType: rule.DayType,
			Cite:    rule.Cite,
			Note:    rule.Note,
		}
		if rule.WarningDays > 0 {
			warningDate := dueDate.AddDate(0, 0, -rule.WarningDays)
			cd.WarningDate = warningDate.Format("2006-01-02")
		}
		deadlines = append(deadlines, cd)
	}

	sort.Slice(deadlines, func(i, j int) bool {
		return deadlines[i].DueDate < deadlines[j].DueDate
	})

	return &types.DeadlineResult{
		Jurisdiction:     ruleset.Jurisdiction,
		JurisdictionName: ruleset.Name,
		TriggerEvent:     triggerEvent,
		TriggerDate:      tDate.Format("2006-01-02"),
		ComputedAt:       time.Now().UTC().Format(time.RFC3339),
		Deadlines:        deadlines,
	}, nil
}

// ─── Calendar helpers ─────────────────────────────────────────────────────────

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

func isBusinessDay(d time.Time, holidays types.HolidayCalendar) bool {
	dow := d.Weekday()
	if dow == time.Saturday || dow == time.Sunday {
		return false
	}
	return !isHoliday(d, holidays)
}

func isHoliday(d time.Time, holidays types.HolidayCalendar) bool {
	if holidays == types.HolidaysNone {
		return false
	}
	year := d.Year()
	hs := getHolidays(year, holidays)
	return hs[d.Format("2006-01-02")]
}

var (
	holidayMu    sync.Mutex
	holidayCache = map[string]map[string]bool{}
)

func getHolidays(year int, calendar types.HolidayCalendar) map[string]bool {
	key := fmt.Sprintf("%d:%s", year, calendar)
	holidayMu.Lock()
	defer holidayMu.Unlock()
	if h, ok := holidayCache[key]; ok {
		return h
	}
	var h map[string]bool
	switch calendar {
	case types.HolidaysUSFederal:
		h = usFederalHolidays(year)
	case types.HolidaysUKBank:
		h = ukBankHolidays(year)
	case types.HolidaysEUInstitutions:
		h = euInstitutionHolidays(year)
	default:
		h = map[string]bool{}
	}
	holidayCache[key] = h
	return h
}

func dateStr(year, month, day int) string {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func observedWeekday(year, month, day int) string {
	d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	switch d.Weekday() {
	case time.Saturday:
		return d.AddDate(0, 0, -1).Format("2006-01-02")
	case time.Sunday:
		return d.AddDate(0, 0, 1).Format("2006-01-02")
	default:
		return d.Format("2006-01-02")
	}
}

func nthWeekday(year, month int, weekday time.Weekday, n int) string {
	// Find first occurrence of weekday in month
	d := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	for d.Weekday() != weekday {
		d = d.AddDate(0, 0, 1)
	}
	return d.AddDate(0, 0, (n-1)*7).Format("2006-01-02")
}

func lastWeekday(year, month int, weekday time.Weekday) string {
	// Last day of month
	last := time.Date(year, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC)
	for last.Weekday() != weekday {
		last = last.AddDate(0, 0, -1)
	}
	return last.Format("2006-01-02")
}

func usFederalHolidays(year int) map[string]bool {
	h := map[string]bool{}
	h[observedWeekday(year, 1, 1)] = true            // New Year's Day
	h[nthWeekday(year, 1, time.Monday, 3)] = true    // MLK Day
	h[nthWeekday(year, 2, time.Monday, 3)] = true    // Presidents Day
	h[lastWeekday(year, 5, time.Monday)] = true      // Memorial Day
	h[observedWeekday(year, 6, 19)] = true           // Juneteenth
	h[observedWeekday(year, 7, 4)] = true            // Independence Day
	h[nthWeekday(year, 9, time.Monday, 1)] = true    // Labor Day
	h[nthWeekday(year, 10, time.Monday, 2)] = true   // Columbus Day
	h[observedWeekday(year, 11, 11)] = true          // Veterans Day
	h[nthWeekday(year, 11, time.Thursday, 4)] = true // Thanksgiving
	h[observedWeekday(year, 12, 25)] = true          // Christmas
	return h
}

func ukBankHolidays(year int) map[string]bool {
	h := map[string]bool{}
	easter := easterDate(year)

	add := func(d time.Time) { h[d.Format("2006-01-02")] = true }
	addStr := func(s string) { h[s] = true }

	// New Year's
	addStr(observedWeekday(year, 1, 1))
	// Good Friday
	add(easter.AddDate(0, 0, -2))
	// Easter Monday
	add(easter.AddDate(0, 0, 1))
	// Early May BH (1st Mon May)
	addStr(nthWeekday(year, 5, time.Monday, 1))
	// Spring BH (last Mon May)
	addStr(lastWeekday(year, 5, time.Monday))
	// Summer BH (last Mon Aug)
	addStr(lastWeekday(year, 8, time.Monday))
	// Christmas substitution
	dec25 := time.Date(year, 12, 25, 0, 0, 0, 0, time.UTC)
	dec26 := time.Date(year, 12, 26, 0, 0, 0, 0, time.UTC)
	switch dec25.Weekday() {
	case time.Saturday:
		h[dateStr(year, 12, 27)] = true
		h[dateStr(year, 12, 28)] = true
	case time.Sunday:
		h[dateStr(year, 12, 26)] = true
		h[dateStr(year, 12, 27)] = true
	default:
		add(dec25)
		switch dec26.Weekday() {
		case time.Saturday:
			h[dateStr(year, 12, 28)] = true
		case time.Sunday:
			h[dateStr(year, 12, 27)] = true
		default:
			add(dec26)
		}
	}
	return h
}

func euInstitutionHolidays(year int) map[string]bool {
	h := map[string]bool{}
	easter := easterDate(year)

	add := func(d time.Time) { h[d.Format("2006-01-02")] = true }

	add(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC))   // New Year's
	add(easter.AddDate(0, 0, 1))                       // Easter Monday
	add(time.Date(year, 5, 1, 0, 0, 0, 0, time.UTC))   // Labour Day
	add(easter.AddDate(0, 0, 39))                      // Ascension Thursday
	add(easter.AddDate(0, 0, 50))                      // Whit Monday
	add(time.Date(year, 12, 25, 0, 0, 0, 0, time.UTC)) // Christmas
	add(time.Date(year, 12, 26, 0, 0, 0, 0, time.UTC)) // Second Christmas
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
