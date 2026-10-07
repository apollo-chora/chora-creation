-- Widen atom_embeddings.embedding to the embedding route's native width.
--
-- The deployment's embedding route (registry id `text-embedding-004`) resolves
-- to LiquidAI's LFM2.5 embedding model on OpenRouter, which returns 1024-dim
-- vectors and REJECTS a `dimensions` override ("produces 1024-dimensional
-- embeddings"). The pgvector column must therefore match, and the gateway
-- client sends output_dimensions=1024 explicitly.
--
-- pgvector cannot cast between widths, so an ALTER on a POPULATED column
-- discards the stored vectors. This deployment has never persisted an atom
-- embedding (the table is empty), which is what makes this a pure schema
-- change; a populated deployment would need a re-embed pass instead.
--
-- The ivfflat index is dropped and rebuilt because its operator class is bound
-- to the column width. Recreated with the same shape as 0001_initial.sql.
DROP INDEX IF EXISTS idx_atom_embeddings_cosine;
ALTER TABLE atom_embeddings ALTER COLUMN embedding TYPE vector(1024);
CREATE INDEX idx_atom_embeddings_cosine
    ON atom_embeddings USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
