[Docs](../index.md) › Features › **Document production**

# Document production

Text in, *documents* out: cited briefs, `.docx` with tracked changes, generated PDFs, and
e-signature dispatch — all native BigLaw implementations built on its tool registry and
provider abstraction (clean-room history: [Provenance](../provenance.md)).

## Word documents

| Tool | What it does |
|---|---|
| `docx_generate` | Build a Word document — headings, prose, bullets, tables, landscape, page breaks |
| `edit_document` | **Tracked-changes redlining** of a `.docx` — minimal `<w:ins>`/`<w:del>` substitutions with smart-quote/whitespace-tolerant anchoring |
| `replicate_document` | Byte-for-byte `.docx` copies to adapt as templates |

## PDF

| Tool | Backend |
|---|---|
| `pdf_extract_text` · `pdf_extract_tables` · `pdf_ocr` · `pdf_generate` | PyMuPDF / Camelot / Tesseract via `scripts/pdf_tools.py` (requires Python 3.11+ and Tesseract for OCR) |

Retained document attachments (original images/PDFs kept from ingestion) can be **placed**
into generated PDFs, and any document can be rendered to PDF via
`GET /documents/export/:docId` — see [Models, persistence & documents](../deployment/models-and-persistence.md).

## E-signing (DocuSeal)

| Tool | What it does |
|---|---|
| `docuseal_send_for_signing` | Dispatch a document for e-signature |
| `docuseal_list_templates` | List available DocuSeal templates |
| `docuseal_submission_status` | Poll a submission's status |

DocuSeal runs locally via `docker-compose.yml`; the admin panel toggles it live.

## DOCX/CSV exports elsewhere

Tabular reviews export landscape `.docx` + CSV ([Tabular review](tabular-review.md));
LPM status reports export DOCX (`GET /reports/:id/docx`, [Billing, LPM & monitors](billing-and-lpm.md));
counter-redlines emit a `.response.docx` ([Negotiation stack](negotiation.md)).

Related: [The bench's tools](agent-tools.md)
