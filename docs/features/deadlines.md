[Docs](../index.md) › Features › **Court deadlines**

# Court deadline calculator

`biglaw-go/internal/deadlines` — pure Go, no external service required. Rule sets are YAML
files in `deadlines/rules/` at the repo root, loaded at startup.

Feed it a trigger event and date; it returns every downstream deadline under the applicable rule set, calendar vs business days computed correctly, jurisdiction holidays applied, with the procedural citation for each.

```bash
curl -X POST http://localhost:3101/deadlines/compute \
  -H "Content-Type: application/json" \
  -d '{ "jurisdiction": "us-federal-frcp", "triggerEvent": "complaint_served", "triggerDate": "2026-09-01" }'
# → deadlines: [{ "event": "answer_due", "date": "…", "cite": "FRCP 12(a)(1)(A)(i)", … }, …]
```

`GET /deadlines/rules` lists the loaded jurisdictions; `POST /matters/:matterNumber/deadlines`
computes and attaches deadlines to a matter.

**Rule sets shipped** (marked `SAMPLE — AI-GENERATED — NOT VERIFIED BY COUNSEL` until a practitioner submits a verified PR):

| File | Jurisdiction | Rules |
|---|---|---|
| `us-federal-frcp.yaml` | US Federal | FRCP answer, reply, MTD opposition, MSJ, FRAP appeal, service, Rule 26(f) |
| `uk-cpr.yaml` | UK | CPR acknowledgment, defence, summary judgment response, appeal notice |
| `eu-competition.yaml` | EU | Competition regulation response, appeal, leniency deadlines |

Holiday tables are computed in-process (US federal, UK bank, EU institutions — Butcher/Meeus Easter). Adding a new jurisdiction is a YAML file drop in `deadlines/rules/`.

> ⚠️ **These rule sets are illustrative examples only.** Deadlines vary by judge, local rules, and standing orders. ALWAYS verify with a licensed attorney before relying on any computed deadline. See [`deadlines/rules/CONTRIBUTING.md`](../../deadlines/rules/CONTRIBUTING.md) to submit a verified rule set.

Related: [REST API](../integration/rest-api.md) · [Legal notices](../legal-notices.md)
