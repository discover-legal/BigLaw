// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Sample matter for `biglaw demo`: an original synthetic credit agreement,
// a small firm playbook, the CP-checklist content, and the clause document
// plus opposing-counsel edits that drive the counter-redline beat. All prose
// here is authored fresh for this demo — none of it is drawn from any real
// agreement or third-party source.

package main

import (
	"strconv"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Beat 1: the seeded matter ────────────────────────────────────────────────

const demoAgreementDocID = "demo-credit-agreement"

const demoAgreementTitle = "Revolving Credit Agreement — Meridian Data Systems / First Harbor Bank ($45,000,000)"

const demoAgreementText = `REVOLVING CREDIT AGREEMENT

This REVOLVING CREDIT AGREEMENT (this "Agreement") is dated as of March 12, 2026, and is entered into between MERIDIAN DATA SYSTEMS, INC., a Delaware corporation (the "Borrower"), the LENDERS party hereto from time to time (the "Lenders"), and FIRST HARBOR BANK, N.A., as administrative agent for the Lenders (the "Administrative Agent").

ARTICLE I — DEFINITIONS

Section 1.01. Defined Terms. "Applicable Margin" means 2.75% per annum for Term SOFR Loans and 1.75% per annum for Base Rate Loans. "Business Day" means any day other than a Saturday, Sunday, or other day on which commercial banks in New York City are authorized or required by law to close. "Maturity Date" means March 12, 2031. "Total Net Leverage Ratio" means, as of any date, the ratio of Consolidated Total Net Debt to Consolidated EBITDA for the four fiscal quarters most recently ended.

ARTICLE II — THE FACILITY

Section 2.01. Revolving Commitments. Subject to the terms and conditions set forth herein, each Lender severally agrees to make revolving loans to the Borrower from time to time during the Availability Period in an aggregate principal amount not exceeding $45,000,000 (the "Revolving Facility"), of which up to $5,000,000 may be utilised by way of letters of credit (the "LC Sublimit").

Section 2.02. Incremental Facility. The Borrower may request one or more increases to the Revolving Facility in an aggregate amount not exceeding $10,000,000, subject to the consent of each increasing Lender and pro forma compliance with the financial covenants.

Section 2.03. Use of Proceeds. The proceeds of the loans shall be used for working capital, capital expenditures, and general corporate purposes of the Borrower and its subsidiaries.

ARTICLE III — INTEREST AND FEES

Section 3.01. Interest. Each Term SOFR Loan shall bear interest at Term SOFR plus the Applicable Margin of 2.75% per annum. Each Base Rate Loan shall bear interest at the Base Rate plus 1.75% per annum. Interest is payable quarterly in arrears.

Section 3.02. Default Interest. Upon the occurrence and during the continuance of an Event of Default, all outstanding obligations shall bear interest at a rate 2.00% per annum above the rate otherwise applicable.

Section 3.03. Commitment Fee. The Borrower shall pay a commitment fee of 0.35% per annum on the average daily unused portion of the Revolving Facility, payable quarterly in arrears.

ARTICLE IV — CONDITIONS PRECEDENT

Section 4.01. Conditions to Closing. The obligation of the Lenders to make the initial extension of credit is subject to the satisfaction of the following conditions precedent: (a) receipt by the Administrative Agent of counterparts of this Agreement executed by each party; (b) certified copies of the Borrower's certificate of incorporation, bylaws, and resolutions authorizing the transactions contemplated hereby; (c) a customary legal opinion of Hale & Winter LLP, counsel to the Borrower; (d) a duly executed security agreement, together with UCC-1 financing statements naming the Borrower as debtor; (e) audited consolidated financial statements of the Borrower for the fiscal year ended December 31, 2025; (f) certificates of insurance naming the Administrative Agent as additional insured and lender loss payee; (g) all documentation required under applicable "know your customer" and anti-money-laundering rules, including a beneficial ownership certification; and (h) payment of all fees and expenses then due, including an upfront fee of 0.50% of the aggregate commitments.

Section 4.02. Conditions to Each Borrowing. The obligation of each Lender to make any loan is subject to: (a) the representations and warranties being true and correct in all material respects; (b) no Default or Event of Default having occurred and continuing; and (c) delivery of a borrowing notice not later than 11:00 a.m. New York City time three Business Days prior to the requested borrowing date.

ARTICLE V — FINANCIAL COVENANTS

Section 5.01. Total Net Leverage Ratio. The Borrower shall not permit the Total Net Leverage Ratio as of the last day of any fiscal quarter to exceed 3.50 to 1.00.

Section 5.02. Interest Coverage Ratio. The Borrower shall not permit the ratio of Consolidated EBITDA to Consolidated Interest Expense for any period of four consecutive fiscal quarters to be less than 2.50 to 1.00.

ARTICLE VI — NEGATIVE COVENANTS

Section 6.01. Indebtedness. The Borrower shall not incur additional indebtedness for borrowed money in excess of $7,500,000 in the aggregate, other than the obligations hereunder and permitted refinancings thereof.

Section 6.02. Liens. The Borrower shall not create or suffer to exist any lien on its assets other than the permitted liens described on Schedule 6.02.

Section 6.03. Dispositions. The Borrower shall not dispose of assets with an aggregate fair market value exceeding $2,000,000 in any fiscal year without the prior written consent of the Required Lenders.

Section 6.04. Restricted Payments. The Borrower shall not declare or pay any dividend or other distribution while any Default is continuing.

ARTICLE VII — EVENTS OF DEFAULT

Section 7.01. Events of Default. Each of the following constitutes an "Event of Default": (a) failure to pay principal when due, or failure to pay interest or fees within three Business Days after the due date; (b) any representation or warranty proving to have been materially incorrect when made; (c) breach of any covenant in Article V or Article VI, if such breach remains unremedied for ten (10) Business Days after written notice from the Administrative Agent; (d) cross-default to any other indebtedness of the Borrower in excess of $5,000,000; (e) the Borrower becomes insolvent or commences any bankruptcy or insolvency proceeding; (f) entry of one or more judgments against the Borrower in excess of $5,000,000 that remain unstayed for 45 days; and (g) a Change of Control occurs.

Section 7.02. Remedies. Upon the occurrence of an Event of Default, the Administrative Agent may, and at the direction of the Required Lenders shall, terminate the commitments and declare all obligations immediately due and payable.

ARTICLE VIII — INDEMNIFICATION

Section 8.01. Indemnity. The Borrower shall indemnify the Administrative Agent and each Lender against all losses, claims, damages, and reasonable out-of-pocket expenses arising out of this Agreement or the use of proceeds, except to the extent resulting from the indemnified party's gross negligence or willful misconduct; provided that the Borrower's aggregate liability under this Section 8.01 shall not exceed $45,000,000.

ARTICLE IX — MISCELLANEOUS

Section 9.01. Notices. All notices shall be in writing and delivered to the addresses set forth on Schedule 9.01.

Section 9.02. Governing Law. This Agreement shall be governed by, and construed in accordance with, the laws of the State of New York.

Section 9.03. Amendments. No amendment or waiver shall be effective unless in writing and signed by the Borrower and the Required Lenders.`

// ─── Beat 1: the demo playbook (written to its own file, never the user's) ─────

// demoPlaybooks returns the firm-tier playbook seeded for the demo: two clause
// positions the counter-redline beat exercises — one the opposing edits cross
// (indemnification cap) and one they squeeze below a red line (notice period).
func demoPlaybooks() []types.Playbook {
	const now = "2026-03-12T00:00:00Z"
	return []types.Playbook{{
		ID:           "demo-playbook-banking",
		Scope:        types.PlaybookScopeFirm,
		Name:         "Demo Firm Playbook — Banking & Finance",
		Description:  "Seeded by `biglaw demo`; safe to delete.",
		PracticeArea: "Banking & Finance",
		Jurisdiction: "US-NY",
		ClauseTypes:  []string{"Indemnification cap", "Notice period"},
		Entries: []types.PlaybookEntry{
			{
				ClauseType:       "Indemnification cap",
				PracticeArea:     "Banking & Finance",
				StandardPosition: "Borrower indemnification must be capped at an aggregate amount not exceeding the total facility amount ($45,000,000).",
				FallbackPosition: "A cap of not less than 50% of the facility amount ($22,500,000) may be accepted with partner approval.",
				RedLines: []string{
					"Never accept uncapped or unlimited indemnification",
					"A cap below $22,500,000 requires partner sign-off",
				},
				DealPoints: []string{
					"Tie the cap expressly to the facility amount so it scales with any accordion increase",
					"Carve out only gross negligence and willful misconduct — resist broader exceptions",
				},
				LastUpdated: now,
			},
			{
				ClauseType:       "Notice period",
				PracticeArea:     "Banking & Finance",
				StandardPosition: "Covenant breaches carry a cure period of ten (10) Business Days after written notice before ripening into an Event of Default.",
				FallbackPosition: "A cure period of five (5) Business Days is acceptable for reporting covenants only.",
				RedLines: []string{
					"Never agree to a cure period shorter than five (5) Business Days",
				},
				DealPoints: []string{
					"Cure periods run from written notice, never from the breach itself",
					"Payment defaults are customarily excluded from cure periods — do not trade the covenant cure away for one",
				},
				LastUpdated: now,
			},
		},
		DocumentCount: 1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}}
}

// ─── Beat 2: tabular review columns ───────────────────────────────────────────

func demoReviewColumns() []interface{} {
	col := func(name, prompt string) map[string]interface{} {
		return map[string]interface{}{"name": name, "prompt": prompt}
	}
	return []interface{}{
		col("Parties", "Who are the parties to this agreement, and in what capacities?"),
		col("Facility amount", "What is the total facility amount, including any sublimits or incremental/accordion capacity?"),
		col("Interest rate", "What interest rate applies, including margins, default interest, and commitment fees?"),
		col("Events of default", "What are the events of default, including any grace or cure periods and cross-default thresholds?"),
	}
}

// ─── Beat 3: CP checklist (docx_generate input, per templates/cp-checklist.json) ─

// demoCPChecklistInput builds the docx_generate payload for a categorized
// conditions-precedent checklist: landscape, one section per category, each a
// four-column table (Index, Clause Number, Clause, Status) with Status left
// blank for the deal team to complete at closing.
func demoCPChecklistInput() map[string]interface{} {
	headers := []interface{}{"Index", "Clause Number", "Clause", "Status"}
	section := func(heading string, rows [][3]string) map[string]interface{} {
		tblRows := make([]interface{}, 0, len(rows))
		for i, r := range rows {
			tblRows = append(tblRows, []interface{}{
				// Index restarts at 1 within each category.
				strconv.Itoa(i + 1), r[0], r[1], "",
			})
		}
		return map[string]interface{}{
			"heading": heading,
			"level":   2,
			"table":   map[string]interface{}{"headers": headers, "rows": tblRows},
		}
	}
	return map[string]interface{}{
		"title":     "Conditions Precedent Checklist — Meridian Revolving Facility",
		"filename":  "demo-cp-checklist",
		"landscape": true,
		"sections": []interface{}{
			section("A. Corporate Authorisations", [][3]string{
				{"4.01(b)", "Certified certificate of incorporation, bylaws, and board resolutions of the Borrower authorizing the transactions", ""},
				{"4.01(g)", "KYC and anti-money-laundering documentation, including a beneficial ownership certification", ""},
			}),
			section("B. Finance and Security Documents", [][3]string{
				{"4.01(a)", "Executed counterparts of the Credit Agreement from each party", ""},
				{"4.01(d)", "Executed security agreement and UCC-1 financing statements naming the Borrower as debtor", ""},
				{"4.01(h)", "Payment of all fees and expenses then due, including the 0.50% upfront fee", ""},
			}),
			section("C. Diligence, Opinions and Reports", [][3]string{
				{"4.01(c)", "Legal opinion of Hale & Winter LLP, counsel to the Borrower", ""},
				{"4.01(e)", "Audited consolidated financial statements for the fiscal year ended December 31, 2025", ""},
				{"4.01(f)", "Certificates of insurance naming the Administrative Agent as additional insured and lender loss payee", ""},
			}),
		},
	}
}

// ─── Beat 4: base clause document + opposing counsel's edits ──────────────────

// Each clause is a single-line paragraph so the .docx visible text matches
// these strings byte-for-byte — the opposing edits anchor on them exactly.
const (
	demoClauseIndemnity  = `The Borrower shall indemnify the Administrative Agent and each Lender against all losses, claims, damages, and reasonable out-of-pocket expenses arising out of this Agreement, except to the extent resulting from an indemnified party's gross negligence or willful misconduct; provided that the Borrower's aggregate liability under this Section shall not exceed $45,000,000 (the "Indemnity Cap").`
	demoClauseCure       = `A breach of any covenant under this Agreement shall constitute an Event of Default only if such breach remains unremedied for ten (10) Business Days after written notice from the Administrative Agent to the Borrower.`
	demoClauseAssignment = `The Lender may assign all or any portion of its rights and obligations under this Agreement with the prior written consent of the Borrower, such consent not to be unreasonably withheld.`
)

// demoBaseClausesInput builds the docx_generate payload for the clause sheet
// that opposing counsel will mark up.
func demoBaseClausesInput() map[string]interface{} {
	section := func(heading, content string) map[string]interface{} {
		return map[string]interface{}{"heading": heading, "level": 2, "content": content}
	}
	return map[string]interface{}{
		"title":    "Key Negotiated Clauses — Meridian Revolving Facility",
		"filename": "demo-base-clauses",
		"sections": []interface{}{
			section("Indemnification", demoClauseIndemnity),
			section("Notice and Cure", demoClauseCure),
			section("Assignment", demoClauseAssignment),
		},
	}
}

// demoOpposingEdit is one tracked change "opposing counsel" makes to the base
// clause document. Kept as a struct (not raw maps) so tests can verify each
// edit anchors into the base clause text.
type demoOpposingEdit struct {
	Find          string
	Replace       string
	ContextBefore string
	ContextAfter  string
	Reason        string
}

// demoOpposingEdits returns opposing counsel's markup: one market-standard
// tweak the playbook has no position on (assignment consent standard) and two
// changes that cross firm red lines (slashed cure period, uncapped indemnity).
func demoOpposingEdits() []demoOpposingEdit {
	return []demoOpposingEdit{
		{
			Find:          "unreasonably withheld",
			Replace:       "unreasonably withheld, conditioned, or delayed",
			ContextBefore: "such consent not to be ",
			ContextAfter:  ".",
			Reason:        "Align the consent standard with market practice.",
		},
		{
			Find:          "ten (10) Business Days",
			Replace:       "three (3) Business Days",
			ContextBefore: "remains unremedied for ",
			ContextAfter:  " after written notice",
			Reason:        "Tighten the covenant cure period.",
		},
		{
			Find:          `shall not exceed $45,000,000 (the "Indemnity Cap")`,
			Replace:       "shall be unlimited",
			ContextBefore: "aggregate liability under this Section ",
			ContextAfter:  ".",
			Reason:        "Remove the indemnity cap.",
		},
	}
}

// demoOpposingEditsInput renders the edits as the edit_document tool payload.
func demoOpposingEditsInput(path string) map[string]interface{} {
	edits := make([]interface{}, 0, len(demoOpposingEdits()))
	for _, e := range demoOpposingEdits() {
		edits = append(edits, map[string]interface{}{
			"find":           e.Find,
			"replace":        e.Replace,
			"context_before": e.ContextBefore,
			"context_after":  e.ContextAfter,
			"reason":         e.Reason,
		})
	}
	return map[string]interface{}{
		"path":   path,
		"author": "Opposing Counsel",
		"edits":  edits,
	}
}
