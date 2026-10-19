-- 0004_vision: documents transcribed by the vision model (internal/docs vision.go, jobs.go).
-- method 'vision': the text is a machine transcription of images (an image file or a PDF with
-- no text layer). A mixed PDF keeps method 'text_layer'; its transcribed pages are in transcript.
-- transcript: provenance {model, pages: [{page, start, end, agreement}], agreement, lowConfidence,
-- transcribedAt, tokens, costUsd}; start/end are byte offsets of each transcribed page in text.
-- NULL: nothing in the document was transcribed.
-- status: 'needs_transcription' is an image or scan stored while no vision role is set (text '',
-- the original kept); the job (jobs.go) transcribes it under a lease (lease_until) and makes it
-- 'ready', or 'failed' with the reason in notes. attempts counts claims (transient failures).
ALTER TABLE documents DROP CONSTRAINT documents_method_check;
ALTER TABLE documents ADD CONSTRAINT documents_method_check CHECK (method IN ('text', 'text_layer', 'vision'));
ALTER TABLE documents
    ADD COLUMN status      text    NOT NULL DEFAULT 'ready' CHECK (status IN ('ready', 'needs_transcription', 'failed')),
    ADD COLUMN transcript  jsonb,
    ADD COLUMN lease_until timestamptz,
    ADD COLUMN attempts    integer NOT NULL DEFAULT 0 CHECK (attempts >= 0);
-- A chunk whose text overlaps a transcribed page (search hits say so).
ALTER TABLE chunks ADD COLUMN transcribed boolean NOT NULL DEFAULT false;
CREATE INDEX documents_pending ON documents (created_at) WHERE status = 'needs_transcription';
