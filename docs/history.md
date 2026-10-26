# History

What the README used to carry as history, moved verbatim at `v2` eb17567: the PRs the README's
"What exists today" heading listed, each PR's named tests, and each PR's line-budget paragraph.
Line budgets are obsolete since owner decision 17 (no total ceiling: `python3 scripts/loc.py` prints
today's figures); they are kept as the record of what each PR cost against its plan.

## What was built, by PR

PR 1: skeleton, config, store, CI; PR 2: model client; PR 3a: documents; PR 3b: model roles, embeddings, hybrid search; PR 3c: vision transcription; PR 11, pulled forward by owner decision 13: the UI and its recorded engine; PR 4a: the agent runtime and the deterministic memo tools; PR 4b: the memo workflow, durable runs and the REST API; Manage and skills; PR 8: sign-in.

## Named tests

Named tests (PLAN.md PR 1): `TestConfigDefaults`, `TestVendorBreakerRefusesDirectAnthropic`,
`TestMigrationFilesOrderedAndUnique`, `TestStoretestSkipsWithoutDSN`,
`TestStoretestFailsWhenRequired`, `TestLocBudget` (Python, `scripts/test_loc.py`), and against
Postgres `TestMigrateIdempotent`, `TestConcurrentMigrateSafe`, `TestFailedMigrationRollsBack`,
`TestFilesRoundTripBytea`, `TestSchemaPerTestIsolation`. Extra: DSN checks and secret redaction
(every shape the review committee used to leak one), the exposure rule (runtime refusal and the
Compose port check with its bypass cases), migration atomicity when the bookkeeping row fails, a
file that ends its own transaction, lock-wait logging and cancellation with the port still
closed, chunked storage and the chunk-size `CHECK`, the 64 MiB boundary, corruption detection,
name/type validation, upload slots, a DSN accepted under a uid with no passwd entry, `/health`, command dispatch, a real
`serve` end to end, and repository consistency (env-var tables, one Go version, deploy files,
LICENSE/NOTICE in the image).

Named tests (PLAN.md PR 2, `internal/model`, no network and no key): `TestChatToolCallRoundTrip`,
`TestTypedErrors`, `TestRetryBackoffBounded`, `TestFakeScriptExhaustedIsError`,
`TestFakeIsDeterministic`, `TestFakeRoutesByAgentName`, `TestCallsPricedOnEveryHost` (PLAN's
`TestLocalCallPricedZeroWithWattHours`, inverted by the owner's round-2 ruling and renamed; with
`TestWattHoursRecorded`),
`TestPricesJSONOverride`, `TestImagePartsRoundTrip`, and in `cmd/biglaw` `TestConfigModelVars`.
v1's pricing vectors are carried as `TestLookupPricing`, `TestCalcCostUSD`,
`TestCalcCostUSD_GPT56Luna`, `TestGPT56Siblings_RecognisedButUnpriced`,
`TestSetModelRates_OverridesBuiltins`, `TestRecord_UnknownModelFlaggedAndCounted`,
`TestWarnUnpricedOnce_PerModel`, `TestBuiltinClaudeRatesUnchanged` (v1's
`TestLocalWattMeteredCallsAreFree` is reversed by owner decision: `TestCallsPricedOnEveryHost`), and
`TestBuiltinPricesParse` checks every row of v1's `basePricing` against the new table. Extra:
request validation, stable request bytes for prompt caching, role fallback, and that no error carries the key, the endpoint URL or upstream text. Round 1 of
review added `TestBilledFailureKeepsUsage`, `TestUnpinnedBehaviours` (no retry after a network
error, `max_completion_tokens`, `Truncated`, the 25% jitter bound, `Retry-After` overflow,
duration over all attempts), `TestTransportErrorsFixedText` (an endpoint echoing the key and the
prompt), `TestAnswerToolCallsChecked`, `TestPriceFileChecks` (a FIFO and a 1 MiB + 1 price file) and, in
`cmd/biglaw`, `TestServeLogsModelSetup`. Round 2 added `TestNilContentSentAsEmpty` and pinned the
DNS phrase, `Fake`'s `Truncated`, a billed failure's duration and watt-hours, and the refusal of
role ids that are URLs or credentials.

Named tests (PLAN.md PR 3, slice 3a; DB tests need Postgres): the carried vectors of v1's
`chunk_test` (`TestTableRowChunking`, `TestProseStillSectionChunked`), `pageindex_test`
(`TestReconstruction`, `TestNumberedNesting`, `TestArticleAndRecitalNesting`, `TestFlatDocument`,
`TestSchemes`), `tabcite_test` (`TestCitationExactAndTolerantNoJudge`,
`TestCitationParaphraseJudgeOneCall`, `TestCitationUncertainJudgeTriggersEnsemble`,
`TestCitationEnsembleMajorityUnsupported`, `TestCitationFabricatedQuoteUnverified`,
`TestCitationJudgeFailureIsAnError` (changed: an error, not "unverified"),
`TestCitationWindowLocatesRegion` (without its page variant); the two cell/tally vectors go with
the diligence table, PR 5), the reader part of `ooxml_test` (`TestOpenRejectsNonDocx`,
`TestTextJoinsRunsAndSeparatesParagraphs`; `TestEscapeUnescapeRoundTrip` and
`TestRoundTripPreservesPartsAndOrder` move to PR 3d with the writer code they test), v1 `protocols` (`TestVerbatimContains`,
`TestNormalizeSourceKey`, `TestResolvesByTitle`) and v1 `services` (`TestExtractPlainText`,
`TestExtractDOCX`); `TestScannedPDFFlaggedNoText`, `TestOutlineHasNumbersTitlesTokens`,
`TestSectionPaginates`, `TestRetrieveNeverLeavesMatter`, `TestSearchRareTermOutranksCommon`,
`TestSearchTopKDeterministic`, `TestSearchExactIdentifier`, `TestLexicalRecallProofRuns` (E4),
`TestLadderMethodsAndCaps`, `TestFileNotReachableOutsideMatter`, `TestFileDeletedWithDocument`,
`TestConcurrentReadsBounded`, `TestUploadReadDeadline`, `TestDownloadNameSafe` and the extended
`TestFilesEdgeCases`. Extra: `TestMarkdownHeadings`, `TestChunksCoverEveryWordOnce`,
`TestExtractPDFTextLayer`, `TestHostilePDFContained`, `TestIngestRefusalLeavesNothing`,
`TestUploadTotalDeadline`, `TestUploadErrorsTyped`, `TestWriteErrorKinds`,
`TestOpenRefusesZipBomb` and, in `cmd/biglaw`, `TestBinaryIsItsOwnPDFReader`. Review round 1
added `TestChildBoundsItself`, `TestChildDiesWithParent`, `TestForgottenMaybeRunChildIsAnError`,
`TestTextFidelity`, `TestTextLinearAndBounded`, `TestSectionTreeBounded`,
`TestHeadingHeuristics`, `TestDownloadBounded`, `TestOrphanFilesSwept`,
`TestSearchRarityIsScoped`, the answer-free questions of E4, and cases in
`TestCitationJudgeFailureIsAnError` (a failed vote), `TestExtractPDFTextLayer` (12 blank pages),
`TestDownloadNameSafe`, `TestFilesEdgeCases` (emoji names), `TestOutlineHasNumbersTitlesTokens`
and `TestSectionPaginates` (out-of-range ids and offsets) and `TestServeEndToEnd` (the orphan
sweep at startup). Review round 2 added `TestReadChildBounded`, `TestChildNearTextCap`,
`TestChildRunsTheRunningImage`, `TestIngestMemoryBounded`, `TestSearchCost`,
`TestReadFileWithholdsUnverifiedTail`, `TestDownloadDeadlinePerWindow`,
`TestIngestSlotWaitBounded` and, in `cmd/biglaw`, `TestServeStopCancelsIngest`; extended
`TestSectionTreeBounded` (table shapes, every chunk column, the exact cap and its note),
`TestConcurrentReadsBounded` (a slot held while the writer blocks), `TestHostilePDFContained`
(the text cap on uploads), `TestOpenRefusesZipBomb` (only text parts are read) and
`TestTextFidelity` (`noBreakHyphen`, `sym`). Two carried vectors changed:
`TestTextJoinsRunsAndSeparatesParagraphs` expects a tab for `<w:tab/>`, which v1 dropped, and
`TestTableRowChunking` expects rows grouped into one chunk, not one chunk per row.

Named tests (PLAN.md PR 3, slice 3b; DB tests need Postgres with pgvector): in `cmd/biglaw`
`TestModelsRoleMap` and `TestRoleIDRefusesQueryCharacters`; in `internal/model`
`TestEmbeddingsPriced`; in `internal/docs` `TestEmbedFailureLeavesLexical`,
`TestBackfillEmbedsOldDocuments`, `TestHybridKeepsScope` (G1 with vectors) and
`TestHybridRecallProofRuns` (gate E4; it skips on the self-test tape, above). Extra:
`TestEmbedTypedErrors`, `TestFakeEmbed`, `TestHybridFusion`, `TestEmbedBatchesAndUsage`, `TestEmbedWaitsBounded`,
`TestE4TapeReplays` (the harness self-test), `TestOpenNeedsVector`, `TestVectorOncePerDatabase`
(storetest creates the extension once per database, not per schema), `TestServeHybridEndToEnd`,
the hybrid figures in `TestSearchCost`, the `embed`/`vision` cases of `TestRolesModel`, and the
`search` field in `TestHealth`. `TestLexicalRecallProofRuns` now shares E4's scoring with the
hybrid gate (`e4Measure`). Review round 1 added `TestReembedNeverMixesModels`, `TestLexicalOnlyNotBuried`, and cases in
`TestBackfillEmbedsOldDocuments` (failures counted and noted, "in a row" pinned), `TestEmbedWaitsBounded`
(the backfill's per-document bound), `TestEmbedFailureLeavesLexical` (a lexical-only scope during
an outage), `TestEmbeddingsPriced` (token cap, ada-002, no usage), `TestEmbedTypedErrors`
(subnormal, huge), `TestFakeEmbed`, `TestRoleIDRefusesQueryCharacters` (bidi, zero-width, key
shapes, length), `TestOpenNeedsVector` (8 concurrent first starts) and `TestDownloadBounded`
(measures the stalled state).

Named tests (PLAN.md PR 3, slice 3c, vision; no model key: `model.Fake` and `httptest` only):
`TestTextModelNeverSentImages`, `TestUnreadableImageNotFabricated`, `TestBlankImageNoContent`,
`TestVisionNotConfiguredTyped503KeepsOriginal`, `TestTranscriptCitationCappedAndDisclosed`,
`TestLowAgreementFindingsGatedIndividually`, `TestScannedPDFDCTPagesExtracted`,
`TestScanEncodingUnsupportedTyped` (CCITT, JBIG2, a second filter, TIFF), `TestTooManyPages413`,
`TestMixedPDFTextLayerNotRewritten` and `TestTranscriptionJobResumesAfterRestart` (DB), and v1
`services` vectors `TestExtractImageWithoutVisionDegrades` (changed: the image is kept, waiting,
with a note naming vision, and the upload is 503) and `TestSniffImageMIME`. Extra:
`TestIllegibleNeverFilled`, `TestVisionFailuresTyped` (G6 kinds, a cut-off reading, the text cap,
the 10-minute bound), `TestLexicalOnlyDisplacementMeasured` (PR 3b r2 L2's figures) and, in
`cmd/biglaw`, `TestServeNeverSendsImagesToTheLead` (a lead-only server sends nothing; restarted
with a vision role it transcribes the waiting image at startup). Changed by 3c:
`TestScannedPDFFlaggedNoText` (a JPEG-scanned PDF without a vision role is stored waiting, 503; a
PDF with nothing on it is still `noText` with its note), `TestIngestRefusalLeavesNothing` and
`TestUploadErrorsTyped` (a damaged image is 422, a TIFF or CCITT scan 422
`scan_encoding_unsupported`, all leaving nothing), `TestExtractPlainText` (an image is a page to
transcribe, never text).

Named tests (PLAN.md PR 4b; DB tests need Postgres with pgvector; every model is `model.Fake` or an
`httptest` server): specs `TestSpecsLoadAndValidate`, `TestExtractFactsTypedValues`; `TestModelErrorsAsJSON`,
`TestConfigEngineVars`; policy `TestAllocationsMatchSpec`, `TestClassShareRefused`,
`TestLedgerPlanRequiredFirst`, `TestPlanFallbackToChecklistDisclosed`, `TestCommitteeRepeatsWhileFunded`,
`TestEscalationFiresOnFastFix`, `TestFinishRefusedWithoutCommittee`,
`TestFinishRefusedWithOpenFindingsWhileFunded`, `TestFinishAcceptsWhenBudgetExhaustedWithDisclosure`,
`TestFinishNeedsEverySuccessCriterion`; paged drafting `TestPagedDraftingLossless` (v1's
`TestPagedBoard_CompactPagingIsLossless`), `TestPriorSummariesPassedInOrder`, `TestDraftGetOnlyFinishedSections`,
`TestSummaryNeverRendered`; `TestFinishCheckIsDryRun`, `TestFinishSubmitRunsSameChecks`,
`TestInjectedDocumentCannotSkipCommittee`, `TestLowAgreementFindingGated`,
`TestDeliverableBadgesTranscriptCitations`, `TestEvidenceNoteCountsTranscriptOnlyFindings`;
`TestLedgerBucketsFindingsByIssue`, `TestLedgerNearDuplicateMerged`, `TestSweepVisitsEverySectionOnce` (+ v1's
three `sectionChunks` vectors), `TestSweepIdempotent`, `TestSweepReportsIncompleteWhenBudgetShort`,
`TestCommitteeCriticsContextFree`, `TestCommitteeRunsThreeInParallel`; v1's 16 `deviations_test` vectors,
`TestDeviationNeedsBothSidesVerified`, `TestOmissionRequiresVerifiedAbsence`; durable runs
`TestResumeAfterKillMidRun`, `TestResumeDoesNotRepeatCommittedSteps`,
`TestResumeChargesInFlightCallConservatively`, `TestRunLeaseSingleOwner`,
`TestBudgetStopEndsRunWithDisclosure`, `TestMaxStepsStop`, `TestFinishRejectedByCriticContinuesLooping`,
`TestAwaitingGatePausesAndResumes`, `TestCancelStopsRun`, `TestRunProgressAPI`; bandit
`TestBanditStatsPersistAcrossTasks`, `TestBanditDelayedCriticCredit`; integrity
`TestUnverifiedFindingNotInLedger`, `TestQuarantineExcludedFromDraft`, `TestFabricatingModelFailsTask`,
`TestDeliverableLintRejectsUnknownFid`, `TestExternalAuthorityFlagged`, `TestModelFailureIsNotAFinding`,
`TestEmptyModelReplyIsError`, `TestDeliverableGateBlocksCompletion`, `TestModelDigitsNotFromLedgerFlagged`,
`TestRetrieveNeverLeavesMatter` (engine level); packs `TestPackSelectionByJurisdictionPrefix`,
`TestCandidatePackDisclosed`; E1/E2 `TestProofRunsOracle`, `TestProofRunsFabricator`; UI embedding
`TestEmbeddedUIServesIndex`, `TestBuildWithoutUIServesNotice`, `TestAPIListsNeverNull`; Python
`test_compare_exit_codes`. Extra: `TestLabRunnerSmoke` (the LAB driver end to end against an
`httptest` server), `TestSweepShareBounded`, `TestSubagentLedgerReadsSkipQuarantine`, `TestUSDBoundFinishDeliversWithoutMeta`,
`TestEventsStream`, `TestE2EServe` (not a check: with `E2E_ADDR` set it serves the API, engine and
`ui/dist` over a scripted model for a browser run). Each guarded behaviour was broken once on purpose
and its test failed (65 deliberate breaks, all caught). Bench fixes (after the first real-model
bench; each scripted with `model.Fake` from the logged behaviour, each failing without its fix):
`TestPhaseSharesHoldInUSD`, `TestCommitteeAlwaysReviewsTheLastDraft`, `TestBudgetKeepsLaterPhases`,
`TestCalcAcceptsWhatModelsSend`, `TestLintSaysHowToFix`, `TestFigureLintIsBounded`,
`TestRendererUsesDrafterHandleSet` (codenames kept for the run) and `TestDeadlinesMeetTheAnswerKey`
(the litigation matter's four deadline criteria, from `eval/tasks`, met by a scripted run). LAB
improvements (after the real Haiku LAB rerun; scripted the same way): `TestSweepPagesFitTheAnswer`,
`TestTablePagedByRows`, `TestSweepReadsInParallelInPlanOrder`, `TestSweepResumesAfterKillMidSweep`,
`TestDevParaphrasedRequirementRefused`, `TestDevRealOmissionsKept`, `TestDevRestatedControllingQuoteRescued`,
`TestFigureLintSkipsLocatorLists`, `TestCoveragePassFromTheReserve`, `TestCoveragePassOnSubmit`,
`TestFakeRouteAnswersByContent`.

## Line budgets

From README § Agent runtime (PR 4a):

- **Line budget:** `agent` 1,242 against 1,100, `analysis` 1,183 against 950, the `docs` adapters
  153 against 50 (§ Budgets: accepted by owner decision 14; no total ceiling since decision 17).

From README § Limitations, Memo workflow (PR 4b):

  - **Line budget:** PR 4b adds 3,818 lines (7,502 → 11,320) against about 3,060 its components carry
    (engine memo part 2,340, api 660, the documents routes 60): `engine` 3,406, `api` 248, the rest
    in `agent` (resume), `docs` (routes), `cmd` (wiring). The plan after 4b (16,822–16,852) broke the
    16,100 ceiling; owner decision 15 raised it to 17,000, decision 16 (bench fixes) to 17,500, and decision 17 dropped the total ceiling (§ Budgets).
    Round 1's fixes added 74 (11,394; plan 16,896–16,926).

From README § Limitations, Documents (PR 3a):

  - **Line budget:** `internal/docs` measures 1,733 lines (1,471 plus `http.go` 262) against the
    1,110 DESIGN plans for what 3a builds plus api's `documents` 200 (moved here, covering the
    routes PR 4b still adds), `docs/ooxml` 174 against its reader's 120, `store` 641 against 580
    and `cmd` 528 against 505. With the 4a adapters and the remaining documents routes counted,
    the plan totals 14,866–14,896, under the 15,500 ceiling (final) by 604–634; with PR 3b's
    pgvector estimate (270–405 lines, DESIGN § PR 3b pgvector estimate) it is 15,136–15,301,
    **under by 199–364**; the ceiling will not rise again, so a later overrun must be trimmed. `scripts/loc.py`'s
    actual count is far below it.

From README § Limitations, Hybrid search and embeddings (PR 3b):

  - **Line budget:** PR 3b adds 401 production lines (3,894 → 4,295) against DESIGN's 270–405
    (§ PR 3b pgvector estimate), at its top: round 1's fixes (mixed-model re-embeds, lexical-only
    fusion, query checks before embedding, vector and token guards, key-shaped ids) were paid for
    by deleting the dimensions option, the "lexical only" startup log, storetest's extension cache
    and doc comments that repeated this README. Per file: `model/embed.go` 118, `docs/embed.go` 131, `docs/search.go` 27, `docs/ingest.go` 13, `model/fake.go` 39, `cmd/biglaw/main.go` 22, `config.go` 18, `model/client.go` 9, `store.go` 9, `storetest` 15. Plan total
    14,866–14,896 + 401 = 15,267–15,297, under the final 15,500 ceiling by 203–233.

From README § Limitations, Vision transcription (PR 3c):

  - **Line budget:** PR 3c adds 572 production lines (4,295 → 4,867: 482 as built, then 90 in
    the round-1 fixes) against DESIGN's 310 for it (`docs` 300 + `cmd` 10; the `model` image parts
    landed in PR 2 and contract v1's `api` 40 is PR 8's): **262 over**. Round 1 per file:
    `docs/vision.go` +68 (the disagreement rule `compare`/`diff`/`lcs` with masking, the 4xx
    refusal, the key scrub, the dimension cap, mixed-PDF failure notes, usage always recorded, the
    refused-spend log), `docs/ingest.go` +17 (reader slots, stamped scan pages, SQL `NULL`),
    `docs/jobs.go` +2, `docs/child.go` +1, `docs/scanpdf.go` +1, `model/errors.go` +1. The prompts
    are data (`vision_prompts.txt`, not counted, as `prices.json` is). Plan total 15,267–15,297 +
    572 − 310 = **15,529–15,559**: over the then-final 15,500 by 29–59, so it went to the owner, who
    raised the ceiling to 15,600 (decision 11; headroom 41–71, which PR 3e's embed endpoint must fit).
    Round 2 adds 33 (4,867 → 4,900: `docs/vision.go` +25 for the positional mask `align` replacing
    `lcs`, figure tokens, marks, line-end hyphens, a damaged page noted in a mixed PDF and the 4xx
    wording; `docs/scanpdf.go` +5 and `docs/ingest.go` +3 for page-shaped legends and the unread-image
    note; `docs/jobs.go` 0): plan **15,562–15,592**, headroom 8–38 for PR 3e's 30–40. Round 3
    removes 2 (4,900 → 4,898: the page-shaped rule out, the alignment's wildcard and second walk and
    the damaged-page note in): plan **15,560–15,590**, headroom 10–40. Round 4 (owner decision 12:
    ceiling 15,650, long-legend scans read now) adds 20 (4,898 → 4,918: `docs/vision.go` +14 for
    `beyond` and the mixed page's notes, `docs/ingest.go` +3, `docs/scanpdf.go` +3 for `mixedMax`):
    plan **15,580–15,610**, headroom 40–70. Round 5 adds 3 (4,918 → 4,921: `docs/vision.go` +1 for
    `beyond`'s letters-and-digits comparison and CR strip, `docs/verify.go` +2 for the quote's copy
    outside the transcript; the cap note and "no call, no Transcript" 0): plan **15,583–15,613**,
    headroom 37–67; with PR 3e's 30–40, 15,613–15,653: PR 3e must stay at or under 37 lines. Round 6
    removes 3 (4,921 → 4,918: `beyond`'s whole-word rule replaces the word-run and 8-character rules,
    the "adds nothing" note and the whole-word quote move in, paid for by compacting `pageList`, the
    WebP sniff and the disputed-word cap): plan **15,580–15,610**, headroom 40–70. Round 7 adds 1
    (4,918 → 4,919: `beyond`'s short-line rule, a short line subtracted only as a whole layer line;
    the quote move's figure-separator edges 0): plan **15,581–15,611**, headroom 39–69; PR 3e must
    stay at or under 39 lines.

From README § Limitations, Model client (PR 2):

  - **Line budget:** `internal/model` measures 714 lines against the 610 the v2 design planned
    (670 in revision 5, less 60 for the price table as data): 104 over its share. `cmd/biglaw`
    measures 505 against PR 1's 477 (+28). The owner raised the ceiling to 14,500 and DESIGN
    records the move: measured the review's way (PR 1 as built, 1,161, plus PR 2's growth over
    plan, the remaining packages at their DESIGN budgets, cmd's import-v1 reservation intact), the
    plan totals 14,245 of 14,500, 255 lines of headroom (superseded: PR 3a's figures and the
    15,500 ceiling are under § Documents above).
