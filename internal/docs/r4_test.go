// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"context"
	"strings"
	"testing"
)

// The bench5 SEC exhibits, verbatim as the LAB driver uploaded them.
const (
	benchEML = "From: Gerald R. Whitmore <gwhitmore@whitmorecapital.com>\nTo: Tomás Delgado <tdelgado@whitmorecapital.com>\nDate: Mon, 15 Apr 2024 09:23:47 -0400\nContent-Type: text/plain; charset=\"utf-8\"\nContent-Transfer-Encoding: quoted-printable\nMIME-Version: 1.0\n\nMessage-ID: <a3f8c291-4e07-4d3b-b1a0-7c6e5f829d14@whitmorecapital.com>\nSubject: Shared Drive Cleanup\nMIME-Version: 1.0\nContent-Type: text/plain; charset=3D\"UTF-8\"\nX-Mailer: Microsoft Outlook 16.0\n\nTom=C3=A1s,\n\nWhen you get a chance, I'd like you to do a clean up of legacy files on the s=\nhared drive. Specifically, the old allocation folders under Trading Ops =E2=\n=80=94 the trade allocation spreadsheets from 2021 and early 2022 are still s=\nitting out there and cluttering things up. Go ahead and clear those out.\n\nI know you and Kevin have been flagging storage capacity issues on that drive=\n for a while now, and frankly we don't need to keep everything from three yea=\nrs ago on the active drive. Let's get it tidied up.\n\nThanks,\n\nGerald R. Whitmore\n"
	benchEMLQuote = "When you get a chance, I'd like you to do a clean up of legacy files on the shared drive. Specifically, the old allocation folders under Trading Ops — the trade allocation spreadsheets from 2021 and early 2022 are still sitting out there and cluttering things up. Go ahead and clear those out."
	benchXLSXRows = "Total Monthly Dues\tSum of all monthly membership dues payments (2022 + 2023 + 2024)\t05/03/2022 – 01/02/2024\t21\t$2,800.00\t$58,800.00\nGRAND TOTAL\tTotal of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini\t05/03/2022 – 01/02/2024\t22\t\t$103,800.00"
)

// TestEmailBodyDecodedFromQuotedPrintable (bench5 SEC F932/F934 quarantined: the .eml was stored
// with its quoted-printable soft breaks, so no quote of its body verified and the April 15 vs April 22
// conflict could not be paired): a plain-text e-mail whose header block says quoted-printable is
// stored decoded (headers verbatim, noted), and its body verifies as a reader quotes it; text that is
// not such an e-mail is unchanged.
func TestEmailBodyDecodedFromQuotedPrintable(t *testing.T) {
	x := textOf(benchEML)
	if !strings.HasPrefix(x.Text, "From: Gerald R. Whitmore <gwhitmore@whitmorecapital.com>\nTo: Tomás Delgado") || !strings.Contains(x.Text, "Date: Mon, 15 Apr 2024 09:23:47 -0400") ||
		!strings.Contains(x.Text, "Tomás,\n") || len(x.Notes) != 1 || !strings.Contains(x.Notes[0], "quoted-printable") {
		t.Fatalf("%q %q", x.Text, x.Notes)
	}
	if v, _ := Verify(context.Background(), x.Text, benchEMLQuote, nil); !v.Verified || v.Method != "exact" {
		t.Errorf("the body quote: %+v", v)
	}
	for _, plain := range []string{"Note: the fee is =E2=80=94 due.\n\nSee Content-Transfer-Encoding: quoted-printable", "Subject: x\nContent-Transfer-Encoding: base64\n\nd=3D1",
		"A line of prose: with a colon\nContent-Transfer-Encoding: quoted-printable\n\nx=3D1"} {
		if y := textOf(plain); y.Text != plain || len(y.Notes) != 0 {
			t.Errorf("changed %q to %q", plain, y.Text)
		}
	}
}

// TestTableRowQuotedWithPipesVerifies (bench5 SEC F388/F390 quarantined: the model quoted an .xlsx row
// with " | " between cells, the document has tabs, so the $103,800 and $58,800 totals never reached
// the join): a quote whose "|" separators stand for the document's cell separators verifies as
// tolerant at CellConfidence; words that are not in the row still do not.
func TestTableRowQuotedWithPipesVerifies(t *testing.T) {
	for _, q := range []string{
		"Total of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini | 05/03/2022 – 01/02/2024 | 22 | | $103,800.00",
		"Total Monthly Dues | Sum of all monthly membership dues payments (2022 + 2023 + 2024) | 05/03/2022 – 01/02/2024 | 21 | $2,800.00 | $58,800.00",
	} {
		v, _ := Verify(context.Background(), benchXLSXRows, q, nil)
		if !v.Verified || v.Method != "tolerant" || v.Confidence != CellConfidence || !strings.HasSuffix(benchXLSXRows[v.Start:v.End], q[len(q)-9:]) {
			t.Errorf("%q: %+v", q, v)
		}
	}
	for _, q := range []string{"Total Monthly Dues | Sum of all monthly payments | 21", "| | |"} {
		if v, _ := Verify(context.Background(), benchXLSXRows, q, nil); v.Verified {
			t.Errorf("%q verified: %+v", q, v)
		}
	}
}
