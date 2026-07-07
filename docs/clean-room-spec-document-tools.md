# Clean-Room Specification — Document-Production & Tabular-Review Tools

**Status:** Draft for reviewer scrub
**Spec author:** (contaminated party — has read the existing derived implementation; authors this spec only, writes no implementation code)
**Audience:** independent implementer (clean party) + clean-room reviewer
**Goal:** Re-implement five tools and three workflow templates natively in Go, from this
specification alone, so that the result carries no copyright derived from *Mike*
(github.com/willchen96/mike, AGPL-3.0) and BigLaw becomes relicensable.

---

## 0. Clean-room rules (read first)

This document contains **only** functional behavior, interface contracts, public-standard
pointers, and acceptance criteria. It deliberately contains **no** source excerpts, code
structure, comment text, or verbatim prompt wording from Mike or from BigLaw's current
(derived) implementation.

**The implementer MAY read / use:**
- This specification.
- The public **ECMA-376 (Office Open XML)** standard and the **Open Packaging Conventions**
  (the `.docx` = ZIP-of-XML format, and the `<w:ins>` / `<w:del>` tracked-change markup).
- BigLaw's own, independently-authored code:
  - `internal/lpm/docx.go` — a minimal dependency-free OOXML writer (Discover Legal copyright).
  - The tool-registration pattern: `ToolImpl`, `Registry.Register`, `providers.ToolParam`.
  - The provider / routing / cost / config interfaces named in §7.

**The implementer MUST NOT read / use:**
- Mike's source at `github.com/willchen96/mike`.
- The current derived Go files: `internal/tools/docx.go`, `internal/tools/trackedchanges.go`,
  `internal/tools/tabular.go`, and their existing test files.
- The TypeScript at the `typescript-final` tag for these features.
- Any verbatim prompt strings carried over from Mike (the extraction system prompt; the three
  built-in workflow prompts). The implementer authors all prompt wording fresh from the
  *requirements* in §5 and §6.

The reviewer scrubs this spec before handover, obtains a non-exposure attestation from the
implementer (template in §10), checks the finished code for resemblance to Mike, and keeps the
dated paper trail.

---

## 1. Scope & deliverables

Re-implement, preserving the exact tool **names**, **input schemas**, and **return shapes**
below (these are stable interfaces the rest of BigLaw already depends on):

| Tool | Purpose |
|---|---|
| `docx_generate` | Build a `.docx` from structured content (headings, prose, bullets, tables; landscape; page breaks). |
| `replicate_document` | Make byte-for-byte `.docx` copies for use as templates. |
| `edit_document` | Apply minimal substitutions to a `.docx` as Word tracked changes. |
| `tabular_review` | Document × column extraction matrix with RAG flags + pinpoint citations. |
| `read_table_cells` | Read a column/row slice of a persisted `tabular_review` result. |

Plus three native workflow templates (§6) to replace the deleted `workflows/mikeoss/` set.

A shared OOXML package (§2) underpins the first three tools.

---

## 2. Shared OOXML core (`internal/ooxml`, new)

A `.docx` is a ZIP archive (Open Packaging Conventions) containing at minimum:
`[Content_Types].xml`, `_rels/.rels`, and `word/document.xml`. `internal/lpm/docx.go` already
demonstrates BigLaw's clean approach to writing these parts; generalize that technique into a
reusable package supporting:

**Writing (new documents):**
- Paragraph styles: heading levels 1–3, normal prose paragraphs, bullet-list items.
- Tables: a header row plus N body rows, each row a fixed cell count; borders on all cells.
- Section/page controls: explicit page breaks; document-level orientation = portrait or
  **landscape** (set page size + margins accordingly via the section properties element).
- All text XML-escaped for the five predefined entities.

**Tracked-change runs (for `edit_document`):**
- Inserted text wrapped in an insertion run (`<w:ins w:author w:date w:id>` around a run).
- Deleted text wrapped in a deletion run (`<w:del …>` with the deleted text as `<w:delText>`).
- A monotonic revision-id counter; an author name and an ISO-8601 timestamp on each revision.
- Output must open cleanly in Word / LibreOffice with the changes shown as reviewable redlines.

**Reading / round-trip (for `edit_document`):**
- Unzip an existing `.docx`, read `word/document.xml`, locate text within paragraph runs,
  perform run-level surgery (see §4), and re-zip preserving the other parts and their order.

> Optional: have `internal/lpm/docx.go` delegate to this package to remove duplication. Not required.

