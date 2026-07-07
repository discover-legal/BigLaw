[Docs](../index.md) › Features › **Research engines**

# Research engines — citations, headnotes, precedents

Three engines that replace the per-seat research subscriptions. All three are exposed over
REST rather than as agent tools.

## Citation engine

`biglaw-go/internal/citations/` — a CourtListener-backed **KeyCite / Shepard's replacement**.

```
GET/POST /citations/check
```

Checks citations against CourtListener (free, public API — an optional
`COURT_LISTENER_API_KEY` raises rate limits). Agents also carry a lighter `citation_check`
tool for in-round source verification, and the **CitationGate** protocol rejects any finding
whose source isn't in the registry — see [Architecture overview](../architecture/overview.md).

## Headnote engine

`biglaw-go/internal/headnotes/` — headnote extraction from case opinions, the
**Westlaw Key Numbers / LexisNexis headnote replacement**.

```
POST /headnotes/generate        (partner only)
```

## Precedent generator

`biglaw-go/internal/precedent/` — precedent document generation from the knowledge store +
playbooks, the **Practical Law Standard Docs / PSL replacement**.

```
POST /precedents/generate       (partner only)
```

Related: [Playbooks & redlining](playbooks.md) · [Connectors](../integration/connectors.md) · [Why BigLaw](../why-biglaw.md)
