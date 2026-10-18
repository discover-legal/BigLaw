-- 0002_docs: documents (owned by internal/docs), their section tree and search chunks.
-- A document's original upload is a files row; deleting the document deletes the file (docs.Delete,
-- one transaction), and a file deleted any other way takes its document with it (the CASCADE).
-- Every read in Go is scoped to a task's document ids (README G1).
CREATE TABLE documents (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL,
    content_type text        NOT NULL,
    file_id      uuid        UNIQUE REFERENCES files (id) ON DELETE CASCADE,
    text         text        NOT NULL,
    method       text        NOT NULL CHECK (method IN ('text', 'text_layer')),
    no_text      boolean     NOT NULL,
    pages        integer     NOT NULL CHECK (pages >= 0),
    notes        text[]      NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- One row per node of the section tree, in pre-order (ord). char_start/char_end are character
-- (not byte) offsets into documents.text of the node with its subsections, for substr().
CREATE TABLE sections (
    document_id uuid    NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    ord         integer NOT NULL CHECK (ord >= 0),
    number      text    NOT NULL,
    title       text    NOT NULL,
    level       integer NOT NULL CHECK (level >= 0),
    char_start  integer NOT NULL CHECK (char_start >= 0),
    char_end    integer NOT NULL CHECK (char_end >= char_start),
    tokens      integer NOT NULL CHECK (tokens >= 0),
    PRIMARY KEY (document_id, ord)
);

-- Search units: body is a verbatim slice of documents.text [byte_start, byte_end); search_text
-- is what is indexed (the body, or a table row paired with its headers). Two generated tsvectors:
-- English (stemmed) for words, simple (unstemmed) for identifiers such as s.9.4 or FS-26-01234.
CREATE TABLE chunks (
    id          bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id uuid    NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    section_ord integer NOT NULL,
    locator     text    NOT NULL,
    body        text    NOT NULL,
    search_text text    NOT NULL,
    context     text    NOT NULL,
    byte_start  integer NOT NULL CHECK (byte_start >= 0),
    byte_end    integer NOT NULL CHECK (byte_end > byte_start),
    tsv         tsvector GENERATED ALWAYS AS (to_tsvector('english'::regconfig, locator || ' ' || search_text)) STORED,
    tsv_simple  tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, locator || ' ' || search_text)) STORED,
    FOREIGN KEY (document_id, section_ord) REFERENCES sections (document_id, ord) ON DELETE CASCADE
);
CREATE INDEX chunks_document ON chunks (document_id);
CREATE INDEX chunks_tsv ON chunks USING gin (tsv);
CREATE INDEX chunks_tsv_simple ON chunks USING gin (tsv_simple);