---

## 3. `docx_generate` and `replicate_document`

### 3.1 `docx_generate`

**Description (for the tool schema):** Generate a Word (.docx) legal document from structured
content (headings, prose, bullet lists, tables). Supports landscape orientation and page
breaks. Returns the output file path.

**Input schema** (`object`):
- `title` *(string, required)* — document title; used as the H1 and, by default, the filename.
- `filename` *(string, optional)* — output filename; defaults to a slug of the title.
- `landscape` *(boolean, optional)* — landscape orientation when true.
- `sections` *(array, required)* — each item is an object:
  - `heading` *(string)* — optional section heading.
  - `level` *(integer)* — heading level, clamped to 1–3.
  - `content` *(string)* — prose; paragraphs separated by blank lines; lines beginning with
    `"- "` render as bullets.
  - `pageBreak` *(boolean)* — start this section on a new page.
  - `table` *(object)* with `headers` *(string[])* and `rows` *(string[][])*, both required when
    `table` is present.

**Behavior:**
- Empty/missing `title` → default to `"Legal Document"`.
- For each section: emit the page break (if set) → heading (if set, clamped level) → prose
  (blank-line-split paragraphs, `"- "` lines as bullets) → table (header row + rows) followed by
  a spacer.
- A table with no headers is skipped.

**Path safety (critical):** Always write into the configured output directory
(`cfg.PDF.OutputDir`); ignore any caller-supplied directory. Resolve to an absolute path and
verify it stays within the output root (reject traversal). Create the directory if missing.
Filename is a slug of `filename` or `title`, extension forced to `.docx`.

**Return (object):** `outputPath`, `filename`, `sectionCount`, `landscape`, `fileSizeBytes`.

### 3.2 `replicate_document`

**Description:** Make byte-for-byte copies of an existing `.docx` as new files (so the caller can
adapt copies as templates without touching the original). Returns the new paths so they can be
fed straight into `edit_document`.

**Input schema:** `path` *(string, required)*, `count` *(integer, default 1, clamp 1–20)*,
`new_filename` *(string, optional, base name; extension forced to `.docx`)*.

**Behavior:**
- Resolve `path` via the same output-dir resolver (absolute, or relative to the output dir);
  reject non-`.docx`.
- Read the source bytes; write `count` identical copies. For `count > 1`, disambiguate filenames
  (e.g. a ` (n)` suffix). Create the directory if missing.

**Return:** on success `{ ok: true, count, copies: [{ path, filename }] }`; on any failure
`{ ok: false, error }` (return the error in the payload, do not throw).

---

## 4. `edit_document` (tracked-changes redlining)

**Description:** Propose edits to a `.docx` as Word tracked changes. Each edit is a precise,
minimal substitution of specific words/characters (not whole-paragraph replacement), anchored
with short before/after context so it can be located unambiguously. Operates on a `.docx` by
path (e.g. one from `docx_generate`). Writes a new redlined `.docx` and returns per-edit
annotations plus the output path.

**Input schema:**
- `path` *(string, required)* — `.docx` to edit (absolute or output-dir-relative).
- `author` *(string, optional)* — tracked-change author; default `"Big Michael"`.
- `edits` *(array, required)* — each item:
  - `find` *(string, required)* — exact substring to replace; keep as short as possible.
  - `replace` *(string, required)* — replacement; empty string = pure deletion.
  - `context_before` *(string, required)* — ~40 chars immediately preceding `find`.
  - `context_after` *(string, required)* — ~40 chars immediately following `find`.
  - `reason` *(string, optional)* — short explanation for the change card.

**Anchoring algorithm (design independently; this is the functional requirement):**
The job is to locate `find` unambiguously within the document's text, even though Word splits
text across multiple runs and may use smart quotes / irregular whitespace. Requirements:
- Match `find` **in context**: the occurrence whose surrounding text best matches
  `context_before` + `context_after`. Prefer a unique exact contextual match; fall back through
  progressively more tolerant strategies (e.g. normalize curly→straight quotes and collapse
  whitespace; then context-only localization) before giving up.
- Operate across run boundaries: the matched span may begin in one run and end in another;
  reconstruct the affected runs so the deletion covers exactly the matched characters and the
  insertion carries the replacement, leaving untouched text in its original runs.
- A pure deletion (`replace` == "") emits only a `<w:del>`; a pure insertion conceptually emits
  only `<w:ins>`; a substitution emits a delete of the old span followed by an insert of the new.
