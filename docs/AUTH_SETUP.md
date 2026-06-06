# Deploying with login (OAuth + access control)

BigLaw is **single-user with no login locally** (`AUTH_ENABLED=false` → one
"local partner" who sees everything). For a shared/firm deployment, turn auth on
and wire one or more OAuth providers. This guide takes ~10 minutes.

## Access model (recap)

- **partner** (admin) — sees every matter, manages the lawyer roster, assigns
  matters to lawyers (and can share one matter across several).
- **lawyer** — sees **only** matters assigned to them. No inter-lawyer
  visibility unless a partner shares a case.

Enforced at every matter-scoped endpoint (list, detail, SSE stream, gates, CSV,
rounds, audit) and documents are scoped to their uploader. The rules are unit
tested (`npm test`) and proven end-to-end over HTTP.

## 1 · Register an OAuth app per provider

The **redirect URI** is always `<PUBLIC_BASE_URL>/auth/<provider>/callback`.

- **Local dev:** set `PUBLIC_BASE_URL=http://localhost:5173` (the UI origin —
  Vite proxies `/auth`, so the session cookie lands on the right origin).
- **Production:** use your real public URL (e.g. `https://app.yourfirm.com`).

### Google
1. [console.cloud.google.com](https://console.cloud.google.com) → **APIs & Services → Credentials**
2. *Create credentials → OAuth client ID* → **Web application**
3. Configure the OAuth consent screen if prompted (scopes: `openid email profile`)
4. **Authorized redirect URI:** `http://localhost:5173/auth/google/callback`
5. Copy → `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`

### Microsoft
1. [portal.azure.com](https://portal.azure.com) → **Microsoft Entra ID → App registrations → New registration**
2. Supported account types: *Accounts in any organizational directory and personal Microsoft accounts*
3. **Redirect URI** (platform **Web**): `http://localhost:5173/auth/microsoft/callback`
4. **Certificates & secrets → New client secret** → copy the **Value** → `MICROSOFT_CLIENT_SECRET`
5. Overview → **Application (client) ID** → `MICROSOFT_CLIENT_ID`

### LinkedIn
1. [linkedin.com/developers](https://www.linkedin.com/developers) → **Create app**
2. **Products** tab → add **"Sign In with LinkedIn using OpenID Connect"**
3. **Auth** tab → **Authorized redirect URLs:** `http://localhost:5173/auth/linkedin/callback`
4. Copy → `LINKEDIN_CLIENT_ID`, `LINKEDIN_CLIENT_SECRET`

You only need the providers you want — any one is enough.

## 2 · Configure the server (`.env`)

```bash
AUTH_ENABLED=true
SESSION_SECRET=        # openssl rand -hex 32
PUBLIC_BASE_URL=http://localhost:5173     # OAuth redirect base (UI origin for local)
PUBLIC_UI_URL=http://localhost:5173       # where to land after login
CORS_ORIGINS=http://localhost:5173        # allow-listed browser origin(s)
ADMIN_EMAILS=you@firm.com                 # provisioned as partner on first login

GOOGLE_CLIENT_ID=        GOOGLE_CLIENT_SECRET=
MICROSOFT_CLIENT_ID=     MICROSOFT_CLIENT_SECRET=
LINKEDIN_CLIENT_ID=      LINKEDIN_CLIENT_SECRET=
```

## 3 · Run

```bash
npm run dev          # backend
cd ui && npm run dev # console
```

Open the console → you'll get the **login screen** with a button per configured
provider → sign in. The first login from an `ADMIN_EMAILS` address becomes a
**partner**; everyone else is provisioned as a **lawyer**. Add/maintain the
roster and assign matters from **Admin · settings**.

## Notes

- Sessions are stateless **signed, httpOnly cookies** (`SESSION_SECRET`); there's
  no server-side session store.
- For production behind a domain, set `PUBLIC_BASE_URL`/`PUBLIC_UI_URL`/
  `CORS_ORIGINS` to that origin and register the matching redirect URIs; cookies
  are marked `Secure` automatically when `PUBLIC_BASE_URL` is `https`.
- An optional `API_KEY` (sent as `x-api-key`) can gate non-browser clients
  independently of OAuth.
