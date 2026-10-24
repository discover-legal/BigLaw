-- 0007_figure_reads_scope: the figure reader's cache is keyed by the reader's spec and prompt hash,
-- the model it ran on and the matter it read for (a drafted section's also by its task), and stores
-- only complete answers (lab-r4 r5 M3, L3). Rows cached under the old key are dropped (a cache: the
-- reader reads those figures again). A row goes with the task that read it and with any document its
-- quotes are from.
DELETE FROM figure_reads;
ALTER TABLE figure_reads
    ADD COLUMN task_id       uuid   NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    ADD COLUMN matter_number text   NOT NULL,
    ADD COLUMN document_ids  uuid[] NOT NULL DEFAULT '{}';
CREATE INDEX figure_reads_task ON figure_reads (task_id);
CREATE INDEX figure_reads_documents ON figure_reads USING gin (document_ids);

CREATE FUNCTION figure_reads_forget_document() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM figure_reads WHERE document_ids @> ARRAY[OLD.id];
    RETURN OLD;
END $$;
CREATE TRIGGER figure_reads_forget_document AFTER DELETE ON documents
    FOR EACH ROW EXECUTE FUNCTION figure_reads_forget_document();
