-- 0023_extensions_and_vector_width
--
-- Two things the reference database has that this series never stated, because
-- they were assumed to pre-exist or were chosen independently.
--
-- 1. The extensions. The reference's own schema dump declares none of them: the
--    objects that use `vector`, `pg_trgm` and `gin_trgm_ops` simply reference
--    types and operator classes the database already provided. A deployment that
--    builds its schema from this series has to create them itself, and it has to
--    be before the objects that need them.
--
-- 2. The vector width. This series introduced the embedding column at 384
--    dimensions. The reference stores 1024, and `vector(384)` and
--    `vector(1024)` are different types, so a 384-wide column cannot read a
--    single embedding from an existing reference database. The column is
--    widened here to match, and the index is renamed to the reference's name.
--
-- The two index names differ and both are kept distinct on purpose: the old
-- index is dropped rather than left beside the new one, because a duplicate
-- index on the same column would double the write cost for no benefit.

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gin;

-- Wide enough for the reference's vectors. Existing rows are dropped with the
-- old column because a 384-wide vector is not a 1024-wide one and there is no
-- meaningful conversion between them; a deployment migrating real data uses the
-- export/import path, which re-embeds.
ALTER TABLE memory_units DROP COLUMN IF EXISTS embedding;
ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS embedding vector(1024);

DROP INDEX IF EXISTS idx_memory_units_embedding;
CREATE INDEX IF NOT EXISTS idx_memory_units_embedding_hnsw
    ON memory_units USING hnsw (embedding vector_cosine_ops);

-- The mental-model column takes the same width, since both are embedded by the
-- same provider boundary and a mismatch would only surface at the first write.
ALTER TABLE mental_models DROP COLUMN IF EXISTS embedding;
ALTER TABLE mental_models ADD COLUMN IF NOT EXISTS embedding vector(1024);

DROP INDEX IF EXISTS idx_mental_models_embedding;
CREATE INDEX IF NOT EXISTS idx_mental_models_embedding_hnsw
    ON mental_models USING hnsw (embedding vector_cosine_ops);
