---
name: figure-conflicts
description: Find figures the documents state differently with join_discrepancies, and state both sides of each.
status: candidate
reviewedBy:
tools:
- join_discrepancies
- numbers
---
Process guidance drawn from the lead's prompt and the join_discrepancies tool's spec.

1. Call join_discrepancies after the sweep, from verified finding values only; a quarantined finding is
   refused. Pass aliases when the record calls one party by several names; they are kept for the run.
2. Only a pair the judge decides is one quantity stated two ways is a conflict. An unclear pair is
   listed as such, and pairs past the cap or the budget are counted as notJudged: disclose them.
3. finish requires the memo to state both figures of every conflict it finds. Cite each side's
   finding; do not adjudicate which value is right.
4. When the result says truncated, call again naming fewer fids for the rest.
