# Citation-integrity spot-check record

The collateral's citation-integrity claims are scoped to the checks recorded here — no more.
Two checks exist, by different reviewers, on different runs; the second was adversarial and
found one defect, which is disclosed and tracked rather than papered over.

## Check 1 — six citations, pipeline release run (reviewer: work-product review session, 2026-07-04)
Run: `biglaw-haiku-release/20260703-035626` deliverable (claude-haiku-4-5, 34/60 run).
All six verified **verbatim** against the task's source documents:

1. Referral ¶21 allocation-rate arithmetic (71 of 87 → 81.6%)
2. The §209(e) statutory text as quoted
3. Referral ¶38's Section 9.1 (Compliance Manual) language
4. The Hargrove & Tilton manual revision date
5. The bank-records exhibit initiation-fee line item
6. The 12% × $185M LP-interest calculation

## Check 2 — twelve quotations, adversarial re-check (reviewer: pre-publication audit session, 2026-07-07)
Run: `haiku-rounds3/20260706-003200` deliverable (the 50/60 run). Result: **11 of 12
verbatim; one truncated quotation presented as verbatim** — the memo repeatedly quotes the
Form ADV as *"no account receives preferential treatment"* where the source reads *"no
account **or group of accounts** receives preferential treatment in the allocation of
investment opportunities"* — an interior truncation with no ellipsis.

Disposition: logged as a writer-discipline defect (quote truncation must carry an ellipsis
or carry the full span; the quote-inviolability invariant covers corruption but not
abbreviation). Fix tracked with the writer v2 follow-ups.

## Contrast check — the minimal-harness baseline (both reviewers, independently)
Run: `haiku-raw/manual-001` (Harvey's own harness, 41/60). Its deliverable asserts
*"Section 209(e) and Section 203(i) … provide for civil penalties up to $5,000 per
violation"* and builds "$10M–$50M" / "$30M–$75M+" exposure estimates on that figure.
**"$5,000", "$50 million", "$30 million", and "$75 million" appear nowhere in the eight
source documents** (verified by corpus search in both reviews). The figures are invented.

## How to reproduce
Deliverables and judge records live with each run's directory in the published run history
(see `docs/benchmarks.md`); source documents are the Harvey LAB task's `documents/` set.
