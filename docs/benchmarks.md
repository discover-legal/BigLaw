[Docs](index.md) › Benchmarks & provenance › **Benchmarks**

# Benchmarks

## Harvey LAB — the accuracy ladder

Benchmarked on Harvey **LAB** (Legal Agent Benchmark, all-pass 60-criterion rubric, white-collar
SEC-referral task; judge claude-sonnet-4-6 throughout, rubric hidden from the agents). The
verified ladder: claude-haiku-4-5 raw in Harvey's own harness scores 41/60; **on the BigLaw
pipeline the same model scores 49/60** — the pipeline beats the raw agent by eight criteria
after a criterion-level forensics pass rebuilt the extraction floor (the full autopsy and fix
history are part of the repo's record). A **local qwen2.5:14b scores 36/60** on the identical
build — a free, on-prem model within five criteria of a cloud model's raw performance. And the
pipeline's standing edge is *integrity*: spot-checked citations verbatim 6/6 against source,
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

## Provenance of the numbers

Benchmark claims in this repo have been through a forensics pass — corrected claims and their
history are recorded in the [CHANGELOG](../CHANGELOG.md). Where a number was found to be a
measurement artefact (e.g. a two-run union coverage measure once misread as a single-run score),
the record says so.

Related: [Architecture overview](architecture/overview.md) · [Why BigLaw](why-biglaw.md)
