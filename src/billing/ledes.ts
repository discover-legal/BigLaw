// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import type { TimeEntry } from "../types.js";

export interface LedesOptions {
  invoiceNumber: string;
}

const CRLF = "\r\n";

function toLedesDate(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}${m}${day}`;
}

function fmt2(n: number): string {
  return n.toFixed(2);
}

function sanitizeField(s: string): string {
  // Strip all control characters and pipe (field delimiter) to prevent injection.
  return String(s ?? "")
    .replace(/[\x00-\x1F\x7F]/g, " ")
    .replace(/\|/g, " ")
    .trim();
}

export function exportLedes1998B(entries: TimeEntry[], opts: LedesOptions): string {
  const today = toLedesDate(new Date());

  const validEntries = entries.filter(e => e.billingUnits && e.billingUnits > 0 && e.endedAt);

  const invoiceTotal = fmt2(validEntries.reduce((s, e) => s + (e.billingAmountUsd ?? 0), 0));

  const dates = validEntries
    .flatMap((e) => [e.startedAt, e.endedAt])
    .filter((d): d is Date => d != null);
  const billingStart = dates.length > 0 ? toLedesDate(new Date(Math.min(...dates.map((d) => d.getTime())))) : today;
  const billingEnd   = dates.length > 0 ? toLedesDate(new Date(Math.max(...dates.map((d) => d.getTime())))) : today;

  const header =
    "INVOICE_DATE|INVOICE_NUMBER|CLIENT_ID|LAW_FIRM_MATTER_ID|INVOICE_TOTAL|" +
    "BILLING_START_DATE|BILLING_END_DATE|INVOICE_DESCRIPTION|LINE_ITEM_NUMBER|" +
    "EXP/FEE/INV_ADJ_TYPE|LINE_ITEM_NUMBER_OF_UNITS|LINE_ITEM_UNIT_COST|" +
    "LINE_ITEM_AM_BILLED|LINE_ITEM_DESCRIPTION|LINE_ITEM_TASK_CODE|" +
    "LINE_ITEM_EXPENSE_CODE|LINE_ITEM_ACTIVITY_CODE|TIMEKEEPER_ID|" +
    "TIMEKEEPER_NAME|LINE_ITEM_REVIEWED_BY_CODE|LINE_ITEM_BUDGET_CODE|" +
    "PEER_REVIEW_BY_CODE|TIMEKEEPER_CLASSIFICATION[]";

  const invoiceNum = sanitizeField(opts.invoiceNumber);

  const rows = validEntries.map((e, i) => {
    const units = fmt2(e.billingUnits * 0.1);
    const rate  = fmt2(e.billingRate ?? 0);
    const billed = fmt2(e.billingAmountUsd ?? 0);
    const desc  = sanitizeField(e.description);
    const tkId  = sanitizeField(e.profileId ?? e.agentId ?? "");
    const tkName = sanitizeField(e.profileName ?? e.agentName ?? "");
    const tkClass = e.agentId ? "AI" : "TK";

    return (
      `${today}|${invoiceNum}|${e.clientNumber ?? ""}|${e.matterNumber ?? ""}|` +
      `${invoiceTotal}|${billingStart}|${billingEnd}||${i + 1}|F|` +
      `${units}|${rate}|${billed}|${desc}|${e.utbmsTaskCode ?? ""}||` +
      `${e.utbmsActivityCode ?? ""}|${tkId}|${tkName}|||` +
      `|${tkClass}[]`
    );
  });

  return ["LEDES1998B[]", header, ...rows].join(CRLF) + CRLF;
}
