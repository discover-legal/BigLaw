[Docs](index.md) › Benchmarks & provenance › **Provenance & licensing**

# License & attribution

BigLaw is distributed under the **Apache License, Version 2.0** ([`LICENSE`](../LICENSE)),
which includes an express patent grant from every contributor. Use it, modify it, embed it,
run it as a service — attribution per the [`NOTICE`](../NOTICE) file is all that is asked.

It builds on one upstream, fully attributed in [`NOTICE`](../NOTICE):

- **Lavern** ("The Shem") — agent definitions & prompts (Apache-2.0)

*"Lavern" and "The Shem" are the marks of their respective authors, used here only for attribution.*

## The clean-room reimplementation

The document-production and tabular-review tools are a **clean-room reimplementation**: the
previous copyleft-derived implementations were deleted from the tree *before* independent
reimplementation from a published functional specification
([`clean-room-spec-document-tools.md`](clean-room-spec-document-tools.md)), with signed
non-exposure attestations from the implementers. That process removed the last copyleft dependency
and is what made the Apache-2.0 relicense possible.

The spec is a dated paper trail — it is kept verbatim and is not edited.

## The TypeScript original

The platform was originally TypeScript; the Go port replaced it on `main` at version 1.0.0.
The retired TypeScript sources are preserved at the git tag `typescript-final`.

Related: [Benchmarks](benchmarks.md) · [CHANGELOG](../CHANGELOG.md)
