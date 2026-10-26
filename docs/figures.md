# Figures: reading, admission, tracing and the join

What a number means is read by a model and never decided by a word list in Go (owner decision,
lab-r4 r4). This page holds the rules in full, moved verbatim from README § Memo workflow
(Evidence), § Agent runtime (`join_discrepancies`) and § Limitations (LAB round 3) at `v2` eb17567.
[architecture.md § 5](architecture.md#5-the-figure-pipeline) is the map. If this page and the code
disagree, the code is right: fix this page in the same change.

## Evidence admission and the figure reader

- **Evidence** (`extract_facts`, `analyze_issue`, `check_deviation`; subagents on the `specialist`
  role). Go completes each call (the section's text, the packs, and for an `extract_facts` call that
  names no issue, a sweep visit's, the plan's issues: each finding names the issue it bears on, and one
  that names none of the plan's is not admitted (a page read again after `op plan` added issues is sent, and admits, only the new
  ones); no per-call cap drops a page's facts: `TestSweepAdmitsOnlyThePlansFindingsOnce`, `TestTablePageKeepsARowEach`, `TestSweepCapNeverDropsProseFacts`) and checks every answer before it
  reaches the ledger: each quote goes through the ladder (`VerifyQuote`, then the `verify_quote`
  tool with its judge); a finding is verified only when every quote is (G2), else it is stored
  quarantined (G3) and never drafted from; a typed value absent from its own quotes is dropped, with a note on its finding
  (`valueNotes`, shown in the API's finding view and the UI), in the sweep's and `extract_facts`'s `notes`, as one LOW
  committee line per run listing the cited findings not raised before (after the other critics' lines, capped, and the
  committee's page puts HIGH and MEDIUM lines first: `TestR4V3AdmissionNotesFloodCommittee`), and as one Limitations
  line from the coverage pass's `unadmitted` list. What a number means is read by a model, never by
  a word list in Go (lab-r4 r4, owner decision): code finds the number spans of a text and reads their
  digits exactly as written (`internal/analysis/figures.go`: digits, separators, a decimal mark, a minus
  written right before them; a span's readings are what its form allows, "1,200" reading 1200 or 1.2;
  dates found by their form; specified by the literal corpus in `figures_test.go`, a generated property
  test and a fuzz test: no panic, spans in bounds and never overlapping, every reading round-trips),
  and the figure reader (`read_figures`, `specs/agents/read_figures.md`, critic role, with its own tools
  `figure_context`, the document around a quote, bounded and paged, and `exact_calc`, exact decimal
  arithmetic) labels each span by id: money, count, percent, date, other or unclear; its currency, its
  scale and the span or words that state it, which mark is its decimal point, its party, whether it is
  a range's end and its sign, what it measures and the period it is for, and for a typed value what its
  typed entity is to it. Go checks every label (`analysis.ReadLabels`): a label for a span it was not given
  is rejected and noted; a scale must be a power of ten whose source is the exact words of the span's own
  passage, whole words outside the span itself, before it (a heading, a table header, a lead-in) or right
  after its digits or its range's far end (never another passage's words, a span id, or words after the
  figure that follows it), or it is not applied: the figure reads at scale 1, unclear
  (`TestR5ScaleSourceIsItsOwnWords`, `TestR6ScaleWordsAndPlace`); a decimal mark must fit
  the digits; a sign may not contradict a written minus; what it cannot check, or what the reader says is
  unclear, is an unclear reading, and the amount is exact arithmetic on the written digits (reading ×
  scale × sign), so no label makes a figure the text does not state (`TestReadLabelsChecksEveryLabel`,
  `TestR4V4ReaderCannotInventSpansOrFigures`). Three label shapes a real reader gives are read as it
  meant them (bench7, `TestBench7LabelChecks`): a count given a period's unit whose words lie right
  after its digits ("21 months") is that period; money or a rate given the unit of its period ("$2,800
  per month") keeps its amount, the unit dropped (a unit that is no period's is unclear); a scale cited
  from the figure's own trailing zeros ("000" of "$8,238,000"), words its passage states nowhere else, is
  no scale: the digits read as written at scale 1, clear (so a table cell whose "($000)" header lies
  outside the passage reads as written, unscaled; when the passage states the cited zeros elsewhere too,
  a header's "($000)", it is unclear, and any other scale with no words of its passage is still not
  applied and unclear); a decimal mark the label leaves empty or names otherwise is unclear wherever the
  digits read two ways ("1.200"). A count value the reader reads as a period ("18 months") is
  admitted as that period when a quote span states it in its unit. A scaled reading states the same figure as another clearly
  only when the other is read at the same scale from equivalent words (folded for case, punctuation and a
  plural, one side's words all among the other's: "(In Millions)" and "million"; no word is known to be a
  scale); otherwise a match is at best unclear, gated (`SameFigure`; a "$500" read "in thousands" never
  verifies a typed "$500,000" or traces a memo's, and a "$500" scaled from "fee" never verifies a typed
  "$500 thousand": `TestR5ScaleSmuggleReachesMemo`, `TestR6ScaleSmuggleWithAScaledValueReachesMemo`). The reader reads
  each finding's verified quotes (less the numbers inside a locator, "Section 14.2", which G8 masks in a
  memo too), typed values (with their class, unit, entity and metric; a date value only its first number,
  for its party) and claim, and each drafted section's figures its drafter wrote (handle values are Go's),
  in calls of at most 60 spans (its answer, a label of 16 fields a span, must fit the subagent's 8,192-token
  answer cap: bench7's calls of 106 and 123 spans were cut off and failed: `TestReaderCallSpansBounded`) and
  32 KB, the smallest items first, an item over them read in parts (a passage's spans split between them;
  a span over 64 characters or a passage over 32 KB is not sent and stays unread: `TestR5OversizeInputIsSplit`).
  When it reads (bench7: a call a page took 164 of the sweep's 268 calls and the sweep read 103 of 142
  pages): a sweep's visits admit their findings unread and the sweep reads them all together at its end
  from the reader's own share (6% of the calls and 10% of the USD, out of the sweep's 30%: replaying
  bench7's 491 SEC findings through admission takes 17 calls, 87 at a call a page:
  `TestSweepReadsItsFindingsTogether`); a finding that states no typed value is read only when a drafted
  section first cites it, in the section's own call (`Finding.Unread` until then; G8 treats its quotes as
  never read: `TestUncitedFindingReadWhenCited`), or, when its claim states a figure, by `finish`
  before its judge from the judge's share, so the join sees its claim's sums (`TestValuelessClaimSumReachesTheJoin`;
  what that cannot fund is counted in Limitations: `TestUnreadClaimsDisclosed`); a sweep that stops on an
  error drops the values of the findings it never read, each with a note (`TestFailedSweepDropsUncheckedValues`); the lead's own `extract_facts`, `analyze_issue` and
  `check_deviation` admissions read at once, out of a call the subagent's grant holds back for it before
  the subagent runs and the quotes' judges cannot spend (`goTurns`, `TestR5ReaderReservedBeforeDrafting`),
  held only when a finding states a typed value; and `finish` reads first, before its judge spends
  anything, every section whose reading is missing, stale or was unfunded (`TestFinishReadsAStaleSectionFirst`).
  Metered and journaled like any call; P4's class shares count the subagents' turns, the sweep's its visits'
  (its reader's are the `read` share's), and the draft share the lead's drafters' calls, not their
  readers' nor a coverage redraft's, ledger kind `figread`: `TestR6CoverReaderCallsLeaveTheDraftShare`. Every
  reader call that fails is logged with its step and why (cut off or off its schema, out of turns,
  unfunded) and counted, ledger kind `figstats`; a batch that fails (not for want of budget or a model)
  is read again in halves, once, so one failure never drops all its findings (`TestFailedReaderBatchSplitInHalves`); a finding whose read failed stays unread, its values dropped with why, and is read again where it is next needed; the task's cost shows the reader's calls, failures by
  why and failure rate (`TestReaderFailuresCounted`). Its answer schema takes any kind, unit, sign or
  decimal as text (ReadLabels reads one off its words as unclear), so one bad label never sinks a call;
  `figure_context` finds a quote as the ladder matched it (tolerantly: `TestFigureContextFindsATolerantQuote`). Its complete answers (one
  accepted label for every span it was sent, not unclear throughout: `TestR6AllUnclearAnswerIsNotCached`) are cached (`figure_reads`, migrations 0006 and 0007) under a key derived from
  the reader's spec, prompt and tools' specs, the model, the matter and what it was sent (a section's also
  its task), so a run read again gets the same labels and an edited prompt reads again; a row goes with its
  task and its documents (`TestR4V4ReadingIsCachedByContent`, `TestR5CacheStoresOnlyCompleteAnswers`,
  `TestR5CacheKeyAndScope`). A finding it could not read (no budget, a failed call) keeps no reading, and its
  values are dropped with a note. A typed quantity is admitted only when a span of its quotes reads as the same kind,
  currency and exact amount as the value's own span (`valueStated`; a share count is never dollars, one
  quote's table header never scales another's figure: `TestR4V4SharesAreNoSum`,
  `TestR4V4HeaderScaleIsTheReadersPerQuote`, `TestR4V4RangeIsTheReaders`); when either reading is unclear
  but they agree on what can be read, it is admitted at low confidence and its finding is gated for a
  lawyer (`TestR4V4UnclearReadingGates`); its reading (`Value.Fig`) is what `Canon`, the joins, `Calc`'s
  operands and `Salience` read; a value with none is never compared, summed or ranked as a quantity.
  A money value's unit is the currency its figure states. Only an
  exact duplicate of a verified finding merges into it, its dropped values' notes with it (`TestR4V4DuplicateKeepsValueNotes`; `restated`: the same issue, the same quotes in the same documents with case and white
  space folded, the same typed values with their entity and metric, never a breach folded
  into a finding that states none); a near restatement (a reversed term, a mirrored party's clause) is a finding of its own
  (`TestRestatedNeverMergesANegatedTerm`, `TestRestatedNeverMergesMirroredParties`); a sum of money the claim states is
  kept as an untyped value only when the figure reader read the claim's span and a verified quote's span as the same
  money, clearly (`claimSums`; never a share count, never a bare "3" of "Section 3": `TestR4V4SharesAreNoSum`): no
  entity or metric, compared by the reader's measure and party of its claim span. `check_deviation`'s requirement is the controlling document's own
  words: Go verifies it in `requirementDocId` (one of `controllingDocIds`) like any quote before the
  subagent runs and hands the subagent the document's text, with the reviewed documents' passages
  most like it (`reviewedPassages`, ≤ 16 KB, searched by 24 of the requirement's distinct content words, its names, defined terms and numbers first, then its longest, `passageQuery`, under the search's 32, a failed search logged, so it answers at once: `TestDeviationGetsReviewedPassages`;
  its only tool is `retrieve`, as Go verifies every quote it gives): JSON escape artefacts (`\"Tommy\"`) are
  undone, and a requirement joined from several passages (a heading and its list, two sentences from
  two paragraphs) verifies part by part, split where the model joined them (at most 12 joins; its
  ellipses stripped from the parts' edges; one normalised index of the document, the caller's ctx),
  every part the document's own words, the parts in the
  document's order, one passage (§ Limitations, LAB round 3); a part under four words only as a
  heading line before another. The verified requirement is then read in a bounded window of the text around
  it (`requirement.go` `contextOf`; lab-r3 r4, coordinator decision: `docs.Parse`'s heading guesses are
  not reliable enough for a safety check, and a wrapped cross-reference read as a heading split a
  requirement's sentence): the requirement plus about 3 KB of the document before it and 3 KB after it,
  each edge moved to a sentence or line end (outward up to 256 bytes, else inward), at most 8 KB
  whatever Parse makes of the document, so introductions, lead-ins without a colon ("The Trustee shall
  not—"), and overrides or sibling sub-clauses nearby ("5.2 Clause 5.1 shall not apply", "Notwithstanding
  the foregoing …") are in it; the heading path above the window (`docs.Parse`'s sections that hold it) is
  shown as a bracketed label only, never a boundary. The window is marked truncated when the requirement's
  sentence or paragraph starts before it (a paragraph starts after a blank line, or at a line break after
  a sentence or clause end before a capital, a quote or bracket or a list marker, so a `.docx`'s
  one-line-break paragraphs count: `TestV5DocxParagraphNotTruncated`), when its first line is a list item
  (the list's "shall not:" introduction may be above it: `TestV5LongListChapeauConfirmed`), or when an
  edge found no sentence or line end and cuts a sentence
  (`TestContextIsTheTextAroundIt`, `TestContextTruncatedWhenTheSentenceStartsBefore`,
  `TestV4WrappedLineSplitsTheSentence`, `TestV4DecimalSiblingsAndChapeau`, `TestV4ContextBoundHolds`). The
  subagent judges against that text (`requirementContext`, the requirement marked ⟦ ⟧), its verdict's
  controlling quote must lie in it (else the verdict is dropped). Before the subagent runs, out of the
  call's grant first (so its cost is never left unfunded behind the verdicts), one bounded call on the
  `critic` role (`rt.ChatWithin`), with the window fenced as document text and nothing else (no verdict,
  correction or reviewed document: bench5 trust, handed the analyst's correction the check judged it and
  dropped the real incapacity-standard deviation), answers whether the controlling document actually
  imposes the requirement as quoted, not negated or overridden (`checkSection`, prompt
  `specs/section_check.md`; `TestSectionCheckAsksAboutTheRequirement`). Whether a reviewed document
  complies is the subagent's question, never the check's. Its answer is read from one strict JSON
  object only: exactly one `answer` key, spelled so, `yes`/`no`/`unclear` in any case, a trailing "." or "!" aside ("No."), any other
  wording ("Yes, but …") unclear; a code fence around it, its tag in any case, is allowed (`TestCheckAnswerStrict`).
  No (negated or overridden) fails the call before the subagent runs, with the checker's reason, so the
  lead quotes the requirement with what governs it; yes admits each verdict, the claim saying "An
  independent check read the surrounding text (N bytes) and found that it imposes the requirement as
  quoted", with the checker's reasoning labelled "not a quotation"; unclear ("could not tell"), or a yes
  on a truncated window, admits it at low confidence, labelled
  for a lawyer's review and gated at finish like a low-confidence transcript (`lowConfidence`); a check
  not had (the grant could not fund it, its call failed, its answer was malformed) admits the verdicts at
  low confidence and gated too, never drops them (`TestSectionCheckNotHadAdmitsLowGated`; bench5 trust
  dropped three real deviations as unfunded). The finding quotes the window verbatim as its controlling citation, the requirement
  marked, with the part and the correction given as the analyst's words, not quotations; prosecute checks
  them against it (`TestV3BlankSeparatedListLosesIntroduction`, `TestV3OverrideBelowNotInContext`,
  `TestSectionCheckNegatedDropsVerdict`, `TestSectionCheckYesAdmitsBenchRequirements`,
  `TestSectionCheckUnclearOrCutIsLow`, `TestSectionCheckChargedWithinGrant`, `TestR2VProbesReadInContext`,
  `TestR2VFragmentsQuotedInContext`, `TestR2VVerdictQuoteOutsideContextRefused`, `TestOnePartFragmentReadInContext`,
  `TestBenchRequirementsReadInContext`, `TestContextIsBounded`). Format characters (zero-width spaces,
  bidi marks) are folded before any match; the parts handed on joined
  by " … " (`TestRequirementPartsVerify`, `TestDevMultiPartRequirementRuns`, `TestStitchedRequirementRefused`,
  `TestEllipsisJoinVerifies`, `TestStitchedRequirementMakesNoDeviation`); a paraphrase or a stitched
  requirement is refused, before any model call, with the nearest exact passage (its whole sentences
  or lines when they fit in 300 bytes, else at most 300 bytes; fenced as document data) to copy
  (`TestDevParaphrasedRequirementRefused`). It keeps a
  deviation only when both sides' quotes verify (a verdict that restated the requirement, its words
  and no figure of its own, stands on the verified requirement: `TestDevRestatedControllingQuoteRescued`),
  and an omission only when the provision does not verify in the reviewed document: string match,
  then the paraphrase judge on its closest passage, with the passage where it belongs quoted beside
  it (`TestDevRealOmissionsKept`; with no judge call left, fewer than half its significant words in
  the document). The subagent's reward for the bandit is the share of its quotes that verified.

## The cross-document join

- **`join_discrepancies`**: cross-document conflicts between verified values, Go's candidates and a
  judge's verdicts (lab-r4 r7, owner decision: whether two figures are one quantity is the agent's, never a
  rule in Go). `analysis.Candidates` proposes the pairs: two values the figure reader read clearly as one
  kind in one currency (money is never compared with a share count; durations and identifiers, kind
  other, are not compared), from two findings in two documents, their values different (dates: not
  compatible at their common precision); two values in two documents are asked once however many findings
  repeat them. Pairs already judged come first whatever the cap (a "yes" never falls out as findings
  arrive); then every value's three most alike, closest counterparts (`rankPairs`: tf-idf cosine of each
  value's entity, metric, reader labels and claim, plus a floor, times the closeness of the amounts or
  dates, so closeness orders pairs that share no word too; no exact label match; summed in a fixed order
  and rounded, so the same input ranks the same way on every call: `TestRankPairsDeterministic`), then
  the rest; the cap is the judge's budget (`CandidateLimit`: 20 pairs a funded call), the rest counted
  (lab-r4 r8 M1, r9: on bench6's ledgers, under the stand-in's, reworded, sparse or synonym labels,
  every true find ranks within the first 94 of 13,037 SEC pairs and 8 of 1,022 trust pairs). A join
  that leaves figures with none of their counterparts asked says how many in Limitations (at the
  default finish budget, 73 of bench6 SEC's figures). The engine's `judge_figures` subagent (critic role, its tool
  `figure_context`; `internal/engine/judge.go`) gets both values' quotes, claims, the analyst's entity and
  metric, the reader's labels (party, counterparty, measure, period, typed entity) and the lead's aliases,
  and answers per pair `yes`, `no` or `unclear` with a reason quoting both passages; each side's figure is
  marked ⟦…⟧ in its quote (the span admission matched its value to, `Reading.Stated`) and given as written
  (`valueText`), and a yes is only for two figures that claim to measure one quantity of one party and
  period and differ (bench7: all four of its yeses were false). Pairs whose amounts are one amount at
  another precision or sign ("$45,000.00", "-45000", "$45,000") are never proposed. Go checks every
  verdict (`CheckVerdicts`: one for a pair it did not propose is rejected, so a "yes" on non-spans or on
  two kinds asserts nothing; a second verdict for one pair or an unknown answer is unclear, and so is a
  yes or no whose reason does not repeat, for each side, four words in a row of that side's quote or
  claim that the other side's texts do not hold: r8 L1, r9 L1; and a yes whose reason does not write both
  figures beside its quotations, by amount ("$103,800" names "103,800.00"), its quotations the runs a side's
  passages hold with four or more words besides the figures' own, whatever quote mark opens them or
  none, so neither a passage's own quote marks nor single or missing quotes turn its figure into the
  reason's words: bench7, `TestBench7FalseYesesAreNotConflicts`, `TestNamesValuesReadsQuotationsByPassage`,
  `TestNamesValuesQuotationsWhateverOpensThem`,
  `TestBench7TrueTargetsReachAYes`). Only "yes" is a conflict (with its reason); "unclear" is a low-confidence Limitations line
  naming the pair; pairs the cap or an unfunded judge left out are counted in a Limitations line
  (`TestJoinAssertsOnlyTheJudgesYes`, `TestJoinRejectsVerdictsGoDidNotPropose`, `TestJoinScenarios`,
  `TestJoinLabelsAreContextNotKeys`, `TestJoinBenchLedgers`). The judge is called like the reader:
  batched (20 pairs, 32 KB a call), on one bandit-picked model, metered and journaled through `Invoke` out
  of its caller's grant (`join_discrepancies`' own 16 turns; finish judges first from a quarter of its
  grant's calls and USD, after its reads and beside the drafting share's keep, at least a quarter of 4 calls when the root is below that keep: `TestFinishJudgeHasAFloor`; the coverage pass from a quarter of its share: r8 L3), and its yes and no answers
  are cached across a matter's runs (`figure_reads`, keyed by the judge's spec hash, the model, the matter
  (a task with no matter number: the task alone), the task's documents, the aliases naming either side
  and the pair's content: `TestJoinVerdictsCachedAcrossRuns`, `TestNoMatterTasksDoNotShareVerdicts`,
  `TestVerdictKeysScopeAliasesAndDocuments`); every verdict is kept in the task's ledger (kind `verdict`,
  keyed by the two values in their two documents and the aliases naming them), so `finish` and the
  coverage pass read the verdicts the tool got. The join runs over every
  verified finding; `fids` only choose which conflicts are reported (those holding a named finding), so a
  cross-issue pair is found from either side (`TestJoinDiscrepanciesFidsDoNotScopeTheJoin`), and the
  lead's aliases are kept for the run (ledger kind `alias`) and given to every judge call
  (`TestJoinAliasesReachFinish`). Its output also names the documents the sweep left partly unread
  (`unreadDocuments`).

