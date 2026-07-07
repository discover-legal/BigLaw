# Apache-release deck — LinkedIn carousel PDF

Slide sources for `collateral/apache-release-deck.pdf` (LinkedIn renders multi-page PDFs as
swipeable carousels). One self-contained HTML file — system fonts, inline SVG grain, CSS/SVG
charts, zero external fetches. 15 pages at 1080 × 1350 px (portrait).

Every number is verified against primary sources: `scores.json` / `metrics.json` per run under
`harvey-labs/results/`, `CHANGELOG.md`, and `docs/benchmarks.md`. Benchmark slides carry the
footnote *judge: claude-sonnet-4-6 · rubric hidden from agents · all runs published in-repo*.
Costs are per-run ledger figures (judge pass excluded); the Harvey-harness baseline and the
GLM-5.2 run (Z.ai plan-billed, no per-run invoice) are computed from their tokens at list
rates and labeled *est.* — the GLM figure is the uncached upper bound, since the ledger
does not capture Zhipu's cache split.

## Re-render

```bash
cd collateral/apache-release-deck
npm i playwright pdf-lib          # anywhere on the module-resolution path from render.mjs,
                                  # or set PLAYWRIGHT_DIR=/path/to/node_modules
node render.mjs                   # → ../apache-release-deck.pdf
node render.mjs --shots ./shots   # + per-slide PNGs for visual QA
```

Uses Playwright's Chromium PDF printing; if the bundled Chromium isn't installed
(`npx playwright install chromium`), it falls back to the system Edge/Chrome channel.
The script asserts no slide overflows 1080 × 1350 and verifies the PDF's page count and size.

## Slide map

| # | Exhibit | Slide |
|---|---|---|
| 1 | A | **Hook** (dark) — "BigLaw is now Apache-2.0" + the one-line what-it-is |
| 2 | B | **The license** — AGPL blocked firm pilots; the clean room: deleted first → published spec → attested implementers; SPDX on 271 files |
| 3 | C | **What shipped** — counter-redlining, Redtime, Integrity Check, verified citations, reviews UI, `biglaw demo`, BELO |
| 4 | D | **Counter-redlining** — parse markup → judge vs four-tier playbook → countered redlines + rationale cards; judge memory, standoff escalation |
| 5 | E | **Verified citations** — the ladder (exact → tolerant → judge → 3-vote ensemble); "Citations verified: N/M" stamp; 6/6 verbatim vs fabricated penalty figures |
| 6 | F | **The ladder chart** (SEC task, /60) — 28 → 34 → 36 → 37 → 41 (Harvey's harness) → 49 → 50† → **51\* GLM-5.2 fast** (best of 2, repeat 47, one round inactive in both); †the 3-round 50 also starved its analysis round (credit outage), healthy 6-round = 49; +9 same-model harness-vs-harness; three vendors, one pipeline |
| 7 | G | **Cost table** — 41/$1.40 est. vs **51\*/$5.47 est./2 h 20** vs 50 (round starved)/$7.92 vs 49/$11.48 vs 34/$13.70 vs 36/~$0; the new top score is also the cheapest 50+ run; Haiku config curve 41/$3.74/40 min → 48/$5.42/40 min → 50/$7.92/75 min → 49/$11.48 — three matches six, confounded by the 3-round starve |
| 8 | H | **Local-model story** — same quantized 14B, 28 → 36; within 5 of the cloud model in Harvey's harness, for electricity |
| 9 | I | **Compare mode** (trust task, /23) — 6 → 9 → 12 (old record) → 14 → **15, local qwen, new record**; the evidence-led port |
| 10 | J | **How 1: grounding** — substring-lock + figure handles; ≈0% → 94% verbatim citations |
| 11 | K | **How 2: the funnel** — saturation harvest, full-text ingest, context-aware caps; 34 → 49 |
| 12 | L | **How 3: mechanical honesty** — cross-doc joins, defense lenses, starvation guards; the voided 27/60 and the invalidated GLM-5.2 thinking 7/60 (14 h, operator-interrupted, 24.7M tokens, malformed deliverable) |
| 13 | M | **Knobs** — context-aware caps, timeout retry, task quarantine; "tuned for a Pi by default" |
| 14 | N | **Honest status** — 51\*/60, `score: 0.0`, all-or-nothing rubric; best-of-2 caveat on the face; corrections published; judge named |
| 15 | O | **CTA** (dark) — `$ biglaw demo` (~$0.03), Apache-2.0, repo |
