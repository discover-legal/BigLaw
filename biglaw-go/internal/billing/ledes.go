// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// LEDES 1998B export — converts BigLaw time entries to the standard e-billing format.

package billing

import (
	"fmt"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const crlf = "\r\n"

// ExportLedes1998B converts time entries to LEDES 1998B pipe-delimited format.
func ExportLedes1998B(entries []types.TimeEntry, invoiceNumber string) string {
	today := ledesDate(time.Now())

	// Only closed entries with billable units produce invoice lines; the
	// invoice total and billing period must be computed from those same
	// entries, or the stated total would not match the sum of the lines.
	var validEntries []types.TimeEntry
	for _, e := range entries {
		if e.BillingUnits > 0 && e.EndedAt != nil {
			validEntries = append(validEntries, e)
		}
	}

	invoiceTotal := 0.0
	for _, e := range validEntries {
		if e.BillingAmountUsd != nil {
			invoiceTotal += *e.BillingAmountUsd
		}
	}

	// Date range
	var allDates []time.Time
	for _, e := range validEntries {
		allDates = append(allDates, e.StartedAt)
		if e.EndedAt != nil {
			allDates = append(allDates, *e.EndedAt)
		}
	}
	billingStart, billingEnd := today, today
	if len(allDates) > 0 {
		earliest, latest := allDates[0], allDates[0]
		for _, d := range allDates {
			if d.Before(earliest) {
				earliest = d
			}
			if d.After(latest) {
				latest = d
			}
		}
		billingStart = ledesDate(earliest)
		billingEnd = ledesDate(latest)
	}

	const header = "INVOICE_DATE|INVOICE_NUMBER|CLIENT_ID|LAW_FIRM_MATTER_ID|INVOICE_TOTAL|" +
		"BILLING_START_DATE|BILLING_END_DATE|INVOICE_DESCRIPTION|LINE_ITEM_NUMBER|" +
		"EXP/FEE/INV_ADJ_TYPE|LINE_ITEM_NUMBER_OF_UNITS|LINE_ITEM_UNIT_COST|" +
		"LINE_ITEM_AM_BILLED|LINE_ITEM_DESCRIPTION|LINE_ITEM_TASK_CODE|" +
		"LINE_ITEM_EXPENSE_CODE|LINE_ITEM_ACTIVITY_CODE|TIMEKEEPER_ID|" +
		"TIMEKEEPER_NAME|LINE_ITEM_REVIEWED_BY_CODE|LINE_ITEM_BUDGET_CODE|" +
		"PEER_REVIEW_BY_CODE|TIMEKEEPER_CLASSIFICATION[]"

	invNum := sanitizeField(invoiceNumber)
	var rows []string
	for i, e := range validEntries {
		units := fmt2(float64(e.BillingUnits) * 0.1)
		rate := "0.00"
		if e.BillingRate != nil {
			rate = fmt2(*e.BillingRate)
		}
		billed := "0.00"
		if e.BillingAmountUsd != nil {
			billed = fmt2(*e.BillingAmountUsd)
		}
		desc := sanitizeField(e.Description)
		tkID := sanitizeField(e.ProfileID)
		tkName := sanitizeField(e.ProfileName)
		tkClass := "TK"
		if e.AgentID != "" {
			tkClass = "AI"
			if tkID == "" {
				tkID = sanitizeField(e.AgentID)
			}
			if tkName == "" {
				tkName = sanitizeField(e.AgentName)
			}
		}

		row := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s||%d|F|%s|%s|%s|%s|%s||%s|%s|%s|||%s|%s[]",
			today, invNum,
			e.ClientNumber, e.MatterNumber,
			fmt2(invoiceTotal),
			billingStart, billingEnd,
			i+1,
			units, rate, billed, desc,
			e.UTBMSTaskCode, e.UTBMSActivityCode,
			tkID, tkName,
			"", tkClass,
		)
		rows = append(rows, row)
	}

	lines := append([]string{"LEDES1998B[]", header}, rows...)
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

func sanitizeField(s string) string {
	s = strings.ReplaceAll(s, "|", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = strutil.Truncate(s, 200)
	}
	return s
}
