[Docs](../index.md) › Features › **The bench's tools**

# The bench's tools

Agents act through a typed tool registry (`biglaw-go/internal/tools/`). Highlights:

| Tool | What it does |
|---|---|
| `search_knowledge` · `read_document` · `list_documents` | Semantic + full-text retrieval over the knowledge base |
| `find_in_document` | Whitespace-tolerant Ctrl+F with cited context windows |
| `extract_from_document` | Structured extraction — parties, dates, amounts, obligations, defined terms |
| `fetch_documents` | Fetch up to 20 documents by ID in one call |
| `query_memory` | Query the inter-round memory store |
| `tabular_review` | Multi-doc × multi-column extraction matrix with RAG flags + **verified** pinpoint citations (exact → tolerant → paraphrase judge → ensemble ladder; 50 docs × 30 columns), persisted via SQLite/Postgres with landscape `.docx` + CSV export — see [Tabular review](tabular-review.md) |
| `read_table_cells` | Read any column/row slice of a persisted review |
| `respond_to_redline` | **Counter-redline loop** — parse opposing counsel's tracked changes, judge each against the playbook cascade, emit a `.response.docx` with countered redlines + rationale cards — see [Negotiation stack](negotiation.md) |
| `register_document_version` · `get_redline_timeline` | **Redtime** — document lineage across negotiation rounds; per-clause timelines, silent-edit detection, playbook drift |
| `check_document_integrity` | **Integrity Check** — Unicode-obfuscation scan (homoglyphs · zero-width · bidi) + unmarked-change detection |
| `docx_generate` | Build a Word document (headings, prose, bullets, tables, landscape, page breaks) |
| `edit_document` | **Tracked-changes redlining** of a `.docx` — minimal `<w:ins>`/`<w:del>` substitutions with smart-quote/whitespace-tolerant anchoring |
| `replicate_document` | Byte-for-byte `.docx` copies to adapt as templates |
| `pdf_extract_text` · `pdf_extract_tables` · `pdf_ocr` · `pdf_generate` | PyMuPDF / Camelot / Tesseract backend (`scripts/pdf_tools.py`) |
| `docuseal_send_for_signing` · `_list_templates` · `_submission_status` | DocuSeal e-signature dispatch + status |
| `web_search` · `translate` · `citation_check` | Tavily search, translation, source verification |
| 7 `clio_*` tools | Clio matters, documents, contacts, notes, activities |
| 32 connector tools | CourtListener · Westlaw · Everlaw · Trellis · Descrybe · Ironclad · iManage · Definely · DocuSign CLM · Lawve AI · Solve Intelligence · Google Drive · Box · Slack · TopCounsel — see [Connectors](../integration/connectors.md) |

## Heavier engines — REST, not agent tools

The heavier engines are exposed over REST rather than as agent tools:

| Engine | Endpoint |
|---|---|
| Court deadline calculator — FRCP / UK CPR / EU Competition, with citations | `POST /deadlines/compute` — see [Court deadlines](deadlines.md) |
| Playbook-aware contract redlining | `POST /redline` — see [Playbooks & redlining](playbooks.md) |
| Headnote extraction (Westlaw Key Number / LexisNexis replacement) | `POST /headnotes/generate` — see [Research engines](research-engines.md) |
| Precedent generation (Practical Law / PSL replacement) | `POST /precedents/generate` |
| Citation checking (CourtListener-backed KeyCite/Shepard's replacement) | `GET`/`POST /citations/check` |
| Tabular review output (tabulate workflow) | `GET /tasks/:id/table.csv` |
| Daily status reports as DOCX (LPM spine) | `GET /reports/:id/docx` — see [Billing, LPM & monitors](billing-and-lpm.md) |

> Document generation, tabular review, and tracked-change redlining are native
> BigLaw implementations built on its tool registry and provider abstraction —
> see [Provenance](../provenance.md) for the clean-room history.

Related: [Architecture overview](../architecture/overview.md) · [REST API](../integration/rest-api.md)
