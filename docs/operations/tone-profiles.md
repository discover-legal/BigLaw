[Docs](../index.md) › Deploy & operate › **Tone profiles**

# Lawyer voice fingerprinting

Drafting agents and the final heavy-tier synthesis call use the **assigned lawyer's writing
style** — so work product reads as if the lawyer wrote it themselves, not as generic AI output.

## How it works

1. Partner or lawyer uploads writing samples to `POST /profiles/:id/tone/import`
   (multipart; 60-second per-profile rate limit) or via the **Voice** modal in Admin › Users
2. Any of the following file types are accepted:
   - **LinkedIn ZIP** (or extracted `Shares.csv` / `Posts.csv`) — detected automatically
   - **DOCX** — paragraphs extracted from `word/document.xml`
   - **PDF** — text extraction via `scripts/pdf_tools.py` (requires Python)
   - **CSV** — scores columns by average text length; uses the richest column
   - **Plain text / Markdown** — split on double-newlines

   (`POST /profiles/:id/tone/linkedin-import` remains as the LinkedIn-only legacy route)
3. Content is sanitised (prompt-injection markers like `FINDING:`/`END_FINDING` and control
   characters are stripped) before reaching any model
4. A chunked recursive MapReduce analysis runs on the light tier: batches of posts → prose
   notes → merged up to a single note → structured `ToneProfile`
5. The `ToneProfile` is stored on the lawyer's profile and injected into all drafting-domain
   agent system prompts and the final synthesis call

`DELETE /profiles/:id/tone` clears the profile.

## Getting a LinkedIn export

1. Go to <https://www.linkedin.com/mypreferences/d/download-my-data>
2. Select **Posts & Articles** → **Request archive**
3. Download the ZIP when LinkedIn emails you the link
4. Upload the ZIP (or the extracted CSV) — or just drop a DOCX, PDF, or CSV of your own writing

Related: [Access control](access-control.md) · [Security](../security.md)
