[Docs](index.md) › Deploy & operate › **Security**

# Security

## ⚠ Experimental — Security Notice

**BigLaw is an experimental research project. It is not production-hardened software.**

The goal of this project is to build the **most comprehensive open legal AI platform possible** — covering the widest breadth of legal workflows, integrations, agent types, and jurisdictions. Comprehensiveness of capability is the primary objective. Test coverage and security hardening, while taken seriously and continuously improved, are secondary to that goal.

**What this means in practice:**

- The platform handles credentials, client matter data, and privileged legal communications. Firms deploying it are responsible for their own threat model.
- The codebase receives ongoing security sweeps and bug fixes, but has **not undergone a formal independent security audit**.
- **Before deploying in any environment where real client data is involved, you must engage an independent security professional (pen tester, security engineer, or FDE — Forward Deployed Engineer / Formal Deployment Expert) to review the deployment configuration and code.**
- `AUTH_ENABLED=false` is the default for local development. **Never expose the API on a public or shared network without enabling authentication.**
- API keys, session secrets, and OAuth credentials must be treated as production secrets regardless of environment.

**Independent security review is not optional for production deployments. It is a prerequisite.**

This notice does not diminish what BigLaw is — it is the most capable open legal AI stack available. It does mean you should not deploy it like a SaaS product without the due diligence that any complex, credential-holding, client-data-processing system requires.

## Security hardening

BigLaw handles legal work product, client PII, and privileged communications — so the
attack surface is treated seriously.

| Area | What's in place |
|---|---|
| **Constant-time, bound auth** | Bearer-token and session-signature comparison use `subtle.ConstantTimeCompare`; bearer identity comes from `API_PROFILE_ID`, never a caller-selected header |
| **Signed sessions** | Session cookies are HMAC-SHA256-signed, httpOnly, SameSite=Lax, Secure on HTTPS, 12 h expiry with jti revocation |
| **Auth rate limiting** | `/auth/*` endpoints are sliding-window rate-limited to 20 req/min per IP |
| **Path traversal** | PDF/docx tools enforce an allow-list of read roots and confine output to the output directory (symlinks resolved) |
| **Prompt injection** | `SanitizePromptContent` strips rogue protocol markers (FINDING/CHALLENGE/RESOLUTION…, case-insensitive) and control characters from all user-supplied content before it reaches a model — task descriptions, round goals, tone imports, debate resolutions |
| **SSRF protection** | Endpoint URLs are validated lexically and again after DNS resolution by a controlled dialer; private/loopback/link-local/reserved results are rejected, and every redirect is revalidated |
| **CSV safety** | Time-entry and tabulate CSV exports neutralise formula injection and strip `\r\n` from field values |
| **Audit integrity** | SHA-256 hash chain verified on restore — tampering logs a warning |
| **Bot authorization** | Provider signatures are verified first, then Slack senders must appear in `SLACK_ALLOWED_USER_IDS`; Teams senders and teams must appear in `TEAMS_ALLOWED_USER_IDS` and `TEAMS_ALLOWED_TEAM_IDS`. Only `help` is public. Work is admitted through a bounded worker queue. |
| **Access control** | Bearer credentials are bound server-side to one profile; reviews, document versions, documents, and attachments carry owner identity across Postgres, SQLite, and memory stores. Ownerless legacy documents are privileged-only; partner gates cover playbook, roster, client, billing, and analytics endpoints. |
| **Conflict checks** | Entity-name normalisation + bidirectional matching, with an optional TypeDB conflict-graph sidecar |
| **Round resilience** | Per-agent round timeout (`AGENT_ROUND_TIMEOUT_MS`, default 300000); an agent that exceeds it gets one retry with an extended budget (`ROUND_TIMEOUT_RETRY_FACTOR`, default 2.0) before recording nothing. A round in which every agent came back empty emits a `round.starved` audit event and annotates the task (`starvedRounds`) so consumers see the run was degraded. Malformed debate resolutions route to a human gate instead of passing silently |
| **Boot task quarantine** | Tasks restored from `TASKS_FILE` in a mid-run status (`running`/`awaiting_gate`) are marked `interrupted` with a `task.interrupted` audit event — their runner goroutine died with the previous process, so silently re-listing them as running left zombie tasks contending with live work. Resubmit to rerun; `RESUME_RUNNING_TASKS=true` restores the old behaviour |
| **Bounded execution** | Durable FIFO admission and fixed worker pools prevent one goroutine per submission or bot/audit event. Pending work is capped by `QUEUE_MAX_PENDING`; JSON bodies (16 MiB), multipart bodies (32 MiB), model responses (32 MiB), and blobs (64 MiB) have absolute ceilings. |
| **No secrets in logs** | API keys appear only in `Authorization` headers; connector error messages are length-capped; response bodies capped (1–2 MB) with 30 s timeouts |

Use Go **1.25.12+** or **1.26.5+**. Earlier patch levels are affected by
GO-2026-5856 / CVE-2026-42505 in `crypto/tls`. The bundled Docker build is pinned
to a fixed toolchain.

Related: [Legal notices & disclaimers](legal-notices.md) · [Access control](operations/access-control.md) · [Audit trail](operations/audit-trail.md)
