-- 0006_figure_reads: the figure reader's labels (internal/engine read_figures), cached by the hash of
-- what it read (its passages, spans and parties, and the reader's version), so a run read again, or
-- a finding restated in another run, gets the same reading instead of another model answer.
CREATE TABLE figure_reads (
    key        text        PRIMARY KEY,
    labels     jsonb       NOT NULL,
    model      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
