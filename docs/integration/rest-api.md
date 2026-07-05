[Docs](../index.md) › Integrate & extend › **REST API reference**

# REST API

The authoritative route map is `biglaw-go/internal/api/` (one file per domain route group);
this page reflects it. The API listens on **:3101** (native) or **:3102** (Docker stack).

```
POST   /tasks                 GET /tasks · /tasks/:id · /tasks/:id/stream (SSE)
DELETE /tasks/:id             POST /tasks/:id/assign         (partner only)
POST   /tasks/from-template   POST /tasks/:id/gates/:gateId/{approve,reject}
GET    /tasks/:id/rounds/:round            GET /tasks/:id/table.csv
GET    /reviews/:id                        (tabular_review matrix as JSON — flags, reasoning, verified citations)
GET    /reviews/:id/table.csv              (tabular_review matrix as CSV)
POST   /tasks/:id/status-report            (LPM status-report spine)
POST   /documents             POST /documents/upload (PDF/text)
GET    /documents             GET /documents/search
GET    /documents/:id/timeline             (Redtime per-clause redline timeline of a version lineage)
GET    /documents/attachments/:docId       GET /documents/attachments/:docId/:attId
GET    /documents/export/:docId            (render document text + images to PDF)
GET    /agents · /templates · /settings   PUT /settings      (admin)
GET    /plugins                                               (partner only)
GET    /me · /profiles        POST /profiles                 (partner only)
                              PATCH /profiles/:id            (partner or profile owner)
                              DELETE /profiles/:id           (partner only)
GET    /clients               POST /clients · PATCH/DELETE /clients/:id   (partner only)
POST   /clients/:id/matters   DELETE /clients/:id/matters/:num            (partner only)
POST   /clients/check-conflict             POST /clients/check-conflict-graph
GET    /clients/:id/briefing               hub-and-spoke client briefing         (partner only)
POST   /clients/:id/ocg                    GET/DELETE /clients/:id/ocg · GET …/ocg/stats
GET    /time-entries          GET /time-entries/export.{json,csv,ledes}    (partner: all; lawyer: own)
GET    /time-entries/{agent-summary,suggestions}
POST   /time-entries/sync-to-clio                                          (partner only)
GET    /analytics/noslegal · /analytics/portfolio-health                  (partner only)
POST   /profiles/:id/tone/import           DELETE /profiles/:id/tone
POST   /profiles/:id/tone/linkedin-import  (LinkedIn-only legacy contract)
GET    /cost/summary                                                       (partner only)
GET    /tasks/:id/cost        GET /profiles/:id/cost
GET    /playbooks · /playbooks/:id · /playbooks/resolve/:clauseType
POST   /playbooks/build       DELETE /playbooks/:id                       (partner only)
POST   /redline               Contract redline (playbook-aware)               (partner only)
POST   /headnotes/generate    Headnote extraction from case opinions          (partner only)
POST   /precedents/generate   Precedent document generation                   (partner only)
GET/POST /citations/check     Citation engine (CourtListener-backed)
GET    /deadlines/rules       POST /deadlines/compute
PUT/GET /clients/:id/matters/:num/budget   POST …/budget/check
GET    /matters/:matterNumber/{health,budget-prediction}
POST   /matters/:matterNumber/deadlines
PUT/GET /matters/:matterNumber/client-voice                  (Remy advocacy briefs)
POST   /dockets/watch · /dockets/check-now  GET /dockets · /dockets/alerts/stream (SSE)
POST   /regulatory/check-now               GET /regulatory/alerts/stream (SSE)
GET    /budget/alerts/stream (SSE)
POST   /pre-bills             GET/PATCH /pre-bills(/:id) · POST /invoices/{validate,upload}
POST   /reports/generate · /portfolio/generate   GET /reports · /reports/:id/docx   (LPM)
POST   /memory/query          GET /jobs · /jobs/stats · POST /jobs/:id/retry
GET    /auth/providers        GET /auth/{google,microsoft,linkedin}/{login,callback}
POST   /auth/logout
GET    /auth/clio/status      GET /auth/clio/{connect,callback}            (connect: partner)
DELETE /auth/clio/disconnect               POST /tasks/from-clio-matter    (partner only)
GET    /audit · /audit/stream (SSE)        GET /health
POST   /bots/teams/webhook                 Teams Outgoing Webhook receiver
POST   /bots/slack/events                  Slack Events API receiver
POST   /bots/{teams,slack}/notify          Internal: post to a channel (partner only)
POST   /bots/{teams,slack}/matter-link     Link a matter to a channel (partner only)
```

Document ingestion (`POST /documents`, `POST /documents/upload`) returns enriched metadata:
```json
{ "id": "…", "practiceArea": "Corporate & M&A", "detectedClient": { "clientNumber": "C-001", "clientName": "Acme Corp" }, "suggestedLawyers": [{ "id": "…", "name": "Jane Smith" }] }
```

Every matter-scoped route enforces access control — see
[Access control](../operations/access-control.md).

Related: [MCP / Claude Code](mcp.md) · [Getting started](../getting-started.md)
