[Docs](../index.md) › Deploy & operate › **Secrets**

# Secrets (Infisical)

`.env` (seeded from `.env.example` by `setup.sh`) is the bootstrap: model keys, auth secrets,
and connector keys can all live there for a local install. For a firm deployment, keep the
secrets in an [Infisical](https://infisical.com) vault instead — only these three vars then
need to be in `.env`; everything else is pulled from the vault at startup
(`biglaw-go/internal/secrets/`):

```bash
INFISICAL_CLIENT_ID=...
INFISICAL_CLIENT_SECRET=...
INFISICAL_PROJECT_ID=...
```

Self-host Infisical: `docker compose -f docker-compose.prod.yml up -d` from the Infisical repo.

Treat API keys, session secrets, and OAuth credentials as production secrets regardless of
environment — see [Security](../security.md).

Related: [Access control](../operations/access-control.md) · [Connectors](../integration/connectors.md)
