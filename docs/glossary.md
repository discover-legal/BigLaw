# BigLaw 2 glossary

The project's own words, each in a line or two, with the code that defines it. The system is
described in [architecture.md](architecture.md), and the design reasons in [patterns.md](patterns.md).

## Evidence

- **Matter**: a client matter. Documents carry a matter number. A task reads only the documents
  it names (its *scope*), and caches are scoped by matter.
- **Scope**: the task's `documentIds`. Every `docs` read takes it, and a document outside it is
  "not found" (G1).
- **Finding**: a claim with its quotes, typed values, entities and relations (`analysis.Finding`),
  plus Go's checks: citations, `Verified`, `Low`, `ValueNotes`, `HookBlocked`, `Figures`,
  `Unread` (`engine.Finding`, [`ledger.go`](../internal/engine/ledger.go)). Its id is `F<n>`.
- **Verified / quarantined**: a finding is verified when *every* quote verifies (G2). Otherwise it
  is quarantined, never drafted from, and counted in the Evidence Note (G3).
- **Usable**: verified, not hook-quarantined, and not rejected by a lawyer at a gate (`usable`,
  `Finding.ok`).
- **Citation**: one quote's verification result: method, confidence, the match and its section
  (`engine.Citation`).
- **Ladder**: quote verification, in order: `exact` (1.0), `tolerant` (0.95, or 0.9 for a table
  row's cells), `judge` (≤ 0.8), `ensemble` (3 votes), else `none`. Transcript matches get
  `@transcript` and are capped at 0.85 (`docs.VerifyQuote`, [`verify.go`](../internal/docs/verify.go)).
- **Value**: one typed figure of a finding: entity, metric, value, unit, date, class, and `Fig`
  (its reading) (`analysis.Value`). Its ref is `F3.v1`.
- **Span**: one number as written in a text: its offsets, digits and separators
  (`analysis.Span`, [`figures.go`](../internal/analysis/figures.go)).
- **Label**: the figure reader's answer for one span id: kind, currency, scale and its source
  words, decimal mark, party, period, and so on (`analysis.Label`).
- **Reading**: a label after Go's checks (`ReadLabels`): kind, currency, an exact signed `Amount`,
  and whether it is *clear* (`analysis.Reading`, [`figread.go`](../internal/analysis/figread.go)).
- **Unclear**: what Go could not check, or what the reader said it could not read. An unclear
  reading is never compared as a quantity. An unclear match admits its finding as `Low`.
- **Low (low confidence)**: `Finding.Low` or `Citation.Low`. The finding gets its own gate at
  finish.
- **Canon**: a value's comparable form ("usd:9000000", "date:2024-04-15"), built from its reading
  (`analysis.Canon`).
- **Handle / codename**: `{Zephyr}`, a neutral name for one verified figure. The drafter writes the
  handle, and Go substitutes the exact value outside quotations (`analysis.Handles`, `Substitute`).
  At most 64.
- **Worksheet**: a `numbers{op:"calc"}` result (sum, diff, ratio, pct, product, date_add,
  date_diff, business_days) with its steps. It is cited as `{W…}` by its wid
  (`analysis.Worksheet`).
- **Locator**: a rule, section or clause number ("Section 14.2", "Rule 6(a)(1)(C)"). It is not a
  figure: G8 and the reader mask it (`analysis.MaskLocators`).
- **Requirement / context window**: `check_deviation`'s controlling words, verified verbatim in
  `requirementDocId`, then read in about 3 KB of text on each side, 8 KB at most (`contextOf`). The
  window is *truncated* when it cuts what governs the requirement.
- **Section check**: one critic call asking whether the controlling document imposes the
  requirement as quoted. It answers yes, no or unclear (`checkSection`).
- **Candidate pair / conflict / verdict**: Go proposes pairs of clearly read values that could be
  one quantity stated two ways (`analysis.Candidates`). `judge_figures` answers each pair yes, no or
  unclear. Only a yes is a *conflict*.
- **Alias**: another name the record uses for a party, supplied by the lead to
  `join_discrepancies` and kept for the run (ledger `alias`).
- **Transcript**: text a vision model read from an image or a scanned page. It keeps its
  provenance, is badged, and is capped at 0.85 (G11). *Mixed* pages keep their text layer as the
  truth for their own words.
- **BELO-lite**: the small ontology of classes and 11 predicates in `analysis/belo.json`. It types
  issues (Conduct, Requirement, Clause or RedFlag), entities and relations.

## Run

- **Task**: one submitted request. Its row holds its run: status, lease, `lost` grants, the
  `specs` snapshot and the `output` ([`runner.go`](../internal/engine/runner.go)).
- **Lead**: the depth-0 agent (role `lead`, prompt `specs/lead.md`) that drives a memo through
  tools.
- **Subagent**: a depth-1 agent with its own spec, prompt, allowed tools, turn cap and output
  schema. Its transcript is discarded. Subagents never call subagents.
- **Fan-out**: a Go tool that runs subagents itself (`sweep`, `committee`, `cover`).
- **Pass**: the engine's own Go tools whose specs are editable items: `sweep`, `committee`,
  `cover`, `finish`, `ledger`.
- **Role**: the model binding a call runs on: `lead`, `specialist`, `critic`, `vision`, `embed`
  (`model.Roles`). Specialist and critic fall back to the lead. Vision and embed never fall back.
- **Plan / issue**: the questions the deliverable must answer, each typed by a BELO-lite class
  (`ledger{op:"plan"}`). Issues are the memo's spine.
- **Sweep**: the evidence floor: `extract_facts` once on every page of every top-level section,
  most relevant first, with duplicates and boilerplate skipped
  ([`sweep.go`](../internal/engine/sweep.go)).
- **Visit**: one sweep unit: a page of a section, or a batch of short sections (`through`).
- **Admission**: Go's check of an evidence subagent's answer before anything is recorded. Two
  paths: `admit` (extract_facts, analyze_issue: unverified findings are quarantined and counted by
  G4) and `deviations` (check_deviation: a verdict that does not verify is dropped with a note, not
  quarantined). A change to admission must cover both.
- **Figure reader**: the `read_figures` subagent (critic role), which labels number spans.
- **Figure judge**: the `judge_figures` subagent, which decides candidate pairs.
- **Committee**: three context-free critics run in parallel: `prosecute` (claims against
  quotes), `reproduce` (answers the questions blind, then is compared with the draft) and
  `audit_gaps` (plan and headings against the checklist). A run gets at most 3 cycles.
- **Critic finding**: a committee finding, HIGH, MEDIUM or LOW, open until the lead resolves it as
  fixed or disclosed (`Critic`, `C<n>`).
- **Cycle**: one committee run, on one draft version.
- **Cover / coverage pass**: once per run, before the deliverable: analyses and redrafts
  *touched* issues, and places salient left-out findings and unstated conflicts
  ([`cover.go`](../internal/engine/cover.go)).
- **Touched**: a plan issue with no section citing a usable finding, or a Conduct or Requirement
  issue with no analysis.
- **Salient finding**: a deviation, a high-severity finding, a typed breach, a salient figure, or a
  claim that says a document departs from another (`salientFinding`).
- **Finish**: `check` (a dry run) or `submit` (lint, coverage, preconditions, render, gates). A
  *forced finish* runs from the reserve when the budget or step cap stops the lead.
- **Lint**: finish's integrity check of a section: G5 citations and G8 figures. Its lines tell the
  drafter how to fix each problem.
- **Limited**: a memo figure G8 cannot trace clearly but does not flag (a never-read quote, a scale
  on one side only, unit words that differ). It is disclosed, and its finding is gated.
- **Computed figures block**: the end of Limitations. It lists every memo figure that traces only
  through a worksheet or a reader's label, with each input and its quote
  ([`computed.go`](../internal/engine/computed.go)).
- **Limitations**: the memo section listing what the run could not do, check or reach (pattern 4).
- **Evidence Note**: the memo section counting verified, quarantined, lawyer-rejected and
  transcript-only findings, and any grounding alert.
- **Gate**: a human decision point (G7): `G-deliverable-N`, `G-<fid>` for a finding, or a hook
  gate. The run waits `awaiting_gate` until `Decide`.
- **Grounding collapse**: G4. From 8 findings, if the unverified share reaches
  `BIGLAW_COLLAPSE_THRESHOLD`, the task fails (`strict`) or carries an alert (`warn`).
- **Step**: one journaled lead turn, tool call or note in `task_steps`: `started`, then
  `committed`, `refused` or `failed`.
- **Journal / replay**: the step log, and the way a resumed run rebuilds its transcript from
  committed steps without repeating them.
- **Meter**: the in-step journal of model calls, which lets a hard crash charge only what was
  spent plus the in-flight worst cases.
- **Lease / heartbeat**: a process's 30 s claim on a running task, renewed every 10 s. A stale
  lease is taken over.
- **Lost**: the grants of steps in flight when a process died, charged at resume.
- **Ledger**: the task's JSON rows by kind and key, written with the step that made them.
- **Bandit**: the tool bandit (UCB1). It adds one hint line before a lead turn and picks a
  subagent's model. It never hides or refuses a tool.
- **Signature**: the bandit's task kind: workflow, practice, jurisdiction prefix, lead model, and a
  document-count bucket.

## Budget

- **Budget**: a task's calls and USD caps, as a tree of grants (`agent.Budget`).
- **Grant**: what one call may spend, carved from its caller's budget and closed when it returns.
- **Worst case (bound)**: a model call's held cost: request bytes as tokens, plus 1,000, as a
  cache write, plus `MaxTokens` of output.
- **Class**: a tool's budget class: `evidence`, `draft`, `committee`, `cover` or `free`. It decides
  its share and its level. (The registry also accepts `plan`, which no built-in spec uses; the plan
  share is P1's budget across all classes before a plan exists.)
- **Level**: evidence < work < review < final. A grant never takes what a higher level keeps.
- **Share**: the most a class may spend, in calls and USD (`Allocate`).
- **Keep**: what the root holds back for later phases: the reserve and meta for finish, one
  committee cycle and (until it has run) the coverage pass, and the drafting share.
- **Reserve / meta**: 5 calls kept for finish, and 3 for its note to the lawyer.

## Configuration and Manage

- **Spec**: a tool's or subagent's JSON definition (`agent.Spec`).
- **Strict-shaped**: every object closed, every property required, optional fields nullable.
- **Spec set**: the built-in files with one snapshot's overrides over them (`engine.Specs`).
- **Item**: one editable unit in Manage: `agent.`, `prompt.`, `pass.`, `tool.`, `server.`,
  `rule.`, `pack.` or `skill.` plus a name (`itemOf`).
- **Override / version**: an admin's saved copy of an item's files, as a row in `spec_versions`.
  `NULL` files mean the built-in again, and `{}` is a tombstone.
- **Snapshot**: the overrides in effect when a task was submitted (`tasks.specs`). The run uses it
  on every claim.
- **Compatibility (`compatSpec`)**: an override may not remove or retype a property Go reads or
  writes, change an enum, or add a property to a schema Go writes.
- **Pack**: jurisdiction or practice guidance with a checklist. It is selected by prefix (`US-NY`
  selects `us` and `us-ny`) and put in the lead's brief and in `analyze_issue`'s request
  ([`packs.go`](../internal/engine/packs.go)).
- **Skill**: a named bundle of process guidance. The brief lists only its name and description,
  and the lead reads the body or a file on demand with `use_skill`. A skill never grants a tool
  ([`skills.go`](../internal/engine/skills.go)).
- **Candidate / reviewed**: a pack's or skill's status. Any content change resets it to candidate
  unless the save names a new reviewer. A deliverable using a candidate says so.
- **Go checks**: engine code around a tool (`hooked`, `sheets`, `aliasJoin`, `finish`'s lint),
  declared as plain-English lines on its `goContract` row and shown read-only on its Manage page.
- **Rule (configurable hook)**: an admin's `{event, match, when, action, failure}` file. Events are
  `before_call`, `after_call`, `finding_admitted` and `task_finish`. Actions are `block`,
  `approve`, `check`, `webhook` and `limitation` ([`hookrules.go`](../internal/engine/hookrules.go)).
- **Check agent**: the subagent a `check` rule calls. Its answer holds `verdict` and `reason`, and
  it is never offered to the lead.
- **Hook quarantine**: `Finding.HookBlocked`. The finding is kept out of the memo, but it is not a
  verification failure, so G4 does not count it.
- **Custom tool**: an admin's MCP or HTTP tool (`specs/tools/`), or an MCP server
  (`specs/servers/`) ([`custom.go`](../internal/engine/custom.go)).
- **Remote policy**: https, ports 443 and 8443, public addresses checked at save and at dial time,
  no proxy, 30 s per call, 1 MB per answer. *Off* without a real admin
  ([`remote.go`](../internal/engine/remote.go)).
- **Secret / origin**: a value set by an admin and bound to one `https://host:port`, the only
  origin it is ever sent to. It is redacted from answers and errors.
- **Principal**: who makes a request (`api.Principal`). With sign-in it is the session's user (an
  admin when `BIGLAW_ADMINS` names them, else a member); without it every request is the local
  user, an admin or not depending on the serve flags.

## Codes

| Code | Meaning |
|---|---|
| G1 | Matter isolation: every read is within the task's documents |
| G2 | A finding verifies only when every quote passes the ladder |
| G3 | Unverified findings are quarantined: never drafted from or read by a subagent; counted |
| G4 | Grounding collapse fails (or alerts) a task whose unverified share is too high |
| G5 | Every `[F-id]` names a usable finding; external authorities are flagged |
| G6 | A model failure is a typed error with fixed text, never "no finding" |
| G7 | Human gates: the deliverable always; findings that are low-confidence or prosecuted |
| G8 | Every figure in the memo traces to a cited reading, a handle or a worksheet |
| G9 | Tool isolation: allowed tools only, no nesting, nested spend charged to the task |
| G10 | Durable runs: resume repeats no committed step and loses no write |
| G11 | Transcripts are marked, capped at 0.85, badged and counted |
| P1 | Plan first (only `retrieve`, `ledger`, `use_skill`, `finish` before one); checklist fallback |
| P2–P3 | A call must be offered to its agent; no subagent or fan-out at depth 1 |
| P4 | Class shares in calls and USD |
| P5 | A grant from the budget, never past what a higher level keeps |
| P6 | Finish needs a committee on the current draft, with HIGH and MEDIUM findings resolved while funded |
| P7 | Escalation: a submit right after a committee whose findings were closed without work is refused once per cycle |
| P8 | The sweep is idempotent |
| P9 | The bandit hints; it never hides or refuses a tool |
| E0–E4 | Eval gates: fixtures (E0), oracle run (E1), fabricator run (E2), scorer self-test (E3), hybrid recall (E4) |
| Gate A / B | Owner-run real-model gates: v1's criteria on two matters (A), and the bandit on versus off (B) |

## Ledger kinds

`issue`, `finding`, `critic`, `section`, `sheet`, `gate`, `visit`, `cycle`, `note`, `escalate`,
`handle`, `analysis`, `cover`, `swept`, `alias`, `figread`, `figstats`, `verdict`
([`ledger.go`](../internal/engine/ledger.go)), plus `skill` ([`skills.go`](../internal/engine/skills.go))
and `hookrun` ([`hookrules.go`](../internal/engine/hookrules.go)). The `note` kind also holds the
rendered memo, under the key `deliverable`.
