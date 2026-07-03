# From 0 to 30/60: getting a local model to do real legal extraction

This is the technique-by-technique account of how BigLaw went from **0 to 30 of 60 rubric
criteria** on a Harvey-style LAB task — running on a single **local, open-weight 14B model**,
with no model swap and without stuffing the whole corpus into one context window. Every gain
is an orchestration change, not a bigger model.

## The benchmark, and how to read the number

The task is a Harvey-style LAB matter: a white-collar **SEC enforcement-referral extraction**
— eight source documents (the referral notice, `.xlsx` exhibits, a compliance review, a Form
ADV, an email) and a 60-criterion rubric, judged by an independent model.

The rubric is **task-level all-or-nothing**: the headline 0–1 score stays `0.00` until
essentially every criterion passes. A raw score is therefore useless as a progress signal, so
the tracked metric is **criteria-passed count out of 60**. 30/60 is *not* a pass — it is the
midpoint of a climb, and that framing matters for everything below.

A second reality: a sampling-temperature local model has **±4–5 criteria of run-to-run
variance**. Single-run deltas inside that band are noise; the signal is the *trajectory* and
the *causal* per-criterion diffs.

## The trajectory

| Stage | Technique | Criteria /60 |
|---|---|---|
| Baseline | local model, naive pipeline | **0** |
| Grounding | staged extract→analyse + hybrid RAG | **10** |
| Tables | exhibit/row-aware chunking | 10 (set shifted — `.xlsx` figures now land) |
| Spine | top-down coverage spine | **13** |
| Sweep | at-start specifics hunt | **22** |
| Recruit | matter classification + decontamination | **28** |
| Paging | paged writing-agent synthesis | **26** (synthesis leak closed) |
| Graph | Lite evidence graph | 26 single / **30** across the set |

The single biggest lesson is that **"accuracy" is not one problem.** It decomposed into four
distinct layers, each of which became the bottleneck in turn once the one before it was fixed:
**grounding → coverage → synthesis → attribution.** Fixing grounding exposed a coverage gap;
fixing coverage exposed a synthesis gap; fixing synthesis exposed an attribution gap. You only
see the next wall after you clear the current one.

## The techniques

### 1. Verbatim grounding (≈0% → 94%)
A local model asked to "summarise the allegations" hallucinates citations. The fix is to never
let it write a fact and a citation in the same breath. Retrieval is **hybrid RAG** — dense
vectors + doc2query expansions + Okapi BM25, fused by Reciprocal Rank Fusion. Extraction is
**staged**: one pass transcribes evidence *verbatim* under a substring-lock (the quote must be
a literal span of the source), a second pass writes conclusions *only over the locked quotes*.
Grounded-citation rate went from near-zero to ~94%.

### 2. Table/exhibit-aware chunking
The figures that matter — dollar amounts, percentages, account numbers — live in `.xlsx`
exhibits. A spreadsheet row (`Category ⇥ Amount ⇥ $7,800,000`) is not a sentence, so a
sentence-oriented chunker and a semantic query both skip it. Fix: **one chunk per data row**,
with a **header-paired embed text** ("Excess profits, Oceanic Fund: $7,800,000") for
findability while the stored text stays the verbatim row for grounding. Spreadsheet figures
became retrievable and extractable.

### 3. Targeted multi-query retrieval
A single blunt query ("cherry-picking trade allocations") puts the specific facts at **rank
17+** — past any reasonable top-k. The exhibits phrase facts in their own vocabulary
("profitable allocation rate", "account ending -7823"), not the section title. Fix: a critic
enumerates the *specific* facts a section needs and runs a **precise query per fact**; every
one lands in the top 8. One query is the wrong key, not too small a k.

### 4. At-start specifics sweep
Querying for specifics only at synthesis is capped by what the findings already contain. Move
it to the front: before the debate rounds, an **entity-aware sweep** hunts figure and citation
queries into findings. This was the jump from 13 to **22/60** — the single largest gain.

