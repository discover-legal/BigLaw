# Adding deadline rules

> **IMPORTANT — LIABILITY NOTICE**
>
> All `.yaml` files shipped in this repository are **sample AI-generated rules**
> and carry the notice `SAMPLE — AI-GENERATED — NOT VERIFIED BY COUNSEL` at the
> top of the file. They are provided for illustration only and **must not be
> relied upon in live matters** until a licensed attorney in the relevant
> jurisdiction has reviewed and verified every rule.
>
> **Before removing the sample notice from any file:**
> 1. A licensed attorney admitted in the relevant jurisdiction must review every
>    rule, citation, and day-count in the file.
> 2. The PR description must name the reviewer and the date of review.
> 3. The `version` field must be updated to reflect the review date.
>
> Discover Legal accepts no liability for missed deadlines arising from
> unverified rules. If in doubt, leave the notice in place.

Drop a `.yaml` file in this directory. It is auto-loaded at startup.

## Required fields

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier, e.g. `us-sdny-local` |
| `jurisdiction` | string | BCP-47-style code matching Big Michael task jurisdictions |
| `name` | string | Human-readable ruleset name |
| `version` | string | Year or version of the rules |
| `holidays` | enum | `us_federal` \| `uk_bank` \| `eu_institutions` \| `none` |
| `rules[].id` | string | Unique rule ID within this file |
| `rules[].trigger` | string | Trigger event name (snake_case) |
| `rules[].event` | string | Resulting deadline event name |
| `rules[].days` | integer | Number of days |
| `rules[].dayType` | `calendar`\|`business` | Whether to count weekends/holidays |
| `rules[].cite` | string | Rule/statute citation |

## Optional fields

| Field | Description |
|---|---|
| `rules[].warningDays` | Warn N days before deadline |
| `rules[].note` | Human-readable note |
| `source` | URL to the authoritative source |

## Trigger event naming convention

Use `snake_case`. Common triggers already in use:
- `complaint_served`, `complaint_filed`
- `answer_filed`, `motion_to_dismiss_filed`
- `summary_judgment_filed`, `final_judgment_entered`
- `claim_form_served`, `defence_served`
- `ec_phase2_opening_decision`, `statement_of_objections_received`
