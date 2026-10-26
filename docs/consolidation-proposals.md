# Consolidation proposals (open items, for the owner's decision)

Design-level normalisation and consolidation still open after the audit of v2 at 29def72. The items
already done (A1, A2, A4, A5, the `candidateLine` part of A3, B3, the README restructure and the doc fixes) were removed from this page; git
history and their PR notes are the record.

Each item gives:
- **Unify**: what would be unified, and into what;
- **Saves**: an estimate of the Go production lines saved, from the function sizes at the audit
  (estimates, not measurements);
- **At risk**: the behaviour or invariant at risk, and the tests that pin it (or would need to);
- **Effort**: S is under a day, M a few days, L a week or more;
- **Visible**: whether a user would see a change.

**A. safe normalisation** has no behaviour change (a refactor PR may do it); **B. design
consolidation** changes behaviour, removes a feature, or changes stored data (the owner decides).
§ D lists the one place where the docs and the code are known to disagree.

---

## A. Safe normalisation (no behaviour change)

Status: not done. A6: three of the five `Finding.Low` sites were Go-hook source Manage used to show,
and the note wording differs by site. A7: it needs a migration and a fallback for runs in flight
across the upgrade.

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
  (Done already: `candidates` and `skillCandidates` folded into `candidateLine`.)
- **Saves.** About 40–60 lines, plus one ordering contract.
- **At risk.** The *order* and the exact wording of the lines, both user-visible in the memo, and
  the rule that `check` and `submit` report the same lines (`TestFinishCheckIsDryRun`,
  `TestFinishSubmitRunsSameChecks`). Add a golden test of one scripted run's Limitations section
  before refactoring, so the order cannot drift silently.
- **Effort.** M. **Visible.** No, provided the golden order is kept.

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
  `aliasJoin`) carry G2, G3 and G8. They must stay compiled code (B3 removed their Manage listing: each
  wrapped tool's page lists its checks).
- **Sweep, committee and cover as fan-outs.** They size their children's grants, order admission
  and journal as one step. As plain subagents they would lose those guarantees.


---

## D. Where the docs and the code disagree today

1. **The requirement window, in a model-visible string.** Since lab-r3 r4 (coordinator decision)
   `contextOf` reads a bounded byte window and uses the section tree only as a label. In
   `deviations` (`internal/engine/deviation.go`), the dropped-verdict reason string still says "the
   section that holds the requirement, as the subagent was sent it", and the `shown` comment beside
   it says "the requirement's section and heading path". The string is model-visible, so it waits
   for a code PR.
