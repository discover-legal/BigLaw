# Reference: the runtime, the memo workflow and Manage in depth

Design detail moved verbatim from the README at `v2` eb17567, by the README section it came from.
The README keeps the contract (configuration, exposure, sign-in, the routes, the status and error
mapping, the remote-call rules, what a user must know). Figures are in [figures.md](figures.md),
documents in [documents.md](documents.md). If this page and the code disagree, the code is right:
fix this page in the same change.

## Model client

- **Requests.** A request names its `Agent` (`lead` or the subagent tool's name), its `Role`
  (`lead`, `specialist`, `critic`, `vision`; resolved through `model.Roles`, the one accessor,
  fed by `BIGLAW_MODELS`) or an explicit `Model`, the messages, the tools, and optionally
  `tool_choice`, `parallel_tool_calls`, `max_tokens` (sent as `max_completion_tokens` to
  `api.openai.com`, which refuses `max_tokens` for reasoning models) and `temperature`. The same
  request encodes to identical bytes every call (a tool's JSON Schema is compacted, deterministically)
  and tools stay in the order given, so a fixed tool list keeps the provider's prompt-cache prefix
  stable; `Strict` sends `"strict": true` (Go validation stays authoritative, since local endpoints
  ignore it). A message's `Content` is a string, `nil` (an assistant turn with only tool calls) or,
  for a user message, `[]model.Part`: text, and images as base64 `data:image/(png|jpeg|webp|gif)`
  URLs only, so the endpoint never fetches a remote URL on BigLaw's behalf. A malformed request (no
  agent, a repeated or malformed tool name, a schema that is not a JSON object, content of any other
  type, a misplaced tool-call id) is refused before sending.
- **Answers.** Text and/or typed tool calls (`id`, `name`, `arguments` as JSON object text; an
  object sent by a non-conforming server is accepted, a missing id becomes `call_<n>`), a
  `Truncated` flag for `finish_reason: length`, and usage. A tool call whose name is malformed
  (`[A-Za-z0-9_-]{1,64}`), whose id repeats another's, or whose arguments are not a UTF-8 JSON
  object fails the call (`ErrUnavailable`); `model.Fake` applies the same check. A 200 the provider
  billed but BigLaw refuses (no choice, nothing usable, a bad tool call) still reports its usage
  and cost on the error (`(*model.Error).Usage`), so the USD budget can count it. Input tokens exclude cache reads
  (`prompt_tokens_details.cached_tokens`) and cache writes (`cache_creation_input_tokens`, sent
  by Anthropic-style gateways); both are assumed to be counted inside `prompt_tokens`, as OpenAI
  does. Redirects are not followed, since prompts carry matter data.
- **Pricing.** Every call is priced from the price table, whatever the endpoint's host: a local
  endpoint is not free and open weights are no reason for $0 either (owner decision; v1 priced
  local calls at $0). `internal/model/prices.json` is the built-in table, in the `BIGLAW_PRICES`
  format and read by the same parser: an object mapping a model id (case-insensitive; a trailing
  `*` makes it a prefix) to `{"in": USD, "out": USD}` per million tokens, both required and from 0
  to 10000, or to `null` for a model that is known but has no price. Repeated ids
  (case-insensitive), unknown fields inside an entry and trailing data are refused at startup.
  Errors never quote the value, an id, or the file's contents. A model id resolves in the
  operator's `BIGLAW_PRICES` table first, then the built-in one; in each, an exact id beats the
  longest prefix, and `vendor/model` (OpenRouter style) is also tried as `model`; a key without a
  `/` matches only that stripped `model`, so `deepseek/deepseek-r1` takes the `deepseek-r1*` row,
  not `deepseek*`. So `BIGLAW_PRICES='{"deepseek*": {"in": 0.3, "out": 1.2}}'` reprices the whole
  family, and `{"claude-haiku-4-5": null}` un-prices one model. (Under Compose a file path must
  exist inside the container; the inline JSON form is simpler.) The built-in table has 86
  entries: 79 of v1's 80 rows from `cost/cost.go` (v1's list prices as of early 2026, unverified
  here; v1's `{0, 0}` placeholders are `null`; its `llama*` placeholder is dropped, which leaves
  `llama*` ids unlisted: the same unknown cost), plus OpenAI's `o1*`, `o1-mini*`, `o1-pro*`, `o3-pro*`,
  `o3-deep-research*`, `o4-mini-deep-research*` and `chatgpt*` (list prices from memory,
  unverified). Open-weights families keep their hosted rates: `qwen2.5:14b` on Ollama is charged
  at the `qwen*` rate, `mistral-nemo` at `mistral*`'s. Cost = (input × in + output × out + cache
  writes × in × 1.25 + cache reads × in × 0.10) / 10⁶. A model with no row or a `null` row has an
  **unknown** cost (nil), never $0; the first call to each such model logs one warning. **A model
  you really run for free** is set to zero explicitly:
  `BIGLAW_PRICES='{"qwen2.5:14b": {"in": 0, "out": 0}}'` (or `{"qwen*": {"in": 0, "out": 0}}` for a
  family). `serve` logs each role's price once at startup (`model pricing role=lead model=…
  price="$3 in / $15 out per 1M tokens"`, or `price="unpriced: …"`).
- **Embeddings.** `Embed` sends up to 256 non-blank UTF-8 texts (each at most 16 KiB) in one
  OpenAI-compatible `POST <URL>/embeddings` (`"encoding_format": "float"`, never `dimensions`:
  vectors come at the model's own length) on the `embed` role's model, with the same retries,
  redirect rule and fixed-text typed failures as a chat call. It returns one vector per input in
  input order (the answer's `index` decides, not its order). An answer without exactly one vector
  per input, all of one length (at most 16,000, pgvector's limit) with a norm between 1e-6 and 1e6
  (zero, subnormal or huge values make pgvector's distances NaN), is `ErrUnavailable`; no vectors
  is `ErrNoContent`; either way a billed answer keeps its usage on the error. `model.Fake` applies
  the same vector checks. **It is priced like every call**: `prompt_tokens` (capped at the
  request's size in bytes, since a token is at least a byte) at the model's input rate from the
  same table (`text-embedding-3-small` $0.02, `-3-large` $0.13, `-ada-002` $0.10 per million
  tokens; there is no `text-embedding*` wildcard, so another vendor's model of that name is
  unpriced; `nomic-embed-text` has no row, so its cost is unknown, not $0, unless `BIGLAW_PRICES`
  sets it). An answer with no `usage` has an unknown cost (nil), never $0. Watt-hours are added
  when `BIGLAW_LOCAL_WATTS` is set (`TestEmbeddingsPriced`, `TestEmbedTypedErrors`). No `embed` role is 503
  `model_not_configured`.
- **Watt-hours.** With `BIGLAW_LOCAL_WATTS` set, every call (successful or billed-but-refused)
  records `BIGLAW_LOCAL_WATTS` × its duration (all attempts) as watt-hours, beside its price. They
  report the energy of your own hardware; they never change the cost. Unset, none are recorded.

## Agent runtime and memo tools

The memo workflow (§ Memo workflow) drives these. What PR 4a ships, all tested with `model.Fake` or no model:

- **Tools and the registry.** A `Tool` has a `Spec` (name, kind `go`/`subagent`/`fanout`, a
  description, input and output JSON Schemas, a budget class, a turn cap, allowed tools for a
  subagent, ops that must be called alone, `Final` for `finish`) and `Call(ctx, Call) (Result,
  error)`; a `Call` carries the task's document ids (G1), the input, a grant and the depth. The
  registry refuses a duplicate name, a 49th tool (`agent.MaxTools`, 48), a schema that is not strict-shaped (every object
  `additionalProperties: false`, every field required, optional fields nullable; in and out), a
  description under three sentences or without a "Do not use" line, and 1–3 `Example: {…}` lines
  that do not validate. Definitions are frozen when registered: the same bytes reach the model on
  every turn (prompt cache), and no matter data can reach a schema. The definition sent leaves out
  `minLength`/`maxLength` (strict mode refuses them); Go enforces them before a tool runs. At most 24 tools are visible to
  one agent (`agent.MaxVisible`; both caps raised from 22/14 for custom agents and tools, § Manage). Specs are data: `internal/docs/specs/*.json`, `internal/analysis/specs/*.json`.
- **One loop, two depths** (`Runtime.Run`). The lead (depth 0) and a subagent (depth 1, the
  `Subagent` tool: its own prompt, allowed tools, turn cap, grant and output schema; its transcript
  is discarded) run the same loop. Every tool call goes through `Runtime.Invoke`, in this order:
  offered to that agent, no subagent or fan-out tool at depth 1, alone when it must be (`finish`,
  `ledger{op:"plan"}`), input schema, the task's `Policy` (PR 4b), then a grant; the tool's usage is
  charged even when it fails and its output must match its schema. A subagent's answer must validate
  (one repair request, then `ErrToolOutput`). At most 8 tool calls run per turn. Tool results older
  than four turns are replaced by a handle naming their step. **Tool outputs are bounded by type and
  never trimmed** (owner decision, lab-r3 r4: a byte cut of JSON panicked on escaped text and showed a
  part as if whole): every tool output that holds a list or long text is one page of at most
  `agent.PageBytes` (12 KB, under the 16 KB retry bound, so a page always reaches the model whole),
  filled by `agent.Page` (`Take` keeps the leading items that fit, at least one; `TakeText` a page of
  text), and says how to get the rest in its schema: `next` (an offset: `retrieve` search, outline and
  section; `ledger` findings, relations, critics and `draft_get`) or `truncated: true` with a narrower
  call (`numbers` handles and table, whole rows; `join_discrepancies`; `finish`'s lists, by priority;
  `committee`, the rest from `ledger` op `critics`; `sweep`, the findings in the ledger and the uncovered
  pages for a later sweep). The other outputs are bounded by their input or answer: `verify_quote` (one
  match), `cover` (≤ 40 issue ids), a subagent's answer (its schema's `maxItems`/`maxLength` and its
  8,192-token answer cap) (`TestRetrievePagesByType`, `TestLedgerPagesByType`, `TestReportsPageByType`,
  `TestAnalysisToolsPageByType`). The runtime's last guard refuses rather than trims: a result over
  100 KB reaches the model as a typed refusal, `{"error":{"code":"result_too_large","message":…,"tool",
  "bytes","limit","next"}}` (`agent.ErrorSchema`, `TooLarge`; `TestOversizedResultRefusedAndShrunk`,
  `TestV4EscapedResultsNeverPanic`); an endpoint's 400/413/422 compacts all but the latest turn's and
  retries, then replaces a latest result over 16 KB by the same refusal and retries once more, for that
  request only (`TestR2VShrinkSticksAndHidesNext`). A tool that panics ends its run with `ErrPanic` (its
  stack logged; its step keeps the usage its meter recorded: `TestPanickedStepKeepsItsUsage`), and the engine fails that task as an internal error; the task goroutine recovers too,
  so a panic never stops `serve` (`TestPanickingToolFailsOnlyItsRun`). Text in tool results is labelled untrusted matter data.
  The model sees refusals (reason and next step) and tool failures in BigLaw's own words only; any
  other error is logged and shown as "internal error". A model failure (G6) ends the run, except a
  subagent's answer cut off at its token limit before any text or whole tool call: that is a bad
  answer (one shorter try, then `ErrToolOutput`), not a failure (`TestSubagentCutOffEmptyAnswerIsNotAModelFailure`).
- **Budget** (`Budget`). Calls and USD per task, held and charged in Go. A model call holds one call
  and its worst-case cost (each request byte a token plus 1,000, as a cache write, plus `MaxTokens`
  of output) and then charges what it spent, a refused-but-billed answer included; a tool's grant is
  up to its turn cap, refused only when no call is free, and its unused part returns when it closes.
  The task keeps back, in calls and USD, what its later phases need (`Keep`, by level: evidence <
  work (lead turns, drafting, free tools) < review (the committee) < final): a grant never takes what
  a higher level keeps, so only `finish` spends the reserve + meta. Query embeddings are charged
  in USD and tokens; an unpriced model counts as a call of unknown cost.
- **Tool bandit** (`Bandit`, `Stats`; `MemStats` here, Postgres in 4b). Per task signature
  (workflow, practice, jurisdiction prefix, lead model, documents 1–3/4–9/10+), each tool's and each
  subagent model binding's calls, reward, failures and spend; a reliability-aware UCB1 score; an
  untried tool ranks first once. It adds one hint line to the latest tool result before a lead turn
  (`retrieve`, `ledger`, `committee`, `finish` first, `verify_quote` never ranked), picks a
  subagent's model only among the role's and the lead's, and never hides or refuses a tool.
  `Off` sends no hint. Every tool result is recorded (refused or failed: reward 0).
- **`retrieve`** (`op` search, outline, section; `k` ≤ 8; sections paged with `next`) and
  **`verify_quote`** (the ladder; the paraphrase judge runs on the `critic` role, at most 4 calls
  from the caller's grant, each held and charged like the loop's own calls through
  `Runtime.ChatWithin`, so it never spends past the grant's calls or USD; the quote and the passage
  each fenced as untrusted text, fence markers inside them neutralised). A document outside the call's
  ids is "not found".
- **`numbers`**: `calc` (exact decimals: `sum`, `diff`, `ratio`, `pct`, `product` (a percentage
  counts as its fraction: 35% of $100,000 is $35,000), `date_add`, `date_diff`, `business_days`
  with the caller's holidays; a negative day count counts back; `date_add` with holidays set, even
  `[]`, rolls a result on a weekend or holiday to the next day that is not, backward for a negative
  count, as FRCP 6(a)(1)(C) and 6(a)(5) do, the worksheet showing the raw date) over verified finding
  values (`F12.v1`, the form extraction results, `ledger` and `handles` show; also read: `[F12].v1`,
  `f12.V1`, `F12 v1`, `F12.d1` = its first date value, a bare `F12` = its only value) or literals (a
  literal that is plainly a full date or "21 days" needs no unit; a count before a date is swapped).
  What cannot be read is an error listing the finding's values by ref, with a call that works
  (`TestCalcAcceptsWhatModelsSend`); a
  worksheet id is a hash of its steps; a quarantined or unknown finding is refused, never 0. Money is
  USD only when its unit is empty, `$`, `usd` or `dollars`; another currency keeps its unit, so a sum
  across currencies is refused. Date differences are exact over any span.
  `handles`: a `{Codename}` per distinct verified money, percentage, count or identifier value (never
  a date), most salient first, 64 at most, assigned over the whole verified ledger (a codename names
  one figure whatever `fids` a call asks for); codenames given before (`kept`) keep their figures
  and a new figure takes the next unused one, so in a run (where the engine keeps them in the
  ledger) a codename never changes meaning; `Substitute` puts the exact value in for a codename or a
  worksheet's `{W…}` (its wid), and an unmapped one is left as written and reported. `table`: a
  key-figures table from verified values only.
- **`join_discrepancies`**: [figures.md § The cross-document join](figures.md#the-cross-document-join).
- **Coverage** (`CheckCoverage`, for `finish{op:"check"}` in 4b): plan issues with no section;
  parties in two or more verified findings, a Conduct issue's actor, or a roster name, that the draft
  never mentions (a firm's name does not cover a person); salient figures that never land.
- **BELO-lite** (`belo.json`): the classes and 11 predicates; `Relations` re-orients a reversed
  triple and drops an ill-typed one.

Limitations of this layer:
- **The USD cap holds only if the endpoint bills within `max_tokens` and adds under 1,000 tokens of
  its own** to a request (the worst-case bound); a provider that bills more is charged anyway, so the
  cap can then be passed by that excess. Unpriced models are bounded by the call cap only.
- A tool's grant takes all the free USD because calls run one at a time; a fan-out that runs
  subagents in parallel (PR 4b's `committee`) must size its grants itself.
- A remote tool that spends past its grant is charged what it reports (money already spent); in
  process a grant cannot be exceeded.
- The bandit's statistics are shared by every task with the same signature: one matter's documents
  can make a tool fail often and push it down another matter's hint (it never hides it). Its
  constants (β 0.5, γ 1, c 0.5) are uncalibrated; gate B measures it on and off.
- Codenames are kept for the run (ledger kind `handle`): one names one figure from its first use,
  also in the summaries later drafters read; the renderer substitutes with the set stored with the
  section, only outside quoted spans. The 64-name pool is the limit: a run with more distinct figures
  leaves the least salient late ones without a handle (a drafter may still state them inside a
  cited quote).
- `join_discrepancies` proposes only what the figure reader read clearly (`Value.Fig`) and asserts only
  what the judge says is one quantity: a judge that calls two different quantities one makes a false
  conflict (shown with its reason, for the lawyer to check), and one that calls one quantity two, or a
  reader that reads one figure unclearly, misses one. No rule in Go decides whose figure it is, what it
  measures, for which period or who else is named (r7: the party, counterparty, period and typed-entity
  matching rules, `oneQuantity` and `periodOf`, were deleted; prompt-faithful role-name labels, "the
  Executive" for two people, had made false conflicts of correct readings). Past the pairs its budget
  judges a matter's join is incomplete and says how many figures it left without any counterpart judged;
  a true conflict whose two values' contexts share no rare word and whose amounts are far apart ranks
  late. A value's document is the quote that
  holds its text, else the finding's first quote.
- Calls in one turn run sequentially, not concurrently; `retrieve` and Go tools spend no calls, so a
  lead can issue up to 8 per turn for every turn its budget funds.

## Memo workflow

- **The ledger** (`ledger`; one Postgres table, a JSON row per kind and key): `plan` (the issues
  the deliverable must answer, each typed by a BELO-lite class), `findings` / `get` (brief or full:
  citations and value refs `F3.v1`; `findings` a page of about 12 KB at a time (under the 16 KB over-long bound), `offset` from the
  result's `next`, `TestLedgerFindingsPage`), `resolve` (a critic finding fixed or disclosed, with a note),
  `draft_get` (a finished section only: `not_drafted` otherwise; paged by byte `offset`), `relations` and
  `critics` (the committee's findings with their status), each paged the same way (`TestLedgerPagesByType`). A subagent may only
  read, and never sees a quarantined finding (G3).
- **Evidence**: [figures.md § Evidence admission](figures.md#evidence-admission-and-the-figure-reader).
- **`sweep`** visits every section of the task's documents (or one document) once, calling
  `extract_facts` on each page of a top-level section, the page alone (no tools offered: one call a
  visit, where a visit that searched with `retrieve` took about three), most relevant first: each
  plan issue's pages ranked by the BM25 score of its question, taken in turn across the issues, then
  the pages no issue's words reach, in document order (`TestSweepReadsRelevantPagesFirst`); a page
  that is an exact copy of an earlier read one (case and white space folded, or the same table rows
  in another order; across documents only a page of 40 words or more) or is pure boilerplate (no
  letter or digit; a table of contents: its Contents heading over entries, or dot-leadered entries with
  heading-like titles and page numbers that never fall, no table row, no sentence; a tab or space-aligned
  list of figures is read: `TestR2VRealTablesNotBoilerplate`) is skipped and listed under `skipped`
  (`TestSweepSkipsDuplicatesAndBoilerplate`, `TestDataTableIsNotBoilerplate`, `TestDistinctPagesAreRead`);
  a copy of a page the sweep did not read counts unread (`TestSkipOfUnreadPageIsUnread`), and the
  skipped pages are a Limitations line (`TestSkippedPagesReachLimitations`); each document's coverage (planned, read, skipped) is
  kept in the ledger (`swept`) and its partly unread documents are reported (`unreadDocuments`, also
  in `join_discrepancies`' output and in Limitations); every read is sized by the answer,
  not the page (`sweepTokens`, 600: a subagent writes five to seven tokens of findings per token of
  dense text, so a 2,000-token page overflows its 8,192-token answer): a long section is read in
  600-token pages (a table in pages of at most 25 rows, its header row by reference:
  `TestSweepPagesFitTheAnswer`; a lead's direct `extract_facts` on a section reads the same pages),
  and consecutive top-level sections shorter than 600 tokens are batched into one visit of at most
  600 (`through`): a document numbered by paragraph is read a few paragraphs at a
  time, not one subagent run per paragraph (`TestSweepBatchesShortSections`); a `through` that is
  not such a run from `sectionId` (with offset 0) fails the call, never reads `sectionId` alone
  (`TestExtractFactsRefusesABadThrough`); a visit is keyed (document, section, offset[, through]) with the plan's issues it was read for, and is
  repeated only for issues `op plan` added since, sent and admitting only those (`TestIssueAddedAfterSweepGetsItsFacts`); a page
  no sweep read again for them is a Limitations line (`TestIssueAddedAfterSweepNeverReadAgainIsDisclosed`);
  it stops at its share of the budget and reports `incomplete` with the sections it did not reach
  (a batch as its whole run, "s3 to s9": `TestSweepUncoveredNamesTheWholeBatch`),
  which also go into Limitations. Never silently partial. It reads up to `sweepWorkers` (4) visits at
  once and is still deterministic: a visit starts with a full grant (the subagent's turns and a
  quarter of the sweep's USD) while one is free, else only once nothing is in flight, with what is
  left, so no grant depends on timing and the shares hold exactly as one at a time; findings are
  admitted (fids, exact-duplicate merges) and visits recorded in reading order (each visit waits, after
  its subagent ran, for the one before it); a visit a worker's quarter could not fund (a call's
  worst case over it: a pricier model on a small budget) is read again once nothing is in flight,
  one at a time with all that is left, so parallelism never reads less than a one-at-a-time sweep
  (`TestSweepFallsBackToSerialWhenAShareCannotFundACall`), and `visited` counts only visits that
  made a model call; its model is the bandit's one pick for the sweep
  (`agent.Call.Model`); a model or journal failure cancels the rest (`TestSweepReadsInParallelInPlanOrder`).
  The sweep is one step: a process stopped mid-sweep commits none of it and the next re-issues it
  (`TestSweepResumesAfterKillMidSweep`). Its output passes its schema also when incomplete (a
  batch's `through` is a field: `TestSweepUncoveredNamesTheWholeBatch`).
- **Drafting** (`draft_section`, one issue per call): Go gives the drafter the issue, its named
  verified findings with their quotes (each naming its document, so the drafter can say which
  document states what: `TestDraftQuotesNameTheirDocument`), their figure handles (`{Zephyr}`), a `{W…}` handle per
  `numbers` worksheet (labelled with what it computes; rendered as a date in words, `$214,340`,
  `35%` or `14 days`), the summaries of sections already written, in plan order, and on a redraft
  the `checks` (lint lines) its current version fails, and the issue's latest `analyze_issue` answer
  (`analysis`: rule, application, conclusion). The section is stored with the run's handle set at
  that point, a citation group (`[F3, F7]`, `[F3.v1]`) split into single citations.
- **`committee`**: three context-free critics in parallel, each on its own explicit grant (the
  cycle's 8/8/4 calls, the last taking the USD remainder): `prosecute` gets the claims and their
  quotes, `reproduce` gets the issue questions only (never the draft) and its answers are compared
  with the draft, `audit_gaps` gets the plan, the drafted headings (not their text) and the packs'
  checklist. Their findings are HIGH/MEDIUM/LOW, open until the
  lead resolves them; a `prosecute` verdict credits or debits the arm that produced the finding
  (delayed credit, ±0.5). A critic that fails leaves a MEDIUM "did not run" finding.
- **Policy** (`policy.go`): P1 nothing but `retrieve`/`ledger` before a plan, and if the lead has
  spent its plan share (7%, at least 3 calls) without one, the packs' checklist becomes the plan
  (disclosed); P4 class shares, each on calls **and** USD (evidence 45% of which sweep 24% of the
  calls and 20% of the USD, its figure reader 6% and 10%, and the coverage pass 10%, kept for it, so the lead's own evidence calls may spend 35%, at least 5% beside a
  full sweep; draft
  15%, committee cycles of 20 calls or 20% of a small budget with a fifth of the USD, a reserve of 5
  and meta 3 only `finish` spends). The root budget keeps back the unspent drafting share from
  evidence, one committee cycle and (until it has run) the coverage pass from everything below the
  committee (lead turns included), and the
  reserve from all but `finish`, so evidence can never spend the draft's budget and the last draft can
  always be reviewed: when the lead runs out with a draft no committee has read, Go runs the
  committee on it from that cycle before the forced finish (`TestPhaseSharesHoldInUSD`,
  `TestCommitteeAlwaysReviewsTheLastDraft`). The lead runs at most three committee cycles (`maxCycles`; bench6 SEC ran
  nine, $4.16, while verified findings stayed uncited): a fourth is refused with what to do instead, finish no longer asks
  for a review of a draft changed after the capped cycles (a Limitations line says so, naming the critic findings resolved
  as fixed after the last cycle: `TestR4V4UnreviewedFixesNamed`), and the coverage pass, whose own
  re-review of its redrafts is beyond the cap, may spend half of what is free beside one more cycle and the reserve when
  that is more than its share, placing left-out findings in up to three rounds of one redraft per issue, at most 16 each,
  60 per round, a request naming at most 100 findings, the section's own citations and its conflicts' findings never cut
  (a section citing more is not redrafted: `TestR4V4PlacementKeepsCitations`; `TestBench6SECCommitteeCapped`). The cycle it keeps is a fifth of the USD until a cycle
  has run, then 1.5 times the costliest cycle so far (`cycleKeep`; bench5's cycles cost $0.30–0.48 of
  the $2.20 kept). `sweep` stops at its USD share as at its calls. The shares are sized from bench5 at
  $11 (lab-r4): a visit cost $0.012–0.014, so the 142-page SEC set needs about $2 of sweep (18%; 30%
  leaves room for half as many pages again); the coverage pass spent about $0.15 an issue on an analysis
  and its redraft, and at 6% had nothing left to place a finding; a SEC-shaped scripted run at bench
  costs per call (`TestSECShapedRunAnalysesAndPlacesWithin11`: 142 pages, six issues, a lead that loops
  drafting and the committee until the budget stops it) analyses every law issue, places the left-out
  findings and spends under $11.
- **`finish`** (`op: "check"` is a dry run of `submit`): lint (every `[F-id]` names a usable
  finding; every number and date in the draft is a handle, a `numbers` worksheet's result or one of
  its inputs, or inside a cited finding's quote or value (G8, the "68.6% for 81.6%" trap; a deadline's
  raw date before a `date_add` worksheet rolled it past a weekend or holiday traces only in a
  paragraph that states the rolled date, and its lint names the rolled `{W…}` to write, so a drafter's
  own unrolled `date_add` cannot put Veterans Day in the memo as the deadline:
  `TestRawDeadlineNeedsItsRoll`), rule and
  section numbers (`Rule 6(a)(1)(C)`, `Section 4.3`, `§ 9.2`) being locators, not figures, also in
  lists and ranges (`Sections 7.3 and 9.1`, `§§ 4.2–4.5`, `clauses 3(a) and 3(b)`, `paragraphs 12,
  14 and 17`; a figure after a list still counts: `TestFigureLintSkipsLocatorLists`): a list ends at
its last locator, and goes on over "and", "," or "–" only to a number that is itself locator-shaped
(`9.1`, `206(2)`) or followed by punctuation, the end or a word a locator takes ("of", "are"), never
to one followed by a noun or unit or after a "," or ";" once "and"/"or" closed the list
("paragraphs 12 and 14, 37 trades" traces 37: `TestFigureLintTracesCountAfterLocatorList`); each
  untraced figure says how to fix it: the handle to write and the finding to cite, the uncited
  finding that states it, or the `numbers` call that computes it (`TestLintSaysHowToFix`); no
  unknown handle, and none inside a quotation (Go never substitutes there, so the quote would show
  `{Heron}`). A memo stating a figure a worksheet computes (over cited values or typed inputs), by
  its `{W…}` handle or written out, lists it in Limitations' Computed figures block, with what it
  computes from what. After two `finish` calls that reported lint problems, the next discloses them in
  Limitations ("Delivered with an unresolved check after 2 rounds of fixes") instead of refusing,
  so redrafting for the lint is bounded (`TestFigureLintIsBounded`)), coverage (plan issues
  without a section, parties never named, salient figures never landing), and P6: a committee on
  the current draft version, its HIGH/MEDIUM findings resolved while the budget can fund another
  fix (3 calls and their USD beside the kept cycle and reserve), every plan issue drafted or disclosed by issue id.
  **Coverage pass** (`cover.go`, once per run, before the first deliverable): a plan issue is only
  *touched* when no drafted section cites a usable finding for it, or when it is a Conduct or
  Requirement issue (what the law makes of facts) with no analysis (an `analyze_issue` answer, or a
  usable `check_deviation` finding of it). For each touched issue, in plan order, Go runs
  `analyze_issue` and redrafts the section from the issue's findings and that analysis, from the
  share kept for it; then the committee reviews the changed draft (inside a passing `submit`; a run
  finishing from its reserve runs the pass as a step before its last review), and each issue still
  touched is disclosed in Limitations ("Coverage: I6 (…) was not analysed: …";
  `TestCoveragePassFromTheReserve`, `TestCoveragePassOnSubmit`). The pass also places what the
  record proves but the memo left out: each salient verified finding no section cites (a
  `check_deviation` finding, one rated high, one with a breach relation, one stating a salient figure,
  or one whose claim says a document deviates, omits, fails to or does not; at most 40) goes to its
  issue, else the issue whose question its claim matches best, and each cross-document figure
  conflict the memo does not state both sides of goes to the issue citing either finding; each such
  issue gets one redraft with them, the committee reviews it, and what is still left out is
  disclosed ("Coverage: N verified findings … never reached the memo"; the parties and the salient
  figures the memo never names are one line each, at most 12 listed: `TestCoverageLinesSummarised`). Every figure conflict the
  memo does not address is also a `finish` lint line ("memo: the documents state … differently"),
  bounded and disclosed like G8's (`TestCoverageDraftsLeftOutEvidence`). `finish`'s grant holds its meta
  call plus the pass's and one review's calls (so a crash mid-finish loses no more), the pass's own
step (from the reserve) holds only its share's calls and USD (`TestCoverStepGrantIsItsShare`), a
pass a model failure ends is still recorded as run, so a retry does not spend a second share on it
(`TestCoveragePassRecordedWhenAModelFails`), and P6/P7's
  "funded" counts them, and the pass's USD until it has run, as kept (`TestCoverageShareKept`). P7: a `submit` within 2
  calls of a committee whose HIGH/MEDIUM findings were resolved without new work is refused once
  per cycle, while funded, with the instruction to fix properly and run the committee again. `submit` then renders: sections in plan order (summaries never), handles substituted
  outside quotes only, process language and orphan fragments stripped, repeated blocks dropped,
  external authorities (cases, statutes the record does not contain) marked inline and listed for
  verification (G5), one-line Sources (a transcript citation badged "machine transcription (model)"
  and linked to the original, G11), Limitations, an Evidence Note (verified vs quarantined,
  and verified findings a lawyer rejected at a gate; transcript-only and low-confidence counts; a
  grounding alert raised during the run) and one meta call's note to the reviewing lawyer. Text from
  documents and models is neutralised where the memo prints it (`mdSafe`): one-line slots (Sources,
  headings, list items) collapse white space; sections and the note keep their lines with `#`/`##`
  demoted to `###`; `<` and fence openers are backslash-escaped. A quote cannot open a section.

## Durable runs

**Durable runs.** Each lead turn and tool call is a step in `task_steps`: recorded `started`
(with its grant) before it runs and `committed`/`refused`/`failed` after, together with the
ledger rows and bandit statistics it wrote, in one transaction. A process claims a task with a
lease (30 s, renewed every 10 s; `FOR UPDATE SKIP LOCKED`) and runs at most 4 at once; a stale
lease (a crashed process) is taken over. On resume the committed steps are replayed from the log
(their outputs reused, no model call repeated) and a step that was in flight is charged at its
grant (`lost`, shown in the cost ledger) and issued again; a process that is stopped (SIGTERM, a
deploy) first settles an in-flight tool step's grant to what the tool spent, its aborted model
call at its worst case, so the resume is charged that rather than the tool's whole grant (every
free dollar of the task; `TestResumeAfterStopMidToolChargesWhatItSpent`). A process that dies
without a word (kill -9, a container restart) cannot settle anything, so each model call inside a
tool's step is journaled as it happens: before it is sent its worst case is added to the started
step's grant, and when it returns it is replaced by what it spent; the resume charges that, the spend
so far plus the worst case of the calls in flight (at most one per sweep worker), not the tool's
whole grant (`TestHardCrashChargesOnlyRecordedSpend`; a tool that has made no model call yet keeps
its whole grant). That holds for every model call inside the step: a committee's three parallel
critics' turns, and the query embeddings their (or any subagent's) `retrieve` makes (`agent.Meter`:
`TestKillDuringCommitteeChargesMeteredSpend`; bench5 trust's $0.5078 lost at a restart mid-committee
was its 15 metered calls, $0.205, plus two critic calls in flight at their worst case, about $0.15 each,
not the committee's grant). A task whose document
was deleted after submission fails (`bad_request`) instead of being claimed again forever. A run stops `finished` (gate open),
`budget_exhausted` or `max_steps` (a draft changed since its last review gets one committee from the
kept cycle, then `finish` is forced from the reserve, open findings disclosed; `maxSteps` defaults to
twice the call budget, at least 400),
`awaiting_gate`, `cancelled` or `failed`. Tool-bandit statistics persist in `tool_stats`.

## Manage

- **Versions** (`spec_versions`, migration 0008): every save is a new version of the item holding all
  its files, who (`author`), when, an optional note and the version it replaced; a revert (to any
  version, or to the built-in: files NULL) and a skill's deletion (files `{}`, a tombstone) are versions
  too, so the table is the history and the **change log**. A save names the version it was edited from
  (`base`): a stale one is 409. A save holds files: one with none, which would delete the item, is
  refused for every built-in (with Delete's reason where Delete refuses too), and a request body is
  exactly one JSON object (400 otherwise); a revert to a version that deleted such an item is refused
  the same way (History marks it unrevertable, with why). A stored version that no longer loads is listed
  as broken (why, and what is stored) and reverts like any other; until it is reverted every other write
  is a 422 naming it and every new task fails naming it. A prompt whose file a version removed is listed
  as deleted. Diffs are a line LCS (Go `diff.go` for the API and the export, the UI's
  `components/diff.ts` for its views and unsaved edits).
- **Snapshot.** `Submit` stores the overrides in effect (each item's latest version, unless it is the
  built-in again) in `tasks.specs`, in the statement that stores the task. `execute` builds the run's
  spec set from it (`newSpecs`: the built-ins with those files over them) on every claim, a resume after
  a crash included, and carries it in the run (`run.specs`) and its context (`withSpecs`): every spec,
  prompt, pack and skill a run reads goes through it (`spec(ctx, …)`, `prompt(ctx, …)`, `packsFor`), and
  the registry is built from it (docs and analysis tools re-registered with their override's spec,
  `respec`). A save reaches the next task only (`TestSnapshotAtStartAndResume`). A binary upgrade still
  changes the built-ins under a resumed run, as it changes all Go code.
- **Cache keys.** The figure reader's and judge's cached answers are keyed by the hash of their spec,
  prompt and the spec of every tool their allowed list names, in the run's set (`Specs.agentHash`), so
  an override of any of them, or a tool added to the list, reads and judges again
  (`TestOverrideChangesCacheKeys`, `TestAgentHashCoversAllowedTools`); the built-ins hash as before,
  so existing rows stay valid.
- **Validation** (`validate`, every save and revert, all problems at once, 422 with `problems[]` by
  file): UTF-8; a spec is one JSON object of `agent.Spec`'s fields (unknown fields refused), keeps its
  name, kind and `final`, has `maxTurns` 1–32 (subagent) or 0–10000 (Go tool), a known class, its
  schemas in `agent/schema.go`'s subset and strict-shaped; a prompt is 1 byte–64 KiB; a pack's front
  matter is `code` (its file's), `status` candidate|reviewed (reviewed needs `reviewedBy`) and the
  checklist; then the whole set as a task would load it: `newSpecs` and the runtime's own registration
  (descriptions, examples, allowed tools are registered Go tools, roles, the 48/24 caps, the lead's
  list). A description is at most 4 KiB with no control or format characters but line breaks. An item's files are exactly its own (no file added to or dropped from a built-in item). No
  subagent's allowed list may name a tool that is the lead's own: a Final tool (`finish`: it ends the
  run) or one with ops that must stand alone in a lead turn, except `ledger`, which refuses its writes
  (`plan`, `resolve`) to a subagent in its own code and is in the built-ins' lists for its reads; the
  fan-outs (`sweep`, `committee`, `cover`) are not Go tools, so the registry already refuses them. Class
  `free` (no class share) is kept for the subagents built free (the figure reader and judge, metered
  from their own shares); any other subagent must take a class with a share.
- **Serial saves.** A save, revert or delete runs in one transaction under an advisory lock
  (`locked`): it reads the set, validates the set it would make, and writes, so two saves of different
  items can never make a set that does not load (`TestSavesAreSerial`).
- **Compatibility** (`compatSpec`). Go code depends on schema shapes, so a schema save must keep, at
  every path the built-in schema has (nested and in `items`), the property and its type list (Go
  decodes or writes it by that shape: removing, renaming or retyping is refused naming the path, e.g.
  `out.findings[].claim: Go reads or writes this field`); every enum keeps exactly the built-in's values
  (Go branches on them: a critic's verdict, a gap's severity, an op; renaming, recasing, adding or
  removing one is refused, `out.verdicts[].verdict: Go branches on this field's values`); a schema Go writes (a Go tool's output, the
  input of a tool Go itself calls: `goWritten`, found from the engine's `rt.Invoke` calls) gains no
  property (strict schemas require every property, and Go sends none); the built-in description's
  examples and Go's own literal inputs (`goInputs`: finish's submit, `committee {}`, `cover {}`) must
  still validate. **Limits:** it compares with the built-in, not with a list of the fields each Go
  line reads, so a field Go never reads is protected too (an enum Go never branches on is frozen as
  well); narrowing a bound (a length, an item count) on a value Go writes is caught only where an
  example or a literal input shows it; a field added to a schema the
  model writes is accepted and ignored by Go; semantics (what a field means) are not checked.
- **The lawyer-review rule.** Everything in a pack or skill is its content: every file (a skill's
  reference files too), every front matter field but `status` and `reviewedBy` (a skill's description
  and tools, a pack's checklist) and the body. A save that changes any of it resets the status to
  candidate and clears `reviewedBy`, unless the save names a reviewer who is not the one before; a new
  item (a skill created, or created again after its deletion) is candidate unless it names its
  reviewer. The response says so (`notes`). A status-only edit (a lawyer recording a review) is kept.

## Configurable hooks

- **Events:** `before_call` and `after_call` (every call `Runtime.Invoke` makes: the lead's, a
  subagent's and Go's fan-out calls; no rule applies to `finish`, whose hook point is `task_finish`, so
  a forced finish is never refused, nor to `verify_quote`, the engine's own quote check),
  `finding_admitted` (each new finding, after its quotes are verified, before it is recorded: admit's
  evidence findings and check_deviation's deviations alike, through the one path that records a new
  finding, `recordFinding`; `TestFindingWritesGoThroughRecord` fails on any other) and
  `task_finish` (after finish's checks pass, before the deliverable renders).
- **Matchers:** a tool or subagent name, a prefix ending in `*`, or `*`; `when` is an optional
  condition on one field (`eq`, `ne`, `contains` (case-insensitive), `exists`, `absent`, `gt`, `lt`) of
  the call's input (`after_call`: `input.*`, `output.*`), of the finding (`claim`, `issueId`,
  `verified`, `low`, `documents`) or of the finish (`stop`). A condition is a trigger, not a control:
  the lead writes what it reads.
- **`block`:** `before_call`: a refusal the lead reads, and the call does nothing. `after_call`: the
  answer is withheld, and so is every effect: for the evidence and drafting subagents (`extract_facts`,
  `analyze_issue`, `check_deviation`, `draft_section`) the rule runs on the subagent's own answer before
  Go admits a finding or saves a section, so nothing is recorded (a sweep's visit is still done, with
  nothing admitted); an after_call `block` or `check` must name one tool, never a tool that writes the
  ledger as it runs (`ledger`, `sweep`, `committee`, `cover`, `numbers`, `join_discrepancies`,
  `use_skill`, the figure reader and judge) or a subagent allowed one (use `before_call` for those).
  `finding_admitted`: the finding is **quarantined by the hook** (`hookBlocked`: never in the memo, a
  drafter's or a subagent's read, nor `numbers`', `join_discrepancies`' (`Engine.Findings`) or the figure reader's, disclosed in its own Limitations line with the count by rule, and
  never counted as a quote that failed verification: G4 and the Evidence Note leave it out; a check that
  fails closed there quarantines the same way). `task_finish`: the submit is refused with the reason;
  the lead can only finish by a forced finish (its budget or max steps), so an unconditional block
  spends the rest of the task's budget and then delivers with the refusal as a Limitations line.
- **`approve`:** names one tool the lead calls (saved otherwise refused), never `committee` or
  `cover`, which a forced finish runs as the lead's own steps (a gate there would pause a run that must
  end). Only the lead's own calls
  of it can be approved, so **every other call of it fails closed**: a sweep's visits, the coverage
  pass's or the committee's calls and a subagent's calls are blocked (`blocked (needs lead approval)`),
  the caller gets the usual refusal, and the deliverable's Limitations count them by rule; the save's
  notes and the rule's summary in Manage list which callers it will block. A hook gate opens with the call's **whole input**, the refused
  step commits and **the run pauses at the gate** (`awaiting_gate`; a crash before the pause pauses on
  resume, as an open hook gate does). The gate is keyed by a 128-bit hash of the canonical input, and
  an approval covers **exactly that input, once**: the lead makes the call again with the same input and
  it goes through; any other input, or the same one again, opens a new gate. A rejection refuses that
  one attempt. `finding_admitted`: the finding takes its own G7 gate. Each note to the lead lists only
  the gates decided since the last. **Without sign-in, anyone who can reach the API can decide a
  gate, hook approvals included**; with it any signed-in user can (member or admin), and the gate
  records who (`decidedBy`). An approve rule cannot yet require a role.
- **`check`:** a named subagent whose input is `{event, subject, text}` and whose output holds
  `verdict` (allow, block or annotate) and `reason`; its model calls spend from the caller's budget and
  are metered with the call's step. A check agent is never offered to the lead. Its own tool calls are
  hooked like any call, but a `check` rule on them is skipped (no check checks a check).
- **`webhook`:** an https POST through the same SSRF-checked client, origin-bound secret and
  off-without-a-real-admin rule as custom tools. The body is **metadata only** by default (`{hook,
  event, taskId, subject, textBytes, at}`); a rule opts in with `sendContent`, and then **matter content
  leaves BigLaw** (the call's input or answer, the finding's claim and quotes, or the draft's text, up to
  8,000 characters, for every matching event of every client and matter; the editor warns). `wait`
  blocks on a 2xx within 30 s (at least once: a step re-run after a crash sends it again); otherwise it
  is sent in the background (at most 8 in flight; beyond, it is dropped and the outcome says so), its
  answer only logged. With no real admin a webhook cannot be sent, so a waited one fails as its rule
  says: closed blocks every call it matches.
- **`limitation`:** a Limitations line.
- **Known limits:** an after_call block withholds what BigLaw records, never what already happened
  elsewhere: a custom tool's remote effect (an MCP or HTTP call already made) is not undone, and a
  withheld drafter's own numbers worksheet (written by its calls during drafting) stays in the ledger,
  unused; the cap of 8 background webhooks in flight is shared by the whole server, not per rule or task.
- **Order:** rules run by `order`, then name; a block or a gate stops the rest for that event.
- **Failure:** a rule that cannot do its action (a check unfunded, failed or answering no verdict, a
  waited webhook that failed, a field it cannot compare) **fails closed** (blocks) or **open** (lets the
  event through), as it says; a limitation or a webhook that does not wait never blocks. Any other error
  (a record that could not be written, a panic, a stopped run) fails the call, whatever the policy. A
  rule that blocks everything does not wedge a run (it ends), but a running task keeps its snapshot:
  cancel it and submit again.
- **Journal:** every outcome (`allowed`, `blocked`, `gated`, `approved`, `annotated`, `noted`, `sent`,
  `dropped`, `skipped`, `failed-open`, `failed-closed`) is a ledger row (kind `hookrun`, keyed by the
  lead step and its order in it) written with the step, and a log line, so a crash and resume neither
  repeat nor lose one.
- **Validation** at save: the event and action pair, the matcher and condition, that a named tool,
  subagent or check agent exists with the check's schemas, the approve and after_call limits above, the
  webhook URL and secret, a message of at most 2,000 characters with no control or format characters, no
  Go hook's or reserved name; a subagent a rule checks with cannot be deleted. A Test call runs the item
  alone, without the rules.

## Limitations (LAB rounds 3 and 4)

- **LAB round 3 (sweep order and skips, multi-part requirements, crash journal, left-out
  evidence, raw deadlines):**
  - The sweep's order is lexical (BM25 of the plan's questions): generic questions ("what
    allegations are made") rank weakly, and a page that matters for a reason its words do not
    share (a summary sheet whose figure contradicts another document) can come late. Its skips are
    conservative: no page of the SEC or trust sets is a duplicate or boilerplate, so there they save
    nothing. Only an exact copy (case and white space folded, or the same lines of a table in another
    order) is skipped as a duplicate, and its coverage follows the page it copies (a copy of a page
    the sweep did not read is unread too); a page is boilerplate only with no letter or digit, or as
    a table of contents (a Contents heading, or dot-leadered heading-like entries with page numbers that
    never fall) with no table row and no sentence. The count of skipped pages and their
    documents is a Limitations line.
  - One call a visit is an inference from the bench's token counts (about 26,000 input tokens a
    visit, three calls), not a measured split. The lab-r4 shares are sized from bench5's costs per
    call (a visit $0.012–0.014, a committee cycle $0.30–0.48, an analysis and its redraft about $0.15)
    and are not yet measured on a rerun.
  - A salient finding is judged by its tool, severity, typed relations, figures or the words of its
    claim ("deviates", "does not", "fails to"): a left-out finding stated otherwise is not placed, and
    a claim that merely says "does not" is. The pass places at most 40, one redraft per issue, the
    issues with a conflict or the strongest left-out finding first, from the 40% of its USD its
    analyses leave for it (`placeShare`); what it cannot place is one Limitations line.
  - A requirement's parts must be the document's words, in its order, one passage: only white space
    and list markers between them, or, after a sentence, at most two whole sentences (400 bytes); never
    a heading or a list's introduction between them. A fragment is not refused: it is read and quoted
    in about 3 KB of the text before and after it (at most 8 KB). **An override or a governing
    introduction more than about 3 KB away, or in another clause, a definition or a schedule elsewhere in
    the document ("9. Clause 4 shall not apply …" pages later, a defined term's meaning), is not seen.**
    A requirement whose sentence or paragraph starts before the window, or whose window starts at a list
    item, is admitted only at low confidence, for a lawyer's review, so a requirement in a long paragraph
    (over 3 KB above it) or deep in a long list is always gated. Whether the text imposes the requirement as quoted (a
    negation, an override) is the section check's judgment, one critic call per `check_deviation` call:
    the context makes the governing words visible and the check reads them, which does not make a
    verdict right (an analyst who misreads a requirement the document does impose is checked only by
    the claim's quoted window and prosecute).
  - The join, worksheets, the Computed figures block and scales: [figures.md § Limitations](figures.md#limitations).
  - The raw-date rule is paragraph-wide: a list that states the raw date and the rolled deadline on
    separate lines without a blank line between is one paragraph and passes; one with a blank line is
    flagged.
- **LAB improvements (sweep pages, parallel sweep, deviations, G8 lists, coverage pass):**
  - Answer-sized pages cost about three times the visits of 2,000-token pages on long documents;
    the parallel sweep pays that in time, not budget. No real model has run them yet: the LAB rerun is
    the owner's.
  - A parallel visit holds a quarter of the sweep's USD. One call's worst case (8,192 output tokens)
    is a few cents on a small model but about $0.13 on a mid-priced one, so on a small budget a
    quarter may not fund one call: such a visit is refused, then read in the serial fallback with
    all that is left (its findings admitted after the parallel visits'); what the refused attempt
    spent before its refusal is spent twice. A
    call billed above its own worst-case bound (a pricing anomaly) can make which later visits get a
    full grant depend on timing. The sweep is one step: a process stopped mid-sweep re-reads it all.
  - A table page's header is the first row of the run of rows the page continues, ended by a blank
    line, prose or a `#` marker: two tables back to back with no line between them read as one
    (their header is the first's), as the chunker reads them, and a table with a blank line inside it
    reads as two. A page deep in a long table reads back to the table's start (doubling windows):
    a 16 MiB section's last page reads up to the whole section once.
  - The coverage pass judges "analysed" by the issue's class (Conduct and Requirement need an
    analysis); an analysis is the model's reasoning, not a verified quote: its facts still cite
    verified findings, and its rules are flagged as external authorities when the record lacks them.
    It runs once, one analysis and one redraft per touched issue, within its 10% share (`Allocation.Cover`); issues it does
    not reach are disclosed, not analysed.
  - An omission is confirmed absent by the paraphrase judge on the reviewed document's closest
    passage: a provision stated far from where its words cluster can be judged absent wrongly (the
    finding then quotes the passage it was judged against, for the lawyer to check).

## Limitations of the model client

  - Every test uses `httptest` or `model.Fake`. `Chat`'s caller is the agent loop and `Embed`'s is
    documents; the only real endpoints exercised are the owner's bench and LAB runs. Behaviour against
    other real endpoints (Ollama, vLLM, other vendors) is unproven until the owner's gates run them.
  - **Every call is charged, including on your own GPU** (owner decision). An Ollama model under
    an open-weights family name (`qwen2.5:14b`, `mistral-nemo`, `deepseek-r1:14b`) is charged at
    that family's hosted rate, so the USD cap counts money not spent unless you set the model to
    `{"in": 0, "out": 0}` in `BIGLAW_PRICES`. A model with no row (`llama3.1:8b`, `gemma3`, a
    fine-tune) is unknown, never $0: check the startup `model pricing` line.
  - A model with no price (unknown, or `null`) has an unknown cost: the USD cap cannot count it,
    and only the call cap bounds it. One warning is logged per model per process.
  - The built-in rates are v1's list prices (early 2026) and were not re-verified for v2; they
    may be stale. v1's substring families became prefix keys, so a name that only *contains* a
    family (`my-haiku-finetune`) is no longer priced as that family; `vendor/model` ids are
    still matched. One carried vector changed (`gemini-2.5-flash-lite-preview` now takes the
    exact flash-lite row's 0.10/0.40, not v1's contradictory class rate 0.075/0.30).
  - Inside one `BIGLAW_PRICES` entry, Go matches `in`/`out` case-insensitively and a repeated
    field keeps the last value; an exactly repeated id keeps the last
    entry (Go's JSON decoder), a case-variant repeat is refused. Errors name no id, so a typo in
    a long file is found by bisecting it.
  - Upstream 400/413/422 (a context-length or schema refusal) is `ErrUnavailable` "HTTP 4xx" with
    its `Status`, not retried by the client; the agent loop compacts the transcript and tries
    again, twice at most (§ Agent runtime). Tool-call names are checked against the tools offered by the loop.
  - A 200 whose body is an error object (some gateways) is `ErrNoContent` "no choices", not
    retried, and an OpenAI `refusal` is reported as an answer with neither text nor tool calls (its
    text is dropped); both are 422 `no_content` and their usage, when the body gave one, is charged
    (PR 4a decision). A 200 whose body cannot be read (one malformed `usage` field, a string or a
    fraction, is enough) is `ErrUnavailable` with `Status: 200`: the answer is lost, and the task
    ledger counts it as one call of unknown cost. An answer with no
    `usage` object counts as 0 tokens, so a priced model's call reads $0.
  - A call that fails outright (non-200, transport error) carries no usage or watt-hours: a local
    GPU's energy for a failed generation is not metered. Watt-hours assume one machine at a fixed
    draw: a hosted endpoint with `BIGLAW_LOCAL_WATTS` set records watt-hours it did not use.
  - Cache accounting assumes `prompt_tokens` includes cached and cache-write tokens (OpenAI's
    convention). A gateway that reports them separately would make input tokens read low.
  - `Retry-After` is read only in its seconds form; an HTTP date falls back to the backoff.
    Without a caller deadline a call can take up to 3 × 5 minutes plus waits.
  - Answers whose `content` is an array of parts, streaming, and `n > 1` are not supported (the
    first choice is used). The number and size of tool calls in an answer are bounded only by the
    32 MiB answer cap; the agent loop must cap what it executes.
  - `strict: true` is sent only to `api.openai.com` (PR 4a); every other endpoint gets the same
    strict-shaped schema without the flag. `parallel_tool_calls` is passed through as given.
  - `model.Fake` hands out an agent's replies in call order, so concurrent calls *for the same
    agent* get replies in a scheduling-dependent order; script those calls sequentially, or route
    them by content (`Fake.Route`).
