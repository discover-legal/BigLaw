// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// LEDES 1998B export — converts BigLaw time entries to the standard e-billing format.
//
// Format (LEDES Oversight Committee, LEDES 1998B): line 1 is "LEDES1998B[]",
// line 2 the 24 field names, then one line per invoice line item. Fields are
// separated by "|" and every line ends with "[]". Dates are CCYYMMDD; for fee
// lines (type "F") LINE_ITEM_NUMBER_OF_UNITS is hours. The 24 fields, in order:
//
//	INVOICE_DATE | INVOICE_NUMBER | CLIENT_ID | LAW_FIRM_MATTER_ID |
//	INVOICE_TOTAL | BILLING_START_DATE | BILLING_END_DATE |
//	INVOICE_DESCRIPTION | LINE_ITEM_NUMBER | EXP/FEE/INV_ADJ_TYPE |
//	LINE_ITEM_NUMBER_OF_UNITS | LINE_ITEM_ADJUSTMENT_AMOUNT | LINE_ITEM_TOTAL |
//	LINE_ITEM_DATE | LINE_ITEM_TASK_CODE | LINE_ITEM_EXPENSE_CODE |
//	LINE_ITEM_ACTIVITY_CODE | TIMEKEEPER_ID | LINE_ITEM_DESCRIPTION |
//	LAW_FIRM_ID | LINE_ITEM_UNIT_COST | TIMEKEEPER_NAME |
//	TIMEKEEPER_CLASSIFICATION | CLIENT_MATTER_ID
//
// Field lengths enforced here: INVOICE_NUMBER is Char(20) and
// TIMEKEEPER_NAME Char(30), "Last, First" for people.
//
// TIMEKEEPER_CLASSIFICATION takes PT (partner), AS (associate), OC (of
// counsel), LA (legal assistant / paralegal) or OT (other timekeeper).

package billing

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/discover-legal/biglaw-go/internal/types"
)

const crlf = "\r\n"

// Ledes1998BFields is the 1998B field order (the spec's header line, sans "[]").
var Ledes1998BFields = []string{
	"INVOICE_DATE", "INVOICE_NUMBER", "CLIENT_ID", "LAW_FIRM_MATTER_ID",
	"INVOICE_TOTAL", "BILLING_START_DATE", "BILLING_END_DATE",
	"INVOICE_DESCRIPTION", "LINE_ITEM_NUMBER", "EXP/FEE/INV_ADJ_TYPE",
	"LINE_ITEM_NUMBER_OF_UNITS", "LINE_ITEM_ADJUSTMENT_AMOUNT", "LINE_ITEM_TOTAL",
	"LINE_ITEM_DATE", "LINE_ITEM_TASK_CODE", "LINE_ITEM_EXPENSE_CODE",
	"LINE_ITEM_ACTIVITY_CODE", "TIMEKEEPER_ID", "LINE_ITEM_DESCRIPTION",
	"LAW_FIRM_ID", "LINE_ITEM_UNIT_COST", "TIMEKEEPER_NAME",
	"TIMEKEEPER_CLASSIFICATION", "CLIENT_MATTER_ID",
}

// LEDES 1998B timekeeper classifications.
const (
	TKPartner        = "PT"
	TKAssociate      = "AS"
	TKOfCounsel      = "OC"
	TKLegalAssistant = "LA"
	TKOther          = "OT"
)

// MaxInvoiceNumberLen is the 1998B INVOICE_NUMBER width (Char 20).
const MaxInvoiceNumberLen = 20

// maxTimekeeperNameLen is the 1998B TIMEKEEPER_NAME width (Char 30).
const maxTimekeeperNameLen = 30

// LedesOptions carries invoice-level values the time entries do not hold.
type LedesOptions struct {
	InvoiceNumber      string
	InvoiceDescription string
	LawFirmID          string // firm tax ID / e-billing ID (blank when unknown)
	// Profile resolves a timekeeper's profile for classification; nil-safe.
	Profile func(profileID string) *types.LawyerProfile
	Now     time.Time // zero → time.Now()
	// Location is the firm's timezone for INVOICE_DATE, billing period and
	// LINE_ITEM_DATE (work done at 7pm New York is that day's work, not the
	// next UTC day's). nil → UTC.
	Location *time.Location
}

