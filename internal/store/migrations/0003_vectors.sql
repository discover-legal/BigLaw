-- 0003_vectors: chunk embeddings for hybrid search (internal/docs). The vector type is pgvector's;
-- store.Open creates the extension once per database before migrating (extensions are
-- database-wide, migrations are per schema).
-- embedding has no fixed dimension: a document records the model that embedded all of its chunks
-- (embed_model; NULL = lexical only, also while a batch is half done), and search compares a query
-- only with chunks of its own model and length. Usage of the calls that embedded it: embed_tokens,
-- and embed_cost_usd (NULL when the model has no price: unknown, never $0).
-- No HNSW or IVFFlat index: search ranks one matter's chunks exactly. An approximate index
-- filtered to a matter's documents returns fewer than k rows (0 of 8 in the PR 3b probe: the
-- index's candidates were other matters'), so it would cost writes and break scoped recall.
ALTER TABLE chunks ADD COLUMN embedding vector;
ALTER TABLE documents
    ADD COLUMN embed_model    text,
    ADD COLUMN embed_tokens   bigint NOT NULL DEFAULT 0 CHECK (embed_tokens >= 0),
    ADD COLUMN embed_cost_usd float8 CHECK (embed_cost_usd >= 0);
