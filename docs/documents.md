# Documents in depth

The design detail of `internal/docs` (and of stored files' memory), moved verbatim from README
§ Documents, § Files and § Limitations at `v2` eb17567. README § Documents keeps the contract: the
scope rule, ingest, the page reader, the verification ladder, the transcription jobs and the routes.
If this page and the code disagree, the code is right: fix this page in the same change.

## Ingest

PDFs and `.docx` files are parsed in a **child process** of the same binary (`biglaw` re-executed
with a private argument, no environment, so it never holds the server's secrets): the PDF parser
recurses on nested objects, and a few MB of nesting is a fatal stack overflow that would
otherwise kill the server (`TestHostilePDFContained`). The child is killed when the request ends
(its ingest slot is free at once) or after 2 minutes, and it bounds itself even if the server
dies first (crash, `kill -9`, OOM kill): it exits at its own deadline and within 250 ms of its
parent's death, and on Linux the kernel kills it with its parent (`Pdeathsig`)
(`TestChildBoundsItself`, `TestChildDiesWithParent`). Its stack is 64 MiB and, on Linux, its data
segment is a **hard** 1 GiB cap (`RLIMIT_DATA`): a decompression bomb dies at the cap and the
upload is 422 `unreadable` (measured: a 512 MiB text bomb under a 256 MiB test cap died at
142–221 MiB peak RSS across runs). A document's text is capped at **16 MiB** (`maxText`, about
8,000 pages; the file itself may be up to 64 MiB): more, from a PDF, a `.docx` or an uploaded
text file, is 422 `unreadable` ("its text is larger than …"). The child sends its text as a
length-prefixed raw block after a one-line JSON header, which the server reads through hard bounds
(a 64 KiB header, exactly the stated length up to the cap, valid UTF-8, nothing after:
`TestReadChildBounded`), into one allocation. At the cap a child peaks at about 60 MiB RSS on
escape-heavy text, under half its memory cap, every run (`TestChildNearTextCap`), so acceptance
near the cap does not depend on garbage-collector timing. The child is the running image
(`/proc/self/exe` on Linux), so replacing or deleting the binary on disk while serving does not
change the reader (`TestChildRunsTheRunningImage`); a reader that cannot start is a 500 that says
so, not "damaged". Two run at once (`ingestSlots`); an upload waits at most 10 minutes for a slot,
then is 503 `busy` and its file is removed (`TestIngestSlotWaitBounded`). A binary whose `main`
(or test package's `TestMain`) forgot `docs.MaybeRunChild()` gets a clear error instead of
re-running itself (`TestForgottenMaybeRunChildIsAnError`).

`.docx` text is read in one linear pass of an XML decoder (a 9 KB file of 160,000 runs took
48 s in v1's reader; 1,000,000 runs now take about a second: `TestTextLinearAndBounded`), with
elements matched by namespace, whatever their prefix: `<w:tab/>` is a tab, `<w:br/>`/`<w:cr/>` a
line break, `<w:noBreakHyphen/>` is U+2011 (as in `FS‑26‑01234`), a `<w:sym/>` is its character
unless it is a private-use Symbol-font code (dropped), character references are decoded, comments ignored, hidden text (`<w:vanish/>`),
deleted text and markup-compatibility fallbacks left out, nesting capped at 256 elements, and
footnotes and endnotes appended after the body as `[id] text` under a `Footnotes`/`Endnotes`
line (`TestTextFidelity`). Entities are never expanded. Text hidden only by formatting (white on
white, a 1-point font) is read like any other text.

## Section tree, chunks, search and vision transcription

**Section tree and chunks.** v1's page index, rewritten: articles, sections (`Section 1`, `§ 1`),
decimal clauses (`1`, `1.1`; the first number at most three digits, so a year is not a heading,
and a number followed by a lower-case word, such as `100 dollars were paid` or `1.5 million`, is
body text, and so is a bare number before a street word, `12 Elm Street, …`), `(a)` and `(i)` items,
schedules/exhibits/annexes, recitals, and Markdown `#` or bold headings. An article, section, `§` or
schedule line whose title runs on from its number in lower case or with punctuation (`Article 4 of the
Trust, …`, `Section 9, …`, a PDF-wrapped cross-reference) is body text; a separator (`—`, `:`, `.`)
before the title keeps it a heading (`TestWrappedLinesAreNotHeadings`). The tree is bounded so storage stays linear in the input (each chunk carries its
section path): at most 10,000 sections per document (later headings stay body text of the last
one, and the document's `notes` say so, so an outline never looks complete when it is not),
titles cut at 120 bytes, numbers of at most 24 bytes and five decimal levels, so a path has at
most six levels; titles lose bidi and other invisible or control characters. **Bound:** every
chunk column together (path, body, search text, column context) is at most 3× the input for each
hostile shape `TestSectionTreeBounded` builds except one (measured 1.0×–2.9×: a long heading over
40,000 items 2.2×, 2,500 rows of 100 one-digit cells under 100 named columns 2.9×). The bound is
**additive, not a ratio**: about 4× the input (a body, its search text at most 1.5× plus the
column list, a path and a context per chunk of at least 1,024 bytes) **plus** up to 1,165 bytes
per section (a path of six levels of number, 120-byte title and separator, 906 bytes, and a
context of 259), so at most about 11.7 MB more per document at the 10,000-section cap, because a
section's last chunk can be short. Small documents of many short, deeply nested sections show it:
9,990 sections of 120-byte titles six levels deep (1.3 MB in) store 9.0 MB, **6.9×** (3.7 MB, 373
bytes per section, over 4×), and the ratio falls as the document grows past the section cap
(`TestSectionTreeBounded` "deep titles", which asserts the additive bound). v1's tree copied the full path into every chunk (3,284×
and 168 s for the long heading); round 1's table rows copied the whole header into every row
(a 120 KB, 20,000-column table: 1.2 GB and 20 s; 9 ms and 260 KB now). `TestHeadingHeuristics`. Joining every section's own text gives back the document byte for byte, so every
section and chunk is a verbatim slice (`TestReconstruction`, `TestChunksCoverEveryWordOnce`).
Chunks hold at most 400 tokens (3.5 characters per token), cut at a sentence end, else at white
space. A tab-separated table is chunked by whole rows, grouped until a chunk holds at least
1,024 bytes (a row longer than the token cap is split like prose); the first row (after the
start or a `#`/`## Sheet:` line that is not itself a row, so a `#` row-number column stays a
column) is the header, however long (only its first 1,401 cells are kept as names, the most a
row under the token cap can pair). Each data cell is indexed as
`Column: cell` while the names added stay within half the row's length (bare cells after that),
and a chunk's `context` names the sheet and columns (at most 256 bytes; indexed once per chunk)
(`TestTableRowChunking`). So a table hit returns a few rows, not one. A line with two or more
tabs is a table row, never a heading (a numeric row such as `1⇥2023⇥$400` used to become a
section).

**Search.** Postgres full text (the lexical leg; with an `embed` role it is fused with vector
similarity, below): each chunk has a stored English `tsvector` and a `simple` (unstemmed) one. Search reads the scope's
chunks (by document) and matches each term against them once, so a query costs time in
proportion to the scope's size times its terms; there is no GIN index, because this ranking
query cannot use one (`EXPLAIN` showed none used, so it would only cost writes). The bound: at
most **32 distinct words** per query (more is 400 `bad_request`) and a **10 s** statement timeout
(then 400, "search fewer documents, or use fewer or rarer words"). `TestSearchCost` prints the cost on this machine: on a
4 MiB scope of 12,425 chunks, 2 common words took 135 ms, 32 absent words 128 ms, and 32 words of
which 6 are common 492 ms (the round-1 query: 193 ms, 444 ms and 867 ms on the same data). Every word of the query is a separate term (OR, not AND; quotes, `-` and other
operators are plain text, so a quoted phrase is its words), scored as `ts_rank_cd` of that term (saturated) times its rarity in the scope
(`ln(1 + N/(1 + df))`), on both vectors, summed; ties go to the lower chunk id. So a rare term
outranks a repeated common one (`TestSearchRareTermOutranksCommon`), results are deterministic
(`TestSearchTopKDeterministic`), and identifiers such as `s.9.4`, `C-13`, `FS-26-01234`,
`Schedule A` or `Annex I` (whose letter is an English stop word) are found
(`TestSearchExactIdentifier`), and rarity is counted within the scope only, so another matter's
documents cannot reorder this one's results (`TestSearchRarityIsScoped`). Queries are at most
1,024 bytes; `k` is 1–50.

**Hybrid search (PR 3b; owner decision 9).** With an `embed` role in `BIGLAW_MODELS`, every
chunk is embedded at ingest (its section path and search text, at most 4,096 characters, 64 chunks
per call, after the ingest slot is released) and so is every query whose scope holds an embedded
document (the word limit is checked first, so a refused query is never embedded or billed; a scope
with nothing embedded is searched lexically without an embeddings call, so it keeps working during
an embedding outage). The lexical ranking above is fused with the scope's embedded chunks ranked
**exactly** by cosine distance to the query's vector: reciprocal-rank fusion,
`1/(60 + lexical rank) + 1/(60 + vector rank)`, each leg's ties by chunk id, so `Rank` is the fused
score and results are deterministic (`TestHybridFusion`). **A chunk of a lexical-only document**
(its embedding failed, or the model changed and the backfill has not reached it) has no vector
rank, so it scores `2/(60 + lexical rank)`: its lexical rank counts for both legs, so it competes
on equal terms instead of being buried under embedded chunks that share one common word
(`TestLexicalOnlyNotBuried`: the only chunk naming "Zebracorn Holdings" stays first). Every
embedded chunk in scope is a candidate, so a hybrid search returns up to `k` hits even when no word
matches. Both legs read only the scope's documents (G1, `TestHybridKeepsScope`). There is **no
HNSW or IVFFlat index**: an approximate index filtered to one matter's documents returns fewer
than `k` rows (measured at PR 3b: with the HNSW index forced, a scoped top-8 over 50,000 vectors of
which 40 were in scope returned 0 rows; pgvector's iterative scan recovers that only up to its
scan limit), so an index would cost writes and break scoped recall. A query costs scope ×
dimensions; on a 4 MiB scope of 12,426 short clauses with dense unit vectors (a probe run for PR
3b's round-1 fixes, 4 x86 cores), 2 common words took 245–350 ms, 2 absent words (the vector leg
dominates) 105–180 ms and 32 words of which 6 are common 555–710 ms, at 512 and at 1,536
dimensions alike (the lexical leg dominates); `TestSearchCost` prints its own hybrid figures. The
`vector` column has no fixed length: a document records the model that embedded **all** its chunks
(`embedModel`, with the calls' `embedTokens` and `embedCostUsd`, null when unpriced), and the vector
leg reads only documents of the current `embed` model whose vectors have the query's length.
Embedding a document first clears its `embedModel`, and sets it only after every batch landed at
one vector length, so a run that fails part-way (or whose batches come back at different lengths)
leaves it lexical only, never a previous model's name over a mix of two models' vectors
(`TestReembedNeverMixesModels`: roll back to the old model and it is re-embedded completely). A
failed or cancelled embedding call never fails an ingest: the document is stored lexical only with
the note "Search over this document is lexical only: embedding its chunks failed or was cancelled
…" (`TestEmbedFailureLeavesLexical`). `serve` runs a **backfill** at startup, one document at a
time (each within the same 10-minute bound as an ingest), for documents stored before the role was
set, under another model, or whose embedding failed; it skips documents stored after it starts (by
the database's clock), notes each failure on its document, logs how many it embedded and how many
failed (a warning when any failed), stops with the server, and gives up after 3 documents **in a
row** fail (`TestBackfillEmbedsOldDocuments`, `TestServeHybridEndToEnd`). A failed **query**
embedding is returned as its model error (502/503/504), never a silent lexical answer. With no
`embed` role, search is the lexical leg alone, as `/health` says.

**Scale and disk (no index).** Each query reads every embedded chunk in its scope. Stored at 1,536
dimensions (what `text-embedding-3-small` returns), a vector is 6,148 bytes (512: 2,052); the probe
above's chunks table was 119 MB for 12,426 chunks at 1,536 dimensions, 58 MB at 512. The round-1
auditor measured ≈ 8 KB per chunk with TOAST at 1,536 dimensions (≈ 95 MB for a maximum-size
document of about 12,000 chunks) and, for one scope, 497–615 ms warm at 51,000–120,000 chunks and
about 1.1 s cold on 4 vCPUs. The rule for operators: keep a matter's scope × 8 KB within free RAM,
and expect search to near its 10 s timeout past about **100,000 chunks per matter on x86** (≈ 20,000
on a Pi 4 with SD or USB storage: an estimate, not measured). Past that, search fails with 400
"search fewer documents".

**Gate E4 (`TestHybridRecallProofRuns`): not yet judged.** Over the 68 planted proof-run facts, a
search within the fact's matter with an **answer-free question** (one per fact in
`answer_key.json`, written without the fact's figures or conclusion, before the first measurement,
not tuned since) must return a chunk holding one of its evidence strings in the top 8 for ≥ 0.95
(65/68). Lexical search alone gives **60/68 = 0.882** (every evidence string counted separately:
86/106 = 0.811), and with the fact's **title**, which states the answer, 66/68 = 0.971 (100/106),
an upper bound; `TestLexicalRecallProofRuns` prints both and fails only below those floors. Lexical
misses by question: `REG-05`, `REG-14`, `DD-06`, `DD-09`, `LIT-D1`, `LIT-I3`, `LIT-I6`, `LIT-C6`.
The hybrid gate runs in CI without a model key by replaying `eval/e4/embeddings.json`, a tape of
`sha256(text) → vector` for every text the measurement embeds (the 355 proof-run chunks as the
chunker cuts them, and the 68 questions and 68 titles: 491 vectors), with the model that recorded
it, at that model's own vector length (the length `serve` stores: there is no dimensions setting). **The committed tape is a self-test tape**, recorded offline by a bag-of-words
hash embedder (`biglaw-selftest-hash`, 64 dimensions), because no embedding key exists where this
was built. It proves the harness only (`TestE4TapeReplays`: every text is on the tape, replay
leaves no document lexical, the vectors are the hash embedder's), and on it
`TestHybridRecallProofRuns` **skips** with "E4 GATE NOT JUDGED". Its recall with that tape,
50/68, is no gate result (a bag-of-words embedder fused with lexical search did worse than lexical
alone). The owner records the real tape once, and from then on CI judges the gate (`t.Errorf`
below 65/68) and fails if any text it embeds is missing from the tape (the chunker or the fixtures
changed: re-record). In **every** mode (replay, record, live) a matter with a document left
lexical only (a failed or refused embeddings call, or on replay a text not on the tape) fails the
test with "E4 NOT JUDGED" before any recall is scored or any tape written, so a flaky endpoint
cannot produce a verdict or a partial tape. `make test` prints the verdict line (in CI too) and,
where the database is required, fails unless the gate passed or skipped as not judged:

```bash
make e4-record EMBED_MODEL=text-embedding-3-small \
  BIGLAW_MODEL_URL=https://api.openai.com/v1 BIGLAW_MODEL_KEY=sk-… \
  BIGLAW_TEST_DSN='postgres://…?sslmode=disable'      # writes eval/e4/embeddings.json; commit it
make e4-live EMBED_MODEL=… BIGLAW_MODEL_URL=… BIGLAW_TEST_DSN=…   # live, no tape (BIGLAW_MODEL_KEY if the endpoint needs one)
```

Any model may be named; the tape records which. `make e4-record EMBED_MODEL=biglaw-selftest-hash`
regenerates the self-test tape offline. Scoring note (r2 auditor): for a few multi-hop facts
(`DD-06`, `LIT-I3`, `LIT-D1`) the question retrieves the operative clause while the key designates
only the other hop, so 2–3 misses are partly measurement; the per-evidence-string figure is
printed beside the gate figure and the gate is not moved for it. Gate A (PR 4b) measures recall
through the agent's own queries.

**Vision transcription (PR 3c; DESIGN revision 5 § 4).** Images (PNG, JPEG, GIF, WebP, by magic
bytes) and scanned PDF pages are sent to the model of the `vision` role in `BIGLAW_MODELS`, as
base64 `image_url` parts through the same OpenAI-compatible client (every call priced like any
other). A PDF page is scanned when it has no text layer, **or only a stamp** (under 80 characters of
text layer, a Bates number or a header, beside a page image, as v1 counted): such a page is read and
its stamp kept after the transcript, outside the transcript's span (`TestBatesStampedScanTranscribed`).
Characters are counted, not bytes. A page with **80 to 399 characters** of text layer over JPEG page
images (a production page under a long legend, a short letter on letterhead, a title page with a logo)
is **mixed** (owner decision 12): its images are read like a scan (two readings, every agreement,
masking and low-confidence rule), its text layer is stored verbatim as the truth for its own words,
and the page stored is only the reading's lines that add to the text layer, then the text layer (the
added lines are the transcript span; the note says only lines a text layer lacks are kept). The exact
rule (`beyond`): the text layer's letters-and-digits words (a figure such as "1,250,000.00" is one word
with its separators; case and ligatures folded) are joined with nothing between them; a reading line is
subtracted only when its own words, joined the same way, occur in that string starting where a layer
word starts and ending where a layer word ends. A word the text layer splits ("D ear") or glues
("receiptat"), or a compound the reading breaks at a line end ("third-\nparty"), is the same line; a
figure inside a longer one ("$125,000.00" beside "$1,250,000.00"), a stamp inside a word ("APPROVED"
beside "unapproved", "PAID IN FULL" beside "unpaid in full") or a part of a word is kept. A short
line (under 3 words or under 12 letters and digits, not ending in a hyphen: a stamp, a handwritten
figure, a signature, a tick) is subtracted only when it is a whole line of the text layer: "PAID"
beside "Amount paid: $0.00", "APPROVED" beside "to be approved", "$500" beside "Suite 500", "Smith"
beside a "SMITH-0001" Bates number are kept; a stamp the layer itself carries as a line is not. A line
with no letter or digit ("✓", "☒") is kept (a mark is information). When in
doubt the line is kept: a duplicate is disclosed as transcript, a dropped image-only line would be
lost (`TestBeyondWholeLayerWords`: every case above, and 3,000 random layer/reading pairs checked
against the joins of every run of whole layer words). A CR, alone or before a line feed, ends a line.
When the reading adds nothing the text layer is stored alone (trimmed) with the note "Page N: its
image was read and adds nothing beyond its text layer." Such a page never holds or refuses the document: with no vision role, or when its
reads fail or disagree, its text layer is stored with the note "an image on the page was not read"
(`TestShortTextOverPageImageMixed`: the 185-character letter on a page-sized JPEG with no vision role,
with disagreeing readings, with one reading carrying a 30-name partner sidebar, and with agreeing
ones, stored once; a 133-character "HIGHLY CONFIDENTIAL – ATTORNEYS' EYES ONLY" legend whose
readings repeat it, the body stored once with the legend; a reading identical to a letter with a
"third-\nparty" line break, or to a text layer that splits "D ear" and glues "orderdated", stored once
with only "ACME LLP" added; the cost of a mixed page recorded). A quote found in a transcript line
and again later verifies at the later copy when that one is outside every transcript span and is
whole words (no letter or digit just before or after it, nor a figure's separator: "paid $500" never
moves into "paid $5000", "paid $500,000" or "$500.50", nor "500" into "12 500";
a text layer's words stay `exact` 1.0 when an added line repeats them; `TestMixedPageKeepsWhatTheLayerLacks`). The
cut, 400 characters, is five lines of 80: a legend with its protective-order reference and Bates
number runs 55–266 characters, a page of text about 3,700. A page with 400 characters or more over
any image (a letter page, an OCR'd scan) is a page of text: stored as it is, with no call, never held
or refused, and noted (a 2-page letter on letterhead makes 0 calls).
There is no OCR engine and no rasteriser: a scanned PDF page is read only when it is one to four
JPEG images stored whole (`/DCTDecode`), which the reader child finds by their offsets in the file
and the server sends **as they are** (`TestScannedPDFDCTPagesExtracted`: the bytes sent are the
embedded JPEGs). A page whose images use another encoding (CCITT fax, JBIG2, Flate bitmaps, a JPEG
under a second filter), a form XObject, an encrypted PDF, a JPEG stream whose `/Length` is wrong, or
a TIFF image is 422 `scan_encoding_unsupported`, with advice to export as JPEG or from a phone
scanner app, when the document has no text at all (`TestScanEncodingUnsupportedTyped`); beside a
text layer or stamps such a page is noted as unread and the text stored (a one-line cover letter
with a logo, a CCITT-scanned production with Bates stamps: `TestBatesStampedScanTranscribed`). Every image's header is checked
(`image.DecodeConfig`; WebP by magic only) before it is stored or sent: a damaged one, or one
declaring more than 12,000 pixels a side or 50 million in all (a decompression bomb for the model's
server), is 422 `unreadable`, with no call made, when every scanned page's image is such, stamps or not
(`TestImageDimensionsCapped`, `TestMixedPDFDamagedImageNoted`); otherwise that page is noted as
damaged and unread, nothing is sent for it, and the rest is read or stored (`TestMixedPDFDamagedImageNoted`;
a note about unread pages says to upload again once a vision role is set only when none is); a page's images may total 20 MiB, at most
4 per page (`TestChildScanLimits`). The server re-checks the reader child's answer (pages in range,
in order, without a text layer; images inside the file and within 20 MiB): `TestServerRechecksChildAnswer`.
- **Two independent readings.** Each page is read twice, by two different prompts
  (`vision_prompts.txt`; agents `transcribe-a`/`-b`, each at most 4,096 output tokens), each told to
  write `[illegible]` rather than guess and `[blank]` for a page with no text. The text stored is the
  reading as returned (only its marks normalised); to compare the readings, ligatures are made plain
  and a hyphenated word is one token with its hyphen, also when broken at a line end (`third-\nparty`
  is `third-party`; `re-\nlease` is not `release`: `TestBenignNoiseNotLow`); a figure is one token with
  its inner `.` and `,` (so `1.250` is not `1,250`). Agreement is Dice over the readings' word
  multisets (case folded, marks left out; `[illegible…]`, `[unclear…]`, `[blank…]` and `(illegible)`
  are marks, and a guess inside one such as `[illegible: five hundred?]`, also across a line break,
  is dropped): below **0.75** the page is refused, 422
  `transcription_unreliable` for an image or a fully scanned PDF, with nothing stored, no text, no
  chunk, no file (`TestUnreadableImageNotFabricated`: two plausible letters for a noise image); a
  blank reading beside a non-blank one is a disagreement. A stored page is **low confidence** when
  its agreement is below **0.9**, **or**, however high its agreement, when the readings differ on what
  matters: any `[illegible]` mark in either reading; a figure (a token with a digit), a negation or
  modal (`not`, `no`, `nor`, `never`, `n't`, `cannot`, `shall`, `may`, `must`, `will`) one reading has
  and the other lacks; 5 or more words one reading lacks (a dropped or garbled line: every differing
  word counts); or the same words in a different order (swapped parties: a word-level LCS; readings
  whose token counts multiply past 4,000,000, about 2,000 words each, are not compared in order and
  count as different). The page's `disputed` lists those tokens (at most 12: marks, the order flag
  and figures first) and a note names them (`TestReadingsDisagreeOnWhatMatters`:
  $12,500 vs $125,000, FS-26-01234 vs FS-26-01284, "shall not assign" vs "shall assign", a dropped
  line, payer and payee swapped, $1.250 vs $1,250 million, 12.500,00 vs 12,500.00, each in either
  order; one differing prose word is not low;
  `TestAgreementThresholdsPinned`: 0.74 refused, 0.75 stored, 0.889 low, 0.90 not). The first
  reading is kept (the other if it is blank), so which reading's figure is stored is arbitrary, and
  that is why such a page is low confidence. Wherever the other reading has a mark, the readings'
  words are aligned (their LCS, a mark standing for any one word) and every word of the kept reading
  not surely aligned to a word the other read becomes `[illegible]`: one aligned to a mark, and one the
  two tie-breaks of the alignment place differently (where a mark belongs is ambiguous: every
  candidate is masked). So the mark stays at its place even when its word repeats on the page (a
  payee, an amount) or anchors are few (`TestIllegibleMaskedAtItsPlace`: 10,000 random pages of five
  repeating words, a substituted word masked exactly at its place; with words also inserted and
  dropped, a word never kept more often than the marked reading has it); pages too long to align mask
  every occurrence of each word the other reading has fewer of. A mark is never
  filled in from either reading, also when each marks a different word (`TestIllegibleNeverFilled`,
  `TestIllegibleNeverFilledTwoSided`). An endpoint echoing the request's key into a reading has it
  replaced by `[redacted]` (`TestKeyEchoNeverStored`). Nothing readable on any page is 422
  `no_content` (`TestBlankImageNoContent`); a reading cut at the output limit, or longer than 64 KiB
  (16 bytes per token asked for), is 422 `transcription_unreliable`, never a silently short page.
  More than **20** scanned pages is 413 `too_many_pages` before any call (`TestTooManyPages413`). All
  of one document's vision calls stop after 10 minutes, and its text, counted over all its pages,
  after 16 MiB (`TestVisionFailuresTyped`, `TestTextCapCumulative`). Notes round agreement down.
- **A mixed PDF keeps its text layer.** Only its scanned pages are read; each transcript goes in
  its page's place and every text-layer page stays byte for byte (`TestMixedPDFTextLayerNotRewritten`:
  the vision model "reading" different words for them changes nothing). A page there that cannot be
  read (its readings disagree, its vision call fails or is refused, or it is past the first 20
  scanned pages) is left unread with a note naming why, and the text layer is still stored, 201
  (`TestMixedPDFVisionFailureKeepsTextLayer`; v1's model merge of the two is dropped).
- **Provenance.** A document's `transcript` names the model, each transcribed page with its byte
  span in the text, its agreement and `disputed` tokens, the least agreement, `lowConfidence`,
  `transcribedAt`, and the calls' `tokens` and `costUsd` (null when the model is unpriced), recorded
  whenever a vision call was made, also when no page could be transcribed (it is SQL `NULL` only
  when nothing was sent); `method` is `vision` (a mixed PDF stays `text_layer`, its `transcript`
  listing the transcribed pages); a note names the model and says to check quotes against the
  original, which is kept and downloadable. Each chunk that overlaps a transcribed page is marked
  `transcribed` (search hits say so). An upload refused after its vision calls stores nothing, so
  their tokens and cost go to the operator's log (`TestVisionRefusalIs422`).
- **Citations into a transcript** (`VerifyQuote`): the method gets `@transcript`
  (`exact@transcript`, `tolerant@transcript`, `judge@transcript`, …), the confidence is capped at
  **0.85** (below text-exact 1.0 and tolerant 0.95, above the judge's 0.8), the result names the
  model (`transcribedBy`) with the note "machine transcription of an image (model): check against
  the original", and `lowConfidence` is set when the match touches a low-confidence page: per
  quote, so a finding resting on a good page is not flagged
  (`TestTranscriptCitationCappedAndDisclosed`, `TestLowAgreementFindingsGatedIndividually`; the
  engine gates a finding resting only on such quotes for a lawyer, `TestLowAgreementFindingGated`).
- **No vision role.** An image or a fully scanned PDF (stamps included) is stored with
  `status: needs_transcription` (no text beyond its stamps, a note) and the upload is **503
  `vision_not_configured`** with the document; the original is kept
  (`TestVisionNotConfiguredTyped503KeepsOriginal`, `TestExtractImageWithoutVisionDegrades`). Nothing
  is sent to any model: no text role is ever used for images (`TestTextModelNeverSentImages`; end to
  end, `TestServeNeverSendsImagesToTheLead`). A mixed PDF is stored ready with its text layer and a
  note on its unread pages that says to upload it again once a vision role is set: the job does not
  read it later. A vision call's failure on an image or a fully scanned PDF is the model's G6 status
  (502/503/504) and the upload stores nothing; a request the endpoint rejects (HTTP 400 and other
  4xx but 401/403/404/408/429) is 422 `unreadable`, not an outage, and is not retried; its message
  says the cause is the image (damaged, animated, too large) **or** the request (a text-only model
  id, an exhausted account). A waiting document so refused spends one of its 3 attempts and ends the
  job's run, so one refusal never fails the documents queued behind it (`TestVisionRefusalIs422`:
  of 3 waiting documents only the first is tried until it fails at its third run).
- **Reading slots.** A document's extraction holds one of the 2 ingest slots; its vision calls then
  wait for one of 2 reader slots of their own (holding the extracted text, not the file, while they
  wait; the file is read again), so slow or hung vision calls never hold up text, `.docx` or
  text-layer PDF uploads (`TestVisionCallsDoNotHoldIngestSlots`). Each wait is bounded by 10 minutes
  (503 `busy`).
- **Transcription jobs**: README § Documents.
- **Live check (owner, no key here).** Every test runs on `model.Fake` or `httptest`. With a vision
  model on an OpenAI-compatible endpoint:
  ```bash
  BIGLAW_DATABASE_URL=… BIGLAW_MODEL_URL=http://localhost:11434/v1 BIGLAW_MODELS='lead=qwen2.5:14b,vision=qwen2.5vl:7b' \
    ./biglaw serve --allow-no-auth &
  curl -s -F file=@letter.jpg http://127.0.0.1:3101/documents/upload | jq '{status, method, notes, transcript}'
  ```
  then download `GET /documents/{id}/file` and compare it with the transcript by eye.

## Memory

**Memory of one ingest, end to end** (r2: the server, not the capped child, used to take 0.8 GB
for one 431 KB PDF of 63 MiB escape-heavy text: an unbounded buffer of the child's JSON and an
array literal of every chunk body). Measured with `go test -run '^TestIngestMemoryBounded$' -v
./internal/docs` (the server's live heap, sampled every millisecond) while sampling every Postgres
process with `ps -o rss= -C postgres` every 50 ms: ingesting each of seven text-cap shapes raised the server's live heap by
**48–60 MiB** (one run: 15 MiB of escape-heavy text **49 MiB as a PDF** through the child and 49
as a text file; 16 MiB of newlines 48; one header line of 16 M tabs 48; a 16 MiB header of three
4 MiB cells under a 4 MiB `#` line 49–60 across runs; `x\n` × 8 M and `ten bytes\n` × 1.6 M 52).
The test process peaked at 99–111 MiB RSS, the largest Postgres backend at **180 MiB** over the seven
shapes (round 2 measured 127 MiB with the escape-heavy shape alone), and the child at about **60 MiB**
(`TestChildNearTextCap`). The test (skipped under `-race`; `make test` runs it in its non-race
pass) fails above 5× the text cap (80 MiB, a 20 MiB margin over the
largest figure measured). Round 2's version split every section body into a slice of lines and
the header row into a slice of cells first (16 bytes per line or tab): in this test 16 MiB of
newlines cost +288 MiB and a 16 M-tab header +288 MiB (`x\n` lines +172), and a live server
peaked at 326–589 MB; now lines and cells are walked one at a time, and the header keeps at most
the 1,401 cells a pairable row can have (a longer row is split like prose). The live binary
(`VmHWM`, idle 9.8 MB) now peaks at 96 MB for a 16 MiB newline file and then an `x\n` file, and
133 MB for an `x\n` file and a tab-header file at once. Chunks go to Postgres by `COPY`, a row at a time. Add the file
itself, which the reader is handed whole (up to 64 MiB, about 128 MiB peak at the default `GOGC`,
as above). So two ingests at once (`ingestSlots`) of successful files at the text cap take about
2 × (60 + 180 + 60) ≈ 0.6 GB above an idle server, plus up to 2 × 64 MiB of file; the worst case
is two hostile files whose children die at their 1 GiB cap: about 2 GiB of children plus that.

## Limitations

- **Documents (PR 3a):**
  - There is no OCR: images and scanned pages are read by the vision model (§ Vision
    transcription; its limits are below). A text-layer PDF whose layer is wrong (a scan with an invisible, bad OCR
    layer) is taken as is. Text that the PDF reader puts in an odd order (columns, tables) stays
    in that order.
  - A text upload that happens to start with `%PDF-` is read as a PDF and refused; posted text
    (`IngestText`) is always text.
  - Every package whose tests ingest a PDF or `.docx` needs a `TestMain` that calls
    `docs.MaybeRunChild()` (the test binary is the reader child), as `internal/docs` does;
    `cmd/biglaw`'s `main` calls it first (`TestBinaryIsItsOwnPDFReader`). Without it extraction is
    an error, not a crash. Only the 2-minute timeout bounds a child's CPU: a 438-byte PDF whose page
    tree is its own kid holds one of the two ingest slots for 2 minutes (two such uploads every
    2 minutes stop ingestion; there is no quota or rate limit before PR 8).
  - The child's hard memory cap is Linux-only (`RLIMIT_DATA`; elsewhere only a 512 MiB
    `SetMemoryLimit` target, which the runtime can exceed) and is off in race-detector builds (whose
    shadow memory counts against it; `make ci` re-runs the child tests without `-race`). Outside
    Linux the child is the binary at the server's path, not `/proc/self/exe`, so replacing the
    binary while serving changes the reader there. The
    parent-death signal is tied to the server thread that started the child: Go rarely ends a
    thread, but if it did the child would be killed and the upload reported unreadable. The
    250 ms parent-pid check covers every platform.
  - A document's text is capped at 16 MiB (more is 422, split the document), but there is no
    per-matter quota: a 144 KB PDF can store 15 MiB of text and about as much again in chunks.
  - `.docx`: footnote and endnote references are not marked in the body (the notes follow it, by
    id), so a quote spanning a reference's superscript number reads without it; text boxes keep
    their `mc:Choice` copy only; a run whose formatting history (`<w:rPrChange>`) was hidden is
    treated as hidden; text in headers, footers and comments is not read; a document whose `w:`
    namespace is not declared reads as having no text (Word always declares it); Symbol-font
    `<w:sym/>` codes (private use) are dropped, not mapped to Unicode.
  - Table hits return a chunk of several rows (≥ 1,024 bytes), not one row: per-row chunks would
    need the column list stored once per table to stay linear.
  - Citations carry **no page number**: v1's ladder could use a cited page to pick the judge's
    window (`TestCitationWindowLocatesRegion`'s form-feed variant, dropped); v2 locates the window
    by the quote's words, and a PDF cite has no pinpoint page.
  - Section ids are positions in the section tree (`s0`, `s1`, …) of the text as ingested; a
    change to the heading rules does not renumber stored documents, which keep the ids they were
    ingested with. Re-ingest to pick up new rules.
  - **Lexical search alone does not meet gate E4** (0.882 < 0.95; § Documents), and with no
    `embed` role search is lexical only: a question that shares no word with the passage is not
    found (`REG-05`'s fee-table row by its title; most question misses ask about a date or figure
    the passage states in other words). Each query scans the scope's chunks once per term (at most 32), so a very large scope makes every query slower
    (`TestSearchCost` prints the figures; 32 terms on 4 MiB took about 0.5 s here) up to the 10 s
    timeout, which then refuses it; nothing caps a scope's size. Quoted phrases and `-exclusion`
    are not supported (their words are searched).
  - Heading heuristics are guesses: `999 Main Street` or `10 May 2026 the parties met` at a line
    start (a wrapped PDF line) is still a decimal heading, and a sub-clause whose title starts in
    lower case (`1.1 the Supplier shall`) is now body text of its parent.
  - The judge's passage is document text: `verify_quote`'s judge prompt (PR 4a) fences it and
    declares it untrusted data, but a model can still be swayed by text addressed to the judge (`.docx` text hidden by
    formatting, not by `<w:vanish/>`, reaches it too).
  - The tolerant rung folds case, so `ACME` verifies against `Acme` (at 0.95).
  - Migration `0002_docs.sql` changed during this PR's review (the delete trigger, the dropped GIN
    indexes, prose search text stored once; round 2 changed no SQL): a dev database that applied an earlier version
    refuses to start (checksum drift); recreate it.
  - `ledongthuc/pdf` is a small, lightly maintained library pinned at a 2025 pseudo-version; it
    is the second of the three allowed dependencies.
- **Hybrid search and embeddings (PR 3b):**
  - **Gate E4 has not been judged.** The committed tape is the self-test hash embedder's
    (§ Documents), so `TestHybridRecallProofRuns` skips, and whether hybrid search reaches 65/68
    is unknown until the owner runs `make e4-record` with a real model. Fusion can also make recall
    **worse** than lexical alone: with the bag-of-words self-test embedder it fell from 60/68 to
    50/68. If the real tape misses the gate, the fusion constant or weights, not the gate, are the
    next question (owner).
  - The tape is recorded at the model's own length, as `serve` embeds (1,536 dimensions for
    `text-embedding-3-small`: a tape of about 4 MB, three times the old 512-dimension recipe's).
    A model can be run only at its own length: there is no dimensions setting.
  - Every embedded chunk in scope is a vector candidate, so a hybrid search returns `k` hits even
    for a query nothing matches; callers must not read "a hit" as "relevant". No index: each query
    computes the distance to every embedded chunk in scope, under the same 10 s timeout, and the
    vectors cost about 8 KB of disk per chunk at 1,536 dimensions; see § Documents, "Scale and
    disk" for the measured figures and the ceiling (about 100,000 chunks per matter on x86).
  - The lexical-only rule (`2/(60 + lexical rank)`) makes a lexical-only chunk equal to an
    embedded chunk ranked the same by both legs; it cannot gain from meaning, so for a question
    that shares no word with it, a lexical-only document is still not found (as with lexical
    search alone). It is also the **best case** for that chunk: its vector rank is taken to equal
    its lexical rank, so until the backfill embeds it a lexical-only document whose chunks share a
    query word outranks embedded chunks that are semantically closer. Measured
    (`TestLexicalOnlyDisplacementMeasured`): the vector leg's first match, lexically weak, is #5
    behind 8 such chunks and **#10** (off a page of 8) behind 20; #1 once they are embedded. `serve`
    logs how many documents are lexical only after its startup backfill. A neutral imputation would
    trade this for part of the burying it was chosen to avoid (owner's call).
  - A vector space is known only by the model id: if the model behind an id changes (an Azure
    deployment upgraded in place, an Ollama `:latest` re-pull), old documents are not re-embedded,
    and are compared across models (same length) or silently drop out of the vector leg (another
    length) while `/health` still says `hybrid`. Change the id when the model changes, so the
    backfill re-embeds.
  - Paging re-embeds the query for each page (one call, about $0.0000002 at
    `text-embedding-3-small`'s rate): a per-server cache of the last query's vector was left out,
    because its timing would tell one user whether another just searched the same words.
  - Embedding usage is priced on every call, but only an ingest's usage is recorded (on the
    document, and only for a run that embedded every chunk: a failed run's billed calls are not
    stored), and a search's query embedding is charged to the task through `retrieve`'s result
    (PR 4a; a direct `docs.Search` call outside a task is charged to nothing). An embeddings
    answer with no `usage` has an unknown cost (nil); a **chat** answer without `usage` still
    counts 0 tokens and reads $0 for a priced model (PR 2's behaviour, unchanged here).
  - Embedding runs inside the upload request, after the ingest slot is released: a 16 MiB
    document is about 190 calls of 64 chunks at the 1,400-character chunk size, up to about 780
    for short clauses (`TestSearchCost`'s 4 MiB of them is 12,426 chunks), so its upload answers
    only when they finish (or fails over to
    lexical only, with the note, if the client goes away). The backfill runs only at startup: a
    document whose embedding failed stays lexical until the next start. Two servers on one
    database may both backfill the same old document (twice the cost, same result, **as long as
    both use the same `embed` model**: two servers backfilling under different models can interleave
    on one document and leave it labelled with one model over a mix of both models' vectors; run one
    embed model per database). Changing the
    `embed` model makes every document lexical until the backfill re-embeds it; a re-embed that
    fails turns a document embedded by the old model lexical only (its old vectors are no longer
    trusted, and are overwritten when it is next embedded).
  - The key-shape refusal of `BIGLAW_MODELS` ids is a heuristic: a key in another shape is still
    shown and logged, and a real id with 24 letters and digits in a row (an Azure deployment name,
    say) is refused at startup; name such a deployment with a `-` or `_` in it.
  - Inputs are cut at 4,096 characters; an embedding model with a short context (some 512-token
    BERT-style models) may refuse a 1,400-character chunk (HTTP 400 → `model_unavailable`), which
    leaves that document lexical only.
  - **Compose volume:** the Postgres image changed from `postgres:16-alpine` (musl) to
    `pgvector/pgvector:0.8.6-pg16` (Debian, glibc). Collations differ between the two C
    libraries, so a `pgdata` volume created by an earlier pre-release Compose stack should be
    recreated (`docker compose down -v`; v2 has no release yet) rather than reused.
  - **Every chunk of every document goes to the embedding endpoint**, at ingest, and so does
    every search query: with a hosted embedder (OpenAI, a gateway) the full text of privileged
    matter documents leaves the machine, as the chat calls' prompts will. Setting the `embed`
    role for the first time also sends **every stored document** to it (the startup backfill).
    The embed role uses the same `BIGLAW_MODEL_URL` (and vendor breaker) as chat: embeddings
    cannot go to a local endpoint while chat goes to a hosted one. Run a local embedder (Ollama's
    `nomic-embed-text`) to keep the text on the machine.
  - There is no quota, and without sign-in no auth: anyone who can reach the port (with sign-in, anyone signed in) can upload documents and
    make the server pay to embed them (16 MiB of text is about 4.2M tokens at 4 bytes a token: $0.08 at
    `text-embedding-3-small`'s rate, $0.55 at `-large`'s), and every search makes one call.
  - A matter's own documents can steer the vector leg: a chunk written to sit near many queries
    (a "hub") is returned for them, crowding out evidence (inside the scope only: G1 holds). Three
    documents in a row that the endpoint always refuses stop the startup backfill early, so later
    documents stay lexical until the next start (the log counts them; each carries the note).
  - Time bounds: embedding one document, at ingest or in the backfill, stops after 10 minutes
    (then it is lexical only, with the note), a query's embedding after 30 s (then 504
    `model_timeout`; `TestEmbedWaitsBounded`), and an answer with vectors over 16,000 dimensions
    (pgvector's limit) is refused.
- **Vision transcription (PR 3c):**
  - **No vision model has been run.** There is no vision key in the build environment: every test
    uses `model.Fake` or `httptest`, so the prompts, the 0.75/0.9 thresholds, the disagreement rule
    and the 4,096-token output cap are not calibrated on any real model (owner's live check:
    § Vision transcription).
  - Agreement and the disagreement rule are checks against invention, not proof of accuracy: two
    readings that make the **same** mistake (the same misread figure) agree and are stored at normal
    confidence. Where the readings differ on a figure, a negation or modal, a dropped line or word
    order, the page is low confidence and one reading's version is stored (the first), not a merge:
    the note and `disputed` name what the other reading said differently, and the engine's finding gate
    routes such quotes to a person. The rule is conservative: any 5 differing words, or any word
    order difference (two columns read in another order), make a page low confidence, so a long
    page with harmless differences is gated too. Words other than figures and the listed negations
    and modals can change meaning (a name, "Applicant" for "Respondent" in place): one such
    substitution alone is not caught. Pages over 2,000 words are not compared in order and are
    always low confidence.
  - Masking over-masks: where the other reading has a mark, every word of the kept reading outside
    the alignment becomes `[illegible]`, including harmless differences, reordered words and, where
    the alignment is ambiguous, words the other reading did read ("John Smith pays John Smith Jr." vs
    "John Smith pays [illegible] Jr." stores both of the second "John Smith" masked); on a page too
    long to align (over about 2,000 words a reading), every occurrence of a word the other reading has
    fewer of.
  - The stored reading keeps the model's line-end hyphens, ligatures and quotes: a quote of
    "release" does not verify against a stored "re-\nlease" (as with any PDF text layer). A word
    hyphenated at a line end in one reading only ("munici-\npally" vs "municipally") is a differing
    word, and a hyphen read in one reading only ("re-sign" vs "resign", "non-compete" vs "non
    compete") is one too: like any single non-figure word, not low on its own.
  - Benign differences still gate a page: on the round-2 verifier's synthetic Ontario order, 7 of 17
    one-at-a-time harmless variants are low (a page footer, list numbers or cents one reading drops,
    a signature read as `[illegible]`, a party block's or a date's word order, a court stamp only one
    reading has; formerly 8, hyphenation now passing), random pairs of those 88.7% (round 3, compare-only
    folding; 88.1% in round 2), and pairs carrying only formatting noise (reflow, hyphenation,
    ligatures, quotes, markdown, one letter slip) 0 of 2,000 (formerly 1,042).
    These are a model of noise, not a measurement of any vision model; low confidence only gates
    quotes (the engine's finding gates).
  - A waiting document whose image the endpoint refuses holds the queue for 3 job runs (about 30
    minutes) before it fails and the next is tried: a refusal cannot tell the image's fault from the
    request's.
  - A model's **refusal or commentary** ("I can't transcribe this image") is not recognised: if
    both readings phrase it alike they agree, and it is stored as the page's transcript (capped and
    disclosed like any transcript). Telling commentary from text needs a judgement call BigLaw
    does not make here.
  - A WebP image is checked by its magic bytes only (no decoder in the standard library), so a
    damaged one is stored or sent and fails at the model instead (422 when it answers 4xx).
  - **Prompt injection:** text in an image is read by the model like any instruction. A page that
    tells the model to write something else can be obeyed by both readings, which then agree; the
    result is stored as a machine transcription (capped, disclosed), never as text-layer truth, and
    downstream prompts must treat it as untrusted document text (PR 4a).
  - Scans are found only as image XObjects in a page's own resources: inline images (`BI … EI`),
    images inside form XObjects, and pages drawn as vector paths read as blank or unsupported. A PDF
    page with a form feed inside its text layer makes a mixed PDF's pages unplaceable, and then none
    of its scanned pages are transcribed (the note still lists them). TIFF, BMP, HEIC and JPEG 2000
    are not read (no decoder without a dependency). A page with a short text layer and a small
    JPEG (a logo beside a one-line letter under 80 characters) counts as a scan and is read, its text
    layer kept after the transcript (in a one-page document with no vision role it waits, 503).
    A page with 400 or more characters of text layer is never read, whatever image is under it: a
    production page whose legend and Bates number reach 400 characters is stored as its legend only,
    with the note "an image on the page was not read … its text may be missing"; re-export it without
    the legend, or as images, to have it read.
  - **Mixed pages (80–399 characters over an image)** are read even when the image is a logo or
    stationery, not a scan: two vision calls per such page, the image sent off the machine, and a
    logo's words (a firm name) are added as transcript lines. The text layer is subtracted by whole
    reading lines (§ Vision transcription gives the rule): a line is dropped only when its letters and
    digits are the layer's own whole words, so a line that differs only in spacing, hyphens,
    punctuation or case is dropped and the layer's version stored. Kept, by design, as a disclosed
    duplicate (`exact@transcript`, capped): a short line (under 3 words or 12 letters and digits) that
    is not a whole layer line, so a reading that wraps differently from the layer ("Applicant
    monthly" across a layer line break, "Dear Ms. Smith" beside a longer layer line), a legend word or
    Bates digits read alone ("CONFIDENTIAL", "0001") or a figure with a sign the layer lacks ("10.5%")
    is stored twice; a figure whose separators differ from the layer's
    ("1250.00" for "1,250.00"); a reading line that breaks inside a word the text layer glued (a PDF
    whose lines are placed by text-positioning operators reads "consent orderdated"; a reading line
    ending "consent order" is kept, and a quote of it stays `exact@transcript` 0.85 because the layer
    copy is not whole words); a line the reading changed (a figure misread, or rewritten by text in
    the image: prompt injection) beside the text layer's own (that line of the letter twice, once as transcript, capped and
    disclosed); a line mixing a sidebar name with the letter's words (two columns read across) is kept
    whole; a longer body line whose words are all a run of the legend ("Subject to protective order")
    is dropped, its words staying on the page through the text layer. A quote moves to a later layer
    copy that a space and digits follow ("paid $500" into "paid $500 000", a space-grouped figure):
    the right edge cannot refuse a space before a digit without refusing every "October 2, 2026"; it
    needs a transcript figure whose longer, space-grouped form the layer holds later. The added lines are stored before the text
    layer whatever their place on the page. Reading only a mixed page's image cannot see text drawn
    over it; a document over 20 pages with an image reads the first 20 in page order, mixed pages
    included, so a text-less scan past them is not read even when a mixed page before it was: its note
    names the 20-page cap (`TestVisionCapNoted`). Only an `exact` quote moves to a copy outside the
    transcript; a `tolerant` one is still placed at its first copy (`@transcript`, 0.85, when that is an
    added line).
  - A mixed PDF stored while no vision role is set keeps its scanned pages unread for good: the job
    takes only documents with no text layer; its note says to upload it again.
  - **Images leave the machine** with a hosted vision model, as chat prompts will. Setting a vision
    role for the first time also sends every waiting document to it (the startup job run).
  - There is no quota, and without sign-in no auth: anyone who can reach the port (with sign-in, anyone signed in) can make the server pay for
    up to 40 vision calls per upload (20 pages × 2 readings), each up to 4,096 output tokens plus
    its image: at most 163,840 output tokens, $1.05 at `qwen-vl-max`'s output rate ($6.40/M), $1.64
    at `gpt-4o`'s ($10/M), before image input.
  - The calls of a refused transcription (422, or a model failure) are priced by the client but
    stored nowhere (the document is not stored): the operator's log line carries their tokens and
    cost; a failed job's are in the same log. Ingest happens outside any task, so a task's
    ledger never counts these (PR 4a decision; the document records them).
  - **An upload's vision read runs on its request** (DESIGN put it in the leased job; the async
    202 path is PR 8's, PLAN PR 8): the client must keep the connection open for the whole read (up
    to 40 sequential calls, bounded at 10 minutes, then 504 `model_timeout`). A client or proxy
    timeout below that (a reverse proxy's 60 s default) cancels the upload, deletes the file and
    discards the calls already paid; a retry pays again.
  - Memory per vision ingest: the file plus one page's images about three times over (base64 and
    the JSON request), at most about 64 + 3 × 20 MiB, for each of the 2 reader slots; a document
    waiting for a reader holds its extracted text (≤ 16 MiB). Images near the 20 MiB cap exceed
    some providers' own limits (OpenAI's 20 MB per image after base64): those are 422 when the
    endpoint answers 4xx.
  - The job runs every 15 minutes and at startup, not when a role changes while serving (a role
    change needs a restart anyway); a failed document stays failed until the operator re-queues
    it (the SQL above). A finding resting only on `lowConfidence` quotes is gated for a lawyer (§ Memo workflow).
