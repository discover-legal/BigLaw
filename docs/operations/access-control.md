[Docs](../index.md) › Deploy & operate › **Access control**

# Lawyers, roles & access control

BigLaw is multi-user when deployed. Identity comes from **OAuth** (Google,
Microsoft, or LinkedIn) or a bearer API key; each person is a **lawyer profile** with a role:

- **partner** (admin) — sees every matter, manages the lawyer roster, assigns
  matters to lawyers, and manages clients.
- **lawyer** — sees **only** the matters they're assigned to. There is no
  inter-lawyer visibility unless a partner shares a case.

This is enforced at every matter-scoped endpoint and documented in unit tests
(`cd biglaw-go && go test ./...`). On Postgres deployments, database-level row-level
security backs the application-layer checks — see
[Models, persistence & documents](../deployment/models-and-persistence.md).

## Lawyer profiles

Each profile stores name, email, title, role, practice areas (one or more of 15 canonical areas),
bio, and optionally a `ToneProfile` for voice fingerprinting
([Tone profiles](tone-profiles.md)).

## UX modes

| Mode | Accent | Who | Features |
|---|---|---|---|
| `admin` | gold | Partners (immutable) | Everything: user management, analytics, all settings, time reporting |
| `full_flavour` | scarlet | Lawyers (default) | Full law firm stack: all workflows, 32 connectors, conflict checks, time tracking |
| `lite` | amber-gold | Lawyers (partner-assigned) | Core only: submit tasks, view results, library, basic search |

## Auth setup (production)

With `AUTH_ENABLED=true` the API accepts two credentials:

- **Browser OAuth login** (Google / Microsoft / LinkedIn) — `GET /auth/<provider>/login` →
  consent → signed, httpOnly session cookie (HMAC-SHA256, 12 h). First login from an
  `ADMIN_EMAILS` address is provisioned as a **partner**; everyone else as a **lawyer**.
  Auth endpoints are rate-limited to 20 req/min per IP.
- **Bearer API key** (non-browser clients) — `Authorization: Bearer <API_KEY>` (compared in
  constant time) plus `X-Profile-ID: <profile id>` identifying the acting lawyer.

```bash
AUTH_ENABLED=true
SESSION_SECRET=<random 32+ char secret>   # signs session cookies
API_KEY=<random 32+ char secret>          # bearer credential for non-browser clients
PUBLIC_BASE_URL=https://api.your-host
PUBLIC_UI_URL=https://app.your-host
CORS_ORIGINS=https://app.your-host
ADMIN_EMAILS=you@firm.com

GOOGLE_CLIENT_ID=…       GOOGLE_CLIENT_SECRET=…
MICROSOFT_CLIENT_ID=…    MICROSOFT_CLIENT_SECRET=…
LINKEDIN_CLIENT_ID=…     LINKEDIN_CLIENT_SECRET=…
```

**Local dev** runs with auth OFF (`AUTH_ENABLED=false`, the default) — a single "local
partner" who sees everything. **Never expose the API on a shared network with auth off.**

📖 Full step-by-step provider registration: [`AUTH_SETUP.md`](../AUTH_SETUP.md).

## Practice area classification & conflicts

Every document ingest runs light-tier classifier calls: practice-area detection (15 canonical
areas) and client matching against the roster; the response surfaces `suggestedLawyers` whose
practice areas match. Conflict-of-interest checks (`POST /clients/check-conflict`, plus the
TypeDB conflict-graph variant `POST /clients/check-conflict-graph`) run automatically on
client creation.

Related: [Security](../security.md) · [Audit trail](audit-trail.md) · [REST API](../integration/rest-api.md)
