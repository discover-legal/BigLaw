[Docs](../index.md) › Integrate & extend › **Jurisdiction & NOSLEGAL**

# Jurisdiction routing

Specify `jurisdiction` when submitting a task to filter out jurisdiction-specific agents:

```bash
# Via MCP tool
submit_task(description="...", workflowType="roundtable", jurisdiction="UK")

# Via REST API
POST /tasks { "description": "...", "workflowType": "roundtable", "jurisdiction": "US-NY" }
```

Jurisdiction codes use BCP-47-style region tags: `US`, `US-NY`, `EU`, `UK`, `AU`, `SG`, `HK`, `IN`, `CA`, etc.

Filtering rule: an agent tagged `jurisdictions: ["US"]` is excluded from `EU`, `UK`, `AU`, etc. tasks.
Prefix-match: agent `["US"]` is included for task `"US-NY"`, `"US-CA"`. Jurisdiction-neutral agents
(most native agents) are always included regardless — the native bench is jurisdiction-neutral
and applies the governing law each matter specifies.

# NOSLEGAL taxonomy

Tasks carry **NOSLEGAL v4** multi-faceted taxonomy tags, auto-detected at submission time
(a light-tier classifier call); tags on documents are set on ingest:

```json
{ "areaOfLaw": "Corporate Finance", "workType": "Transactional", "sector": "Financial Services", "assetType": "Agreement" }
```

These complement (not replace) the canonical `practiceArea` and `documentType` fields. Use
them for interoperability with NOSLEGAL-compatible legal platforms — the controlled vocabulary
is at <https://github.com/noslegal/taxonomy>.

Aggregate breakdowns across all tasks are available at `GET /analytics/noslegal` (partner only).

Related: [REST API](rest-api.md) · [MCP / Claude Code](mcp.md)
