# Clean-room reimplementation — executed attestation record

**Companion to** [`clean-room-spec-document-tools.md`](clean-room-spec-document-tools.md)
(the specification; its §10 contains the attestation template these records execute).
**Nature of the record — read this first:** the "clean parties" were isolated AI agent
sessions (Claude, via Claude Code), each launched with a fresh context that contained the
specification and the permitted-materials list and nothing else of the prior work. Isolation
was structural, not merely promised: the Mike-derived files were deleted from the tree in a
dated commit (`47d83c3`, 2026-07-01) **before any implementing session was created**, each
session worked in its own git worktree, and each was instructed that consulting the deleted
files via git history, the `typescript-final` tag, or the Mike repository was prohibited.
Each session's closing report contained its attestation and any exposure disclosures,
reproduced verbatim below. The orchestrating session verified commit ordering and merged the
work; the project owner reviewed and approved the release. This record states plainly what a
signature line cannot: the attesting parties are AI sessions, the enforcement was
architectural, and the disclosures are included unedited.

---

## Clean party A — OOXML core, `docx_generate`, `replicate_document`, `edit_document`
Worktree branch `worktree-agent-aa4b92be4b9eb7137` · commit `aec139b` (2026-07-01), merged `5bdea5f`

> "I did not access the Mike project, the deleted derived Go files (deleted them without
> reading), the `typescript-final` tag, or any git history of those paths; all code, tool
> descriptions, and tests were authored from the spec, the permitted BigLaw files, and the
> public OOXML standard. No tool output surfaced forbidden content at any point."

Disclosure: A's worktree had spawned from a pre-deletion tree state; A **deleted the
contaminated files unread** and included those deletions in its commit.

## Clean party B — `tabular_review`, `read_table_cells`
Worktree branch `worktree-agent-ae6e0044f61d89a31` · commit `6278b0d` (2026-07-01), merged `bef4922`

> "I did not access Mike's source, the forbidden derived files (`docx.go`, `docx_test.go`,
> `trackedchanges.go`, `trackedchanges_test.go`, `tabular.go`, `workflows/mikeoss/`), the
> `typescript-final` tag, or any git history; all code, prompt wording, descriptions, and
> tests were authored from the spec. One incidental exposure to disclose: a repo-wide grep
> for the tool names returned five call-site lines from `docx_test.go` (a
> `registerTabularTools()` call and two `reg.tools[...].Exec` invocations, including a
> `review_id:\"nope\"` argument) before I narrowed the search — no implementation logic,
> return-shape handling, or prompt text was seen, and nothing from it informed the design."

Reviewer assessment of the disclosure: the exposed lines are call signatures of the frozen
public interface the specification itself mandates (§1 preserves exact tool names and
schemas); they contain no protectable expression from the derived implementation.

## Clean party C — native workflow templates, provenance scrub
Main tree · commit `35b008b` (2026-07-01)

> "I did not view Mike's source, mikeoss.com, the deleted Mike-derived files,
> `workflows/mikeoss/mike-workflows.json`, or the `typescript-final` tag — all template
> prompt wording was authored fresh from spec §6. (One repo-wide grep incidentally matched
> attribution *description* lines of the deleted file inside other agents' worktrees; no
> prompt text was displayed or read.)"

## Review

- **Ordering verified in git:** deletion `47d83c3` (2026-07-01 16:51) is a strict ancestor
  of every reimplementation commit (`aec139b` 17:33, `6278b0d` 17:32, merges
  `5bdea5f`/`bef4922`, `35b008b`).
- **Resemblance check:** implementations reviewed against the specification's contracts;
  the two incidental exposures assessed above and judged immaterial (interface-only).
- **Reviewed by:** the orchestrating session (this record's author), 2026-07-01–02;
  release approved by the project owner (hordruma), 2026-07-02, effected in the
  relicensing commit `a42e685`.
