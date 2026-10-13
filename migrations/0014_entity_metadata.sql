-- 0014_entity_metadata
-- Adds the entity metadata column.
--
-- The column was declared in the schema inventory but never created by any
-- migration, so a read that selected it would fail on a fresh database and the
-- listing worked only because the query substituted a literal. It is added here
-- with the reference's shape: JSONB, not null, defaulting to an empty object, so
-- a caller can always read it without a null check.

ALTER TABLE entities ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
