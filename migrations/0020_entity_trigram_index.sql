-- 0020_entity_trigram_index
-- Adds the trigram index that backs fuzzy entity-name resolution.
--
-- Entity resolution compares a supplied name against the names already in the
-- bank and reuses the closest one when it scores high enough. Without an index
-- that comparison is a sequential scan of every entity in the bank, which is
-- the read path every retain write hits.
--
-- The extension itself is installed outside the migration series, the same way
-- the vector extension is, because CREATE EXTENSION needs privileges a
-- migration otherwise does not, and a deployment that cannot install it must
-- still start and fall back to exact matching.

CREATE INDEX IF NOT EXISTS idx_entities_name_trgm
    ON entities USING gin (lower(name) gin_trgm_ops);