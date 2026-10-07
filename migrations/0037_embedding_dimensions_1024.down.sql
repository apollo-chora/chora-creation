-- Destructive rollback: 1024 -> 768 discards every stored atom embedding.
DROP INDEX IF EXISTS idx_atom_embeddings_cosine;
ALTER TABLE atom_embeddings ALTER COLUMN embedding TYPE vector(768);
CREATE INDEX idx_atom_embeddings_cosine
    ON atom_embeddings USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
