[Docs](../index.md) › Features › **Negotiation stack**

# The negotiation stack

Three capabilities that turn redline ping-pong into an instrumented process: the
**counter-redline loop**, **Redtime** negotiation timelines, and the **Integrity Check**
on inbound documents.

## Counter-redline loop

Opposing counsel's tracked changes are parsed, judged clause-by-clause against the
[playbook cascade](playbooks.md), and answered with countered redlines plus a per-change
rationale card.

- Agent tool: `respond_to_redline` — parses the opposing `.docx`'s tracked changes, judges
  each change against the resolved playbook position, and emits a `.response.docx` with
  countered redlines + rationale cards.
- **Judge memory across rounds** — the judge remembers prior rounds of the same negotiation
  and escalates standoffs instead of re-litigating the same clause from scratch.

## Redtime — negotiation timelines

Per-clause timelines across negotiation rounds, with silent-edit detection and playbook drift.

- Agent tools: `register_document_version` (add a `.docx` to a version lineage) and
  `get_redline_timeline` (the per-clause history of that lineage).
- REST: `GET /documents/:id/timeline` — the Redtime per-clause redline timeline of a version
  lineage; the workbench renders it as a timeline view.
- **Silent-edit detection** — a change that appears between versions *without* tracked-changes
  markup is flagged.
- **Playbook drift** — each clause's trajectory is scored against the playbook position, so
  you can see a negotiation drifting away from your standard.

## Integrity Check

Inbound documents are not taken at face value. On every ingest (and on demand via the
`check_document_integrity` tool):

- **Unicode-obfuscation scan** — homoglyphs, zero-width characters, bidirectional-override
  characters.
- **Unmarked-change detection** — text that changed without tracked-changes markup.

## Trying it

`go run ./biglaw-go/cmd/biglaw demo` runs an end-to-end tour that finishes with a live
counter-redline (~$0.03 in model calls). The workbench's Redtime view shows the timelines.

Related: [Playbooks & redlining](playbooks.md) · [Document production](document-production.md) · [The bench's tools](agent-tools.md)