// TimekeeperClassification maps a BigLaw profile to a LEDES classification.
// An explicit job title wins (partner / of counsel / associate / paralegal);
// otherwise the access role decides: partner → PT, lawyer → AS. Anything
// unresolvable is OT ("other timekeeper"), never an invented code.
func TimekeeperClassification(p *types.LawyerProfile) string {
	if p == nil {
		return TKOther
	}
	title := strings.ToLower(p.Title)
	switch {
	case strings.Contains(title, "of counsel"), title == "counsel", strings.Contains(title, "senior counsel"):
		return TKOfCounsel
	case strings.Contains(title, "paralegal"), strings.Contains(title, "legal assistant"),
		strings.Contains(title, "law clerk"):
		return TKLegalAssistant
	case strings.Contains(title, "partner"):
		return TKPartner
	case strings.Contains(title, "associate"):
		return TKAssociate
	}
	switch p.Role {
	case types.RolePartner:
		return TKPartner
	case types.RoleLawyer:
		return TKAssociate
	}
	return TKOther
}

// activityCodeFor derives a UTBMS activity code from the entry's event when
// the entry has none: a gate review is a lawyer reviewing/analysing AI
// findings (A104). A task_run records the AI pipeline's wall-clock and maps
// to no single UTBMS activity, and task (L-series) codes depend on the
// matter's litigation phase, which a time entry does not carry — those stay
// blank unless the entry was coded upstream (LEDES 1998B allows blank codes;
// clients that require them will reject, which is preferable to a wrong code).
func activityCodeFor(e types.TimeEntry) string {
	if e.UTBMSActivityCode != "" {
		return e.UTBMSActivityCode
	}
	if e.Event == types.TimeEventGateReview {
		return "A104"
	}
	return ""
}

// ledesBillable reports whether an entry may appear on a LEDES invoice:
// closed, at least one billing unit, and not written off.
func ledesBillable(e types.TimeEntry) bool {
	return e.EndedAt != nil && e.BillingUnits > 0 && !e.NonBillable
}

// LedesSkipped is an otherwise-billable entry left off the invoice because its
// stored amount disagrees with units × rate (legacy rows); a human must fix it.
type LedesSkipped struct {
	EntryID string `json:"entryId"`
	Reason  string `json:"reason"`
}

// lineTotal derives LINE_ITEM_TOTAL from the one source of truth, hours ×
// rate (hours = units / 10), rounded to cents. ok=false when the stored
// amount contradicts it (or a rate exists but no amount was ever computed):
// such a row is skipped and reported rather than billed at a guessed figure.
func lineTotal(e types.TimeEntry) (total, rate float64, reason string) {
	if e.BillingRate != nil {
		rate = *e.BillingRate
	}
	total = round2(float64(e.BillingUnits) * rate / 10)
	switch {
	case e.BillingAmountUsd == nil && rate > 0:
		return 0, rate, fmt.Sprintf("rate %.2f set but no billed amount recorded", rate)
	case e.BillingAmountUsd != nil && math.Abs(*e.BillingAmountUsd-total) > 0.005:
		return 0, rate, fmt.Sprintf("stored amount %.2f != %d units x %.2f/h = %.2f", *e.BillingAmountUsd, e.BillingUnits, rate, total)
	}
	return total, rate, ""
}

// ExportLedes1998B converts time entries to LEDES 1998B pipe-delimited format.
// See ExportLedes1998BReport.
func ExportLedes1998B(entries []types.TimeEntry, opts LedesOptions) string {
	out, _ := ExportLedes1998BReport(entries, opts)
	return out
}

