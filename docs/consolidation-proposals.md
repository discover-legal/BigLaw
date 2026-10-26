# Consolidation proposals (for the owner's decision)

This is an audit of v2 at 29def72 for **design-level** normalisation and consolidation. It is not
a list of line-level tidy-ups: the refactor pass in PR #59 looked for those and found little to
cut (−19 Go lines).

**Status (branch `v2-consolidate`).** Done, with no behaviour change: A1 (`labelModels`,
`cachedAnswer`, `labelCall`/`labelFailed`, `storeRead` in `figures.go`), A2 (`goContract` in
`register.go`), A3's merge of `candidates`/`skillCandidates` (`candidateLine`; not the single
disclosures builder), A4 (`agent.Fence`, `ToolNameRe`, `ExampleRe`, `Btoi`), A5 (the named
fractions in one block in `policy.go`) and § C (the README restructure: `docs/documents.md`,
`figures.md`, `reference.md`, `history.md`). Not done: A6 (three of the five `Finding.Low` sites
were Go-hook source Manage showed, and the note wording differs by site) and A7 (it needs a
migration and a fallback for runs in flight across the upgrade, which keeps today's code). Part B
is the owner's. B3 is done (branch `v2-toolchecks`): the Go hook items and `hooks.go` are gone, and each
wrapped tool's `goContract` row declares its checks in plain words, shown on its Manage page.

Each item gives:
- **Unify**: what would be unified, and into what;
- **Saves**: an estimate of the Go production lines saved, from the current function sizes
  (`wc`, function bodies). These are estimates, not measurements;
- **At risk**: the behaviour or invariant at risk, and the tests that pin it (or would need to);
- **Effort**: S is under a day, M a few days, L a week or more;
- **Visible**: whether a user would see a change.

The items are split into **A. safe normalisation** (no behaviour change; a refactor PR may do it)
and **B. design consolidation** (it changes behaviour, removes a feature, or changes stored data:
the owner decides). Each part is ranked by value for the risk. § C proposes a README restructure.
§ D lists places where the docs and the code disagree today.

For scale: Go production code is 25,536 lines (`scripts/loc.py` at 29def72), of which
`internal/engine` is about 14,500.

---

## A. Safe normalisation (no behaviour change)

### A1. One batched, cached labeller for the figure reader and the figure judge
- **Unify.** `readFigures`/`readChunk`/`cachedReading`/`cacheReading`
  ([`figures.go`](../internal/engine/figures.go)) and `judge`/`judgeChunk`/`cachedVerdict`/`cacheVerdict`
  ([`judge.go`](../internal/engine/judge.go)) are one algorithm:
  1. pick one model per batch with the bandit;
  2. compute `agentHash`;
  3. look each item up in `figure_reads` under every candidate model;
  4. chunk what is left by count and bytes;
  5. `rt.Invoke` the subagent out of the caller's grant;
  6. check the answer;
  7. cache only complete or decided answers, scoped to matter and documents.

  The proposal is a generic `labeller[Item, Answer]` in a new `engine/labeller.go`, with the
  per-agent parts passed in: key, request, check, "complete" predicate, and how failures split.
  The reader's halving retry and its `figstats` counting become hooks of the generic code.
- **Saves.** About 80–110 lines. The two paths are about 360 lines today (readFigures 69, readChunk
  87, the cache 36, judge 76, judgeChunk 28, the cache 25, keys 20, plus counting).
- **At risk.** The cache keys must stay byte-identical, or every cached row becomes a miss. Pinned
  by `TestOverrideChangesCacheKeys`, `TestAgentHashCoversAllowedTools`,
  `TestR5CacheKeyAndScope`, `TestVerdictKeysScopeAliasesAndDocuments` and
  `TestR6AllUnclearAnswerIsNotCached`. Also at risk: the reader's smallest-first order and halving
  retry (`TestFailedReaderBatchSplitInHalves`, `TestReaderCallSpansBounded`), and the judge's
  "stop when the grant cannot fund another call" (`TestFinishJudgeHasAFloor`). Add one table test
  that runs both agents through the generic path with `model.Fake` and compares their keys with
  today's.
- **Effort.** M. **Visible.** No.

### A2. One table of what Go needs from each tool name
- **Unify.** At least 15 hand-kept name lists encode one fact each about a tool or subagent:
  `MemoVisible`, `goCallers`, `goWritten`, `goInputs`, `passes`, `reservedNames`,
  `ledgerWriters`, `hookedTools`, `forcedSteps`, `subagentSafe`, `unhooked()`, `critics`,
  `goTurns()`, the `hooked` name switch in `Engine.runtime`, `hookDefs`, and the bandit's `Pinned`.
  They sit in `register.go`, `custom.go`, `manage.go`, `manage_validate.go`, `hookrules.go`,
  `committee.go`, `hooks.go` and `agent/bandit.go`. The proposal is one table, `goContract`, keyed
  by name, with fields for each fact:
  - lead-visible, and its position;
  - Go calls it (and from where);
  - Go writes its input (and the literal inputs);
  - it writes the ledger;
  - it is hooked (`after_call` runs on the answer);
  - it is a forced step;
  - its lead-only ops are safe in a subagent;
  - it is unhooked;
  - its reader turns.

  Each current list becomes a query on that table.
- **Saves.** About 30–50 lines, and one place to look. The bigger gain is that adding a tool can no
  longer leave a list behind. Several review findings were exactly that: Manage r1 M1 (`finish` in
  a subagent's list), Manage tools r1 L1 and L2 (unreserved names, lead-only tools), and hooks r1
  LOW (a check agent offered to the lead).
- **At risk.** Each list's exact membership; `goDepends`'s reasons, which are user-visible text,
  must be unchanged. Pinned by `TestCatalogueAndHooks`, `TestManageValidationRefuses`,
  `TestEnumRenameRefusedEndToEnd`, `TestSoloFrozen`, and the hookrules validation tests. Add one
  test asserting the table reproduces every current list.
- **Effort.** S–M. **Visible.** No.

### A3. One disclosure path for Limitations lines
- **Unify.** Limitations lines are produced five ways today:
  1. ledger `note` rows written during the run (plan fallback, sweep, sweep-skipped,
     custom-tools-off, `hook:<rule>`);
  2. lines `finish.check` computes from ledger state (23 `rep.limits = append` sites, plus helpers
     `partyLine`, `figureLine`, `unadmittedLine`, `unreviewedFixes`, `hookQuarantined`,
     `leadOnlyLine`, `staleLine` and `joined.lines`);
  3. lines `render` adds (candidate packs and skills, external authorities, dropped sentences,
     flagged lint: 6 sites);
  4. lines the lead passes in `finish{limitations}`;
  5. the Computed figures block.

  The proposal is one `disclosures` builder (`engine/disclose.go`). Each source registers a
  `func(ctx, taskID) ([]string, error)` with a fixed rank; `check` and `render` both call the one
  builder, and the ledger `note` kind keeps only the lines that must be written at run time.
  `candidates` (packs) and `skillCandidates` (skills) are the same 12-line function over different
  types and fold into one.
- **Saves.** About 40–60 lines, plus one ordering contract.
- **At risk.** The *order* and the exact wording of the lines, both user-visible in the memo, and
  the rule that `check` and `submit` report the same lines (`TestFinishCheckIsDryRun`,
  `TestFinishSubmitRunsSameChecks`). Add a golden test of one scripted run's Limitations section
  before refactoring, so the order cannot drift silently.
- **Effort.** M. **Visible.** No, provided the golden order is kept.

### A4. Shared text helpers for exact duplicates
- **Unify.** Exact duplicates:
  - the fence replacer: `docs/register.go` `fence` = `engine/deviation.go` `fenceText`;
  - the tool-name regex: `agent/registry.go` `toolNameRe` = `engine/custom.go` `toolNameRe`;
  - the example-line regex: `agent/registry.go` `exampleRe` = `engine/manage_validate.go`
    `exampleLine`;
  - `btoi`: `docs/chunk.go` = `engine/packs.go`.

  `agent` is the one package both `docs` and `engine` import, so export these from `agent` (for
  example `agent.Fence`, `agent.ToolNameRe`, `agent.ExampleLines`).
- **Saves.** About 10 lines, and one place each.
- **At risk.** None if they stay byte-identical. `TestRegistryToolCap`,
  `TestManageValidationRefuses` and `TestDevParaphrasedRequirementRefused` (fenced passages) pin
  them.
- **Effort.** S. **Visible.** No.

### A5. One budget-fraction table
- **Unify.** `Allocate` holds the class shares. Other fractions are defined beside their users:
  - `judgeShare` 0.25 and `judgeFloor` 4 (`judge.go`, `finish.go`);
  - `placeShare` 0.4 (`cover.go`);
  - the critics' 2/5, 2/5, 1/5 split (`Allocate`);
  - a sweep visit's quarter of the sweep's USD (`sweep.go`);
  - "half of what is free" for the coverage pass beyond the cap (`cover.go`);
  - `readerReserve` (`figures.go`);
  - the Test call's 8 calls and $0.25 (`custom.go`).

  Move them into `Allocation` (or a `fractions` block in `policy.go`) with the bench numbers that
  size them, so one doc comment explains the whole budget.
- **Saves.** About 0 lines; this is clarity only.
- **At risk.** The arithmetic. Pinned by `TestAllocationsMatchSpec`, `TestPhaseSharesHoldInUSD`,
  `TestCoverageShareKept`, `TestCoverStepGrantIsItsShare`, `TestFinishJudgeHasAFloor` and
  `TestSECShapedRunAnalysesAndPlacesWithin11`.
- **Effort.** S. **Visible.** No.

### A6. One "mark low" helper
- **Unify.** `Finding.Low` is set in five places (`applyReading`, `deviations` twice,
  `finish.check` from the lint gate map, and a `finding_admitted` approve), each writing its own
  `ValueNotes` text in a different shape. A `markLow(f, reason)` helper would give every gated
  finding one shape of note, which the finding view and the committee's admission line then read.
- **Saves.** About 10 lines.
- **At risk.** The note wording, which the finding view shows (`TestR4V4UnclearReadingGates`,
  `TestSectionCheckUnclearOrCutIsLow`).
- **Effort.** S. **Visible.** Only if the wording is changed; keep it.

### A7. Record each step's class at commit
- **Unify.** `classSpend` reads all of `task_steps` grouped by tool on every lead call, then
  resolves each tool's class through the current spec set. It then subtracts two ledger
  side-records, `figread` (drafted sections' reader calls) and `figstats` (the sweep reader's
  spend), to undo spend the class should not carry. So class spend is computed from two shapes of
  data. The proposal: a `class` column on `task_steps` and a per-step `classUsage` written at
  commit (the reader's spend recorded as its own class at the moment it happens), so `classSpend`
  becomes one `GROUP BY class`.
- **Saves.** About 20–30 lines, and the ledger kind `figread`.
- **At risk.** The shares (`TestPhaseSharesHoldInUSD`, `TestR6CoverReaderCallsLeaveTheDraftShare`,
  `TestSweepShareBounded`) and resume. This needs a migration; old rows would need a backfill, or
  a fallback for steps with no class.
- **Effort.** M. **Visible.** No.

---

## B. Design consolidation (changes behaviour or removes a feature: owner decision)

### B1. Make the section check, the paraphrase judge and the meta note registered subagents
- **Unify.** Four model calls bypass the subagent machinery and parse their answers by hand:
  - `checkSection` (`deviation.go`, `rt.ChatWithin` plus `checkAnswer`, about 40 lines of strict
    parsing);
  - `verify_quote`'s judge and ensemble (`docs/register.go`, its own `judgePrompt` constant,
    which is not editable in Manage);
  - `finish.meta`, the note to the lawyer;
  - the two vision prompts (`docs/vision_prompts.txt`, also not editable).

  Today `section_check` and `meta` are editable *prompts* but not *agents*: no spec, no output
  schema, no bandit arm, no per-agent usage in Manage. As subagent specs, they would get
  schema-checked answers and one repair turn (`agent.answer`), the bandit, Manage's usage counts
  and a Test call. `checkAnswer`'s strict reading would become an output schema with an enum.
- **Saves.** About 40–70 lines (`checkAnswer`, the hand-built requests and some usage plumbing).
- **At risk.**
  - `checkAnswer` accepts only one strict JSON object, but tolerates a trailing "." and any case
    (`TestCheckAnswerStrict`). The schema path's lenient JSON extraction (fences, prose around it)
    is *more* permissive: decide whether that is acceptable.
  - The judge must stay inside the caller's grant (`TestSectionCheckChargedWithinGrant`, the
    ladder's caps).
  - The judge's prompt would become editable, which changes the trust model of G2. An admin could
    weaken the ladder. Possibly keep it view-only.
- **Effort.** M. **Visible.** Yes: new Manage items, and slightly different refusal texts.

### B2. Packs become skills with an attach mode
- **Unify.** Packs (`packs/{jurisdiction,practice}/*.md`) and skills (`skills/<name>/SKILL.md`)
  are both front-matter Markdown with `status`/`reviewedBy`, both follow the lawyer-review rule,
  and both produce a "candidate" Limitations line. They differ only in delivery: a pack is
  attached to the brief and to `analyze_issue` by jurisdiction prefix; a skill is listed by name
  and read on demand. The proposal is one guidance type with
  `attach: brief | analyze_issue | on_demand` and a `select:` key (a jurisdiction prefix or a
  practice). Packs' checklists stay a front-matter list.
- **Saves.** About 80–120 lines: `parsePack`/`validatePack`/`selectPacks`/`candidates` against
  their skill twins, plus one Manage section and one UI editor variant (UI about 40 lines).
- **At risk.** `TestPackSelectionByJurisdictionPrefix`, `TestCandidatePackDisclosed`,
  `TestPackReviewRule`, the plan fallback to the checklist (`TestPlanFallbackToChecklistDisclosed`),
  the export's repo paths (`TestExportPatchApplies`) and `manage.json`.
- **Effort.** M–L. **Visible.** Yes: Manage's Packs and Skills sections merge, and the export paths
  move.

### B3. Drop Go hooks as Manage items; show "wrapped by Go" on the wrapped tool
- **Unify.** `hooks.go` embeds copies of `register.go`, `deviation.go` and `render.go` and cuts six
  hooks' source out with `go/parser`, so Manage can show them read-only as kind `hook`. The same
  information, which Go code wraps the tool, is already half-present as `goCallers`/`goDepends` on
  each tool's detail. The proposal: drop the `hook` items, and show each tool's Go wrapper (the
  `hookDefs` text and a link to its source on GitHub) in that tool's own detail. Configurable rules
  remain the only "hooks".
- **Saves.** About 120 Go lines (`hooks.go`), the embeds, and about 30–40 UI lines.
- **At risk.** `TestCatalogueAndHooks`, `manage.json` and the Hooks section of the Manage screen.
  Nothing in a run reads them, but rule validation does: `validateHookRule` refuses a rule named
  after a Go hook (it reads `hooks`), so that check must move or go.
- **Effort.** S–M. **Visible.** Yes: this removes a Manage feature, the view-only Go hook items.

### B4. One "typed verdict" mechanism for critics, check rules and the judge
- **Unify.** Four kinds of subagent return a verdict that Go then acts on:
  - committee critics: prosecute's supported/overstated/unsupported; reproduce's answers;
    audit_gaps' severity;
  - `check` hook agents: allow/block/annotate;
  - `judge_figures`: yes/no/unclear;
  - the section check: yes/no/unclear.

  Each has its own input shape, verdict enum, mapping to an outcome, and failure handling. One
  possible design: the committee's three critics become built-in `task_finish`/`after_call:draft_section`
  *check rules* whose verdict maps to a critic severity, so an admin can add a fourth critic in
  Manage without Go. A smaller step: share one `verdictOf(answer, enum)` reader and one failure
  policy.
- **Saves.** About 50–150 lines, depending on scope.
- **At risk.**
  - P6/P7 and the committee cap (`TestFinishRefusedWithoutCommittee`,
    `TestCommitteeRepeatsWhileFunded`, `TestBench6SECCommitteeCapped`);
  - delayed critic credit to the bandit (`TestBanditDelayedCriticCredit`);
  - context-free inputs (`TestCommitteeCriticsContextFree`);
  - the hooks' "no check checks a check" rule;
  - whether a rule's critic still runs when no admin rule exists.

  This is the largest semantic change here.
- **Effort.** L. **Visible.** Yes (Manage, the committee's output). Possibly also the memo, if
  critics' scopes change.

### B5. Declare hook rules, custom tools and servers with schemas
- **Unify.** `validateHookRule` (43 lines), `validateCustomTool` (43), `validateServer` (14) and
  `validateAuth` (14) check field types and enums by hand, then check cross-field rules. The field
  checks could be a JSON schema per file kind, in the agent subset and kept beside the specs,
  validated with `agent.Schema` (as `POST /tasks` is), leaving only the cross-field and
  set-reference rules in Go (`validateHookRefs`, the approve and after_call limits, origins).
- **Saves.** About 50–70 lines.
- **At risk.** The `problems[]` texts are user-visible in the editor and would change wording from
  "rule.x: event must be one of …" to the schema's "input.event must be one of: …". Pinned by
  `TestManageValidationRefuses`, the hookrules validation tests and `ui/test/manage.test.ts`.
- **Effort.** M. **Visible.** Yes (the wording of save problems).

### B6. One change-log table
- **Unify.** The change log is `spec_versions` (versions) UNION `manage_events` (secret set or
  deleted, Test calls), merged in Go (`Changes`, `versions`). The UI already renders non-version
  rows specially. Either add an `event` action to `spec_versions` with files `NULL` and version 0,
  or keep two tables and serve one view.
- **Saves.** About 15–25 lines.
- **At risk.** This needs a migration (forward-only); `TestManageStoreRoundTrip` and the change-log
  rendering.
- **Effort.** S–M. **Visible.** No, if the API shape is kept.

### B7. Store the deliverable once
- **Unify.** The rendered memo is written as ledger `note`/`deliverable` (inside finish's step),
  then copied by `finished` into `tasks.output`, which the API reads. Since `note` is also the kind
  for Limitations lines, `finish.check` has to skip the key `deliverable` when it reads them.
  Either write `tasks.output` in the step's transaction and drop the ledger copy, or give the memo
  its own ledger kind.
- **Saves.** About 10 lines, plus one special case.
- **At risk.** Resume between finish's commit and `finished` (`TestAwaitingGatePausesAndResumes`,
  `TestResumeAfterKillMidRun`); the API's `output`.
- **Effort.** S. **Visible.** No.

### B8. One date finder
- **Unify.** There are two date grammars:
  - `analysis.ParseDate` (`normalize.go`: one date string; full dates, months, bare years,
    hedges, e-mail headers);
  - `analysis.dateSpans`/`Literal` (`figures.go`: G8's dates, with continental `d.m.y`).

  They differ on purpose in places (G8 treats a bare year as a number span), but share most of
  their patterns. One finder with an option set would keep one regex family.
- **Saves.** About 30–50 lines.
- **At risk.** G8's date tracing and the join's date canons: `figures_test.go`'s literal corpus,
  the property and fuzz tests, `TestRawDeadlineNeedsItsRoll` and `TestDeadlinesMeetTheAnswerKey`.
  It changes behaviour wherever the two grammars disagree today, so it needs a corpus diff first.
- **Effort.** M. **Visible.** Possibly (G8 lint lines on edge-case dates).

### B9. Near-duplicate text predicates (semantic merges)
- **Unify.** Several helpers share a name or a purpose but differ in meaning:
  - **table rows**: `sweeporder.go` `tableRow` (three or more aligned cells, two of them figures),
    `analysis/figread.go` `tableRow` (a line split like its neighbour) and `docs/chunk.go`
    `isTableRow` (two or more tabs);
  - **sentence ends**: four definitions (`quality.go`, `requirement.go`, `analysis/figread.go`,
    `docs/verify.go`);
  - **folding**: `norm` in two packages with different semantics (one lower-cases), `fold` in four places (engine `sweeporder.go`, analysis `join.go`,
    docs `vision.go`, and `figtest`), `unitWords` and `scaleWords`;
  - **control and format character cleaning**: seven sites (`untrustedText`, the description and
    message validators, `store` names, section titles, the ladder's normaliser, `foldCf`);
  - **stop-word lists**: `sweepStop` and `deviation.go` `stopWord`.

  Name each concept once in a small shared helper file and make every differing variant an explicit
  option. **Do not merge semantics blindly**: each variant was tuned by a review round.
- **Saves.** About 40–80 lines.
- **At risk.** Sweep skips and order (`TestDataTableIsNotBoilerplate`,
  `TestR2VRealTablesNotBoilerplate`), header-scale reading (`TestR6ScaleWordsAndPlace`), requirement
  windows (`TestV4…`, `TestV5…`) and chunking (`TestTableRowChunking`).
- **Effort.** M. **Visible.** Only where a variant's behaviour changes.

### B10. Data-driven committee
- **Unify.** `committeeTool` hard-codes three critics, their inputs and their output handling.
  `committee.json` could list the critics with their input builder by name (claims, questions, or
  plan with headings and checklist) and their verdict mapping, so a fourth critic is a spec
  change. This is B4's small version.
- **Saves.** About 0–20 lines; it is flexibility, not size.
- **At risk.** As in B4, for the committee only.
- **Effort.** M. **Visible.** Yes (Manage).

### Overlaps judged and *not* worth consolidating
- **Ledger `verdict` versus the `figure_reads` cache.** These are two shapes of one verdict, on
  purpose: a per-task record (it holds unclear verdicts and the run's aliases) and a cross-run cache
  (yes and no only, deleted with the documents). Document the split; do not merge.
- **Go hooks versus configurable rules at run time.** The Go hooks (`hooked`, `sheets`,
  `aliasJoin`) carry G2, G3 and G8. They must stay compiled code. Only their Manage *listing* is
  optional (B3).
- **Sweep, committee and cover as fan-outs.** They size their children's grants, order admission
  and journal as one step. As plain subagents they would lose those guarantees.

---

## C. Proposed README restructure

The README is 2,593 lines (246 KB). It grew review round by review round and now mixes three
things: the operator's contract (configuration, running it, exposure, the database), the design
(already partly duplicated in code comments, and now in `docs/`), and history (per-PR named-test
lists and line-budget histories). Proposal: keep the README as the contract an operator and a
reviewer need. Move design into `docs/`, and move history to PR notes or a `docs/history.md`.

| Section today | Lines | Proposal |
|---|---:|---|
| What exists today (the table, plus the preamble) | 36 + 11 | Keep; trim the heading's PR list |
| Configuration | 70 | Keep (`TestEnvVarTablesAgree` reads its table). Not touched here: sign-in (PR 8) owns it |
| Model client | 113 | Keep the contract (requests, failures table, pricing rule, `model.Fake`). Move the retry and caching details into `docs/architecture.md` § model |
| Exposure | 45 | Keep. Sign-in (PR 8) owns it |
| Run it, Database | 67 | Keep |
| Documents | 449 | Keep the contract: types, the scope rule, limits, error codes, the routes. Move the section-tree bounds, the search cost measurements, the vision readings' rules and the memory measurements into a new `docs/documents.md` |
| Agent runtime and memo tools | 175 | Move to `docs/architecture.md` (runtime) and `docs/patterns.md` (paging, budget). Keep a short list of tools and their caps |
| Memo workflow, durable runs and the REST API | 433 | Keep the REST route table and the status and error mapping. Move the lifecycle into `docs/architecture.md` § 2 (already written) and the figure rules into a new `docs/figures.md` |
| Manage | 271 | Keep who may edit, the routes, the export, and the remote-call rules (they are security contract). Move validation and compatibility details into `docs/architecture.md` § 4 |
| Files | 71 | Keep the bounds; move the memory measurements to `docs/documents.md` |
| UI | 85 | Keep |
| Test it | 173 | Keep the commands (about 20 lines). Move the "Named tests (PLAN.md PR n)" lists (about 150 lines) to `docs/history.md`; the tests themselves are the record |
| Budgets | 23 | Keep (its line counts now point to `scripts/loc.py`, § D) |
| Limitations | 570 | Keep the limitations a user must know (about 200 lines). Move the per-PR "Line budget" paragraphs (about 100 lines, obsolete since owner decision 17) to `docs/history.md`. Fold the LAB round 3 and round 4 design notes into `docs/figures.md` |

Expected effect: a README of about 1,000–1,200 lines, with no loss of information. The
`TestEnvVarTablesAgree` and `TestGoVersionPinnedEverywhere` anchors (the § Configuration table and
the Go version string) stay in the README. This PR adds only an "Architecture and patterns"
pointer section, and the restructure is the owner's call.

---

## D. Where the docs and the code disagree today

Found while writing `docs/`, at 29def72. Items 1–9 and 11 are fixed in this PR (README and
CLAUDE.md text, code comments, and the no-UI notice's text, whose test pins only the substring
"make build", which it keeps). Item 10 is a constant in a test's code, not a comment, so it is left
as it is and recorded here.

1. **Registry caps.** README § Agent runtime says the registry refuses "a 23rd tool" and that "at
   most 14 tools are visible to one agent". The code says 48 and 24 (`agent.MaxTools`,
   `MaxVisible`), as README § Manage and CLAUDE.md also say. Manage tools r1 L7 flagged the
   mismatch; it is still there.
2. **Budgets table.** README § Budgets and CLAUDE.md say 23,667 Go production lines, and README
   says 2,638 UI lines. `scripts/loc.py` at 29def72 reports 25,536 and 2,791.
3. **The `audit_gaps` input.** README § Memo workflow says "`audit_gaps` gets the draft". The code
   sends it the plan, the drafted headings and the checklist (`committee.go`), and its prompt says
   "reviewing a memo's plan and headings (not its text)".
4. **G7's scope.** README § Gates says a finding gets its own gate when it rests only on
   low-confidence transcript pages. `finish.gates` also gates every cited finding that is `Low`
   (unclear readings, unconfirmed deviations, limited figures) and every one with an open HIGH
   prosecute finding. Other README paragraphs mention some of these, but the Gates paragraph is
   incomplete.
5. **The coverage pass share.** README § Limitations (LAB improvements) says the pass runs "within
   its 6% share". `Allocate` gives it 10% (`Cover: calls * 10 / 100`), as README § Policy says.
6. **"No model has run it."** README § Limitations (Memo workflow) says every test scripts the
   model and no model has run it, and § Model client says no real endpoint has been exercised. The
   README elsewhere reports real bench and LAB runs (bench5–7, "the real Haiku LAB rerun"), and the
   code comments cite them.
7. **The `make build` note.** README § Run it says `make build` gives a binary with "no UI yet".
   `make build` embeds `ui/dist` when it exists. The notice that `internal/api/api.go` serves at
   `/` says "Build it with `make build` (Node 22: it builds `ui/` and embeds it), or use the REST
   API and MCP directly". `make build` does not build the UI (`make ui build` does), and MCP is not
   built.
8. **The requirement window, in comments.** The file comment of `internal/engine/requirement.go`
   says a requirement is read "in its section … the leaf section of the document's own section tree
   … never a guess at layout", and `reqText`'s comment in `deviation.go` speaks of "the section that
   holds it". Since lab-r3 r4 (coordinator decision), `contextOf` reads a bounded byte window and
   uses the tree only as a label. Fixed for those two comments. **Still stale (not fixed here):**
   in `deviations`, the dropped-verdict reason string (model-visible, so it waits for a code PR)
   says "the section that holds the requirement, as the subagent was sent it", and the `shown`
   comment beside it says "the requirement's section and heading path".
9. **The near join, in a comment.** The `claimSums` comment in `figures.go` says "the near join
   does" compare untyped sums. The near join was deleted in lab-r4 r7 (CLAUDE.md).
10. **A stale test constant (left as it is).** `TestRegistryToolCap` (`boundaries_test.go`) still
    plans the memo lead at 11 visible tools (`"memo": 11`). `MemoVisible` has 12 since `use_skill`.
    This is harmless, because the test only checks the planned lists against `agent.MaxVisible`
    (24). Update the constant the next time that test is touched.
11. **The G-code table is not in the repo.** The repo README uses G1–G11 and P1–P9 throughout,
    but their definitions live only in the planning contract outside the repo. `docs/glossary.md`
    now carries them.
