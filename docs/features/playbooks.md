[Docs](../index.md) › Features › **Playbooks & redlining**

# Playbook cascade

BigLaw ships a four-tier playbook system that replaces Contract Express, Practical Law Standard Docs,
and any precedent library that charges per user:

```
client (3) > matter (2) > personal (1) > firm (0)
```

Client requirements win; firm defaults are the market-standard baseline. A playbook at a higher
priority level overrides the corresponding clause or preference from any lower level.

```bash
# Build a firm-level fallback playbook from the knowledge store (partner only)
POST /playbooks/build { "scope": "firm", "practiceArea": "Commercial Contracts", "name": "Standard NDA positions" }

# Resolve which clause position wins across the cascade
GET /playbooks/resolve/limitation_of_liability?clientId=C-001&matterNumber=M-001

# List / inspect / delete
GET /playbooks          GET /playbooks/:id          DELETE /playbooks/:id
```

## Playbook-aware redlining

`POST /redline` (partner only) runs the playbook-aware contract redline engine
(`biglaw-go/internal/redline/` — the Definely / Kira replacement): the contract is reviewed
clause-by-clause against the resolved cascade position and returned as a redline.

The same cascade drives the [counter-redline loop](negotiation.md) — opposing counsel's
tracked changes are judged against the resolved playbook position for each clause.

Related: [Negotiation stack](negotiation.md) · [Research engines](research-engines.md) · [REST API](../integration/rest-api.md)
