# BigLaw user guide

For a lawyer who wants to use BigLaw, not program it. BigLaw reads a matter's documents, answers a
question about them in a memo, and checks every quote in that memo against the documents before
you see it. You approve every memo before it counts as done. The rest of the repository is for
developers; you do not need it.

## 1. Install it (once, about 10 minutes)

1. Install [Docker Desktop](https://docs.docker.com/get-docker/) and start it.
2. Open a terminal (Terminal on a Mac; on Windows, a WSL terminal) and paste:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
   ```

3. It asks one question: paste an [OpenRouter](https://openrouter.ai/keys) API key (the AI model
   runs at OpenRouter and is billed there), or press Enter to use [Ollama](https://ollama.com) on
   this computer (free to run, but needs a strong computer; first run
   `ollama pull qwen2.5:14b` and `ollama pull nomic-embed-text`).
4. When it says "BigLaw is running", open **http://127.0.0.1:3101** in your browser.

BigLaw is reachable only from this computer. Its settings and the database live in a folder named
`biglaw` where you ran the command. To update, run the same command again: your settings and
documents are kept.

If the Matters screen shows **No AI model set up yet**, BigLaw can store and search documents but
cannot run a task: whoever installed it adds a model in `biglaw/.env` and restarts it.

## 2. Add documents to a matter

1. Click **Documents** at the top.
2. In **Add documents**, choose one or more **Files**: PDF, Word (`.docx`), plain text, or a
   scanned image (PNG, JPEG, GIF, WebP).
3. Type the **Matter** number (for example `2026-014`). A number BigLaw has not seen starts a new
   matter; existing numbers are offered as you type.
4. Click **Upload** (the button says how many files). Each file reports "filed to …" or why it was refused.

A scan is read by the AI twice and the two readings are compared. If no model that reads images is
set up, the scan is kept and read later, once one is. Below the upload box you can search a matter's
documents and open any document section by section.

## 3. Start a task

1. Click **Matters** and pick the matter's card.
2. Under **New task**, type your question in plain words, for example "When is our answer to the
   complaint due?"
3. Check **Jurisdiction** (`US` by default; for example `US-NY` for New York).
4. Click **Start task**. BigLaw opens the task.

The task works through the documents on its own. The **Run log** tab shows each step as it
happens; **Cancel run** stops it. A task usually takes minutes, not seconds.

## 4. Review and approve

When the run finishes it stops with the status **awaiting gate**: **Your review is needed.**
Nothing is complete until a lawyer approves it.

- **Read the deliverable first** (the **Deliverable** tab). Every factual sentence cites a finding,
  shown as `[F1]`, `[F2]`…; click one to see it.
- The **Findings** tab shows each finding's claim and the exact quotes it rests on. Each quote has a
  badge:
  - **exact**: the quote appears word for word in the document;
  - **tolerant**: it matches once quote marks, capitals and spacing are ignored;
  - **judge** or **ensemble**: an AI judge (or three) found the quote supported by a nearby passage;
    check these yourself;
  - **@transcript** (for example "exact @transcript"): the quote was found in the AI's reading of a
    scan, not in typed text. Check it against the original (**Open the scan**);
  - **excluded**: not found in the documents. Such a finding is **quarantined** and kept out of the
    memo.
  **Show in source** opens the document at the quoted passage.
- Sometimes BigLaw asks about single findings first (**N decisions needed**): a finding cited at low
  confidence, or one a reviewing critic marked unsupported. **Approve** it, or type a reason and
  **Reject** it (a rejected finding leaves the memo). The run then goes on.
- Then **Approve deliverable**, or **Reject…**, say what the lead should fix, and **Reject
  deliverable**. BigLaw revises the memo with your reason and comes back to you.

With sign-in, every decision records who made it.

## 5. Read and download the memo

The **Deliverable** tab has **Download (.md)** (a plain-text Markdown file that any text editor opens), **Copy** and **Print**. A Word (`.docx`) export is not built yet.

The memo ends with:
- **Sources**: every finding the memo cites, with its quotes.
- **Limitations**: what BigLaw could not check or did not do, in plain words. Read them before you
  rely on the memo. Typical lines: the jurisdiction and practice guidance used has not yet been
  reviewed by a named lawyer; cases and statutes cited are flagged, not verified (BigLaw has no
  citator); a page that was not read, or a figure BigLaw could not confirm. A Limitations line is a
  warning, not an error: it tells you where your own review matters most.
- **Computed figures** (when there are any): figures BigLaw calculated rather than quoted (a sum, a
  deadline from a date plus a period), each with the quotes it starts from. **Check first** lists
  the ones that rest on a reading you should confirm.
- **Evidence Note**: how many findings were verified and how many were quarantined.
- **Note to the reviewing lawyer**, when the lead has one.

If a task **failed**, the task page says why in a sentence (for example, the AI model was not set
up, or too many quotes failed verification, so BigLaw refused to write a memo on unchecked
evidence). Fix what it names, then start the task again.

## 6. Costs

Every task has a spending limit (by default 400 AI calls and $5). The task page shows **Budget
spent** and **Calls left**; the **Cost** tab lists every AI call with its tokens and dollar cost. A
model BigLaw has no price for is shown as "unpriced" (an unknown cost, never $0). With a local model
the cost tab can also show **estimated energy** in watt-hours, if whoever installed BigLaw set the
computer's power draw. With Ollama, calls are still priced at that model's hosted rate unless whoever
installed BigLaw sets its price to zero (README § Model client, Pricing). A task that reaches its
limit finishes its memo from a reserve it kept back, and says what was left open.

## 7. Good to know

- **Who can see what.** Without sign-in, anyone who can reach BigLaw from this computer can use it.
  With your firm's sign-in, everyone signed in sees every matter; admins also change the agents in
  **Manage**. Everything else here works the same.
- **Your data** (documents, tasks, memos) is in BigLaw's database on this computer. The text of a
  task's documents is sent to the AI model you set up (OpenRouter, or Ollama on this computer).
- **Backups.** Ask whoever installed BigLaw to back up the database before each update
  (README § Database).
- **Manage** (for admins) shows every agent, tool, guidance pack and skill BigLaw uses. You do not
  need it to run tasks.