### 5. Neurosymbolic figure landing
Even with the right figure retrieved, a 14B transcribes `81.6%` as `68.6%`. So stop asking it
to type digits. Drafters write placeholders — `{{FIG: the relevant allocation rate}}` — and
the exact grounded value is **injected mechanically** from the matching source row. Unmatched
placeholders are dropped, never guessed; figures the drafter omitted entirely are appended by
construction. The model never touches a digit, so it cannot garble one.

### 6. Top-down coverage spine
Letting document structure *emerge* from bottom-up clustering is variance-prone: a whole
allegation category (often the figure-richest) can vanish run-to-run, and with no section there
is nowhere for its facts to go. Fix: extract the matter's **own enumerated allegations** and
make each a **guaranteed section**. Coverage stops depending on luck.

### 7. Matter classification + precise recruitment
The orchestrator was staffing a *securities* matter with *patent* analysts — it recruited on a
thin task description, and the practice area actually lives in the exhibits. Fix: **classify
the matter from its documents**, then recruit specialists on that classification, **one per
distinct allegation**, off a **single shared, deduped enumeration** that both recruitment and
the coverage spine consume (rolling two separate enumerations let them diverge — recruitment
would staff an allegation the spine had no section for). With decontaminated prompts (generic
fact-*types*, never the rubric's answers), this reached **28/60** — the honest, generalizable
peak of the "retrieve-it" era.

### 8. Paged writing-agent synthesis
28/60 was largely a *retrieval floor*: the facts were in the findings, but the synthesis step
— a compressing "stitch" that merged section drafts and removed "repetition" — silently dropped
whole allegations on the way to the document. The fix uses the platform's own **DyTopo writing
agents over the evidence blackboard**, with **context paging**: each section is authored from
the board, then **compacted** to a handle so it stops consuming context; later authors **uncompact**
any finished section on demand to stay consistent; final assembly uncompacts everything and
concatenates **losslessly**. Only the active section is ever full-size in context, so the
deliverable can far exceed the model's window — and nothing is dropped by construction. This
closed the synthesis leak (the Bellini kickback allegation, previously absent from every
output, now survives findings→deliverable).

### 9. Lite evidence graph
The residual misses were all **relational**: an entity was in the document but its *relationship*
or *attribution* was wrong — Crescent Bay named as a cherry-picking victim when it is the
directed-brokerage victim; Ostrowski's 40% ownership and Whitmore's $22.2M stake present in the
findings but never connected to the right party. Flat findings cannot hold relations, so a typed
graph does. A **two-pass, grounded extractor** (entity-anchored Pass 1 — explicitly
parenthetical- and omission-clause-aware, because a probe showed single-pass extraction drops
facts buried in "did not disclose … (a 40% owner)" constructions — then a relation Pass 2) builds
a per-matter graph. Every edge is **grounded**: if its quote is not verbatim in the source chunk
it is dropped, never kept (a wrong edge would *bake in* the very mis-attribution we are removing).
Facts route **per-section** by entity/allegation overlap, so a "victim-of → directed-brokerage"
edge cannot render under cherry-picking. This recovered the Whitmore 12%/$22.2M attribution
criteria that had been dead in every prior run.

### 10. BELO — an epistemic ontology, and a spine *discovered* not enumerated
Run-to-run variance turned out to have a single dominant cause: the **section spine** was an LLM
*enumeration* over retrieved passages, which non-deterministically grabbed policy boilerplate
(Form ADV "review of accounts" triggers) as "allegations" and dropped real ones (a whole
category would vanish), swinging the score ±10 and confounding every comparison. Enumeration is
the wrong primitive — the spine is a *latent structure to recover*, not a list to generate.

The fix is the **BigLaw Epistemic Legal Ontology (BELO)** (`internal/ontology/`): a three-layer
RDF/OWL ontology — typed domain classes + controlled predicates with domain/range; a reified
`Claim` carrying provenance, grounding, epistemic status, and Claim→Claim stance edges; analytic
concepts and derived defense issues. Extraction maps onto it: `Normalize()` canonicalises the
model's free-text relations and **re-orients reversed triples via domain/range** ("Section 206
violates <conduct>" → "<conduct> violates Section 206"). The spine is then the graph's typed
`Conduct` nodes — **discovered, not enumerated** — consolidated into categories. Run at
temperature 0 over the charging document (selected by allegation-language density, paged to a
token budget — allegations live in the charging doc, not the exhibits), it yields the **complete
six-category spine every run**. The ±10 wobble is gone.

### 11. What3Words figure handles
Even with figures extracted and routed to the right section, a weak drafter *abstracted them
away* — it wrote "an outsized share" instead of "$7,800,000", refusing the `{{FIG: description}}`
meta-placeholder (a construct it resists) and so emitting figureless prose. The fix borrows
What3Words: give each figure a **neutral codename** (Zephyr, Quasar…) the model drops into prose
like any word — LLM-native, and *inert* so it doesn't skew attention the way a descriptive
placeholder would. The figure is shown to the drafter **with its digit masked by the handle**, so
it never reads (and cannot garble) the number; the exact grounded value is substituted by exact
key afterward (no fuzzy matching). Figures now land verbatim across most sections.

## Two principles that fell out of it

**Retrieval floor, then intelligence.** The 28/60 "peak" was reachable by brute context-stuffing
— it is a *retrieval floor*, not a measure of reasoning. The right architecture guarantees that
floor (every fact the corpus holds reaches the findings) and *then* spends agent intelligence on
the analytical criteria stuffing can't reach. The two compound; they don't compete. The paged +
graph architecture already passes several analysis criteria that the dumb context-dump never did.

**Optimise on the weak model.** Every technique here was forced by a small local model's failure
modes. Architecture that makes a weak model accurate translates *up* to a strong one; a strong
model papers over the same logical gaps and teaches you nothing. The benchmark is run with the
rubric hidden from the agents (judge-only) — gains have to be real, not taught to the test.

## Honest status

This is criteria recovered across the architecture, not a passed task. A large share of the
run-to-run variance was traced to the section spine (technique 10) and removed: the spine is now
**discovered from the typed evidence graph, complete (six of six categories) and deterministic**,
rather than a stochastic enumeration. Figures land verbatim across most sections via the handle
mechanism (technique 11). The remaining known gap is narrow and located: a flagship section can
still under-state its figures when its handle list is **noise-heavy** — the salient harm figures
($8.2M, $438K, 4,217) buried among many small numbers from a statistical exhibit — so the drafter
fails to surface them. The next step ranks handles by salience (and guarantees the top ones land),
which is expected to close that gap without re-introducing variance. The benchmark is a climb, not
a pass; gains are measured with the rubric hidden from the agents.

## Update (2026-07): 34/60 — the same pipeline across model tiers

The build now measures across model tiers with the pipeline held constant (judge:
claude-sonnet-4-6 throughout, rubric hidden from the agents):

| Model on the BigLaw pipeline | Criteria /60 |
|---|---|
| qwen2.5:14b (local) | **27** |
| claude-haiku-4-5 | **34** (18.5 min wall time) |
| claude-sonnet-4-6 tier | **34** |

Two readings, honestly framed. First, the "optimise on the weak model" bet pays out upward: a
light cloud tier reaches 34/60 on orchestration built and debugged entirely against a 14B's
failure modes — the headline climb is now **0 → 30 → 34**. Second, the remaining criteria
concentrate in granular-fact extraction (the salience-ranking gap described in *Honest status*
above), which a model swap alone does not clear — the architecture, not the model, is the next
lever. The Sonnet run makes that second reading measurable: at roughly five times Haiku's cost
(3.8M tokens, ~$14 vs ~$2) it passes exactly the same 34 criteria — the pipeline saturates
above the Haiku tier, so every remaining criterion is architecture work, not model budget.
The task is still not passed; the climb continues.
