// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package billing

import (
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func f64(v float64) *float64 { return &v }

func TestExportLedes1998BFormat(t *testing.T) {
	start := time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	profiles := map[string]*types.LawyerProfile{
		"p-partner": {ID: "p-partner", Role: types.RolePartner},
		"p-assoc":   {ID: "p-assoc", Role: types.RoleLawyer},
		"p-para":    {ID: "p-para", Role: types.RoleLawyer, Title: "Senior Paralegal"},
	}
	entries := []types.TimeEntry{
		{ID: "1", ProfileID: "p-partner", ProfileName: "Pat Partner", ClientNumber: "C-1", MatterNumber: "M-1",
			Description: "Review | memo []", Event: types.TimeEventGateReview, StartedAt: start, EndedAt: &end,
			BillingUnits: 5, BillingRate: f64(800), BillingAmountUsd: f64(400)},
		{ID: "2", ProfileID: "p-assoc", ProfileName: "Ann Assoc", ClientNumber: "C-1", MatterNumber: "M-1",
			Description: "Task: research", Event: types.TimeEventTaskRun, StartedAt: start, EndedAt: &end,
			BillingUnits: 1, BillingRate: f64(400), BillingAmountUsd: f64(40), UTBMSTaskCode: "L120"},
		{ID: "3", ProfileID: "p-para", ProfileName: "Pam Para", ClientNumber: "C-1", MatterNumber: "M-1",
			Description: "Index", Event: types.TimeEventTaskRun, StartedAt: start, EndedAt: &end,
			BillingUnits: 2, BillingRate: f64(150), BillingAmountUsd: f64(30)},
		// Excluded: written off (failed AI run), open, zero units.
		{ID: "4", ProfileID: "p-assoc", Description: "Task: failed", Event: types.TimeEventTaskRun, StartedAt: start, EndedAt: &end,
			NonBillable: true, NonBillableReason: "task failed"},
		{ID: "5", ProfileID: "p-assoc", Description: "open", StartedAt: start, BillingUnits: 3},
		{ID: "6", ProfileID: "p-assoc", Description: "zero", StartedAt: start, EndedAt: &end},
		{ID: "7", AgentID: "agent-x", AgentName: "Agent X", Description: "draft", Event: types.TimeEventAgentWork,
			StartedAt: start, EndedAt: &end, BillingUnits: 1, BillingRate: f64(100), BillingAmountUsd: f64(10)},
	}
	out := ExportLedes1998B(entries, LedesOptions{
		InvoiceNumber: "INV-1", LawFirmID: "12-3456789",
		Profile: func(id string) *types.LawyerProfile { return profiles[id] },
		Now:     time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC),
	})
	if !strings.HasSuffix(out, "\r\n") {
		t.Fatal("file must end with CRLF")
	}
	lines := strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n")
	if lines[0] != "LEDES1998B[]" {
		t.Fatalf("line 1 = %q", lines[0])
	}
	if lines[1] != strings.Join(Ledes1998BFields, "|")+"[]" || len(Ledes1998BFields) != 24 {
		t.Fatalf("header = %q", lines[1])
	}
	if len(lines) != 2+4 {
		t.Fatalf("want 4 line items (failed/open/zero excluded), got %d:\n%s", len(lines)-2, out)
	}
	idx := map[string]int{}
	for i, f := range Ledes1998BFields {
		idx[f] = i
	}
	type want struct{ class, units, total, act, task, desc string }
	wants := []want{
		{"PT", "0.50", "400.00", "A104", "", "Review   memo [ ]"},
		{"AS", "0.10", "40.00", "", "L120", "Task: research"},
		{"LA", "0.20", "30.00", "", "", "Index"},
		{"OT", "0.10", "10.00", "", "", "AI agent: draft"},
	}
	for i, w := range wants {
		row := lines[2+i]
		if !strings.HasSuffix(row, "[]") || strings.Count(row, "[]") != 1 {
			t.Fatalf("row %d must end with exactly one [] terminator: %q", i, row)
		}
		f := strings.Split(strings.TrimSuffix(row, "[]"), "|")
		if len(f) != 24 {
			t.Fatalf("row %d has %d fields, want 24: %q", i, len(f), row)
		}
		get := func(k string) string { return f[idx[k]] }
		if get("TIMEKEEPER_CLASSIFICATION") != w.class || get("LINE_ITEM_NUMBER_OF_UNITS") != w.units ||
			get("LINE_ITEM_TOTAL") != w.total || get("LINE_ITEM_ACTIVITY_CODE") != w.act ||
			get("LINE_ITEM_TASK_CODE") != w.task || get("LINE_ITEM_DESCRIPTION") != w.desc {
			t.Errorf("row %d = %q", i, row)
		}
		if get("INVOICE_TOTAL") != "480.00" || get("INVOICE_DATE") != "20261015" ||
			get("LINE_ITEM_DATE") != "20261001" || get("EXP/FEE/INV_ADJ_TYPE") != "F" ||
			get("LAW_FIRM_ID") != "12-3456789" || get("LINE_ITEM_NUMBER") != string(rune('1'+i)) {
			t.Errorf("row %d invoice-level fields wrong: %q", i, row)
		}
	}

	// Round-trip through the invoice parser.
	items := ParseLEDES(out)
	if len(items) != 4 || items[0].TimekeeperClass != "PT" || items[0].Amount == nil || *items[0].Amount != 400 ||
		items[0].Hours == nil || *items[0].Hours != 0.5 || items[0].Date != "20261001" {
		t.Fatalf("parse round-trip: %+v", items)
	}
	// Headerless parse uses the spec positions.
	headerless := strings.Join(lines[2:], "\r\n")
	items = ParseLEDES(headerless)
	if len(items) != 4 || items[1].TaskCode != "L120" || items[1].TimekeeperName != "Ann Assoc" || *items[1].Rate != 400 {
		t.Fatalf("headerless parse: %+v", items)
	}
}

func TestTimekeeperClassification(t *testing.T) {
	cases := []struct {
		p    *types.LawyerProfile
		want string
	}{
		{nil, "OT"},
		{&types.LawyerProfile{Role: types.RolePartner}, "PT"},
		{&types.LawyerProfile{Role: types.RoleLawyer}, "AS"},
		{&types.LawyerProfile{Role: types.RoleLawyer, Title: "Of Counsel"}, "OC"},
		{&types.LawyerProfile{Role: types.RolePartner, Title: "Of Counsel"}, "OC"},
		{&types.LawyerProfile{Role: types.RoleLawyer, Title: "Legal Assistant"}, "LA"},
		{&types.LawyerProfile{Role: types.RoleLawyer, Title: "Equity Partner"}, "PT"},
		{&types.LawyerProfile{Role: "unknown"}, "OT"},
	}
	for _, tc := range cases {
		if got := TimekeeperClassification(tc.p); got != tc.want {
			t.Errorf("%+v → %s want %s", tc.p, got, tc.want)
		}
	}
}
