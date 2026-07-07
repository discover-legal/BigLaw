[Docs](../index.md) › Features › **Tabular review**

# Tabular review & verified citations

A multi-document × multi-column extraction matrix — the due-diligence grid — with a
**citation-verification ladder** behind every cell, so the export carries a verified tally
instead of an unverifiable wall of pinpoint cites.

## What it does

- **Matrix extraction** — up to **50 documents × 30 columns** per review, with RAG flags and
  per-cell reasoning (`tabular_review` agent tool).
- **Verified pinpoint citations** — every citation is checked up the ladder:
  **exact → tolerant → paraphrase judge → ensemble**, and each carries its verification
  *method* and *confidence*. The export shows the verified tally.
- **Persistence** — reviews persist via SQLite/Postgres; `read_table_cells` lets agents read
  any column/row slice of a persisted review.
- **Exports** — landscape `.docx` and CSV.

## Endpoints

```
GET /reviews/:id              tabular_review matrix as JSON — flags, reasoning, verified citations
GET /reviews/:id/table.csv    tabular_review matrix as CSV
GET /tasks/:id/table.csv      tabulate-workflow output as CSV
```

## In the workbench

Walk due-diligence grids with verification-state **citation pills** — click through to the
highlighted source quote in the underlying document.

The `biglaw demo` command (`go run ./biglaw-go/cmd/biglaw demo`) seeds documents and runs a
live tabular review as part of its tour.

Related: [Grounding & coverage](../architecture/grounding.md) · [The bench's tools](agent-tools.md) · [REST API](../integration/rest-api.md)