// ExportLedes1998BReport converts time entries to a LEDES 1998B file with one
// invoice per (client, matter): each block has its own INVOICE_NUMBER, its
// own INVOICE_TOTAL, billing period and line numbering. With more than one
// invoice in the file the numbers are the base suffixed "-1", "-2", … in
// (client, matter) order — unique per invoice, and cut to the 20-character
// 1998B width by shortening the base, never the suffix. Dates are in
// opts.Location (the firm's timezone; UTC when nil). Entries that cannot be
// billed as stored — amount contradicting units × rate, or no client number
// (an invoice must name its client) — are returned as skipped, not billed.
func ExportLedes1998BReport(entries []types.TimeEntry, opts LedesOptions) (string, []LedesSkipped) {
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	date := func(t time.Time) string { return t.In(loc).Format("20060102") }
	today := date(now)

	type line struct {
		e           types.TimeEntry
		total, rate float64
	}
	type invoice struct {
		client, matter string
		lines          []line
	}
	var order []string
	invoices := map[string]*invoice{}
	var skipped []LedesSkipped
	for _, e := range entries {
		if !ledesBillable(e) {
			continue
		}
		total, rate, reason := lineTotal(e)
		if reason == "" && strings.TrimSpace(e.ClientNumber) == "" {
			reason = "no client number: assign the work to a client before invoicing it"
		}
		if reason != "" {
			skipped = append(skipped, LedesSkipped{EntryID: e.ID, Reason: reason})
			continue
		}
		key := e.ClientNumber + "\x00" + e.MatterNumber
		inv := invoices[key]
		if inv == nil {
			inv = &invoice{client: e.ClientNumber, matter: e.MatterNumber}
			invoices[key] = inv
			order = append(order, key)
		}
		inv.lines = append(inv.lines, line{e: e, total: total, rate: rate})
	}
	sort.Strings(order)

	invDesc := sanitizeField(opts.InvoiceDescription)
	firmID := sanitizeField(opts.LawFirmID)
	out := []string{"LEDES1998B[]", strings.Join(Ledes1998BFields, "|") + "[]"}
	base := sanitizeField(opts.InvoiceNumber)
	for n, key := range order {
		inv := invoices[key]
		invNum := base
		if len(order) > 1 {
			suffix := fmt.Sprintf("-%d", n+1)
			invNum = truncate(base, MaxInvoiceNumberLen-len(suffix)) + suffix
		}
		invNum = truncate(invNum, MaxInvoiceNumberLen)

		// The invoice total and billing period come from exactly the lines
		// on this invoice, so INVOICE_TOTAL equals the sum of LINE_ITEM_TOTAL.
		invoiceTotal := 0.0
		earliest, latest := inv.lines[0].e.StartedAt, *inv.lines[0].e.EndedAt
		for _, l := range inv.lines {
			invoiceTotal += l.total
			if l.e.StartedAt.Before(earliest) {
				earliest = l.e.StartedAt
			}
			if l.e.EndedAt.After(latest) {
				latest = *l.e.EndedAt
			}
		}

		for i, l := range inv.lines {
			e := l.e
			tkID, tkName, desc := e.ProfileID, LedesTimekeeperName(e.ProfileName), e.Description
			var tkClass string
			if e.AgentID != "" {
				// AI time is billed to the AI timekeeper at the AI rate (the
				// entry's own rate). LEDES has no AI class: "other timekeeper",
				// labelled so the client can see it is AI work.
				tkClass = TKOther
				tkID, tkName = e.AgentID, e.AgentName
				if tkName == "" {
					tkName = e.AgentID
				}
				desc = "AI agent: " + desc
			} else {
				var p *types.LawyerProfile
				if opts.Profile != nil && e.ProfileID != "" {
					p = opts.Profile(e.ProfileID)
				}
				tkClass = TimekeeperClassification(p)
			}

			fields := []string{
				today,                               // INVOICE_DATE
				invNum,                              // INVOICE_NUMBER
				sanitizeField(inv.client),           // CLIENT_ID
				sanitizeField(inv.matter),           // LAW_FIRM_MATTER_ID
				fmt2(round2(invoiceTotal)),          // INVOICE_TOTAL
				date(earliest),                      // BILLING_START_DATE
				date(latest),                        // BILLING_END_DATE
				invDesc,                             // INVOICE_DESCRIPTION
				fmt.Sprintf("%d", i+1),              // LINE_ITEM_NUMBER
				"F",                                 // EXP/FEE/INV_ADJ_TYPE
				fmt2(float64(e.BillingUnits) * 0.1), // LINE_ITEM_NUMBER_OF_UNITS (hours)
				"0.00",                              // LINE_ITEM_ADJUSTMENT_AMOUNT
				fmt2(l.total),                       // LINE_ITEM_TOTAL = round2(hours × rate)
				date(e.StartedAt),                   // LINE_ITEM_DATE (firm timezone)
				sanitizeField(e.UTBMSTaskCode),      // LINE_ITEM_TASK_CODE
				"",                                  // LINE_ITEM_EXPENSE_CODE (fees only)
				sanitizeField(activityCodeFor(e)),   // LINE_ITEM_ACTIVITY_CODE
				sanitizeField(tkID),                 // TIMEKEEPER_ID
				sanitizeField(desc),                 // LINE_ITEM_DESCRIPTION
				firmID,                              // LAW_FIRM_ID
				fmt2(l.rate),                        // LINE_ITEM_UNIT_COST
				truncate(sanitizeField(tkName), maxTimekeeperNameLen), // TIMEKEEPER_NAME
				tkClass, // TIMEKEEPER_CLASSIFICATION
				"",      // CLIENT_MATTER_ID (client's own matter ref; unknown)
			}
			out = append(out, strings.Join(fields, "|")+"[]")
		}
	}
	return strings.Join(out, crlf) + crlf, skipped
}

