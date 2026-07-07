[Docs](index.md) › Benchmarks & provenance › **Benchmarks**

# Benchmarks

## Harvey LAB — the accuracy ladder

Benchmarked on Harvey **LAB** (Legal Agent Benchmark, all-pass 60-criterion rubric, white-collar
SEC-referral task; judge claude-sonnet-4-6 throughout, rubric hidden from the agents). The
verified fix-wave ladder (every scored run is published under
[`benchmarks/runs/`](../benchmarks/runs/README.md)):

| Run | Criteria /60 |
|---|---|
| claude-haiku-4-5 — Harvey's own raw harness | 41 |
| release build — claude-haiku-4-5 / claude-sonnet-4-6 | 34 / 34 |
| resilience-wave build — qwen2.5:14b (local) | **39** |
| fix-wave build — claude-haiku-4-5, 6 rounds | 49 |
| compounded build — claude-haiku-4-5, 3 rounds | **50** |
| compounded build — GLM-5.2 (cross-vendor), fast, 3 rounds | **52** |

Read this as harness-vs-harness on the same model: the pipeline beats claude-haiku-4-5's raw
agent by **+9** (50 vs 41). Each rung is a technique, not a model swap: a criterion-level
forensics pass first rebuilt the extraction floor (34→49; the autopsy is in the repo's
record); a rounds ablation then showed that most of the debate rounds' value lands by round
3; and **re-entrant machinery** — letting a later round act on the entities, aliases, and
conducts an earlier round discovered, instead of every mechanical pass running once at round
0 before any understanding exists — plus a provider-resilience wave (call backoff, durable
agent recruitment across restarts, loud round-error signaling) unlocked a genuinely healthy
3-round measurement for the first time. That measurement surfaced one more finding: the
writer's own anti-fabrication guards (built to stop invented totals and template spray) were
over-broad and cutting a few *true* figures alongside the false ones — a targeted fix
(three-component-only subset sums, round-robin limitations joins) recovered them without
reopening the original fabrication holes. GLM-5.2 at **52** and the local **qwen2.5:14b at 39**
(a new record, on the resilience-wave build ahead of the writer fix — likely a floor, not a
ceiling) round out the cross-vendor picture. The pipeline's standing edge is *integrity*:
spot-checked citations verbatim 6/6 on the release run (11/12 on the 50-run, the one
truncation disclosed — record: [`benchmarks-citation-spotcheck.md`](benchmarks-citation-spotcheck.md)),
while the raw run fabricated statutory penalty figures — the numbers a firm actually gets
sanctioned over.

LAB scores a *task* 1.0 only on a perfect 60/60 — the task is **not yet passed**; the criterion
count, not the binary score, is the meaningful signal.

- **Technique-by-technique account** of how local-model verbatim grounding went from ~0% to 94%:
  [`local-accuracy-journey.md`](local-accuracy-journey.md)
- **Benchmark harness**: [`benchmarks/harvey-lab/`](../benchmarks/harvey-lab/README.md)
- **How grounding and coverage are engineered**: [Grounding & coverage](architecture/grounding.md)

## Go port vs TypeScript original

The Go backend serves the same route contract at 1.25×–6.9× the throughput of the retired
TypeScript implementation (autocannon, 50 concurrent connections; the Go numbers carry Docker
virtualization overhead the TS numbers don't). Full methodology and per-endpoint results:
[`benchmarks-go-vs-ts.md`](benchmarks-go-vs-ts.md).

## Compare mode — the trust-instruction task

The second scored LAB task (`compare-trust-documents-against-client-instructions`, 23 criteria,
same judge) exercises the deviation-detection path — compliance review rather than extraction.
Its ledger, one build at a time: claude-haiku-4-5 scored **6/23** pre-fix-wave, **9/23** on the
fix-wave build (the wave transfers without task-specific tuning), and **14/23** after the
deviation-path port — and the same ported build then took the *local qwen2.5:14b* from its old
record of 12/23 to **15/23**, the current task record: on compare-mode work the free local model
beats the cloud tier on the identical build. The port was evidence-led: the criterion diff showed the pipeline quoting
requirements without adjudicating them, and one document masking another in blended retrieval —
both fixed mechanically (per-document saturation retrieval, per-part verdicts, a grounded
numeric join). Deviation summaries now withhold any value that cannot be substring-verified
against source.

## Provenance of the numbers

Benchmark claims in this repo have been through a forensics pass — corrected claims and their
history are recorded in the [CHANGELOG](../CHANGELOG.md). Where a number was found to be a
measurement artefact (e.g. a two-run union coverage measure once misread as a single-run score),
the record says so.

Related: [Architecture overview](architecture/overview.md) · [Why BigLaw](why-biglaw.md)