- Each applied edit gets the author + timestamp + a fresh revision id.

**Behavior & limits:**
- Reject non-`.docx`; reject empty `edits`.
- Apply each edit independently; collect successes and failures separately (one unfindable
  anchor must not abort the rest).
- Write to a sibling file named `<stem>.redlined.docx` in the same directory.

**Return:** `{ ok: true, outputPath, appliedCount, errorCount, annotations: [...], errors: [...] }`.
`annotations` describes each applied change (enough for a UI change card: the find/replace, the
reason, the author). `errors` lists edits that could not be anchored, with a short cause. On a
top-level failure (file unreadable, etc.) return `{ ok: false, error }`.

---

## 5. `tabular_review` and `read_table_cells`

### 5.1 `tabular_review`

**Description:** Run a tabular review across one or more documents. Define columns (each a
question/field to extract); for every document × column the tool extracts a cited answer with a
RAG flag (green/grey/yellow/red) and reasoning. Returns a matrix suitable for due-diligence, CP
checklists, or comparison tables.

**Input schema:**
- `documentIds` *(string[], required)* — knowledge-store document IDs = rows; cap at **50**.
- `columns` *(array, required)* — each `{ name (string), prompt (string) }`; cap at **30**.

**Behavior:**
- Validate inputs and the presence of a knowledge store; if missing, return a structured error
  with an empty `rows` array (do not throw).
- Select the extraction-tier model via `routing.SelectModel(..., TaskExtraction)` and resolve a
  provider (§7).
- For each document: fetch full text from the knowledge store. If not found, every cell for that
  row is a `grey` "document not found" cell. Otherwise run **one model call per cell**, the calls
  for a row issued concurrently, each given: the document text (truncated to a per-doc character
  cap, ~120k) and the column's extraction prompt.
- Record per-call cost against the task (§7).
- Parse each model response into `{ summary, flag, reasoning }` (see prompt requirements below).
  On a failed/garbled cell, produce a `grey` "extraction failed" cell with the error in
  `reasoning` — never abort the matrix.
- Tally flags across all cells. Persist the full matrix in an in-memory store keyed by a freshly
  generated `reviewId` so `read_table_cells` can slice it later in the run (guard with a mutex).

**Extraction prompt — requirements only (author the wording fresh):**
- Instruct the model to return **only** a JSON object: `{ "summary": string, "flag": string,
  "reasoning": string }`.
- `flag` ∈ `green | grey | yellow | red`, meaning: green = clearly addressed / favourable;
  grey = not addressed / not found; yellow = present but qualified, unusual, or needs review;
  red = problematic, onerous, or non-market.
- `summary` holds only the extracted value with inline citations and no explanation; every
  factual claim is immediately followed by a citation in the format
  `[[page:N||quote:exact short verbatim excerpt]]` (≤ ~25 words per quote, narrowly scoped; do
  not reuse one long quote across claims).
- All explanation/justification goes in `reasoning` only.
- If the field is not found, `summary` states "Not Found" and `flag` is `grey`.

**Return (object):**
- `reviewId` *(string)*.
- `columns` *(string[])* — column names in order.
- `rows` *(array)* — each `{ documentId, document, cells: [{ column, summary, flag, reasoning }] }`,
  cells in column order.
- `flagTally` *(object)* — count per flag value.
- `legend` *(object)* — the four flag meanings above.

### 5.2 `read_table_cells`

**Description:** Read extracted cells from a prior `tabular_review` by its `review_id`. Pass
`col_indices` and/or `row_indices` (0-based) to read a subset; omit either to read all columns or
all rows.

**Input schema:** `review_id` *(string, required)*, `col_indices` *(integer[], optional)*,
`row_indices` *(integer[], optional)*.

**Behavior:** Look up the persisted review; if absent, return `{ ok: false, error }` instructing
the caller to run `tabular_review` first. Otherwise slice rows/columns by the given 0-based
indices (out-of-range indices ignored), returning the selected cells with their column name,
summary, flag, and reasoning.

---

## 6. Native workflow templates (replace `workflows/mikeoss/`)

Author three native `templates/*.json` task templates (BigLaw's `TaskTemplate` shape; auto-loaded
from `templates/`). **Write all prompt text fresh** from the functional outlines below — do not
reuse the deleted Mike workflow wording. Each should reference `docx_generate` for output where a
document deliverable is implied, and may use `{{description}}` / `{{document}}` placeholders.