// LedesTimekeeperName renders a person's name the way 1998B wants it, family
// name first: "Jane Q. Smith" → "Smith, Jane Q.", "John Smith Jr." →
// "Smith Jr., John". A name already holding a comma, or a single word, is
// left as it is.
func LedesTimekeeperName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || strings.Contains(name, ",") {
		return name
	}
	parts := strings.Fields(name)
	last := len(parts) - 1
	if last > 1 && nameSuffixes[strings.ToLower(strings.TrimSuffix(parts[last], "."))] {
		last--
	}
	if last < 1 {
		return name
	}
	return strings.Join(parts[last:], " ") + ", " + strings.Join(parts[:last], " ")
}

var nameSuffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true, "esq": true}

// truncate cuts ASCII s to at most n bytes.
func truncate(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) > n {
		return s[:n]
	}
	return s
}

func fmt2(f float64) string {
	return fmt.Sprintf("%.2f", f)
}

// sanitizeField makes free text safe for a 1998B field: the delimiters ("|"
// separates fields, "[]" terminates a line) and line breaks are removed, and
// the text is reduced to printable ASCII — LEDES 1998B is an ASCII format and
// e-billing intake rejects or garbles other bytes. Accents are stripped
// (é→e), common typography is mapped (“”→", —→-, …→...), anything else
// becomes "?". Truncated to 200 characters after transliteration.
func sanitizeField(s string) string {
	s = toASCII(s)
	s = strings.ReplaceAll(s, "|", " ")
	s = strings.ReplaceAll(s, "[]", "[ ]")
	if len(s) > 200 {
		s = s[:200] // ASCII: bytes == characters
	}
	return s
}

var asciiPunct = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", "\u201A", "'", "\u201B", "'",
	"\u201C", `"`, "\u201D", `"`, "\u201E", `"`, "\u00AB", `"`, "\u00BB", `"`,
	"\u2013", "-", "\u2014", "-", "\u2212", "-", "\u2010", "-", "\u2011", "-",
	"\u2026", "...", "\u00A0", " ", "\u2022", "*", "\u00A7", "S.", "\u00B6", "P.",
	"\u00A9", "(c)", "\u00AE", "(R)", "\u2122", "(TM)", "\u20AC", "EUR", "\u00A3", "GBP",
	"\u00DF", "ss", "\u00C6", "AE", "\u00E6", "ae", "\u0152", "OE", "\u0153", "oe",
	"\u00D8", "O", "\u00F8", "o", "\u0141", "L", "\u0142", "l", "\u00D0", "D", "\u00F0", "d",
	"\u00DE", "Th", "\u00FE", "th", "\u0131", "i", "\u2044", "/",
)

// stripMarks folds compatibility forms (NFKD: ﬁ→fi, fullwidth Ａ→A, ½→1⁄2,
// superscripts) and drops combining marks (é→e) and format characters
// (zero-width space/joiners, BOM, bidi controls).
var stripMarks = transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)), runes.Remove(runes.In(unicode.Cf)), norm.NFC)

func toASCII(s string) string {
	if t, _, err := transform.String(stripMarks, s); err == nil {
		s = t
	}
	s = asciiPunct.Replace(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			b.WriteByte(' ')
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// FirmLocation is the firm's timezone for billing dates: FIRM_TIMEZONE (an
// IANA name such as "America/New_York"), default UTC. An invalid name falls
// back to UTC and is reported via ok=false so the caller can warn.
func FirmLocation() (loc *time.Location, ok bool) {
	name := strings.TrimSpace(os.Getenv("FIRM_TIMEZONE"))
	if name == "" {
		return time.UTC, true
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC, false
	}
	return l, true
}