## Limitations

  - The join asserts what the figure judge says (r7): its verdicts are a model's, checked for form
    (`CheckVerdicts`: a verdict only for a pair Go proposed, one per pair, a reason that quotes both
    passages), not for truth. A judge that calls two quantities one makes a conflict the memo is asked to state (its reason
    shown); "unclear" is disclosed as a low-confidence pair for the lawyer; pairs past the cap or that
    the budget could not judge are counted, not judged.
  - Worksheets (r7 H1, M1): an operand traces only as a cited value's ref, a traced worksheet's result
    in its unit, or a literal the cited quotes state as its unit says, by the figure reader's kind
    (money in its currency, a percentage, a plain number read as a count or a bare number or multiple,
    a date they state, a period read as a duration in that unit, or a day count that is the days between
    two dates they state); never a number read as other, an identifier (r8 H1: "Section 30" is no
    30-day period, "Section 12" no multiplier). A result traces a memo figure only of its own kind and
    currency (a period by its amount never). A period the record does not state (a court rule not among
    the cited quotes) leaves its deadline untraced: cite the rule. `product` takes any numbers with at
    most one amount of money, `ratio` two in at most one currency, `date_add` days, weeks, months or
    years (a month or year on from a day the target month lacks is its last day: January 31 + 1 month
    is February 29, 2024; r9 M1). A duration's unit and a multiple's sign must cite the words beside
    the span that state them (`unitFrom`, r9 H1), checked by position and shape only (`statesUnit`, r10
    L2): a unit's words start at the first word right after the digits (only spaces, ")" or "-"
    between, never "(": "Rule 12(b)" states nothing) and that word has two or more letters; a
    multiple's sign is one character right against the digits or a space away ("1.5x", "x 1.5"); no
    word list, so "of" in "Section 30 of" can be cited, and is caught where the words are compared or
    shown. A memo's period (read as a duration) traces only to a cited duration of that amount and
    unit whose words agree with the memo's as written (case, punctuation and a plural aside), a
    worksheet's period, or the days between two cited dates, never to a section number's digits (r9
    H2); a cited period whose words differ ("days" against "business days" or "of") makes it limited:
    listed first in the block below and its findings gated (r10 M1).
  - The Computed figures block (`computed.go`, r10, r11): one block at the end of Limitations lists,
    once each with the sections that state it, every figure a section states that traces only
    through a worksheet, whatever its operands (a ref's value is the figure reader's reading of it,
    its scale and kind; a literal is a cited reading) and whatever form the figure is written in (G8
    decides: each paragraph is traced a second time without the worksheets, and a figure that then
    fails is listed with the worksheet it traces through alone; a `{W…}` handle's worksheet is listed
    too), and every memo figure that traces only by a reader's label (a period whose words differ
    from the record's, an identifier with a cited quantity's digits, r10 L5). A worksheet result the
    memo writes at a scale ("$228.5 million") is that worksheet's item ("written in the memo as …"),
    not a separate scale line (r11 L5). Each input is written as the memo writes figures with its
    finding and a short quote, cut at a space, comma or pipe, a table or CSV row's from its first
    cell, a date's wherever the quote writes it ("$212,500,000 [F1] “…purchase price of
    $212,500,000”"). Items resting on a unit, multiple or scale the figure reader read from words, on
    a count or number's kind alone (no unit words: it may be a section number), or on the days
    between two cited dates come first ("Check first"); ⚠ marks, at the top, an input whose cited
    words are not its unit, a count used as a period (by its unit, or with none as `date_add`'s or
    `business_days`' days, r12 M1), or a result over inputs read at a scale that the memo writes out
    digit for digit, by its `{W…}` handle, or at a scale from other words ("$8 million" over "$5
    billion" read ×10⁶), and gates the findings it rests on, as a memo figure's would be (r11 H, L1,
    r12 L1); a memo scale whose words agree with every input's ("3,120 million" over "2810, EUR
    million") is listed, not gated. A ref's quote is the span admission matched its value to
    (`Reading.Stated`). Arithmetic over money, percentages and dates the quotes write out follows
    ("Arithmetic"). `numbers` keeps a value's "business days" (`date_add` refuses it; `business_days`
    counts it). So every figure that one wrong unit, scale or kind label could change is shown to the
    lawyer with the words it rests on; whether the label is right is the lawyer's check, since Go
    never decides what words mean.
  - A figure written at one scale beside a cited one at another (the memo's "$5M" for a cited
    "$5,000,000", or its "$5,000,000" for a cited "$5 million", or "$5 per share" scaled from "per
    share") is limited, not traced: disclosed in Limitations and its citing findings gated for a
    lawyer (r7 M4, L2). A scale's words must be adjacent to the figure (right after it or after its
    currency code, earlier in its sentence, or its passage's first line when that is a header, no
    sentence and no figure of its own the words scale, over a table row: a line split by as many tabs,
    "|" or commas (two or more, neither line a sentence) as a row next to it; r8 M2, r9 L3),
    with a letter or a "000" ("($000)"; a bare "$" is none: r7 M2, L1). A caption over prose ("Amounts
    in millions" above a sentence) is no header: such a figure is unclear, gated.
