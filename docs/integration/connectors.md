[Docs](../index.md) › Integrate & extend › **Connectors**

# Legal data connectors

BigLaw ships 32 connector tools across 15 providers, all using Streamable HTTP MCP (JSON-RPC 2.0).
Unconfigured connectors return a structured `{ error: "not configured" }` — they never crash the
server, so they are always safe to register in agent allowedTools.

Security: endpoint URLs are SSRF-validated at startup; response bodies are capped;
requests time out at 30 s; API keys never appear in logs or error messages
(see [Security](../security.md)).

**Legal research & courts**

| Provider | Tools | Activation |
|---|---|---|
| CourtListener | `court_listener_search`, `_opinion`, `_docket` | Always on (optional key for higher rate limits) |
| Westlaw / CoCounsel | `westlaw_research`, `_check_citation` | `WESTLAW_API_KEY` |
| Everlaw | `everlaw_search_documents`, `_get_review_set` | `EVERLAW_API_KEY` |
| Trellis | `trellis_search_cases`, `_get_docket`, `_judge_analytics` | `TRELLIS_API_KEY` |
| Descrybe | `descrybe_search_cases`, `_check_citation` | `DESCRYBE_API_KEY` |
| Solve Intelligence | `solve_intelligence_search_patents`, `_draft_claims` | `SOLVE_INTELLIGENCE_API_KEY` |

**Contract & document management**

| Provider | Tools | Activation |
|---|---|---|
| Ironclad | `ironclad_search_contracts`, `_get_contract` | `IRONCLAD_API_KEY` |
| DocuSign CLM | `docusign_search_contracts`, `_get_envelope` | `DOCUSIGN_API_KEY` |
| iManage | `imanage_search`, `_get_document` | `IMANAGE_API_KEY` |
| Definely | `definely_analyze_structure`, `_resolve_definition` | `DEFINELY_API_KEY` |
| Lawve AI | `lawve_review_contract`, `_search_clauses` | `LAWVE_API_KEY` |

**VDR & productivity**

| Provider | Tools | Activation |
|---|---|---|
| Google Drive | `google_drive_search`, `_get_file` | `GOOGLE_DRIVE_API_KEY` |
| Box | `box_search`, `_get_file` | `BOX_API_KEY` |
| Slack | `slack_search`, `_send_message` | `SLACK_API_KEY` |

**Outside counsel**

| Provider | Tools | Activation |
|---|---|---|
| TopCounsel | `topcounsel_route_matter`, `_get_panel` | `TOPCOUNSEL_API_KEY` |

**Practice management**

| Provider | Tools | Activation |
|---|---|---|
| Clio | `clio_list_matters`, `clio_get_matter`, `clio_list_documents`, `clio_download_document`, `clio_create_activity`, `clio_create_note`, `clio_list_contacts` | `CLIO_CLIENT_ID` + `CLIO_CLIENT_SECRET` (OAuth) |

## Clio — getting started

Clio uses OAuth 2.0 rather than a static API key.

1. Log in to Clio as a firm admin → **Settings → Developer Applications → New Application**.
   Enable API access for **Matters**, **Contacts**, **Documents**, **Activities**, **Notes**,
   and **Users**, then copy the Client ID and Client Secret.
2. Configure `.env`:

   ```bash
   CLIO_CLIENT_ID=your-client-id
   CLIO_CLIENT_SECRET=your-client-secret

   # Must match where the firm's data is hosted — wrong region = 401 on every call
   # us (default) | eu | ca | au
   CLIO_REGION=us
   ```

3. Connect: have a **partner** visit `GET /auth/clio/connect`. This redirects to Clio's OAuth
   consent screen; after approval, tokens are persisted (default `./data/clio-tokens.json`,
   override with `CLIO_TOKENS_FILE`) and auto-refresh. Check status any time:

   ```bash
   curl http://localhost:3101/auth/clio/status
   # → { "connected": true, "firmName": "Smith & Jones LLP", "connectedAt": "…" }
   ```

4. Use it:

   ```bash
   # Import a matter: fetch details, ingest attached documents, submit a task
   curl -X POST http://localhost:3101/tasks/from-clio-matter \
     -H "Content-Type: application/json" \
     -d '{ "matterId": 12345, "workflowType": "roundtable" }'

   # Sync billable time to Clio (already-synced entries are skipped)
   curl -X POST http://localhost:3101/time-entries/sync-to-clio \
     -H "Content-Type: application/json" \
     -d '{ "clioMatterId": 12345, "matterNumber": "001-2024" }'
   ```

With Clio connected, agents can use the seven `clio_*` tools and Big Michael's client-briefing
swarm pulls matters, contacts, and notes into every `@BigMichael briefing` run.
`DELETE /auth/clio/disconnect` revokes the stored tokens.

**Matter import:** `POST /tasks/from-clio-matter` fetches a Clio matter's details, ingests its
attached documents into the knowledge base, and submits a BigLaw task in one call.

**Time sync:** `POST /time-entries/sync-to-clio` pushes BigLaw billable time entries back to a
Clio matter as activity records, preserving 6-minute billing unit rounding. Idempotent — entries
are stamped with `clioSyncedAt` on success and skipped on subsequent calls.

Related: [Plugins & adapters](plugins.md) · [The bench's tools](../features/agent-tools.md) · [Big Michael](../features/big-michael.md)