1. **Conditions-Precedent checklist** — from a credit/financing agreement, produce a categorized
   CP checklist as a **landscape** `.docx`. Per category: a heading + a four-column table
   (Index, Clause Number, Clause, Status), Status left blank, Index sequential within category.
2. **Credit-agreement summary** — structured legal summary of a credit agreement: parties/lenders,
   facilities, key economic terms, covenants, events of default, security, unusual/non-market
   terms; cite clause/schedule references.
3. **Shareholders'-agreement summary** — structured summary of an SHA: parties, share capital,
   board/governance, reserved matters, transfer restrictions, drag/tag, exit, unusual terms.

Then delete `workflows/mikeoss/`, the `MikeOSSWorkflow` type + `LoadMikeOSSWorkflows` loader in
`internal/adapters/adapters.go`, and its wiring in `cmd/biglaw/main.go`.

---

## 7. Integration points (BigLaw-internal, clean)

The implementer wires the tools into the existing registry exactly as the other tools do:
- Register via `Registry.Register(...)` inside `registerDocxTools` / `registerTrackedChangesTools`
  / `registerTabularTools`, called from the registry constructor (already wired).
- `ToolImpl{ Name, Schema: providers.ToolParam{...}, Exec: func(input map[string]interface{},
  ctx agents.ToolContext) (interface{}, error) }`.
- Model selection: `routing.SelectModel(cfg, routing.SelectParams{TaskType: routing.TaskExtraction})`,
  then `provReg.Get(model)` → `prov.Chat(providers.ChatParams{...})`.
- Cost: record per model call against `ctx.TaskID` using the existing cost helper.
- Config: output directory is `cfg.PDF.OutputDir` (default `./output/documents`).
- Knowledge store: `ctx.KnowledgeStore.GetFullText(docID)`.

---

## 8. Acceptance criteria (behavioral tests to author fresh)

1. **docx_generate** — given a title + sections (heading, prose with a `"- "` bullet, a table),
   writes a `.docx` under the output dir; the file is a valid ZIP containing
   `[Content_Types].xml`, `_rels/.rels`, `word/document.xml`; return reports a non-zero
   `fileSizeBytes` and the right `sectionCount`.
2. **landscape** — `landscape: true` yields landscape section properties.
3. **generate → edit round-trip** — a generated `.docx` can be edited by `edit_document` with a
   simple substitution; `appliedCount == 1`, `errorCount == 0`, output is `<stem>.redlined.docx`
   and remains a valid `.docx` with one insertion + one deletion revision.
4. **edit anchoring tolerance** — a `find` spanning two runs, and one differing only by
   curly-vs-straight quotes, both resolve and apply.
5. **edit miss** — an unfindable anchor lands in `errors`, not `annotations`, and does not abort
   other edits in the same call.
6. **replicate_document** — `count: 3` produces three distinct files; non-`.docx` input returns
   `{ ok: false }`.
7. **path traversal** — a `path`/`filename` attempting to escape the output dir is rejected.
8. **tabular_review** — two docs × two columns yields a 2×2 matrix; each cell has a valid flag;
   `flagTally` sums to 4; missing doc id → `grey` row.
9. **read_table_cells** — slicing by `col_indices`/`row_indices` returns the right subset; an
   unknown `review_id` returns `{ ok: false }`.

---

## 9. Provenance & headers

- New files carry `// SPDX-License-Identifier: AGPL-3.0-only` (or the project's chosen identifier
  at relicense time) and `// Copyright (C) 2026 Discover Legal` — **no** "ported from Mike" notes.
- Remove the Mike entries from `NOTICE`, `README.md` (the document-tools note and attribution
  line), and `CHANGELOG.md` as part of cleanup.
- Fix the two comments that reference `adapters/lavern.ts` (`adapters.go`, `agents/base.go`) so
  they describe the function rather than the TS origin (cosmetic; not a Mike issue).

---

## 10. Implementer non-exposure attestation (reviewer keeps signed)

> I, ________________________, confirm that in implementing the document-production and
> tabular-review tools described in this specification I did not consult, view, or copy from:
> (a) the Mike project source (github.com/willchen96/mike) in any form; (b) BigLaw's prior
> implementation of these tools (`internal/tools/docx.go`, `trackedchanges.go`, `tabular.go`, or
> their tests); (c) the corresponding TypeScript at the `typescript-final` tag. All code and
> prompt wording I wrote was authored from this specification and the public OOXML standard.
>
> Signed: ____________________  Date: __________
