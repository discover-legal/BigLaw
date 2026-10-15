[Docs](../index.md) › Features › **Court deadlines**

# Court deadline calculator

`biglaw-go/internal/deadlines` — pure Go, no external service required. Rule sets are YAML
files in `deadlines/rules/` at the repo root, loaded at startup.

Feed it a trigger event and date; it returns every downstream deadline under the applicable rule set, calendar vs business days computed correctly, jurisdiction holidays applied, with the procedural citation for each.

```bash
curl -X POST http://localhost:3101/deadlines/compute \
  -H "Content-Type: application/json" \
  -d '{ "jurisdiction": "US-FED", "triggerEvent": "complaint_served", "triggerDate": "2026-12-04" }'
# → deadlines: [{ "event": "answer_due", "dueDate": "2026-12-28", "unadjustedDate": "2026-12-25",
#                 "warningDate": "2026-12-23", "adjustments": ["2026-12-25 (Friday) is Christmas Day — …"],
#                 "cite": "FRCP 12(a)(1)(A)(i)", … }],
#   limitations: ["… state holidays (FRCP 6(a)(6)(C)) … NOT modelled …", …]
```

`jurisdiction` is the rule set's `jurisdiction` code (`US-FED`, `UK`, `EU-COMP`), not its file id.
An unknown `triggerEvent` is a **400** whose body lists `validTriggers`; an unknown jurisdiction
is a 404 listing `validJurisdictions`.

**Counting.** Calendar-day periods exclude the trigger day and, when the last day is a
Saturday, Sunday or holiday, roll forward to the next working day — FRCP 6(a)(1)(C) /
FRAP 26(a)(1)(C) for `us_federal`, CPR 2.8(5) for `uk_bank` (and CPR 2.8(4): periods of 5 days
or less skip weekends and bank holidays), Reg. 1182/71 Art. 3(4) for `eu_institutions`.
Rules with **negative** `days` count *backward* from the trigger ("at least 7 days before the
hearing": the trigger date is the hearing); a backward period ending on a non-working day moves to
the **preceding** working day (FRCP 6(a)(5)), and `uk_bank` counts clear days (CPR 2.8(3)), so
CPR 24.5(1) evidence for a Friday 20 November hearing is due Thursday 12 November. Such deadlines
carry `direction: "before"`. Business-day rules count working days only. Each deadline carries the raw `unadjustedDate`
and an `adjustments` list explaining every skipped day; the warning date is counted back from
the adjusted due date and moved *earlier* (never later) off non-working days. Every result
carries `limitations` — what is not modelled (US state legal holidays where the district court
sits, ad-hoc presidential closures, FRCP 6(d) mail days, Scotland/NI bank holidays, the full
EU institutions' holiday lists). US federal holidays follow 5 U.S.C. 6103(a) (Juneteenth from
2021) with Saturday→Friday / Sunday→Monday observed dates, including across a year boundary.

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
