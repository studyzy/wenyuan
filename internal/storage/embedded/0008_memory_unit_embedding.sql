-- 0008_memory_unit_embedding (embedded copy of migrations/0008_memory_unit_embedding.sql)
-- Adds the embedding column and its similarity index to the memory-unit table,
-- so the semantic retrieval arm can rank by real vector similarity instead of a
-- deterministic stand-in.
--
-- The width is fixed at 384, which is the dimension the provider boundary
-- declares as its single compile-time constant and the width the reference
-- implementation stores. A configurable width would let the column and the
-- boundary disagree, so the two are pinned together by this one value.
--
-- The extension itself is installed by the migration runner before this
-- statement runs, because the runner must resolve where the type lives rather
-- than assuming the table's own schema holds it.

ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS embedding vector(384);

-- Cosine distance is the operator class the reference uses, so ranking agrees
-- with a deployment that migrated its data.
CREATE INDEX IF NOT EXISTS idx_memory_units_embedding
    ON memory_units USING hnsw (embedding vector_cosine_ops);
