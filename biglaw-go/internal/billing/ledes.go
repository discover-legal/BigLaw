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
// TIMEKEEPER_CLASSIFICATION takes PT (partner), AS (associate), OC (of
// counsel), LA (legal assistant / paralegal) or OT (other timekeeper).

package billing

import (
	"fmt"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/strutil"
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

// LedesOptions carries invoice-level values the time entries do not hold.
type LedesOptions struct {
	InvoiceNumber      string
	InvoiceDescription string
	LawFirmID          string // firm tax ID / e-billing ID (blank when unknown)
	// Profile resolves a timekeeper's profile for classification; nil-safe.
	Profile func(profileID string) *types.LawyerProfile
	Now     time.Time // zero → time.Now()
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

// LedesBillable reports whether an entry may appear on a LEDES invoice:
// closed, at least one billing unit, and not written off.
func LedesBillable(e types.TimeEntry) bool {
	return e.EndedAt != nil && e.BillingUnits > 0 && !e.NonBillable
}

// ExportLedes1998B converts time entries to LEDES 1998B pipe-delimited format.
func ExportLedes1998B(entries []types.TimeEntry, opts LedesOptions) string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	today := ledesDate(now)

	// Only billable entries produce invoice lines; the invoice total and
	// billing period are computed from those same entries so the stated
	// total equals the sum of the line totals.
	var valid []types.TimeEntry
	for _, e := range entries {
		if LedesBillable(e) {
			valid = append(valid, e)
		}
	}

	lineTotals := make([]float64, len(valid))
	invoiceTotal := 0.0
	for i, e := range valid {
		if e.BillingAmountUsd != nil {
			lineTotals[i] = round2(*e.BillingAmountUsd)
		}
		invoiceTotal += lineTotals[i]
	}

	billingStart, billingEnd := today, today
	if len(valid) > 0 {
		earliest, latest := valid[0].StartedAt, *valid[0].EndedAt
		for _, e := range valid {
			if e.StartedAt.Before(earliest) {
				earliest = e.StartedAt
			}
			if e.EndedAt.After(latest) {
				latest = *e.EndedAt
			}
		}
		billingStart, billingEnd = ledesDate(earliest), ledesDate(latest)
	}

	invNum := sanitizeField(opts.InvoiceNumber)
	invDesc := sanitizeField(opts.InvoiceDescription)
	firmID := sanitizeField(opts.LawFirmID)

	lines := []string{"LEDES1998B[]", strings.Join(Ledes1998BFields, "|") + "[]"}
	for i, e := range valid {
		rate := 0.0
		if e.BillingRate != nil {
			rate = *e.BillingRate
		}
		tkID := e.ProfileID
		tkName := e.ProfileName
		var tkClass string
		desc := e.Description
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
			sanitizeField(e.ClientNumber),       // CLIENT_ID
			sanitizeField(e.MatterNumber),       // LAW_FIRM_MATTER_ID
			fmt2(invoiceTotal),                  // INVOICE_TOTAL
			billingStart,                        // BILLING_START_DATE
			billingEnd,                          // BILLING_END_DATE
			invDesc,                             // INVOICE_DESCRIPTION
			fmt.Sprintf("%d", i+1),              // LINE_ITEM_NUMBER
			"F",                                 // EXP/FEE/INV_ADJ_TYPE
			fmt2(float64(e.BillingUnits) * 0.1), // LINE_ITEM_NUMBER_OF_UNITS (hours)
			"0.00",                              // LINE_ITEM_ADJUSTMENT_AMOUNT
			fmt2(lineTotals[i]),                 // LINE_ITEM_TOTAL
			ledesDate(e.StartedAt),              // LINE_ITEM_DATE
			sanitizeField(e.UTBMSTaskCode),      // LINE_ITEM_TASK_CODE
			"",                                  // LINE_ITEM_EXPENSE_CODE (fees only)
			sanitizeField(activityCodeFor(e)),   // LINE_ITEM_ACTIVITY_CODE
			sanitizeField(tkID),                 // TIMEKEEPER_ID
			sanitizeField(desc),                 // LINE_ITEM_DESCRIPTION
			firmID,                              // LAW_FIRM_ID
			fmt2(rate),                          // LINE_ITEM_UNIT_COST
			sanitizeField(tkName),               // TIMEKEEPER_NAME
			tkClass,                             // TIMEKEEPER_CLASSIFICATION
			"",                                  // CLIENT_MATTER_ID (client's own matter ref; unknown)
		}
		lines = append(lines, strings.Join(fields, "|")+"[]")
	}
	return strings.Join(lines, crlf) + crlf
}

func ledesDate(t time.Time) string {
	// Use UTC components — invoice dates are derived from UTC timestamps
	// elsewhere, so a non-UTC server must not shift INVOICE_DATE / line
	// dates by a day.
	return t.UTC().Format("20060102")
}

func fmt2(f float64) string {
	return fmt.Sprintf("%.2f", f)
}

// sanitizeField removes the 1998B delimiters ("|" separates fields, "[]"
// terminates a line) and line breaks from free text.
func sanitizeField(s string) string {
	s = strings.ReplaceAll(s, "|", " ")
	s = strings.ReplaceAll(s, "[]", "[ ]")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = strutil.Truncate(s, 200)
	}
	return s
}
